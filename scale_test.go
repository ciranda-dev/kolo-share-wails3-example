package main

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	koloid "github.com/ciranda-dev/kolo-id"
	kololog "github.com/ciranda-dev/kolo-log"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	kolostore "github.com/ciranda-dev/kolo-store"
	kolosync "github.com/ciranda-dev/kolo-sync"
)

// heapSampler records the largest HeapInuse until stop is called.
func heapSampler() (stop func() uint64) {
	var peak atomic.Uint64
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		var ms runtime.MemStats
		for {
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > peak.Load() {
				peak.Store(ms.HeapInuse)
			}
			select {
			case <-done:
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	})
	return func() uint64 { close(done); wg.Wait(); return peak.Load() }
}

func heapNow() uint64 {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

func mib(n uint64) float64 { return float64(n) / (1 << 20) }

// TestScaleLogOf150000Entries builds a log with the entry count of a 150 GB item (about 1 MiB a chunk), with small
// payloads so it needs little disk, and times what grows with the count: appending, the signed head and its proofs,
// a full pull over loopback with the app's own pullLog into a Pebble store, and the check of every entry when the
// store is opened again. KOLO_SHARE_SCALE_ENTRIES changes the count, and KOLO_SHARE_SCALE_WRITER=pebble puts the sender's log on Pebble
// too, which syncs the disk at every append and takes minutes.
func TestScaleLogOf150000Entries(t *testing.T) {
	n := 150_000
	if v, err := strconv.Atoi(os.Getenv("KOLO_SHARE_SCALE_ENTRIES")); err == nil && v > 0 {
		n = v
	}
	if testing.Short() || raceOn && os.Getenv("KOLO_SHARE_SCALE_ENTRIES") == "" {
		n = 5000
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	puller, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}

	// Append: the time of each tenth must not grow with the length of the log.
	base := heapNow()
	src := kolostore.New(kolostore.NewMemory())
	if os.Getenv("KOLO_SHARE_SCALE_WRITER") == "pebble" { // every append syncs the disk: about 5 ms each
		db, err := kolostore.OpenPebble(filepath.Join(t.TempDir(), "writer"))
		if err != nil {
			t.Fatal(err)
		}
		src = kolostore.New(db)
	}
	defer src.Close()
	l, err := src.Create(owner)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 64)
	tenth := n / 10
	var tenths [10]time.Duration
	start := time.Now()
	for i := range n {
		payload[0], payload[1], payload[2] = byte(i), byte(i>>8), byte(i>>16)
		t0 := time.Now()
		if _, err := l.Append(payload); err != nil {
			t.Fatal(err)
		}
		tenths[min(i/tenth, 9)] += time.Since(t0)
	}
	appended := time.Since(start)
	grown := heapNow() - base
	t.Logf("append %d entries: %v (%.0f us each), first tenth %v, last tenth %v; heap +%.1f MiB (%d bytes per entry)",
		n, appended.Round(time.Millisecond), float64(appended.Microseconds())/float64(n), tenths[0].Round(time.Millisecond), tenths[9].Round(time.Millisecond), mib(grown), grown/uint64(n))
	// The in-memory backend keeps its keys in one sorted slice, so only the Pebble writer says anything about the log.
	if os.Getenv("KOLO_SHARE_SCALE_WRITER") == "pebble" && tenths[9] > 2*tenths[0]+time.Second {
		t.Errorf("appending slows down with the length of the log: first tenth %v, last tenth %v", tenths[0], tenths[9])
	}
	if grown/uint64(n) > 2048 {
		t.Errorf("the log keeps %d bytes in memory per entry", grown/uint64(n))
	}

	// The signed head and its proofs at the full length.
	t0 := time.Now()
	head, err := l.Head()
	if err != nil || head.Size != uint64(n) {
		t.Fatalf("head: %+v %v", head, err)
	}
	headTook := time.Since(t0)
	t0 = time.Now()
	if _, err := l.InclusionProof(uint64(n/2), uint64(n)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ConsistencyProof(uint64(n/3), uint64(n)); err != nil {
		t.Fatal(err)
	}
	t.Logf("head %v, inclusion and consistency proofs %v", headTook.Round(time.Microsecond), time.Since(t0).Round(time.Microsecond))
	if headTook > time.Second {
		t.Errorf("the head takes %v at %d entries", headTook, n)
	}

	// A full pull over loopback, into Pebble, with the app's own pullLog.
	srv := kolosync.NewServer(func(peer, own ed25519.PublicKey, id kololog.LogID) kolosync.Access { return kolosync.Ciphertext })
	if err := srv.Offer(l); err != nil {
		t.Fatal(err)
	}
	ln, err := kolosecure.Listen(ctx, "127.0.0.1:0", owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(ctx); err == nil {
			srv.Serve(ctx, c)
		}
	}()
	conn, err := kolosecure.Dial(ctx, ln.Addr().String(), puller, owner.PublicKey(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	dir := filepath.Join(t.TempDir(), "replica")
	rst, cleanup, err := openStore(dir, 0, &storeBudget{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	runtime.GC()
	stop := heapSampler()
	var pulling atomic.Pointer[kolostore.Replica]
	var slot *kolostore.Replica
	t0 = time.Now()
	if err := pullLog(ctx, conn, rst, owner.PublicKey(), l.ID(), uint64(n), &pulling, &slot); err != nil {
		t.Fatal(err)
	}
	pulled := time.Since(t0)
	peak := stop()
	t.Logf("pull %d entries over loopback into Pebble: %v (%.0f entries/s), peak heap %.1f MiB, store on disk %.1f MiB",
		n, pulled.Round(time.Millisecond), float64(n)/pulled.Seconds(), mib(peak), mib(uint64(dirSize(dir))))
	if slot.Len() != uint64(n) {
		t.Fatalf("the replica holds %d of %d entries", slot.Len(), n)
	}
	if mib(peak) > 1024 {
		t.Errorf("a pull of %d entries peaked at %.0f MiB of heap", n, mib(peak))
	}
	if pulled > 20*time.Minute {
		t.Errorf("a pull of %d entries took %v", n, pulled)
	}

	// The limit the receive sets from the offer is hit at one entry over.
	var over *kolostore.Replica
	if err := pullLog(ctx, conn, rst, owner.PublicKey(), l.ID(), uint64(n)-1, &pulling, &over); err == nil {
		t.Errorf("a log over the entry limit was accepted")
	}

	// The check of every signature, hash and Merkle root when the store is opened again.
	id := l.ID()
	cleanupKeep(t, rst)
	runtime.GC()
	stop = heapSampler()
	t0 = time.Now()
	db, err := kolostore.OpenPebble(dir)
	if err != nil {
		t.Fatal(err)
	}
	reopened := kolostore.New(db)
	defer reopened.Close()
	rep, err := reopened.OpenReplica(owner.PublicKey(), id, mustHeader(t, ctx, conn, owner.PublicKey(), id))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("open the replica again, which checks %d entries: %v, peak heap %.1f MiB", rep.Len(), time.Since(t0).Round(time.Millisecond), mib(stop()))
	if rep.Len() != uint64(n) {
		t.Fatalf("after reopening: %d of %d", rep.Len(), n)
	}
	got, err := rep.Head()
	if err != nil || got.Root != head.Root || got.Size != head.Size {
		t.Fatalf("the replica's head is not the sender's: %v", err)
	}
}

func mustHeader(t *testing.T, ctx context.Context, conn kolosync.Conn, owner ed25519.PublicKey, id kololog.LogID) []byte {
	t.Helper()
	h, err := kolosync.FetchHeader(ctx, conn, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// cleanupKeep closes st without removing its directory.
func cleanupKeep(t *testing.T, st *kolostore.Store) {
	t.Helper()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}
