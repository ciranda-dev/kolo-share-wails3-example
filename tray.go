package main

import (
	"fmt"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// macOptions keeps the app running while its windows are hidden. With ApplicationShouldTerminateAfterLastWindowClosed,
// AppKit ended the app when a click on the tray icon hid the main window, the only one on screen. Closing the
// main window with its red button still quits, through its WindowClosing hook in main.
func macOptions() application.MacOptions { return application.MacOptions{} }

// shownWindow is the main window, as much of it as a click on the tray icon needs.
type shownWindow interface {
	IsVisible() bool
	Hide() application.Window
}

// trayClick hides the main window when it is on screen, and opens the app otherwise. Hide only takes the window
// off the screen: it does not close it, so the WindowClosing hook that quits never runs.
func trayClick(w shownWindow, openApp func()) {
	if w.IsVisible() {
		w.Hide()
		return
	}
	openApp()
}

// TrayState is what the menu bar or tray icon shows: the active icon while a transfer runs or an offer waits, the
// status line of its menu and the tooltip, and the pairing code that "Copy pairing code" copies (empty: the item is
// hidden).
type TrayState struct {
	Active  bool   `json:"active"`
	Status  string `json:"status"`
	Tooltip string `json:"tooltip"`
	Code    string `json:"code"`
	// ShowInNotch is the setting, for the checkbox of the menu.
	ShowInNotch bool `json:"showInNotch"`
}

// trayUI is the menu bar or tray icon; the app's comes from Wails, and tests have none.
type trayUI interface {
	set(TrayState)
}

// trayOf maps the running transfers, the waiting pairing code and the connected sessions to the tray: the
// transfer the notch would show gives the status line. An offer shows only its sender, name and size.
func trayOf(running []Transfer, code string, connected int) TrayState {
	n := pickNotch(running, nil, code != "")
	st := TrayState{Active: len(running) > 0, Status: "Idle", Tooltip: "Kolo Share", Code: code}
	switch {
	case n.ID == "" && n.HasCode:
		st.Status = "Waiting for your pairing code to be used"
	case n.ID == "" && connected == 1:
		st.Status = "Connected to 1 computer"
	case n.ID == "" && connected > 1:
		st.Status = fmt.Sprintf("Connected to %d computers", connected)
	}
	if n.ID == "" || !n.Active {
		return st
	}
	switch n.State {
	case stateOffered:
		if n.HasOffer {
			st.Status = fmt.Sprintf("%s wants to send you %s, %s", n.PeerName, n.Name, humanBytes(n.BytesTotal))
			st.Tooltip = "Kolo Share: " + st.Status
		} else {
			st.Status = fmt.Sprintf("Offering %s, waiting for %s", n.Name, n.PeerName)
		}
	case statePreparing:
		st.Status = fmt.Sprintf("Preparing %s", n.Name)
		if n.Direction == "receive" {
			st.Status = fmt.Sprintf("%s is preparing %s", n.PeerName, n.Name)
		}
	case stateSending:
		st.Status = fmt.Sprintf("Sending %s, %s received", n.Name, humanBytes(n.BytesDone))
	case stateQueued:
		st.Status = fmt.Sprintf("Waiting to receive %s", n.Name)
	case stateReceiving:
		st.Status = fmt.Sprintf("Receiving %s, %s downloaded", n.Name, humanBytes(n.BytesDone))
	case stateVerifying:
		st.Status = fmt.Sprintf("Receiving %s, %.0f%%", n.Name, max(n.Percent, 0))
	}
	if n.Others > 0 {
		st.Status += fmt.Sprintf(" (+%d)", n.Others)
	}
	return st
}

func (s *ShareService) trayLocked() TrayState {
	running := make([]Transfer, 0, len(s.jobs))
	for _, j := range s.jobs {
		if !j.t.final() {
			running = append(running, j.t)
		}
	}
	code := ""
	if pr := s.pairing; pr != nil && pr.p.Role == roleInviter && pr.p.State == pairWaiting {
		code = pr.p.Code
	}
	connected := 0
	for _, ss := range s.sessions {
		if ss.s.State == sessConnected {
			connected++
		}
	}
	st := trayOf(running, code, connected)
	st.ShowInNotch = s.saved.showInNotch()
	return st
}

// pushTray tells the tray icon what it shows, when that changed.
func (s *ShareService) pushTray() {
	s.trayMu.Lock()
	defer s.trayMu.Unlock()
	s.mu.Lock()
	st := s.trayLocked()
	changed := st != s.lastTray
	s.lastTray = st
	s.mu.Unlock()
	if changed && s.tray != nil {
		s.tray.set(st)
	}
}
