package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	kolofiles "github.com/ciranda-dev/kolo-files"
	kololog "github.com/ciranda-dev/kolo-log"
	kolostore "github.com/ciranda-dev/kolo-store"
	kolosync "github.com/ciranda-dev/kolo-sync"
)

func TestRevealCommandNeverOpens(t *testing.T) {
	dir := "/home/ana/Downloads"
	for _, name := range []string{"Invoice.app", "$HOME", "%PATH%", "-x", "a --some-flag", "line\nbreak", "--", "a b"} {
		p := filepath.Join(dir, name)
		mac := revealCommand("darwin", p)
		if mac.name != "open" || len(mac.args) != 2 || mac.args[0] != "-R" || mac.args[1] != p {
			t.Errorf("darwin %q: %+v", name, mac)
		}
		lin := revealCommand("linux", p)
		if lin.name != "xdg-open" || len(lin.args) != 1 || lin.args[0] != dir {
			t.Errorf("linux %q: %+v (the name must not reach the file manager)", name, lin)
		}
	}
	win := revealCommand("windows", `C:\Users\ana\Downloads\$HOME %PATH% -x.app`)
	if win.name != "explorer.exe" || len(win.args) != 1 || win.args[0] != `/select,"C:\Users\ana\Downloads\$HOME %PATH% -x.app"` {
		t.Fatalf("windows: %+v", win)
	}
}

func TestRevealPassesTheSavedItem(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "Invoice.app")
	dollar := filepath.Join(dir, "$HOME")
	os.MkdirAll(filepath.Join(bundle, "Contents", "MacOS"), 0o700)
	os.WriteFile(dollar, nil, 0o600)
	s := NewShareService(Config{}, func(string, any) {}, nil)
	var got []string
	s.reveal = func(p string) error { got = append(got, p); return nil }
	var err error
	if s.hist, err = openHistory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{bundle, dollar} {
		s.hist.add(HistoryItem{Direction: "receive", Result: stateDone, SavedTo: p})
		if err := s.Reveal(p); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 || got[0] != bundle || got[1] != dollar {
		t.Fatalf("revealed %q", got)
	}
}

func TestPlanLimitsFilesAndFolders(t *testing.T) {
	m := fstest.MapFS{}
	for i := range 20 {
		m["top/"+string(rune('a'+i))] = &fstest.MapFile{}
	}
	_, err := planCheckout(m, kindFolder, limits{bytes: 1, nodes: 10})
	if !errors.Is(err, ErrTooManyFiles) || explain(err, limits{nodes: 10}).Reason != "too-many-files" {
		t.Fatalf("20 empty files with a limit of 10: %v", err)
	}
	if p, err := planCheckout(m, kindFolder, limits{bytes: 1, nodes: 21}); err != nil || p.files != 20 || p.nodes != 21 {
		t.Fatalf("at the limit: %+v %v", p, err)
	}
	lim := limits{bytes: 32 << 30, nodes: defaultMaxFiles}
	if lim.metaEntries() != defaultMaxFiles+1 || lim.contentEntries() != 4*defaultMaxFiles+2*(32<<30)/(256<<10)+64 {
		t.Fatalf("entry limits %d %d", lim.metaEntries(), lim.contentEntries())
	}
}

func TestScanSourceLimits(t *testing.T) {
	src := t.TempDir()
	for i := range 5 {
		os.WriteFile(filepath.Join(src, string(rune('a'+i))), []byte("x"), 0o600)
	}
	os.Symlink(src, filepath.Join(src, "link"))
	_, err := scanSource(src, 3)
	var se *ShareError
	if !errors.As(err, &se) || se.Reason != "source-too-many" || se.Detail != "3" {
		t.Fatalf("over the limit: %v", err)
	}
	sc, err := scanSource(src, 10)
	if err != nil || sc.files != 5 || sc.nodes != 6 || sc.skipped != 1 || sc.bytes != 5 {
		t.Fatalf("scan: %+v %v", sc, err)
	}
}

// lyingOffer offers a real volume of 10 empty files under an offer that claims files, so the receiver's bounds
// come from the offer and not from its own limits.
func lyingOffer(t *testing.T, alice, bob *peer, files int) (Transfer, string) {
	t.Helper()
	sa, sb := connect(t, alice, bob)
	alice.hooks.tamperOffer = func(o *offer) { o.nodes, o.files = files+1, files }
	src := filepath.Join(t.TempDir(), "many")
	os.Mkdir(src, 0o700)
	for i := range 10 {
		os.WriteFile(filepath.Join(src, string(rune('a'+i))), nil, 0o600)
	}
	offerItem(t, alice, sa, src)
	dest := t.TempDir()
	return saveOffer(t, bob, sb, "many", dest), dest
}

