package main

import (
	"strings"
	"sync/atomic"
	"testing"
)

// Auto-save saves at most half of the free space the folder had when it was turned on; the next item waits for
// an answer and auto-save is off.
func TestAutoSaveStopsAtHalfTheFreeSpace(t *testing.T) {
	var asked atomic.Int32
	alice, bob := network(t, Config{}, func(s *ShareService) {
		s.hooks.freeSpace = func(string) (int64, error) {
			if asked.Add(1) == 1 {
				return 100, nil // what SetAutoSave sees: a budget of 50 bytes
			}
			return 1 << 40, nil // what the receives see
		}
	})
	sa, sb := connect(t, alice, bob)
	dest := t.TempDir()
	if _, err := bob.SetSessionFolder(sb, dest); err != nil {
		t.Fatal(err)
	}
	if v, err := bob.SetAutoSave(sb, true); err != nil || !v.AutoSave {
		t.Fatalf("auto-save: %+v %v", v, err)
	}
	if _, err := alice.Send(sa, writeFile(t, "one.txt", []byte(strings.Repeat("1", 30)))); err != nil {
		t.Fatal(err)
	}
	got := bob.rec.find(t, "one.txt saved", func(tr Transfer) bool { return tr.Name == "one.txt" && tr.final() })
	if got.State != stateDone {
		t.Fatalf("the first item: %+v %+v", got.State, got.Error)
	}
	offerItem(t, alice, sa, writeFile(t, "two.txt", []byte(strings.Repeat("2", 30))))
	in := incoming(t, bob, sb, "two.txt")
	v := bob.rec.waitSession(t, sb, func(v Session) bool { return v.Notice != nil && v.Notice.Reason == "autosave-limit" })
	if v.AutoSave {
		t.Fatalf("auto-save stays on after its budget: %+v", v)
	}
	if tr := bob.rec.until(t, in.ID, func(Transfer) bool { return true }); tr.State != stateOffered {
		t.Fatalf("the second item moved without an answer: %+v", tr)
	}
	// Turning it on again gives a new budget, from the free space then.
	if v, err := bob.SetAutoSave(sb, true); err != nil || !v.AutoSave {
		t.Fatalf("auto-save again: %+v %v", v, err)
	}
	if _, err := alice.Send(sa, writeFile(t, "three.txt", []byte("3"))); err != nil {
		t.Fatal(err)
	}
	if got := bob.rec.find(t, "three.txt saved", func(tr Transfer) bool { return tr.Name == "three.txt" && tr.final() }); got.State != stateDone {
		t.Fatalf("the third item: %+v %+v", got.State, got.Error)
	}
}
