//go:build smoke

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestSmokeAgainstRunningApp sends and receives a file through the entry node of a running app, such as
// `./bin/kolo-share -first 127.0.0.1:4000`: go test -tags smoke -run Smoke with KOLO_SMOKE_ENTRY=127.0.0.1:4000.
func TestSmokeAgainstRunningApp(t *testing.T) {
	entry := os.Getenv("KOLO_SMOKE_ENTRY")
	if entry == "" {
		t.Skip("set KOLO_SMOKE_ENTRY to the entry node of a running app")
	}
	var mu sync.Mutex
	var copied string
	var notch []string
	alice := startPeer(t, Config{Bootstrap: entry}, func(s *ShareService) {
		s.copyText = func(text string) { mu.Lock(); copied = text; mu.Unlock() }
		s.emitNotch = func(name string, data any) {
			b, _ := json.Marshal(data)
			mu.Lock()
			notch = append(notch, string(b))
			mu.Unlock()
		}
	})
	alice.rec.waitNetwork(t, netReady)
	bob := startPeer(t, Config{Bootstrap: entry}, nil)
	bob.rec.waitNetwork(t, netReady)

	src := filepath.Join(t.TempDir(), "smoke.bin")
	body := randomBytes(t, 5<<20)
	os.WriteFile(src, body, 0o600)
	sent := share(t, alice, src)
	alice.NotchCopyCode() // the notch's Copy button
	mu.Lock()
	if copied != sent.Code {
		t.Fatal("the notch's Copy did not copy the code")
	}
	mu.Unlock()
	got := receive(t, bob, sent.Code, t.TempDir(), stateDone, stateFailed)
	if got.State != stateDone {
		t.Fatalf("receive: %+v", got.Error)
	}
	b, err := os.ReadFile(got.SavedTo)
	if err != nil || !bytes.Equal(b, body) {
		t.Fatalf("bytes differ (%v)", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, e := range notch {
		if strings.Contains(e, sent.Code) {
			t.Fatalf("the notch got the code: %s", e)
		}
	}
	if ts := alice.Transfers(context.Background()); len(ts) != 1 || ts[0].Code != "" {
		t.Fatalf("Transfers from a window that is not the main one: %+v", ts)
	}
	t.Logf("received %d bytes through %s, byte for byte equal; %d notch events, none with the code", len(b), entry, len(notch))
}
