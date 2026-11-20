package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	koloblobs "github.com/ciranda-dev/kolo-blobs"
	kolodht "github.com/ciranda-dev/kolo-dht"
	kolofiles "github.com/ciranda-dev/kolo-files"
	koloid "github.com/ciranda-dev/kolo-id"
	invite "github.com/ciranda-dev/kolo-invite"
	kolostore "github.com/ciranda-dev/kolo-store"
	kolosync "github.com/ciranda-dev/kolo-sync"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// fakeShownWindow records what a click on the tray icon does to the main window.
type fakeShownWindow struct {
	visible bool
	calls   []string
}

func (w *fakeShownWindow) IsVisible() bool { return w.visible }
func (w *fakeShownWindow) Hide() application.Window {
	w.calls, w.visible = append(w.calls, "hide"), false
	return nil
}

// A click on the tray icon hides the window and the next one opens the app. Nothing on that path closes the
// window, and the app does not ask AppKit to end it when the window is gone from the screen (the cause of the
// app quitting on a click).
func TestTrayClickTogglesAndNeverQuits(t *testing.T) {
	if macOptions().ApplicationShouldTerminateAfterLastWindowClosed {
		t.Fatal("hiding the main window from the tray would end the app")
	}
	w := &fakeShownWindow{visible: true}
	open := func() { w.calls, w.visible = append(w.calls, "open"), true }
	trayClick(w, open)
	trayClick(w, open)
	if strings.Join(w.calls, ",") != "hide,open" || !w.visible {
		t.Fatalf("two clicks: %v", w.calls)
	}
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfigPrecedence(t *testing.T) {
	c, err := LoadConfig([]string{"-bootstrap", "127.0.0.1:5000"}, env(map[string]string{"KOLO_BOOTSTRAP": "127.0.0.1:6000", "KOLO_SHARE_FIND_TIMEOUT": "3s"}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Bootstrap != "127.0.0.1:5000" || c.bootstrapFrom != "flag" || c.FindTimeout != 3*time.Second {
		t.Fatalf("flag must win: %+v", c)
	}
	c, err = LoadConfig(nil, env(map[string]string{"KOLO_BOOTSTRAP": "127.0.0.1:6000"}), io.Discard)
	if err != nil || c.bootstrapFrom != "env" {
		t.Fatalf("env: %+v %v", c, err)
	}
	if _, err := LoadConfig(nil, env(map[string]string{"KOLO_SHARE_MAX_DOWNLOAD": "lots"}), io.Discard); !errors.Is(err, ErrConfig) || !errors.Is(err, ErrMalformed) {
		t.Fatalf("bad env: %v", err)
	}
}

func TestLoadConfigLocal(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		env  map[string]string
		want bool
		from string
	}{
		"none":          {nil, nil, false, ""},
		"flag":          {[]string{"-local"}, nil, true, "flag"},
		"flag true":     {[]string{"-local=true"}, nil, true, "flag"},
		"env 1":         {nil, map[string]string{"KOLO_SHARE_LOCAL": "1"}, true, "env"},
		"env 0":         {nil, map[string]string{"KOLO_SHARE_LOCAL": "0"}, false, "env"},
		"flag over env": {[]string{"-local=false"}, map[string]string{"KOLO_SHARE_LOCAL": "1"}, false, "flag"},
	} {
		cfg, err := LoadConfig(c.args, env(c.env), io.Discard)
		if err != nil || cfg.Local != c.want || cfg.localFrom != c.from {
			t.Errorf("%s: %+v %v", name, cfg, err)
		}
	}
	if _, err := LoadConfig(nil, env(map[string]string{"KOLO_SHARE_LOCAL": "maybe"}), io.Discard); !errors.Is(err, ErrMalformed) {
		t.Errorf("bad KOLO_SHARE_LOCAL: %v", err)
	}
	if _, err := LoadConfig([]string{"-local=maybe"}, env(nil), io.Discard); err == nil {
		t.Error("bad -local value accepted")
	}
	// The flag and the variables of the two other networks reach resolve as they are given.
	cfg, _ := LoadConfig([]string{"-local", "-bootstrap", "127.0.0.1:4000"}, env(nil), io.Discard)
	if _, err := cfg.resolve(); !errors.Is(err, ErrConflict) {
		t.Errorf("-local -bootstrap: %v", err)
	}
	cfg, _ = LoadConfig([]string{"-local"}, env(map[string]string{"KOLO_BOOTSTRAP": "127.0.0.1:4000"}), io.Discard)
	if _, err := cfg.resolve(); !errors.Is(err, ErrConflict) {
		t.Errorf("-local with KOLO_BOOTSTRAP: %v", err)
	}
	cfg, _ = LoadConfig(nil, env(nil), io.Discard)
	if s, err := cfg.resolve(); err != nil || s.mode != modePublic {
		t.Errorf("no flags: %+v %v", s, err)
	}
	cfg, _ = LoadConfig([]string{"-bootstrap", ""}, env(nil), io.Discard)
	if _, err := cfg.resolve(); !errors.Is(err, ErrMissing) {
		t.Errorf("-bootstrap with no node: %v", err)
	}
}

// -congestion and KOLO_SHARE_CONGESTION name the controller of the session connection: bbr when neither is given, the
// flag over the variable, and anything but reno, cubic or bbr is an error that names the setting.
func TestCongestionSetting(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct {
		args []string
		env  map[string]string
		want string
	}{
		"none":          {nil, nil, "bbr"},
		"flag reno":     {[]string{"-congestion", "reno"}, nil, "reno"},
		"flag cubic":    {[]string{"-congestion=cubic"}, nil, "cubic"},
		"flag bbr":      {[]string{"-congestion", "bbr"}, nil, "bbr"},
		"env reno":      {nil, map[string]string{"KOLO_SHARE_CONGESTION": "reno"}, "reno"},
		"flag over env": {[]string{"-congestion", "cubic"}, map[string]string{"KOLO_SHARE_CONGESTION": "reno"}, "cubic"},
	} {
		cfg, err := LoadConfig(c.args, env(c.env), io.Discard)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		cfg.DataDir = dir
		if s, err := cfg.resolve(); err != nil || s.Congestion != c.want {
			t.Errorf("%s: %q, %v, want %q", name, s.Congestion, err, c.want)
		}
	}
	for name, c := range map[string]struct {
		args []string
		env  map[string]string
	}{
		"flag":          {[]string{"-congestion", "fast"}, nil},
		"flag case":     {[]string{"-congestion", "BBR"}, nil},
		"env":           {nil, map[string]string{"KOLO_SHARE_CONGESTION": "vegas"}},
		"flag over env": {[]string{"-congestion", "nope"}, map[string]string{"KOLO_SHARE_CONGESTION": "bbr"}},
	} {
		cfg, err := LoadConfig(c.args, env(c.env), io.Discard)
		if err == nil {
			cfg.DataDir = dir
			_, err = cfg.resolve()
		}
		var ce *ConfigError
		if !errors.Is(err, ErrMalformed) || !errors.As(err, &ce) || ce.Setting != "congestion" {
			t.Errorf("%s: %v, want a malformed congestion setting", name, err)
		}
	}
}

func TestBootstrapFailure(t *testing.T) {
	dir := t.TempDir()
	public, _ := Config{DataDir: dir}.resolve()
	local, _ := Config{DataDir: dir, Local: true}.resolve()
	own, _ := Config{DataDir: dir, Bootstrap: "node.example.org:7447"}.resolve()
	test, _ := Config{DataDir: dir, Bootstrap: "127.0.0.1:4000"}.resolve()
	for _, c := range []struct {
		name         string
		set          settings
		hosted       bool
		reason, from string
	}{
		{"public", public, false, "public-unreachable", ""},
		{"local, port taken", local, false, "local-port-taken", local.local.String()},
		{"local, hosted", local, true, "bootstrap-failed", local.local.String()},
		{"own", own, false, "bootstrap-failed", "node.example.org:7447"},
		{"loopback list", test, false, "no-local-entry-node", "127.0.0.1:4000"},
	} {
		reason, detail := bootstrapFailure(c.set, c.hosted, kolodht.ErrNoNodes)
		if reason != c.reason || (c.from != "" && detail != c.from) {
			t.Errorf("%s: %s %q", c.name, reason, detail)
		}
	}
	// The unreachable public network names the three options.
	msg := shareErr("public-unreachable", kolodht.ErrNoNodes, "").Message
	for _, want := range []string{"public network", "-bootstrap", "-local"} {
		if !strings.Contains(msg, want) {
			t.Errorf("public-unreachable %q lacks %q", msg, want)
		}
	}
}

// listsPublicNode reports whether text names one of the public entry nodes.
func listsPublicNode(text string) bool {
	for _, a := range kolodht.PublicServers() {
		if strings.Contains(text, a.Addr().String()) {
			return true
		}
	}
	return false
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	// Nothing configured: the public network, with kolo-swarm's own list of entry nodes.
	s, err := Config{DataDir: dir}.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if s.mode != modePublic || s.nodesFrom != "public" || len(s.nodes) != 0 || s.local.IsValid() || s.first.IsValid() ||
		s.nodesText != "" || s.entryCount() != len(kolodht.PublicServers()) || s.Listen != publicListen ||
		s.MaxDownload != defaultMaxDownload || s.FindTimeout != defaultFindTimeout {
		t.Fatalf("defaults: %+v", s)
	}
	// -local alone: the entry node on this computer, on loopback only.
	s, err = Config{DataDir: dir, Local: true}.resolve()
	if err != nil || s.mode != modeLocal || s.nodesFrom != "local" || !slices.Equal(s.nodes, []string{"127.0.0.1:7451"}) ||
		s.local.String() != "127.0.0.1:7451" || s.nodesText != "" || s.entryCount() != 1 || s.Listen != localListen {
		t.Fatalf("local: %+v %v", s, err)
	}
	if s, err := (Config{DataDir: dir, Local: true, source: loopbackSource(7000)}).resolve(); err != nil || s.local.String() != "127.0.0.1:7000" {
		t.Fatalf("local port: %+v %v", s, err)
	}
	// -bootstrap alone: your own network, used alone, the public list is not touched.
	for name, c := range map[string]Config{
		"bootstrap": {DataDir: dir, Bootstrap: "127.0.0.1:4000", bootstrapFrom: "flag"},
		"env":       {DataDir: dir, Bootstrap: "127.0.0.1:4000", bootstrapFrom: "env"},
		"first":     {DataDir: dir, First: "127.0.0.1:4000"},
	} {
		if s, err := c.resolve(); err != nil || s.mode != modeOwn || s.local.IsValid() || s.nodesFrom == "local" || s.nodesFrom == "public" ||
			len(s.nodes) != 1 || s.nodes[0] != "127.0.0.1:4000" || listsPublicNode(s.nodesText) {
			t.Errorf("%s: %+v %v", name, s, err)
		}
	}
	// -local with -bootstrap or -first is an error, never a silent choice.
	for name, c := range map[string]Config{
		"bootstrap":      {DataDir: dir, Local: true, Bootstrap: "127.0.0.1:4000", bootstrapFrom: "flag"},
		"env bootstrap":  {DataDir: dir, Local: true, Bootstrap: "127.0.0.1:4000", bootstrapFrom: "env"},
		"empty flag":     {DataDir: dir, Local: true, bootstrapFrom: "flag"},
		"first":          {DataDir: dir, Local: true, First: "127.0.0.1:4000"},
		"bootstrap only": {DataDir: dir, Local: true, Bootstrap: "node.example.org:7447"},
	} {
		_, err := c.resolve()
		var ce *ConfigError
		if !errors.Is(err, ErrConflict) || !errors.Is(err, ErrConfig) || !errors.As(err, &ce) || ce.Setting != "local" {
			t.Errorf("%s: err %v, want a local conflict", name, err)
		}
	}
	// -bootstrap given with no entry node is an error, not the public network.
	if _, err := (Config{DataDir: dir, bootstrapFrom: "flag"}).resolve(); !errors.Is(err, ErrMissing) {
		t.Errorf("empty -bootstrap: %v", err)
	}
	s, err = Config{DataDir: dir, Bootstrap: " 127.0.0.1:7447, 127.0.0.1:7448 ,"}.resolve()
	if err != nil || s.nodesFrom != "flag" || len(s.nodes) != 2 || s.Listen != localListen {
		t.Fatalf("loopback list set in code: %+v %v", s, err)
	}
	s, err = Config{DataDir: dir, Bootstrap: "node.example.org:7447", bootstrapFrom: "flag"}.resolve()
	if err != nil || s.Listen != publicListen {
		t.Fatalf("public list: %+v %v", s, err)
	}
	s, err = Config{DataDir: dir, First: "127.0.0.1:4000"}.resolve()
	if err != nil || len(s.nodes) != 1 || s.nodes[0] != "127.0.0.1:4000" || s.Listen != localListen {
		t.Fatalf("first node: %+v %v", s, err)
	}
	for name, c := range map[string]Config{
		"first not loopback": {DataDir: dir, First: "192.168.1.2:4000"},
		"first port 0":       {DataDir: dir, First: "127.0.0.1:0"},
		"first ipv6":         {DataDir: dir, First: "[::1]:4000"},
		"listen ipv6":        {DataDir: dir, Listen: "[::]:0"},
		"listen v4-mapped":   {DataDir: dir, Listen: "[::ffff:127.0.0.1]:0"},
		"negative download":  {DataDir: dir, MaxDownload: -1},
		"short find timeout": {DataDir: dir, FindTimeout: time.Millisecond},
		"lookup too long":    {DataDir: dir, LookupInterval: 2 * time.Hour},
		"node ipv6":          {DataDir: dir, Bootstrap: "[::1]:4000", bootstrapFrom: "flag"},
		"node port 0":        {DataDir: dir, Bootstrap: "127.0.0.1:0", bootstrapFrom: "flag"},
		"node garbage":       {DataDir: dir, Bootstrap: "not a node", bootstrapFrom: "flag"},
		"too many nodes":     {DataDir: dir, Bootstrap: manyNodes(17), bootstrapFrom: "flag"},
	} {
		if _, err := c.resolve(); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: err %v, want ErrConfig", name, err)
		}
	}
}

func testLimits(bytes int64) limits { return limits{bytes: bytes, nodes: 1000} }

// testWriter saves into dest with no downloaded mark and a pending list in a temporary folder.
func testWriter(t *testing.T, dest string) writer {
	return writer{folder: dest, mark: func(*os.File) error { return nil }, pending: &pendingList{path: filepath.Join(t.TempDir(), pendingFile)}}
}

func manyNodes(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "127.0.0.1:%d,", i)
	}
	return b.String()
}

