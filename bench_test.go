package main

import (
	"context"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	kolofiles "github.com/ciranda-dev/kolo-files"
	koloid "github.com/ciranda-dev/kolo-id"
)

const benchBytes = 1 << 30

// benchSource writes a file of benchBytes of random bytes in a temp directory.
func benchSource(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bench.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := io.CopyN(f, rand.Reader, benchBytes); err != nil {
		t.Fatal(err)
	}
	return path
}

func mbPerSecond(d time.Duration) float64 { return float64(benchBytes) / (1 << 20) / d.Seconds() }

// prepareOnce stores path in a new volume the way runOffer does, with the store openStore picks for size, and
// returns how long it took.
func prepareOnce(t *testing.T, path string, size int64) time.Duration {
	t.Helper()
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	var b storeBudget
	start := time.Now()
	st, cleanup, err := openStore(filepath.Join(t.TempDir(), "store"), size, &b)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
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
	defer v.Close()
	var stored counter
	if err := fillVolume(context.Background(), v, path, "bench.bin", &stored); err != nil {
		t.Fatal(err)
	}
	return time.Since(start)
}

// TestBenchPrepare prepares 1 GiB with the default store and with the tuned one, each with a synced commit at every
// chunk and with the commits grouped: KOLO_SHARE_BENCH=1 go test -run BenchPrepare -v.
func TestBenchPrepare(t *testing.T) {
	if os.Getenv("KOLO_SHARE_BENCH") != "1" {
		t.Skip("set KOLO_SHARE_BENCH=1")
	}
	old := deferWrites
	t.Cleanup(func() { deferWrites = old })
	path := benchSource(t)
	for round := 1; round <= 2; round++ {
		for _, c := range []struct {
			name    string
			size    int64
			grouped bool
		}{{"default, a sync for each chunk", 0, false}, {"tuned, a sync for each chunk", benchBytes, false}, {"default, grouped commits", 0, true}, {"tuned, grouped commits", benchBytes, true}} {
			deferWrites = c.grouped
			d := prepareOnce(t, path, c.size)
			t.Logf("round %d: prepare 1 GiB, %-30s %6.1f MB/s (%v)", round, c.name, mbPerSecond(d), d.Round(time.Millisecond))
		}
	}
}

// TestBenchTransfer sends and receives 1 GiB between two apps on loopback, with default stores and with tuned ones.
func TestBenchTransfer(t *testing.T) {
	if os.Getenv("KOLO_SHARE_BENCH") != "1" {
		t.Skip("set KOLO_SHARE_BENCH=1")
	}
	path := benchSource(t)
	for _, tuned := range []bool{false, true} {
		name := "default"
		if tuned {
			name = "tuned"
		}
		t.Run(name, func(t *testing.T) {
			full := func(s *ShareService) {
				if !tuned {
					s.tuned.open = maxTunedStores // no place left: every store keeps the defaults
				}
			}
			alice, bob := networkWith(t, Config{}, Config{}, full, full)
			sa, sb := connect(t, alice, bob)
			bob.SetSessionFolder(sb, t.TempDir())
			start := time.Now()
			id, err := alice.Send(sa, path)
			if err != nil {
				t.Fatal(err)
			}
			alice.rec.wait(t, id, stateOffered)
			offered := time.Now()
			in := incoming(t, bob, sb, "bench.bin")
			if err := bob.SaveOffer(in.ID); err != nil {
				t.Fatal(err)
			}
			bob.rec.wait(t, in.ID, stateVerifying)
			pulled := time.Now()
			if tr := bob.rec.wait(t, in.ID, stateDone, stateFailed); tr.State != stateDone {
				t.Fatalf("receive: %+v", tr)
			}
			done := time.Now()
			t.Logf("%-7s prepare %6.1f MB/s (%v), receive into the store %6.1f MB/s (%v), save %6.1f MB/s (%v)", name,
				mbPerSecond(offered.Sub(start)), offered.Sub(start).Round(time.Millisecond),
				mbPerSecond(pulled.Sub(offered)), pulled.Sub(offered).Round(time.Millisecond),
				mbPerSecond(done.Sub(pulled)), done.Sub(pulled).Round(time.Millisecond))
		})
	}
}
