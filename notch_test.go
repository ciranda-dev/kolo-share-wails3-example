package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	koloblobs "github.com/ciranda-dev/kolo-blobs"
	koloid "github.com/ciranda-dev/kolo-id"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestNotchNeverGetsTheCode(t *testing.T) {
	var mu sync.Mutex
	var notchEvents []string
	var copied string
	alice, bob := network(t, Config{}, nil)
	alice.emitNotch = func(name string, data any) {
		b, _ := json.Marshal(data)
		mu.Lock()
		notchEvents = append(notchEvents, name+" "+string(b))
		mu.Unlock()
	}
	alice.copyText = func(s string) { mu.Lock(); copied = s; mu.Unlock() }
	code := showCode(t, alice)
	if n := alice.Notch(); !n.HasCode || n.ID != "" || n.State != pairWaiting {
		t.Fatalf("notch state %+v", n)
	}
	alice.NotchCopyCode()
	mu.Lock()
	if copied != code.Code {
		t.Fatal("NotchCopyCode did not copy the pairing code")
	}
	mu.Unlock()
	inNotch := context.WithValue(context.Background(), application.WindowKey, namedWindow{notchWindowName})
	if p := alice.Pairing(inMain); p.Code != code.Code {
		t.Fatalf("main window: %+v", p)
	}
	for _, ctx := range []context.Context{inNotch, context.Background()} {
		if p := alice.Pairing(ctx); p.Code != "" || p.State != pairWaiting {
			t.Fatalf("other window: %+v", p)
		}
	}
	pasteCode(t, bob, code.Code)
	ja := alice.rec.waitPairing(t, code.ID, final)
	alice.rec.waitSession(t, ja.SessionID, connected)
	alice.mu.Lock()
	ss := alice.sessions[ja.SessionID]
	secrets := []string{code.Code, string(ss.tag[:]), string(ss.topic[:]), fmt.Sprintf("%x", ss.tag), fmt.Sprintf("%x", ss.topic)}
	alice.mu.Unlock()

	// Off: nothing more reaches the notch but one empty state.
	if _, err := alice.SetShowInNotch(false); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	before := len(notchEvents)
	mu.Unlock()
	sent := offerItem(t, alice, ja.SessionID, writeFile(t, "a.txt", []byte("a")))
	alice.CancelTransfer(sent.ID)
	alice.rec.wait(t, sent.ID, stateStopped)
	if n := alice.Notch(); n.ID != "" {
		t.Fatalf("Notch() with the setting off: %+v", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(notchEvents) != before {
		t.Fatalf("notch events with the setting off: %v", notchEvents[before:])
	}
	if len(notchEvents) == 0 {
		t.Fatal("the notch got nothing while on")
	}
	for _, e := range notchEvents {
		for _, x := range secrets {
			if strings.Contains(e, x) {
				t.Fatalf("the notch got a secret: %s", e)
			}
		}
	}
}

type countingNotch struct{ n *atomic.Int32 }

func (c countingNotch) Show() { c.n.Add(1) }
func (c countingNotch) Hide() {}

func TestNotchShowsOffer(t *testing.T) {
	var shown, opened atomic.Int32
	alice, bob := network(t, Config{}, func(s *ShareService) {
		s.notch = countingNotch{&shown}
		s.openApp = func() { opened.Add(1) }
	})
	sa, sb := connect(t, alice, bob)
	offerItem(t, alice, sa, writeFile(t, "first.txt", []byte("1")))
	incoming(t, bob, sb, "first.txt")
	offerItem(t, alice, sa, writeFile(t, "second.txt", []byte("22")))
	incoming(t, bob, sb, "second.txt")
	n := bob.Notch()
	// Two offers in a row open the notch once.
	if !n.HasOffer || n.Name != "first.txt" || n.PeerName == "" || n.BytesTotal != 1 || n.Others != 1 || shown.Load() != 1 {
		t.Fatalf("the notch shows %+v (shown %d)", n, shown.Load())
	}
	if tr := trayNow(bob.ShareService); !tr.Active || !strings.Contains(tr.Status, "wants to send you first.txt, 1 bytes") || !strings.HasPrefix(tr.Tooltip, "Kolo Share: ") {
		t.Fatalf("tray: %+v", tr)
	}
}

func TestNotchSaveActsOnShownOffer(t *testing.T) {
	var opened atomic.Int32
	alice, bob := network(t, Config{}, func(s *ShareService) { s.openApp = func() { opened.Add(1) } })
	sa, sb := connect(t, alice, bob)
	sentA := offerItem(t, alice, sa, writeFile(t, "holiday.jpg", []byte("1")))
	incoming(t, bob, sb, "holiday.jpg")
	offerItem(t, alice, sa, writeFile(t, "other.bin", []byte("22")))
	incoming(t, bob, sb, "other.bin")
	dest := t.TempDir()
	bob.SetSessionFolder(sb, dest)
	shown := bob.Notch()
	if shown.Name != "holiday.jpg" || !shown.HasOffer {
		t.Fatalf("the notch shows %+v", shown)
	}
	// The sender withdraws the item the notch shows, and the click arrives before the notch redraws: nothing
	// else may be saved in its place.
	alice.CancelTransfer(sentA.ID)
	bob.rec.wait(t, shown.ID, stateWithdrawn)
	bob.NotchSaveOffer(shown.ID)
	// The save that cannot run opens the app instead; once it has, nothing is left to write.
	poll(t, "the app opens for a save that cannot run", func() bool { return opened.Load() >= 1 })
	assertEmpty(t, dest)
	if opened.Load() != 1 {
		t.Fatalf("a save that could not run opened the app %d times", opened.Load())
	}
	if tr := bob.rec.find(t, "other.bin", func(tr Transfer) bool { return tr.Name == "other.bin" }); tr.State != stateOffered {
		t.Fatalf("an item the person never saw moved: %+v", tr)
	}

	// Save without a folder opens the app; with one it saves the offer the notch shows.
	n := bob.Notch()
	bob.mu.Lock()
	bob.sessions[sb].s.Folder = ""
	bob.mu.Unlock()
	bob.NotchSaveOffer(n.ID)
	if opened.Load() != 2 {
		t.Fatal("save without a folder did not open the app")
	}
	bob.SetSessionFolder(sb, dest)
	bob.NotchSaveOffer(n.ID)
	if got := bob.rec.wait(t, n.ID, stateDone, stateFailed); got.State != stateDone || got.Name != "other.bin" {
		t.Fatalf("other.bin: %+v", got)
	}
	third := offerItem(t, alice, sa, writeFile(t, "third.txt", []byte("3")))
	n = bob.Notch()
	bob.NotchDeclineOffer(n.ID)
	if tr := bob.rec.wait(t, n.ID, stateDeclined); tr.Name != "third.txt" {
		t.Fatalf("decline: %+v", tr)
	}
	alice.rec.wait(t, third.ID, stateDeclined)
	if entries, _ := os.ReadDir(dest); len(entries) != 1 {
		t.Fatalf("folder holds %v", entries)
	}
}

func TestNotchAndTrayMapping(t *testing.T) {
	if n := pickNotch(nil, nil, false); n.ID != "" || n.Active {
		t.Fatalf("nothing: %+v", n)
	}
	send := Transfer{ID: "1", Direction: "send", Kind: kindFile, Name: "a.jpg", State: stateOffered, PeerName: "Bob", BytesTotal: 10, Started: 1}
	newer := Transfer{ID: "2", Direction: "send", Kind: kindFolder, Name: "pics", State: stateSending, PeerName: "Bob", BytesDone: 2048, BytesTotal: 4096, Started: 2}
	recv := Transfer{ID: "3", Direction: "receive", Kind: kindFolder, Name: "docs", State: stateVerifying, BytesDone: 25, BytesTotal: 100, FilesTotal: 3, Started: 0}
	offerOld := Transfer{ID: "4", Direction: "receive", Kind: kindFile, Name: "old.txt", State: stateOffered, PeerName: "Ana", BytesTotal: 5, Started: 5}
	offerNew := Transfer{ID: "5", Direction: "receive", Kind: kindFile, Name: "new.txt", State: stateOffered, PeerName: "Ana", BytesTotal: 7, Started: 9}

	if n := pickNotch([]Transfer{send, newer}, nil, true); n.ID != "2" || n.HasCode || n.Others != 1 || !n.Active || n.Percent != 50 {
		t.Fatalf("newest send: %+v", n)
	}
	if n := pickNotch([]Transfer{send, recv, newer}, nil, false); n.ID != "3" || n.Percent != 25 || n.Others != 2 {
		t.Fatalf("receive first: %+v", n)
	}
	if n := pickNotch([]Transfer{send, recv, offerNew, offerOld, newer}, nil, true); n.ID != "4" || !n.HasOffer || n.PeerName != "Ana" || n.BytesTotal != 5 {
		t.Fatalf("the oldest offer first: %+v", n)
	}
	if n := pickNotch(nil, nil, true); n.ID != "" || !n.HasCode || n.State != pairWaiting {
		t.Fatalf("a waiting code: %+v", n)
	}
	if n := notchOf(send, 0); n.HasOffer || n.Percent != unknownPercent {
		t.Fatalf("an own offer is not one to answer: %+v", n)
	}
	recv.State, recv.BytesTotal = stateReceiving, 0
	if n := pickNotch([]Transfer{recv}, nil, false); n.Percent != unknownPercent {
		t.Fatalf("size unknown: %+v", n)
	}
	done := Transfer{ID: "6", Direction: "receive", Kind: kindFolder, State: stateDone, FilesTotal: 12}
	if n := pickNotch(nil, &done, false); n.Active || n.Message != "Saved 12 files." || n.Percent != 100 || n.FilesTotal != 12 {
		t.Fatalf("done: %+v", n)
	}
	done.Kind, done.Name = kindFile, "a.jpg"
	if n := notchOf(done, 0); n.Message != "Saved a.jpg." {
		t.Fatalf("done file: %+v", n)
	}
	failed := Transfer{ID: "7", State: stateFailed, Error: explain(koloblobs.ErrCorrupt, limits{})}
	if n := notchOf(failed, 0); n.Message != failed.Error.Message || n.Active || n.Error == nil || n.Error.Reason != "corrupt" {
		t.Fatalf("failed: %+v", n)
	}
	if n := notchOf(Transfer{State: stateDelivered, Name: "a.jpg", PeerName: "Bob"}, 0); n.Message != "Bob saved a.jpg." {
		t.Fatalf("delivered: %+v", n)
	}
	for state, msg := range map[string]string{stateStopped: "Stopped.", stateDeclined: "Declined.", stateWithdrawn: "The sender cancelled it."} {
		if n := notchOf(Transfer{State: state}, 0); n.Message != msg {
			t.Fatalf("%s: %+v", state, n)
		}
	}

	if tr := trayOf(nil, "", 0); tr.Active || tr.Status != "Idle" || tr.Code != "" || tr.Tooltip != "Kolo Share" {
		t.Fatalf("idle tray: %+v", tr)
	}
	if tr := trayOf(nil, "koloinv0x", 0); tr.Active || tr.Status != "Waiting for your pairing code to be used" || tr.Code != "koloinv0x" {
		t.Fatalf("code tray: %+v", tr)
	}
	if tr := trayOf(nil, "", 3); tr.Status != "Connected to 3 computers" {
		t.Fatalf("sessions tray: %+v", tr)
	}
	if tr := trayOf(nil, "", 1); tr.Status != "Connected to 1 computer" {
		t.Fatalf("one session tray: %+v", tr)
	}
	if tr := trayOf([]Transfer{send, newer}, "", 1); !tr.Active || tr.Status != "Sending pics, 2 KiB received (+1)" || tr.Tooltip != "Kolo Share" {
		t.Fatalf("sending tray: %+v", tr)
	}
	recv.State, recv.BytesTotal, recv.BytesDone = stateVerifying, 200, 90
	if tr := trayOf([]Transfer{recv}, "", 1); tr.Status != "Receiving docs, 45%" {
		t.Fatalf("receiving tray: %+v", tr)
	}
	if tr := trayOf([]Transfer{send}, "", 1); tr.Status != "Offering a.jpg, waiting for Bob" {
		t.Fatalf("offered tray: %+v", tr)
	}
	// An offer shows its sender, name and size only.
	if tr := trayOf([]Transfer{offerOld}, "", 1); !tr.Active || tr.Status != "Ana wants to send you old.txt, 5 bytes" || tr.Tooltip != "Kolo Share: Ana wants to send you old.txt, 5 bytes" {
		t.Fatalf("offer tray: %+v", tr)
	}
}

func TestNothingOutsideStartAndShutdown(t *testing.T) {
	s := NewShareService(Config{DataDir: filepath.Join(t.TempDir(), "data")}, func(string, any) {}, nil)
	var se *ShareError
	if _, err := s.newPairing(roleInviter, pairStarting); !errors.As(err, &se) || se.Reason != "network-not-ready" {
		t.Fatalf("before Start: %v", err)
	}
	if _, err := s.CreatePairing(inMain); !errors.As(err, &se) {
		t.Fatalf("CreatePairing before Start: %v", err)
	}
	s.hooks.identity, _ = koloid.Generate()
	if _, err := s.Start(); err != nil {
		t.Fatal(err)
	}
	s.ServiceShutdown()
	if _, err := s.newPairing(roleInviter, pairStarting); !errors.As(err, &se) || se.Reason != "app-closed" {
		t.Fatalf("after shutdown: %v", err)
	}
	if _, err := s.newSession(make(ed25519.PublicKey, ed25519.PublicKeySize), "x", [sessionSecretSize]byte{}); !errors.As(err, &se) || se.Reason != "app-closed" {
		t.Fatalf("a session after shutdown: %v", err)
	}
}

// trayNow is what the tray icon shows now.
func trayNow(s *ShareService) TrayState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trayLocked()
}

// fakeClipboard is the clipboard of an app in a test.
type fakeClipboard struct {
	mu   sync.Mutex
	text string
}

func (c *fakeClipboard) set(s string) { c.mu.Lock(); c.text = s; c.mu.Unlock() }
func (c *fakeClipboard) get() string  { c.mu.Lock(); defer c.mu.Unlock(); return c.text }

func TestCopiedCodeLeavesTheClipboardWhenThePairingEnds(t *testing.T) {
	var clip fakeClipboard
	alice, bob := network(t, Config{}, nil)
	alice.copyText, alice.clipText = clip.set, clip.get
	code := showCode(t, alice)
	alice.NotchCopyCode()
	if clip.get() != code.Code {
		t.Fatal("the code was not copied")
	}
	pasteCode(t, bob, code.Code)
	alice.rec.waitPairing(t, code.ID, final)
	if got := clip.get(); got != "" {
		t.Fatalf("the clipboard still holds %q after the pairing ended", got)
	}
}

func TestCopiedCodeIsNotTakenBackOverSomethingElse(t *testing.T) {
	var clip fakeClipboard
	alice, _ := network(t, Config{}, nil)
	alice.copyText, alice.clipText = clip.set, clip.get
	code := showCode(t, alice)
	alice.copyCode(code.Code)
	clip.set("a recipe the person copied since")
	alice.CancelPairing(code.ID)
	alice.rec.waitPairing(t, code.ID, final)
	if clip.get() != "a recipe the person copied since" {
		t.Fatalf("the clipboard was changed: %q", clip.get())
	}
}

func TestCopiedCodeIsClearedWhenThePairingIsCancelled(t *testing.T) {
	var clip fakeClipboard
	alice, _ := network(t, Config{}, nil)
	alice.copyText, alice.clipText = clip.set, clip.get
	code := showCode(t, alice)
	alice.copyCode(code.Code)
	alice.CancelPairing(code.ID)
	alice.rec.waitPairing(t, code.ID, final)
	if clip.get() != "" {
		t.Fatalf("the clipboard still holds %q", clip.get())
	}
}