func TestReceiveRefusesTooManyEntriesBeforePulling(t *testing.T) {
	defer func(f func(context.Context, kolosync.Conn, *kolostore.Replica) error) { pull = f }(pull)
	var pulls atomic.Int32
	pull = func(ctx context.Context, c kolosync.Conn, r *kolostore.Replica) error {
		pulls.Add(1)
		return kolosync.Pull(ctx, c, r)
	}
	alice, bob := network(t, Config{}, nil)
	// 10 files offered as 3: the metadata head is over what an item of 3 files can have.
	got, dest := lyingOffer(t, alice, bob, 3)
	if got.State != stateFailed || got.Error.Reason != "too-many-entries" {
		t.Fatalf("receive: %+v", got.Error)
	}
	if pulls.Load() != 0 {
		t.Fatalf("%d pulls: the head should stop it first", pulls.Load())
	}
	assertEmpty(t, dest)
}

func TestLogThatGrowsAfterItsHead(t *testing.T) {
	defer func(f func(kololog.VerifiedHead) uint64) { headEntries = f }(headEntries)
	headEntries = func(kololog.VerifiedHead) uint64 { return 0 } // every head looks empty
	alice, bob := network(t, Config{}, nil)
	got, dest := lyingOffer(t, alice, bob, 3)
	if got.State != stateFailed || got.Error.Reason != "too-many-entries" {
		t.Fatalf("receive: %+v", got.Error)
	}
	assertEmpty(t, dest)
}

func TestNoPairingWhileRestarting(t *testing.T) {
	_, bob := network(t, Config{}, nil)
	if !bob.beginRestart() {
		t.Fatal("beginRestart")
	}
	var se *ShareError
	if _, err := bob.CreatePairing(inMain); !errors.As(err, &se) || se.Reason != "busy-restarting" {
		t.Fatalf("create: %v", err)
	}
	if _, err := bob.JoinPairing(showCodeText(t)); !errors.As(err, &se) || se.Reason != "busy-restarting" {
		t.Fatalf("join: %v", err)
	}
	bob.endRestart()
	if bob.beginRestart() != true {
		t.Fatal("a second restart after the first ended")
	}
	bob.endRestart()
}

// watchFS calls before when name is opened.
type watchFS struct {
	fs.FS
	before func(name string)
}

func (w watchFS) Open(name string) (fs.File, error) {
	w.before(name)
	return w.FS.Open(name)
}

