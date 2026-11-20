package main

import (
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log"
	"os"
	"runtime"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

//go:embed build/tray
var trayIcons embed.FS

const notchWindowName = "notch"

func main() {
	limitMemory(os.Getenv)
	cfg, err := LoadConfig(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		log.Fatal(err)
	}
	svc := NewShareService(cfg, nil, dialogs{})

	app := application.New(application.Options{
		Name:        "Kolo Share",
		Description: "Connect two computers once, then send files and folders both ways, built with Kolo",
		Icon:        appIcon,
		Services: []application.Service{
			application.NewService(svc),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: macOptions(),
	})

	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:  mainWindowName,
		Title: "Kolo Share",
		// Window sized to the golden ratio (1000 / 618 ≈ 1.618), as the chat example.
		Width:          1000,
		Height:         618,
		EnableFileDrop: true,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(7, 11, 18),
		URL:              "/",
	})
	// Events go to one window each: Event.Emit would send the pairing code to the notch page too.
	svc.emit = func(name string, data any) { win.DispatchWailsEvent(&application.CustomEvent{Name: name, Data: data}) }
	win.OnWindowEvent(events.Common.WindowFilesDropped, func(e *application.WindowEvent) {
		svc.filesDropped(e.Context().DroppedFiles())
	})
	// The notch window and the tray outlive the main window, so closing it quits the app, as in the chat example.
	var quit sync.Once
	win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) { quit.Do(app.Quit) })
	openApp := func() { win.Show().Focus() }

	// On systems without a notch the library returns an inert window, and Show and Hide do nothing.
	notch := app.Window.NewNotchWindow(withHousing(notchOptions(), notchAreaOf(primaryScreen())))
	svc.notch, svc.openApp = notchAdapter{notch}, openApp
	svc.emitNotch = func(name string, data any) {
		if w, ok := app.Window.GetByName(notchWindowName); ok {
			w.DispatchWailsEvent(&application.CustomEvent{Name: name, Data: data})
		}
	}
	svc.copyText = func(text string) { app.Clipboard.SetText(text) }
	svc.clipText = func() string { text, _ := app.Clipboard.Text(); return text }
	svc.reveal = func(path string) error { return runReveal(revealCommand(runtime.GOOS, path)) }
	if err := app.GlobalShortcut.Register(notchShortcut, func() { notch.Show() }); err != nil {
		log.Printf("shortcut %s: %v", notchShortcut, err)
	}

	if trayUsable(runtime.GOOS, os.Getenv, dialUnix) {
		svc.tray = newTray(app, win, svc, openApp)
	} else {
		log.Print("no tray icon: this session has no D-Bus session bus")
	}

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// notchOptions makes the notch window hidden at start: the transfers show it, and the shortcut.
func notchOptions() application.NotchWindowOptions {
	return application.NotchWindowOptions{
		Width:    notchWindowWidth,
		Height:   notchWindowH,
		Animated: true,
		WindowOptions: application.WebviewWindowOptions{
			Name:         notchWindowName,
			URL:          "/notch/",
			Hidden:       true,
			HideOnEscape: true,
		},
	}
}

type notchAdapter struct{ w *application.NotchWindow }

func (n notchAdapter) Show() { n.w.Show() }
func (n notchAdapter) Hide() { n.w.Hide() }

// dialogs opens the native file and folder dialogs of Wails.
type dialogs struct{}

func (dialogs) open(folder bool, title string) (string, error) {
	d := application.Get().Dialog.OpenFile().
		SetTitle(title).
		CanChooseFiles(!folder).
		CanChooseDirectories(folder).
		CanCreateDirectories(folder).
		ResolvesAliases(true)
	return d.PromptForSingleSelection()
}

// wailsTray is the menu bar icon on macOS and the tray icon elsewhere, with its menu.
type wailsTray struct {
	tray                *application.SystemTray
	status, copy, notch *application.MenuItem
	icon, active        []byte
	template            bool
	menu                *application.Menu

	mu                   sync.Mutex // the menu's callbacks run on goroutines of their own
	code                 string
	lastActive, hasState bool
}

func newTray(app *application.App, win *application.WebviewWindow, svc *ShareService, openApp func()) *wailsTray {
	t := &wailsTray{tray: app.SystemTray.New()}
	t.icon, t.active, t.template = trayImages(runtime.GOOS)
	t.setIcon(false)
	t.tray.SetTooltip("Kolo Share")

	t.menu = app.NewMenu()
	t.menu.Add("Open Kolo Share").OnClick(func(*application.Context) { openApp() })
	t.status = t.menu.Add("Idle").SetEnabled(false)
	t.copy = t.menu.Add("Copy pairing code").SetHidden(true).OnClick(func(*application.Context) {
		t.mu.Lock()
		code := t.code
		t.mu.Unlock()
		svc.copyCode(code)
	})
	if runtime.GOOS == "darwin" {
		t.notch = t.menu.AddCheckbox("Show transfers in the notch", true).OnClick(func(ctx *application.Context) {
			if _, err := svc.SetShowInNotch(ctx.ClickedMenuItem().Checked()); err != nil {
				log.Print("notch setting: ", err)
			}
		})
	}
	t.menu.AddSeparator()
	t.menu.Add("Quit").OnClick(func(*application.Context) { app.Quit() })
	t.tray.SetMenu(t.menu)
	t.tray.OnClick(func() { trayClick(win, openApp) })
	return t
}

// set runs on the main thread, where the menu and the icon may be changed.
func (t *wailsTray) set(st TrayState) {
	application.InvokeAsync(func() {
		t.mu.Lock()
		t.code = st.Code
		changed := !t.hasState || st.Active != t.lastActive
		t.lastActive, t.hasState = st.Active, true
		t.mu.Unlock()
		t.status.SetLabel(st.Status)
		t.tray.SetTooltip(st.Tooltip)
		t.copy.SetHidden(st.Code == "")
		if t.notch != nil {
			t.notch.SetChecked(st.ShowInNotch)
		}
		if changed {
			t.setIcon(st.Active)
		}
	})
}

func (t *wailsTray) setIcon(active bool) {
	img := t.icon
	if active && len(t.active) > 0 {
		img = t.active
	}
	if t.template {
		t.tray.SetTemplateIcon(img)
	} else {
		t.tray.SetIcon(img)
	}
}

// trayImages picks the icons in build/tray for goos: the macOS template images, else the 32 px ones.
func trayImages(goos string) (icon, active []byte, template bool) {
	read := func(name string) []byte {
		b, _ := fs.ReadFile(trayIcons, "build/tray/"+name)
		return b
	}
	if goos == "darwin" {
		if icon = read("tray-template@2x.png"); icon != nil {
			return icon, read("tray-template-active@2x.png"), true
		}
	}
	return read("tray-32.png"), read("tray-active-32.png"), false
}
