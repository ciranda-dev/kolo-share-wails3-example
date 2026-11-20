package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ciranda-dev/kolo-share-wails3-example/internal/netsim"
)

const (
	bigDefaultGiB = 3
	bigMaxGiB     = 12
	// bigDiskFactor is the temporary disk a run uses at its peak, in times the item: the sender's store, the
	// receiver's store and the saved file, measured at 3.5 for 4 GiB (a store holds about 1.2 times its item).
	bigDiskFactor = 3.5
	// bigDiskBudget is the temporary data one test may use; a larger size needs KOLO_SHARE_BIG_ALLOW_DISK=1.
	bigDiskBudget = 12 << 30
)

// bigSource writes a file of n bytes of pseudo-random data, which Pebble and the chunker cannot shrink, and returns
// its SHA-256. The bytes come from AES in counter mode, fast enough that the disk is the limit.
func bigSource(t *testing.T, path string, n int64) string {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	block, _ := aes.NewCipher(make([]byte, 32))
	ks := cipher.NewCTR(block, make([]byte, aes.BlockSize))
	h := sha256.New()
	buf := make([]byte, 4<<20)
	for left := n; left > 0; {
		b := buf[:min(int64(len(buf)), left)]
		clear(b)
		ks.XORKeyStream(b, b)
		h.Write(b)
		if _, err := f.Write(b); err != nil {
			t.Fatal(err)
		}
		left -= int64(len(b))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// diskPeak samples the bytes under dirs every half second until stop is called and returns the largest total.
func diskPeak(dirs ...string) (stop func() int64) {
	var mu sync.Mutex
	var peak int64
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			var sum int64
			for _, d := range dirs {
				sum += dirSize(d)
			}
			mu.Lock()
			peak = max(peak, sum)
			mu.Unlock()
			select {
			case <-done:
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
	})
	return func() int64 {
		close(done)
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		return peak
	}
}

// TestBenchBig sends and receives one real file between two apps in this process, through a netsim link, and
// reports the speed of each stage, the peak memory and the peak temporary disk. It is opt in:
//
//	KOLO_SHARE_BIG=1 KOLO_SHARE_BIG_GIB=3 go test -run BenchBig -v -timeout 3h
//
// KOLO_SHARE_BIG_GIB is the size in GiB (default 3, at most 12, and at most 3.4 unless KOLO_SHARE_BIG_ALLOW_DISK=1, since
// a test may use 12 GiB of temporary disk); KOLO_SHARE_BIG_MBIT the link's rate in Mbit/s each
// way (default 0, unlimited) and KOLO_SHARE_BIG_RTT_MS its round trip (default 20). The disk it needs while it runs
// is about 3.5 times the size: the sender's store, the receiver's store and the saved file (the source file is
// removed once the item is offered). KOLO_SHARE_BIG_BASELINE=1 turns off the grouped commits and the parallel save,
// for a before and after on one computer. Everything lives in temporary directories that the test removes.
func TestBenchBig(t *testing.T) {
	if os.Getenv("KOLO_SHARE_BIG") != "1" {
		t.Skip("set KOLO_SHARE_BIG=1")
	}
	gib := float64(bigDefaultGiB)
	if v, err := strconv.ParseFloat(os.Getenv("KOLO_SHARE_BIG_GIB"), 64); err == nil && v > 0 {
		gib = v
	}
	if gib > bigMaxGiB {
		t.Logf("KOLO_SHARE_BIG_GIB %v is over the cap of %d: using the cap", gib, bigMaxGiB)
		gib = bigMaxGiB
	}
	if need := gib * (1 << 30) * bigDiskFactor; need > bigDiskBudget && os.Getenv("KOLO_SHARE_BIG_ALLOW_DISK") != "1" {
		t.Skipf("%.2f GiB needs about %.1f GiB of temporary disk, over the %d GiB a test may use: set KOLO_SHARE_BIG_GIB to %.1f or less, or KOLO_SHARE_BIG_ALLOW_DISK=1",
			gib, need/(1<<30), bigDiskBudget>>30, float64(bigDiskBudget)/(1<<30)/bigDiskFactor)
	}
	size := int64(math.Round(gib * (1 << 30)))
	mbit, _ := strconv.Atoi(os.Getenv("KOLO_SHARE_BIG_MBIT"))
	rttMs := 20
	if v, err := strconv.Atoi(os.Getenv("KOLO_SHARE_BIG_RTT_MS")); err == nil && v >= 0 {
		rttMs = v
	}
	old := waitFor
	waitFor = 3 * time.Hour
	t.Cleanup(func() { waitFor = old })
	if os.Getenv("KOLO_SHARE_BIG_BASELINE") == "1" {
		// Each chunk synced on its own and one goroutine copying the files: what the app did before the groups
		// of commits and the parallel save. Run with GOWORK=off for a sender that seals one chunk at a time too.
		defers, workers := deferWrites, maxCopyWorkers
		deferWrites, maxCopyWorkers = false, 1
		t.Cleanup(func() { deferWrites, maxCopyWorkers = defers, workers })
		t.Log("BASELINE: synced commit per chunk, one copy worker")
	}

	half := netsim.Profile{Rate: mbit * 1_000_000 / 8, Delay: time.Duration(rttMs) * time.Millisecond / 2}
	alice, bob, sa, sb, link := linkedPair(t, half, modeApp)
	dest := t.TempDir()
	bob.SetSessionFolder(sb, dest)

	src := filepath.Join(t.TempDir(), "big.bin")
	t0 := time.Now()
	want := bigSource(t, src, size)
	t.Logf("made the %.2f GiB source in %v", gib, time.Since(t0).Round(time.Millisecond))

	stopDisk := diskPeak(alice.data, bob.data, dest)
	stopHeap := heapSampler()
	start := time.Now()
	id, err := alice.Send(sa, src)
	if err != nil {
		t.Fatal(err)
	}
	if tr := alice.rec.wait(t, id, stateOffered, stateFailed); tr.State != stateOffered {
		t.Fatalf("send: %+v", tr.Error)
	}
	offered := time.Now()
	os.Remove(src) // the store holds the item now
	in := incoming(t, bob, sb, "big.bin")
	pullStart := time.Now()
	if err := bob.SaveOffer(in.ID); err != nil {
		t.Fatal(err)
	}
	if tr := bob.rec.wait(t, in.ID, stateVerifying, stateDone, stateFailed); tr.State == stateFailed {
		t.Fatalf("receive: %+v", tr.Error)
	}
	pulled := time.Now()
	if tr := bob.rec.wait(t, in.ID, stateDone, stateFailed); tr.State != stateDone {
		t.Fatalf("receive: %+v", tr.Error)
	}
	end := time.Now()
	peakDisk := stopDisk()
	peakHeap := stopHeap()

	if got := fileSHA(t, filepath.Join(dest, "big.bin")); got != want {
		t.Fatalf("the saved file is not the sent one: %s, want %s", got, want)
	}
	rate := func(d time.Duration) float64 { return float64(size) / 1e6 / d.Seconds() }
	total := end.Sub(start)
	t.Logf("size %.2f GiB over a link of %d Mbit/s (0 is unlimited) and %d ms round trip", gib, mbit, rttMs)
	t.Logf("prepare on the sender  %8.1f MB/s (%v)", rate(offered.Sub(start)), offered.Sub(start).Round(time.Millisecond))
	t.Logf("pull into the store    %8.1f MB/s (%v)", rate(pulled.Sub(pullStart)), pulled.Sub(pullStart).Round(time.Millisecond))
	t.Logf("save to the folder     %8.1f MB/s (%v)", rate(end.Sub(pulled)), end.Sub(pulled).Round(time.Millisecond))
	t.Logf("whole, offer to saved  %8.1f MB/s (%v), with the prepare %8.1f MB/s (%v)", rate(end.Sub(pullStart)), end.Sub(pullStart).Round(time.Millisecond), rate(total), total.Round(time.Millisecond))
	t.Logf("peak heap %.0f MiB, peak resident size of the process (both apps and the test) %.0f MiB, peak temporary disk %.2f GiB (%.2f times the item)",
		mib(peakHeap), float64(maxRSS())/(1<<20), float64(peakDisk)/(1<<30), float64(peakDisk)/float64(size))
	down := link.Down()
	t.Logf("link: %d bytes delivered to the receiver, dropped %d in the queue, %d by loss", down.Bytes, down.DroppedQ, down.DroppedLoss)
	// A straight-line scaling of the whole-transfer speed, not a measurement.
	hundred := time.Duration(float64(100e9) / (float64(size) / total.Seconds()) * float64(time.Second))
	t.Logf("EXTRAPOLATION, not measured: 100 GB at this overall speed would take %v", hundred.Round(time.Minute))
}

// TestBenchManyFiles sends and receives a folder of many small files, which is what a scan and a store of 100,000
// nodes cost, with the same link as TestBenchBig. KOLO_SHARE_BIG=1, KOLO_SHARE_BIG_FILES is the count (default
// 20000, at most 99000, which with its 495 folders stays under the default limit of 100,000 nodes).
func TestBenchManyFiles(t *testing.T) {
	if os.Getenv("KOLO_SHARE_BIG") != "1" {
		t.Skip("set KOLO_SHARE_BIG=1")
	}
	n := 20000
	if v, err := strconv.Atoi(os.Getenv("KOLO_SHARE_BIG_FILES")); err == nil && v > 0 {
		n = min(v, 99000)
	}
	old := waitFor
	waitFor = 3 * time.Hour
	t.Cleanup(func() { waitFor = old })
	alice, bob, sa, sb, _ := linkedPair(t, netsim.Profile{Delay: 10 * time.Millisecond}, modeApp)
	dest := t.TempDir()
	bob.SetSessionFolder(sb, dest)
	src := filepath.Join(t.TempDir(), "many")
	var bytes int64
	for i := range n {
		dir := filepath.Join(src, strconv.Itoa(i/200))
		if i%200 == 0 {
			os.MkdirAll(dir, 0o700)
		}
		body := make([]byte, 512+i%2048)
		bytes += int64(len(body))
		if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(i)), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	id, err := alice.Send(sa, src)
	if err != nil {
		t.Fatal(err)
	}
	if tr := alice.rec.wait(t, id, stateOffered, stateFailed); tr.State != stateOffered {
		t.Fatalf("send: %+v", tr.Error)
	}
	offered := time.Now()
	in := incoming(t, bob, sb, "many")
	if err := bob.SaveOffer(in.ID); err != nil {
		t.Fatal(err)
	}
	if tr := bob.rec.wait(t, in.ID, stateVerifying, stateDone, stateFailed); tr.State == stateFailed {
		t.Fatalf("receive: %+v", tr.Error)
	}
	pulled := time.Now()
	if tr := bob.rec.wait(t, in.ID, stateDone, stateFailed); tr.State != stateDone {
		t.Fatalf("receive: %+v", tr.Error)
	}
	end := time.Now()
	perSec := func(d time.Duration) float64 { return float64(n) / d.Seconds() }
	t.Logf("%d files, %.1f MB: prepare %v (%.0f files/s), pull %v (%.0f files/s), save %v (%.0f files/s), peak resident size %.0f MiB",
		n, float64(bytes)/1e6, offered.Sub(start).Round(time.Millisecond), perSec(offered.Sub(start)),
		pulled.Sub(offered).Round(time.Millisecond), perSec(pulled.Sub(offered)), end.Sub(pulled).Round(time.Millisecond), perSec(end.Sub(pulled)), float64(maxRSS())/(1<<20))
}