func TestCheckoutWritesUnderAPartName(t *testing.T) {
	dest := t.TempDir()
	m := fstest.MapFS{"top/a.txt": {Data: []byte("a")}, "top/b.txt": {Data: []byte("b")}}
	var during []string
	var w writer
	pendingDuring := 0
	v := watchFS{m, func(name string) {
		if name == "top/b.txt" {
			pendingDuring = len(w.pending.items)
			entries, _ := os.ReadDir(dest)
			for _, e := range entries {
				during = append(during, e.Name())
			}
		}
	}}
	w = testWriter(t, dest)
	p, err := planCheckout(m, kindFolder, testLimits(1<<20))
	if err != nil {
		t.Fatal(err)
	}
	var written counter
	var n atomic.Int64
	got, err := w.checkout(context.Background(), v, p, &written, &n)
	if pendingDuring != 1 {
		t.Fatalf("pending while writing: %d", pendingDuring)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(during) != 2 || during[1] != "top" || !ownPart(during[0]) {
		t.Fatalf("while writing the folder held %v", during)
	}
	if empty, _ := os.ReadDir(filepath.Join(dest, during[1])); len(empty) != 2 || got != filepath.Join(dest, "top") {
		t.Fatalf("after: %v %s", empty, got)
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 1 {
		t.Fatalf("left %v", entries)
	}
	if len(w.pending.items) != 0 {
		t.Fatalf("pending %v", w.pending.items)
	}
}

func TestSweepAfterACrash(t *testing.T) {
	data, dest := t.TempDir(), t.TempDir()
	root, _ := os.OpenRoot(dest)
	defer root.Close()
	// A receive that was writing: its part and its empty placeholder.
	root.MkdirAll(".kolo-share-0011.part/sub", 0o700)
	root.WriteFile(".kolo-share-0011.part/sub/half.bin", []byte("half"), 0o600)
	root.Mkdir("photos", 0o700)
	// A receive that had finished its rename: its result stays.
	root.WriteFile("done.txt", nil, 0o600)
	// Hostile entries are not followed.
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "keep"), nil, 0o600)
	items := []pendingEntry{
		{Folder: dest, Part: ".kolo-share-0011.part", Top: "photos"},
		{Folder: dest, Part: ".kolo-share-0022.part", Top: "done.txt"},
		{Folder: dest, Part: "../" + filepath.Base(outside), Top: "x"},
		{Folder: outside, Part: "keep", Top: "keep"},
		{Folder: "relative", Part: ".kolo-share-1.part", Top: "x"},
	}
	b, _ := json.Marshal(items)
	os.WriteFile(filepath.Join(data, pendingFile), b, 0o600)
	l, err := sweepPending(data)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 1 || entries[0].Name() != "done.txt" {
		t.Fatalf("left %v", entries)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal("the sweep left the folder of an entry")
	}
	if len(l.items) != 0 {
		t.Fatalf("pending after the sweep: %v", l.items)
	}
}

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"invoice\u202egpj.exe": "invoice_gpj.exe",
		"a\u2066b\u2069c":      "a_b_c",
		"\u200eleft\u200f":     "_left_",
		"zero\u200bwidth":      "zero_width",
		"tab\there":            "tab_here",
		"line\nbreak":          "line_break",
		"caf\u00e9 \u65e5.txt": "caf\u00e9 \u65e5.txt",
	} {
		if got := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
	dest := t.TempDir()
	v := localVolume(t, map[string]string{"top/": "", "top/photo\u202egpj.exe": "x"})
	p, err := planCheckout(v, kindFolder, testLimits(1<<20))
	if err != nil {
		t.Fatal(err)
	}
	var written counter
	var n atomic.Int64
	got, err := testWriter(t, dest).checkout(context.Background(), v, p, &written, &n)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(got, "photo_gpj.exe")); err != nil {
		t.Fatalf("saved name: %v", err)
	}
	two := localVolume(t, map[string]string{"top/": "", "top/a\u200b.txt": "1", "top/a\u200c.txt": "2"})
	p, _ = planCheckout(two, kindFolder, testLimits(1<<20))
	dest = t.TempDir()
	if _, err := testWriter(t, dest).checkout(context.Background(), two, p, &written, &n); explain(err, limits{}).Reason != "name-collision" {
		t.Fatalf("two names that clean to one: %v", err)
	}
	assertEmpty(t, dest)
}

func TestCheckoutMarksEverything(t *testing.T) {
	dest := t.TempDir()
	v := localVolume(t, map[string]string{"top/": "", "top/a": "a", "top/sub/": "", "top/sub/b": "b"})
	var marked []string
	w := testWriter(t, dest)
	w.mark = func(f *os.File) error {
		rel, _ := filepath.Rel(dest, f.Name())
		marked = append(marked, filepath.ToSlash(rel))
		return errors.New("no marks on this disk") // logged once, never fatal
	}
	p, _ := planCheckout(v, kindFolder, testLimits(1<<20))
	var written counter
	var n atomic.Int64
	if _, err := w.checkout(context.Background(), v, p, &written, &n); err != nil {
		t.Fatal(err)
	}
	if len(marked) != 4 {
		t.Fatalf("marked %v", marked)
	}
	if q := quarantineValue(time.Unix(0x6538, 0)); q != "0081;6538;Kolo Share;" {
		t.Fatalf("quarantine value %q", q)
	}
	if zoneIdentifier != "[ZoneTransfer]\r\nZoneId=3\r\n" {
		t.Fatal("zone identifier")
	}
}

func TestNotchOptionsHidden(t *testing.T) {
	o := notchOptions()
	if !o.WindowOptions.Hidden || o.WindowOptions.Name != notchWindowName || !o.Animated {
		t.Fatalf("%+v", o.WindowOptions)
	}
}

type namedWindow struct{ name string }

func (w namedWindow) Name() string { return w.name }

