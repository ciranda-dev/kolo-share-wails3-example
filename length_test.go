package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	kolostore "github.com/ciranda-dev/kolo-store"
	kolosync "github.com/ciranda-dev/kolo-sync"
)

var (
	linuxRules   = rulesFor("linux")
	darwinRules  = rulesFor("darwin")
	windowsRules = rulesFor("windows")
)

func lengthReason(err error) (reason, detail, cause string) {
	var se *ShareError
	if !errors.As(err, &se) {
		return "", "", ""
	}
	return se.Reason, se.Detail, se.Cause
}

func topError(r lengthRules, name string) error {
	_, err := savedTop(r, name)
	return err
}

func TestRulesOfEverySystem(t *testing.T) {
	if linuxRules.name != 255 || linuxRules.path != 4095 || linuxRules.wide {
		t.Fatalf("linux %+v", linuxRules)
	}
	if darwinRules.name != 255 || darwinRules.path != 1023 || darwinRules.wide {
		t.Fatalf("darwin %+v", darwinRules)
	}
	if windowsRules.name != 0 || windowsRules.path != 2*32767 || !windowsRules.wide {
		t.Fatalf("windows %+v", windowsRules)
	}
	// 100 characters of 3 bytes are 300 bytes and 100 UTF-16 units.
	cjk := strings.Repeat("\u4e2d", 100)
	if linuxRules.measure(cjk) != 300 || windowsRules.measure(cjk) != 200 {
		t.Fatal("measure")
	}
}