func TestHistoryBounds(t *testing.T) {
	dir := t.TempDir()
	h, err := openHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range maxHistory + 20 {
		if _, err := h.add(HistoryItem{ID: string(rune('a' + i%26)), Name: "n", At: int64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	list := h.list()
	if len(list) != maxHistory || list[0].At != maxHistory+19 {
		t.Fatalf("got %d items, first at %d", len(list), list[0].At)
	}
	info, err := os.Stat(filepath.Join(dir, historyFile))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("history file mode %v", info.Mode().Perm())
	}
	again, err := openHistory(dir)
	if err != nil || len(again.list()) != maxHistory {
		t.Fatalf("reopened: %d %v", len(again.list()), err)
	}
	if err := again.clear(); err != nil || len(again.list()) != 0 {
		t.Fatal("clear")
	}

	// A file that is not ours is an empty history, not an error.
	os.WriteFile(filepath.Join(dir, historyFile), []byte("{not json"), 0o600)
	if h, err := openHistory(dir); err != nil || len(h.list()) != 0 {
		t.Fatalf("corrupt file: %v", err)
	}
	big := make([]HistoryItem, 200)
	b, _ := json.Marshal(big)
	os.WriteFile(filepath.Join(dir, historyFile), b, 0o600)
	if h, err := openHistory(dir); err != nil || len(h.list()) != maxHistory {
		t.Fatalf("long file: %d %v", len(h.list()), err)
	}
	os.WriteFile(filepath.Join(dir, historyFile), make([]byte, maxHistoryFile+1), 0o600)
	if h, err := openHistory(dir); err != nil || len(h.list()) != 0 {
		t.Fatalf("oversized file: %v", err)
	}
}

func TestSettingsFile(t *testing.T) {
	dir := t.TempDir()
	s, err := loadSaved(dir)
	if err != nil || !s.showInNotch() {
		t.Fatalf("missing file: %+v %v", s, err)
	}
	off := false
	if err := saveSettings(dir, savedSettings{ShowInNotch: &off, DisplayName: "Ana"}); err != nil {
		t.Fatal(err)
	}
	s, err = loadSaved(dir)
	if err != nil || s.showInNotch() || s.DisplayName != "Ana" {
		t.Fatalf("saved: %+v %v", s, err)
	}
	os.WriteFile(filepath.Join(dir, settingsFile), []byte("[1,2"), 0o600)
	if s, err := loadSaved(dir); err != nil || !s.showInNotch() {
		t.Fatalf("corrupt: %+v %v", s, err)
	}
}

func TestCopyName(t *testing.T) {
	for _, c := range []struct {
		name string
		i    int
		dir  bool
		want string
	}{
		{"photo.jpg", 1, false, "photo.jpg"},
		{"photo.jpg", 2, false, "photo (2).jpg"},
		{"archive.tar.gz", 3, false, "archive.tar (3).gz"},
		{".profile", 2, false, ".profile (2)"},
		{"notes.d", 2, true, "notes.d (2)"},
		{"plain", 12, false, "plain (12)"},
	} {
		if got := copyName(c.name, c.i, c.dir); got != c.want {
			t.Errorf("copyName(%q, %d, %v) = %q, want %q", c.name, c.i, c.dir, got, c.want)
		}
	}
}

func TestCheckSource(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.txt")
	os.WriteFile(file, []byte("x"), 0o600)
	if kind, name, err := checkSource(file); err != nil || kind != kindFile || name != "a.txt" {
		t.Fatalf("file: %s %s %v", kind, name, err)
	}
	if kind, _, err := checkSource(dir); err != nil || kind != kindFolder {
		t.Fatalf("folder: %s %v", kind, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(file, link); err == nil {
		if _, _, err := checkSource(link); !errors.Is(err, ErrSource) {
			t.Fatalf("link: %v", err)
		}
	}
	for _, p := range []string{"", "relative/path", filepath.Join(dir, "missing")} {
		var se *ShareError
		if _, _, err := checkSource(p); !errors.As(err, &se) || se.Kind != kindSource {
			t.Fatalf("%q: %v", p, err)
		}
	}
	root := "/"
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	if _, _, err := checkSource(root); !errors.Is(err, ErrSource) {
		t.Fatalf("root: %v", err)
	}
}

func TestCheckFolder(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	os.WriteFile(file, nil, 0o600)
	if err := checkFolder(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", "rel", file, filepath.Join(dir, "missing")} {
		var se *ShareError
		if err := checkFolder(p); !errors.As(err, &se) || se.Kind != kindBadFolder {
			t.Errorf("%q: %v", p, err)
		}
	}
}

// localVolume builds a volume in memory with the given files ("dir/" for a folder) under top.
func localVolume(t *testing.T, files map[string]string) *kolofiles.Volume {
	t.Helper()
	st := kolostore.New(kolostore.NewMemory())
	t.Cleanup(func() { st.Close() })
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	meta, err := st.Create(id)
	if err != nil {
		t.Fatal(err)
	}
	content, err := st.Create(id)
	if err != nil {
		t.Fatal(err)
	}
	v, err := kolofiles.Create(meta, content)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	// Parents first.
	for depth := 0; depth < 8; depth++ {
		for _, n := range names {
			if strings.Count(strings.TrimSuffix(n, "/"), "/") != depth {
				continue
			}
			var err error
			if strings.HasSuffix(n, "/") {
				err = v.Mkdir(strings.TrimSuffix(n, "/"))
			} else {
				err = v.WriteFile(context.Background(), n, strings.NewReader(files[n]))
			}
			if err != nil {
				t.Fatalf("%s: %v", n, err)
			}
		}
	}
	return v
}

func caseSensitive(t *testing.T, dir string) bool {
	t.Helper()
	p := filepath.Join(dir, "CaseProbe")
	os.WriteFile(p, nil, 0o600)
	defer os.Remove(p)
	_, err := os.Stat(filepath.Join(dir, "caseprobe"))
	return err != nil
}

func TestCheckoutStaysInside(t *testing.T) {
	files := map[string]string{
		"share/":                  "",
		"share/a.txt":             "alpha",
		"share/sub/":              "",
		"share/sub/deep/":         "",
		"share/sub/deep/b.txt":    "beta",
		"share/CON.txt":           "device name on Windows",
		"share/trailing dot.":     "Windows trims it",
		"share/caf\u00e9.txt":     "nfc",
		"share/sub/..hidden":      "two dots in a name",
		"share/with\\backslash":   "a separator on Windows",
		"share/sub/deep/c<d>.txt": "reserved character on Windows",
	}
	v := localVolume(t, files)
	outside := t.TempDir()
	dest := t.TempDir()
	// A link that waits in the folder under the share's own name, pointing outside.
	if err := os.Symlink(outside, filepath.Join(dest, "share")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	p, err := planCheckout(v, kindFolder, testLimits(1<<20))
	if runtime.GOOS == "windows" {
		if !errors.Is(err, kolofiles.ErrUnsafeName) {
			t.Fatalf("plan on Windows: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var written counter
	var n atomic.Int64
	got, err := testWriter(t, dest).checkout(context.Background(), v, p, &written, &n)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dest, "share (2)") {
		t.Fatalf("saved to %s", got)
	}
	if int(n.Load()) != p.files || written.n.Load() != p.bytes {
		t.Fatalf("counted %d files %d bytes, plan %d %d", n.Load(), written.n.Load(), p.files, p.bytes)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote outside the folder: %v", entries)
	}
	for name, body := range files {
		if strings.HasSuffix(name, "/") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(got, filepath.FromSlash(strings.TrimPrefix(name, "share/"))))
		if err != nil || string(b) != body {
			t.Errorf("%s: %q %v", name, b, err)
		}
	}
	assertInside(t, dest, map[string]bool{filepath.Join(dest, "share"): true})
}

// assertInside walks dir and fails on any link but the allowed ones and any path outside dir.
func assertInside(t *testing.T, dir string, allowedLinks map[string]bool) {
	t.Helper()
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			t.Fatal(err)
		}
		if rel, err := filepath.Rel(dir, p); err != nil || strings.HasPrefix(rel, "..") {
			t.Fatalf("%s is outside %s", p, dir)
		}
		if d.Type()&fs.ModeSymlink != 0 && !allowedLinks[p] {
			t.Fatalf("checkout made a link: %s", p)
		}
		return nil
	})
}

func TestCheckoutCaseCollision(t *testing.T) {
	dest := t.TempDir()
	if caseSensitive(t, dest) {
		t.Skip("the temporary folder is case-sensitive")
	}
	v := localVolume(t, map[string]string{"share/": "", "share/Readme.txt": "one", "share/README.txt": "two"})
	p, err := planCheckout(v, kindFolder, testLimits(1<<20))
	if err != nil {
		t.Fatal(err)
	}
	var written counter
	var n atomic.Int64
	_, err = testWriter(t, dest).checkout(context.Background(), v, p, &written, &n)
	if se := explain(err, limits{}); !errors.Is(err, fs.ErrExist) || se.Kind != kindUnsafeName {
		t.Fatalf("err %v (%+v)", err, se)
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Fatalf("left %v behind", entries)
	}
}

func TestPlanRefuses(t *testing.T) {
	two := localVolume(t, map[string]string{"a.txt": "a", "b.txt": "b"})
	if _, err := planCheckout(two, kindFile, testLimits(1<<20)); !errors.Is(err, ErrWrongKind) {
		t.Fatalf("two top entries: %v", err)
	}
	file := localVolume(t, map[string]string{"a.txt": "a"})
	if _, err := planCheckout(file, kindFolder, testLimits(1<<20)); !errors.Is(err, ErrWrongKind) {
		t.Fatalf("file for a folder code: %v", err)
	}
	if p, err := planCheckout(file, kindFile, testLimits(1<<20)); err != nil || p.files != 1 || p.bytes != 1 || p.dir {
		t.Fatalf("file: %+v %v", p, err)
	}
	big := localVolume(t, map[string]string{"d/": "", "d/x": strings.Repeat("x", 100), "d/y": strings.Repeat("y", 100)})
	if _, err := planCheckout(big, kindFolder, testLimits(150)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over the limit: %v", err)
	}
}

func TestExplainKinds(t *testing.T) {
	for err, kind := range map[error]string{
		context.Canceled:        kindCancelled,
		ErrNotFound:             kindNotFound,
		kolosync.ErrNotFound:    kindCancelled,
		invite.ErrUsedUp:        kindRefused,
		invite.ErrUnreachable:   kindLost,
		ErrPeerProtocol:         kindRefused,
		ErrOfferMismatch:        kindCorrupt,
		ErrTooLarge:             kindTooLarge,
		koloblobs.ErrCorrupt:    kindCorrupt,
		kolofiles.ErrCorrupt:    kindCorrupt,
		kolosync.ErrInvalid:     kindCorrupt,
		ErrIncomplete:           kindCorrupt,
		kolofiles.ErrUnsafeName: kindUnsafeName,
		kolofiles.ErrLimit:      kindLimit,
		kolosync.ErrTimeout:     kindLost,
		kolosync.ErrBusy:        kindLost,
		ErrNotReady:             kindNetwork,
		&fs.PathError{Op: "open", Path: "/x", Err: fs.ErrPermission}: kindDisk,
		errors.New("strange"): kindInternal,
	} {
		se := explain(err, limits{bytes: 32 << 30, nodes: 100})
		if se.Kind != kind {
			t.Errorf("%v: kind %s, want %s", err, se.Kind, kind)
		}
		if se.Message == "" || strings.Contains(se.Message, "goroutine") || strings.Contains(se.Message, "\n") {
			t.Errorf("%v: message %q", err, se.Message)
		}
	}
	if m := explain(ErrTooLarge, limits{bytes: 32 << 30, nodes: 100}).Message; !strings.Contains(m, "32 GiB") {
		t.Fatalf("limit sentence: %q", m)
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 bytes", 1023: "1023 bytes", 1024: "1 KiB", 1536: "1.5 KiB", 32 << 30: "32 GiB"} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestReasons(t *testing.T) {
	kinds := map[string]bool{kindInvalidCode: true, kindBadFolder: true, kindBusy: true, kindNotFound: true, kindRefused: true,
		kindCorrupt: true, kindTooLarge: true, kindUnsafeName: true, kindLimit: true, kindDisk: true, kindLost: true,
		kindNetwork: true, kindConfig: true, kindSource: true, kindCancelled: true, kindInternal: true}
	for reason, r := range reasons {
		if !kinds[r.kind] || r.text == "" || strings.ContainsRune(r.text, 0x2014) {
			t.Errorf("%s: %+v", reason, r)
		}
		se := shareErr(reason, errors.New("kolo-share: the cause"), "/the/detail")
		if se.Reason != reason || se.Kind != r.kind || strings.Contains(se.Message, "{") {
			t.Errorf("%s: %+v", reason, se)
		}
		if strings.Contains(r.text, "{cause}") && !strings.Contains(se.Message, "the cause") || strings.Contains(se.Message, "kolo-share: the") {
			t.Errorf("%s: cause in %q", reason, se.Message)
		}
	}
	se := explain(&fs.PathError{Op: "open", Path: "/x/y", Err: fs.ErrPermission}, limits{})
	if se.Reason != "disk" || se.Detail != "/x/y" || se.Cause != "permission denied" || se.Message != "Could not write /x/y: permission denied." {
		t.Fatalf("disk: %+v", se)
	}
	b, _ := json.Marshal(se)
	if !strings.Contains(string(b), `"reason":"disk"`) || strings.Contains(string(b), "Err") {
		t.Fatalf("json: %s", b)
	}
}

func TestRevealOnlySavedPaths(t *testing.T) {
	dir := t.TempDir()
	saved := filepath.Join(dir, "photo.jpg")
	os.WriteFile(saved, nil, 0o600)
	var shown []string
	s := NewShareService(Config{}, func(string, any) {}, nil)
	s.reveal = func(p string) error { shown = append(shown, p); return nil }
	var err error
	if s.hist, err = openHistory(dir); err != nil {
		t.Fatal(err)
	}
	s.hist.add(HistoryItem{ID: "1", Direction: "receive", Result: stateDone, SavedTo: saved})
	s.hist.add(HistoryItem{ID: "2", Direction: "receive", Result: stateFailed, SavedTo: filepath.Join(dir, "failed")})
	for _, p := range []string{"", "photo.jpg", dir, "/etc/passwd", filepath.Join(dir, "failed"), saved + "/..", filepath.Join(dir, "other")} {
		if err := s.Reveal(p); !errors.Is(err, ErrNotSaved) {
			t.Errorf("Reveal(%q): %v", p, err)
		}
	}
	if err := s.Reveal(filepath.Join(dir, ".", "photo.jpg")); err != nil {
		t.Fatal(err)
	}
	if len(shown) != 1 || shown[0] != filepath.Join(dir, "photo.jpg") {
		t.Fatalf("shown %v", shown)
	}
	os.Remove(saved)
	if err := s.Reveal(saved); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("removed file: %v", err)
	}
}