func TestTrayUsable(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "bus")
	up := func(p string) error {
		if p == sock || p == "@abstract" {
			return nil
		}
		return os.ErrNotExist
	}
	for _, c := range []struct {
		goos string
		env  map[string]string
		want bool
	}{
		{"darwin", nil, true},
		{"windows", nil, true},
		{"linux", nil, false},
		{"linux", map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:path=/nowhere"}, false},
		{"linux", map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:path=" + sock + ",guid=1"}, true},
		{"linux", map[string]string{"DBUS_SESSION_BUS_ADDRESS": "tcp:host=x;unix:abstract=abstract"}, true},
		{"linux", map[string]string{"XDG_RUNTIME_DIR": filepath.Dir(sock)}, true},
	} {
		if got := trayUsable(c.goos, env(c.env), up); got != c.want {
			t.Errorf("%s %v: %v, want %v", c.goos, c.env, got, c.want)
		}
	}
}

func TestDataDirChecks(t *testing.T) {
	if runtime.GOOS != "windows" {
		open := filepath.Join(t.TempDir(), "open")
		os.Mkdir(open, 0o700)
		os.Chmod(open, 0o755)
		var se *ShareError
		if err := prepareDataDir(open); !errors.As(err, &se) || se.Reason != "data-dir-open" {
			t.Fatalf("open data dir: %v", err)
		}
	}
	foreign := t.TempDir()
	os.MkdirAll(filepath.Join(foreign, transfersDir), 0o700)
	os.WriteFile(filepath.Join(foreign, transfersDir, "notes.txt"), []byte("mine"), 0o600)
	var se *ShareError
	if err := resetTransfers(foreign); !errors.As(err, &se) || se.Reason != "data-dir-foreign" {
		t.Fatalf("foreign transfers: %v", err)
	}
	if _, err := os.Stat(filepath.Join(foreign, transfersDir, "notes.txt")); err != nil {
		t.Fatal("a foreign file was removed")
	}
	ours := t.TempDir()
	if err := resetTransfers(ours); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(ours, transfersDir, "7", "store"), 0o700)
	if err := resetTransfers(ours); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(ours, transfersDir)); len(entries) != 1 || entries[0].Name() != transfersMarker {
		t.Fatalf("after the reset: %v", entries)
	}
}