func TestSavedPathRefusesANameOverTheLimit(t *testing.T) {
	if _, err := savedPath(linuxRules, "top/"+strings.Repeat("a", 255)); err != nil {
		t.Fatalf("255 bytes: %v", err)
	}
	long := strings.Repeat("secret-", 40)
	_, err := savedPath(linuxRules, "top/"+long[:256])
	reason, detail, cause := lengthReason(err)
	if reason != "name-length" || detail != "256" || cause != "255" || !errors.Is(err, ErrNameLength) {
		t.Fatalf("256 bytes: %v", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("the sentence names the item: %q", err.Error())
	}
	// Three-byte characters: 86 of them are 258 bytes.
	if _, err := savedPath(linuxRules, strings.Repeat("\u4e2d", 86)); !errors.Is(err, ErrNameLength) {
		t.Fatalf("86 CJK characters: %v", err)
	}
	// A name is measured as it is saved: each U+202E becomes one "_".
	if _, err := savedPath(linuxRules, strings.Repeat("\u202e", 100)); err != nil {
		t.Fatalf("cleaned names: %v", err)
	}
	// No rules, no check: the tests that plan with limits{} keep working.
	if _, err := savedPath(lengthRules{}, strings.Repeat("a", 5000)); err != nil {
		t.Fatalf("zero rules: %v", err)
	}
}

func TestTopNameLeavesRoomForACopySuffix(t *testing.T) {
	if err := topError(darwinRules, strings.Repeat("a", 250)); err != nil {
		t.Fatalf("250 bytes: %v", err)
	}
	reason, detail, cause := lengthReason(topError(darwinRules, strings.Repeat("a", 251)))
	if reason != "name-length" || detail != "256" || cause != "255" {
		t.Fatalf("251 bytes: %s %s %s", reason, detail, cause)
	}
	// The room is real: "name (99)" of a 250 byte name is a 255 byte name.
	if got := copyName(strings.Repeat("a", 250), 99, true); len(got) != 255 {
		t.Fatalf("copy of a 250 byte name is %d bytes", len(got))
	}
}

func TestWindowsCountsUTF16AndLeavesNamesToLocalPath(t *testing.T) {
	// 300 bytes but 100 units: no length check of the app, LocalPath checks units on Windows.
	if _, err := savedPath(windowsRules, strings.Repeat("\u4e2d", 100)); err != nil {
		t.Fatalf("100 units: %v", err)
	}
	if err := (plan{longest: 40000, rules: windowsRules}).fits(`C:\Users\ana`); err != nil {
		t.Fatalf("40000 bytes are 20000 units: %v", err)
	}
	reason, _, _ := lengthReason((plan{longest: 66000, rules: windowsRules}).fits(`C:\Users\ana`))
	if reason != "path-length" {
		t.Fatalf("66000 bytes: %q", reason)
	}
}

func TestPlanCountsTheFolderInThePathLimit(t *testing.T) {
	deep := fstest.MapFS{}
	path := "top"
	for range 130 {
		path += "/" + strings.Repeat("d", 7) // 8 bytes a level: about 1050 bytes
		deep[path+"/f"] = &fstest.MapFile{Data: []byte("x")}
	}
	planFor := func(r lengthRules) plan {
		p, err := planCheckout(deep, kindFolder, limits{bytes: 1 << 20, nodes: 1000, rules: r})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if err := planFor(linuxRules).fits("/home/ana/Downloads"); err != nil {
		t.Fatalf("linux takes about 1000 bytes: %v", err)
	}
	err := planFor(darwinRules).fits("/Users/ana/Downloads")
	reason, detail, cause := lengthReason(err)
	if reason != "path-length" || cause != "1023" || detail == "" || !errors.Is(err, ErrPathLength) {
		t.Fatalf("darwin: %v", err)
	}
	// The folder's own length counts: a path that fits in a short folder does not in a long one.
	tight := plan{longest: 1023 - len("/x") - 1, rules: darwinRules}
	if err := tight.fits("/x"); err != nil {
		t.Fatalf("exactly at the limit: %v", err)
	}
	if err := tight.fits("/xx"); err == nil {
		t.Fatal("one byte over the limit")
	}
}

func TestPlanRefusesALongNameInsideTheTree(t *testing.T) {
	m := fstest.MapFS{"top/" + strings.Repeat("n", 300): &fstest.MapFile{Data: []byte("x")}}
	_, err := planCheckout(m, kindFolder, limits{bytes: 1 << 20, nodes: 1000, rules: linuxRules})
	if reason, detail, _ := lengthReason(err); reason != "name-length" || detail != "300" {
		t.Fatalf("plan: %v", err)
	}
	if _, err := planCheckout(m, kindFolder, testLimits(1<<20)); err != nil {
		t.Fatalf("without rules: %v", err)
	}
}

// playRules makes the receiver save under another system's limits; the sender keeps its own.
func playRules(r lengthRules) func(*ShareService) {
	return func(s *ShareService) { s.hooks.rules = &r }
}

func TestOfferWithALongNameIsDeclinedAtOnce(t *testing.T) {
	alice, bob := network(t, Config{}, playRules(lengthRules{name: 40, path: 4095}))
	sa, sb := connect(t, alice, bob)
	src := filepath.Join(t.TempDir(), strings.Repeat("n", 41)+".txt")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	declined(t, alice, sa, src)
	v := bob.rec.waitSession(t, sb, func(v Session) bool { return v.Notice != nil && v.Notice.Reason == "name-length" })
	if v.Notice.Detail != "45" || v.Notice.Cause != "40" {
		t.Fatalf("notice %+v: the length of the name and the limit", v.Notice)
	}
}

// refusedBeforeContent sends src to a receiver that plays rules r and checks that it refuses with reason after
// the metadata and before any content: one pull, nothing in the folder.
func refusedBeforeContent(t *testing.T, r lengthRules, src, name, reason string) {
	t.Helper()
	defer func(f func(context.Context, kolosync.Conn, *kolostore.Replica, *kolosync.PullMeter) error) { pull = f }(pull)
	var pulls atomic.Int32
	pull = func(ctx context.Context, c kolosync.Conn, rep *kolostore.Replica, m *kolosync.PullMeter) error {
		pulls.Add(1)
		return kolosync.Pull(ctx, c, rep)
	}
	alice, bob := network(t, Config{}, playRules(r))
	sa, sb := connect(t, alice, bob)
	offerItem(t, alice, sa, src)
	dest := t.TempDir()
	got := saveOffer(t, bob, sb, name, dest)
	if got.State != stateFailed || got.Error == nil || got.Error.Reason != reason {
		t.Fatalf("receive: %+v %+v", got.State, got.Error)
	}
	if strings.Contains(got.Error.Message, "zzz") {
		t.Fatalf("the sentence names a file: %q", got.Error.Message)
	}
	if n := pulls.Load(); n != 1 {
		t.Fatalf("%d pulls: only the metadata should be pulled", n)
	}
	assertEmpty(t, dest)
}

func TestReceiveRefusesALongNameBeforeTheContent(t *testing.T) {
	src := filepath.Join(t.TempDir(), "docs")
	os.MkdirAll(filepath.Join(src, "sub"), 0o700)
	if err := os.WriteFile(filepath.Join(src, "sub", strings.Repeat("zzz", 20)), randomBytes(t, 3<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	refusedBeforeContent(t, lengthRules{name: 50, path: 4095}, src, "docs", "name-length")
}

func TestReceiveRefusesALongPathBeforeTheContent(t *testing.T) {
	src := filepath.Join(t.TempDir(), "docs")
	deep := src
	for range 12 {
		deep = filepath.Join(deep, "level-zzz")
	}
	os.MkdirAll(deep, 0o700)
	if err := os.WriteFile(filepath.Join(deep, "f"), randomBytes(t, 3<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	refusedBeforeContent(t, lengthRules{name: 255, path: 100}, src, "docs", "path-length")
}

func TestReceiveStillSavesAtTheLimit(t *testing.T) {
	alice, bob := network(t, Config{}, playRules(lengthRules{name: 255, path: 4095}))
	sa, sb := connect(t, alice, bob)
	src := filepath.Join(t.TempDir(), "docs")
	os.Mkdir(src, 0o700)
	name := strings.Repeat("n", 200)
	if err := os.WriteFile(filepath.Join(src, name), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	offerItem(t, alice, sa, src)
	dest := t.TempDir()
	if got := saveOffer(t, bob, sb, "docs", dest); got.State != stateDone {
		t.Fatalf("receive: %+v %+v", got.State, got.Error)
	}
	if _, err := os.Stat(filepath.Join(dest, "docs", name)); err != nil {
		t.Fatal(err)
	}
}