func TestExplicitZeroLimits(t *testing.T) {
	for _, args := range [][]string{{"-max-download", "0"}, {"-max-files", "0"}} {
		c, err := LoadConfig(args, env(nil), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.resolve(); !errors.Is(err, ErrConfig) {
			t.Fatalf("%v: %v", args, err)
		}
	}
	c, _ := LoadConfig([]string{"-max-files", "12"}, env(nil), io.Discard)
	if s, err := c.resolve(); err != nil || s.MaxFiles != 12 {
		t.Fatalf("max-files: %+v %v", s.MaxFiles, err)
	}
}

func TestUnsafeLocalPathIsNotCorrupt(t *testing.T) {
	se := explain(&fs.PathError{Op: "localpath", Path: "bad\u202ename", Err: fs.ErrInvalid}, limits{})
	if se.Reason != "unsafe-name" || strings.ContainsRune(se.Detail, '\u202e') {
		t.Fatalf("%+v", se)
	}
}

func TestPlanChecksEveryNameItself(t *testing.T) {
	// Windows refuses CON.txt; play that here, where LocalPath allows every cleaned name.
	defer func(f func(string) (string, error)) { localPath = f }(localPath)
	localPath = func(name string) (string, error) {
		if strings.Contains(name, "CON.txt") {
			return "", &fs.PathError{Op: "localpath", Path: name, Err: kolofiles.ErrUnsafeName}
		}
		return kolofiles.LocalPath(name)
	}
	m := fstest.MapFS{"top/ok.txt": {Data: []byte("1")}, "top/CON.txt": {Data: []byte("2")}}
	if _, err := planCheckout(m, kindFolder, testLimits(1<<20)); explain(err, limits{}).Reason != "unsafe-name" {
		t.Fatalf("plan: %v", err)
	}
	// And the writer checks again, for a plan made elsewhere.
	dest := t.TempDir()
	var written counter
	var n atomic.Int64
	p := plan{top: "top", local: "top", dir: true, nodes: 3, files: 2, bytes: 2}
	if _, err := testWriter(t, dest).checkout(context.Background(), m, p, &written, &n); explain(err, limits{}).Reason != "unsafe-name" {
		t.Fatalf("checkout: %v", err)
	}
	assertEmpty(t, dest)
	// Invalid UTF-8 is cleaned to U+FFFD, not refused.
	if got, err := savedPath(lengthRules{}, "bad\xff.txt"); err != nil || got != "bad\ufffd.txt" {
		t.Fatalf("savedPath: %q %v", got, err)
	}
}

func TestCaseOnlyFolders(t *testing.T) {
	dest := t.TempDir()
	if caseSensitive(t, dest) {
		t.Skip("the temporary folder is case-sensitive")
	}
	v := localVolume(t, map[string]string{"top/": "", "top/Docs/": "", "top/docs/": "", "top/Docs/a": "1", "top/docs/b": "2"})
	p, _ := planCheckout(v, kindFolder, testLimits(1<<20))
	var written counter
	var n atomic.Int64
	_, err := testWriter(t, dest).checkout(context.Background(), v, p, &written, &n)
	se := explain(err, limits{})
	if se.Reason != "name-collision" || !strings.HasPrefix(strings.ToLower(se.Detail), "top/docs") || strings.Contains(se.Message, partPrefix) {
		t.Fatalf("Docs and docs: %+v", se)
	}
	assertEmpty(t, dest)
}

func TestErrorsNameThePathInTheShare(t *testing.T) {
	part := partPrefix + "00ff" + partSuffix
	err := inShare(&fs.PathError{Op: "mkdir", Path: part + "/docs/a", Err: fs.ErrPermission}, part, "photos")
	se := explain(err, limits{})
	if se.Reason != "disk" || se.Detail != "photos/docs/a" || strings.Contains(se.Message, partPrefix) {
		t.Fatalf("%+v", se)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatal("the cause is lost")
	}
	other := errors.New("not a path")
	if inShare(other, part, "photos") != other {
		t.Fatal("other errors must pass as they are")
	}
}

func TestNotchStateNeverFollowsOff(t *testing.T) {
	var mu sync.Mutex
	var events []NotchState
	s := startPeer(t, Config{First: freeUDP(t)}, func(s *ShareService) {
		s.emitNotch = func(_ string, data any) {
			mu.Lock()
			events = append(events, data.(NotchState))
			mu.Unlock()
		}
	})
	s.mu.Lock()
	s.lastDone, s.lastDoneAt = &Transfer{ID: "9", Direction: "receive", Kind: kindFile, Name: "a.txt", State: stateDone}, time.Now()
	s.mu.Unlock()
	// Between reading the state and sending it, the user turns the setting off.
	s.hooks.notchHook = func() {
		s.hooks.notchHook = nil
		done := make(chan struct{})
		go func() { s.SetShowInNotch(false); close(done) }()
		select {
		case <-done: // only possible when the send and the setting are not ordered
		case <-time.After(300 * time.Millisecond):
		}
	}
	s.sendNotch()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(events)
		last := NotchState{}
		if n > 0 {
			last = events[n-1]
		}
		mu.Unlock()
		if n >= 2 {
			if last.ID != "" {
				t.Fatalf("a state came after the empty one: %+v", events)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("events %+v", events)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSourceLinkReason(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skip(err)
	}
	var se *ShareError
	if _, _, err := checkSource(link); !errors.As(err, &se) || se.Reason != "source-link" {
		t.Fatalf("%v", err)
	}
}

func TestTrayImagesEmbedded(t *testing.T) {
	for goos, wantTemplate := range map[string]bool{"darwin": true, "windows": false, "linux": false} {
		icon, active, template := trayImages(goos)
		if len(icon) < 8 || len(active) < 8 || template != wantTemplate || string(icon[1:4]) != "PNG" || string(active[1:4]) != "PNG" {
			t.Errorf("%s: %d %d bytes, template %v", goos, len(icon), len(active), template)
		}
		if string(icon) == string(active) {
			t.Errorf("%s: the active icon is the idle one", goos)
		}
	}
}

func TestOpenRegularRefusesALinkPlantedAfterTheWalk(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret.txt")
	os.WriteFile(secret, []byte("not for sharing"), 0o600)
	plain := filepath.Join(dir, "plain.txt")
	os.WriteFile(plain, []byte("ok"), 0o600)
	f, err := openRegular(plain)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	// The file the walk saw is replaced by a link to another file the person can read.
	os.Remove(plain)
	if err := os.Symlink(secret, plain); err != nil {
		t.Skip("cannot make a link here:", err)
	}
	f, err = openRegular(plain)
	var se *ShareError
	if !errors.As(err, &se) || se.Reason != "source-link" {
		t.Fatalf("a link in place of a file: %v", err)
	}
	if f != nil {
		t.Fatal("the link was opened")
	}
	if _, err := openRegular(filepath.Join(dir, "missing")); !errors.As(err, &se) || se.Reason != "source-unreadable" {
		t.Fatalf("missing file: %v", err)
	}
}
