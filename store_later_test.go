package main

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	kolostore "github.com/ciranda-dev/kolo-store"
)

// slowBackend is a memory backend whose batch commits take a while and are counted, like a synced disk.
type slowBackend struct {
	kolostore.Backend
	delay   time.Duration
	mu      sync.Mutex
	commits int
	failAt  int // the commit with this number (from 1) fails; 0 never
	biggest int
}

var errDisk = errors.New("disk failed")

func (s *slowBackend) NewBatch() kolostore.Batch {
	return &slowBatch{Batch: s.Backend.NewBatch(), s: s}
}

type slowBatch struct {
	kolostore.Batch
	s   *slowBackend
	ops int
}

func (b *slowBatch) Put(k, v []byte)         { b.ops++; b.Batch.Put(k, v) }
func (b *slowBatch) Delete(k []byte)         { b.ops++; b.Batch.Delete(k) }
func (b *slowBatch) DeleteRange(s, e []byte) { b.ops++; b.Batch.DeleteRange(s, e) }
func (b *slowBatch) Commit() error {
	time.Sleep(b.s.delay)
	b.s.mu.Lock()
	b.s.commits++
	n := b.s.commits
	b.s.biggest = max(b.s.biggest, b.ops)
	b.s.mu.Unlock()
	if b.s.failAt == n {
		return errDisk
	}
	return b.Batch.Commit()
}

func scanAll(t *testing.T, b kolostore.Backend) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := b.Scan(nil, nil, false, 0, func(k, v []byte) bool { out[string(k)] = string(v); return true }); err != nil {
		t.Fatal(err)
	}
	return out
}

// Random batches of puts, deletes and range deletes, with reads in between, give the same answers as the plain
// backend at every step, and the same contents once it is closed.
func TestLaterBackendMatchesPlain(t *testing.T) {
	for seed := range 20 {
		rng := rand.New(rand.NewPCG(uint64(seed), 7))
		plain := kolostore.NewMemory()
		inner := &slowBackend{Backend: kolostore.NewMemory(), delay: time.Duration(rng.IntN(300)) * time.Microsecond}
		later := newLaterBackend(inner)
		key := func() []byte { return fmt.Appendf(nil, "k%02d", rng.IntN(40)) }
		for step := range 300 {
			a, b := plain.NewBatch(), later.NewBatch()
			for range 1 + rng.IntN(4) {
				switch r := rng.IntN(20); {
				case r < 12:
					k, v := key(), bytes.Repeat([]byte{byte(rng.IntN(256))}, rng.IntN(50))
					a.Put(k, v)
					b.Put(k, v)
				case r < 18:
					k := key()
					a.Delete(k)
					b.Delete(k)
				default:
					lo := key()
					hi := slices.Clone(lo)
					hi[2]++
					a.DeleteRange(lo, hi)
					b.DeleteRange(lo, hi)
				}
			}
			if err := a.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := b.Commit(); err != nil {
				t.Fatal(err)
			}
			k := key()
			want, werr := plain.Get(k)
			got, gerr := later.Get(k)
			if !bytes.Equal(want, got) || !errors.Is(gerr, werr) && gerr != werr {
				t.Fatalf("seed %d step %d: Get(%s) = %q, %v; want %q, %v", seed, step, k, got, gerr, want, werr)
			}
			if step%37 == 0 {
				if w, g := scanAll(t, plain), scanAll(t, later); fmt.Sprint(w) != fmt.Sprint(g) {
					t.Fatalf("seed %d step %d: scan differs", seed, step)
				}
			}
		}
		want := fmt.Sprint(scanAll(t, plain))
		scanAll(t, later) // waits for the queue
		if got := fmt.Sprint(scanAll(t, inner.Backend)); got != want {
			t.Fatalf("seed %d: the disk holds %s, want %s", seed, got, want)
		}
		if err := later.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// What is queued reaches the disk when the backend closes, in the order of the commits, and a slow disk gets fewer,
// larger writes than there are commits.
func TestLaterBackendGroupsWritesAndKeepsOrder(t *testing.T) {
	rec := &orderBackend{Backend: kolostore.NewMemory()}
	inner := &slowBackend{Backend: rec, delay: 2 * time.Millisecond}
	later := newLaterBackend(inner)
	const n = 400
	for i := range n {
		b := later.NewBatch()
		b.Put([]byte("same"), fmt.Appendf(nil, "%d", i))
		b.Put(fmt.Appendf(nil, "k%04d", i), bytes.Repeat([]byte{1}, 1000))
		if err := b.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := later.Get([]byte("same")); err != nil || string(got) != fmt.Sprint(n-1) {
		t.Fatalf("the newest value of a key: %q %v", got, err)
	}
	if err := later.Close(); err != nil {
		t.Fatal(err)
	}
	inner.mu.Lock()
	commits, biggest := inner.commits, inner.biggest
	inner.mu.Unlock()
	if commits >= n/4 || biggest < 4 {
		t.Fatalf("%d commits became %d writes, the largest of %d operations", n, commits, biggest)
	}
	if len(rec.keys) != n+1 || string(rec.last["same"]) != fmt.Sprint(n-1) {
		t.Fatalf("on disk: %d keys, same = %q", len(rec.keys), rec.last["same"])
	}
	if !slices.IsSorted(rec.keys[1:]) || rec.keys[0] != "same" {
		t.Fatalf("the keys reached the disk out of order: %q", rec.keys[:6])
	}
	if err := later.NewBatch().Commit(); !errors.Is(err, kolostore.ErrClosed) {
		t.Fatalf("a commit after Close: %v", err)
	}
	if err := later.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
}

// orderBackend records the keys of every batch put into it, in order of arrival.
type orderBackend struct {
	kolostore.Backend
	mu   sync.Mutex
	keys []string
	last map[string][]byte
}

func (o *orderBackend) NewBatch() kolostore.Batch {
	return &orderBatch{Batch: o.Backend.NewBatch(), o: o}
}

type orderBatch struct {
	kolostore.Batch
	o    *orderBackend
	puts []laterOp
}

func (b *orderBatch) Put(k, v []byte) {
	b.puts = append(b.puts, laterOp{'p', bytes.Clone(k), bytes.Clone(v)})
	b.Batch.Put(k, v)
}

func (b *orderBatch) Commit() error {
	err := b.Batch.Commit()
	b.o.mu.Lock()
	defer b.o.mu.Unlock()
	if b.o.last == nil {
		b.o.last = map[string][]byte{}
	}
	for _, p := range b.puts {
		if _, seen := b.o.last[string(p.key)]; !seen {
			b.o.keys = append(b.o.keys, string(p.key))
		}
		b.o.last[string(p.key)] = p.value
	}
	return err
}

// The queue is bounded: a Commit waits for the disk once laterQueueMax bytes wait, so a fast producer cannot hold the
// transfer in memory.
func TestLaterBackendBoundsTheQueue(t *testing.T) {
	inner := &slowBackend{Backend: kolostore.NewMemory(), delay: 3 * time.Millisecond}
	later := newLaterBackend(inner)
	defer later.Close()
	chunk := bytes.Repeat([]byte{7}, 4<<20)
	peak := 0
	for i := range 80 { // 320 MiB through a 64 MiB queue
		b := later.NewBatch()
		b.Put(fmt.Appendf(nil, "k%03d", i), chunk)
		if err := b.Commit(); err != nil {
			t.Fatal(err)
		}
		later.mu.Lock()
		peak = max(peak, later.bytes)
		later.mu.Unlock()
	}
	if peak > laterQueueMax+(4<<20)+1024 {
		t.Fatalf("the queue reached %d bytes, the bound is %d", peak, laterQueueMax)
	}
	if peak < laterQueueMax/2 {
		t.Fatalf("the queue never filled (%d): the test does not test the bound", peak)
	}
}

// A disk that fails fails every later Commit with its error; what was queued stays readable, and Close returns.
func TestLaterBackendDiskError(t *testing.T) {
	inner := &slowBackend{Backend: kolostore.NewMemory(), delay: time.Millisecond, failAt: 2}
	later := newLaterBackend(inner)
	var first error
	for i := range 200 {
		b := later.NewBatch()
		b.Put(fmt.Appendf(nil, "k%03d", i), []byte("v"))
		if err := b.Commit(); err != nil {
			first = err
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !errors.Is(first, errDisk) {
		t.Fatalf("Commit after a failed write: %v", first)
	}
	if v, err := later.Get([]byte("k000")); err != nil || string(v) != "v" {
		t.Fatalf("a queued key after the failure: %q %v", v, err)
	}
	if err := later.Scan(nil, nil, false, 0, func(_, _ []byte) bool { return true }); !errors.Is(err, errDisk) {
		t.Fatalf("Scan after the failure: %v", err)
	}
	if err := later.Close(); !errors.Is(err, errDisk) {
		t.Fatalf("Close after the failure: %v", err)
	}
}

// Many goroutines commit and read at once; every key reads back as its last value (run with -race).
func TestLaterBackendConcurrent(t *testing.T) {
	inner := &slowBackend{Backend: kolostore.NewMemory(), delay: 200 * time.Microsecond}
	later := newLaterBackend(inner)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 100 {
				k := fmt.Appendf(nil, "g%d-%03d", g, i)
				b := later.NewBatch()
				b.Put(k, k)
				if err := b.Commit(); err != nil {
					t.Error(err)
					return
				}
				if v, err := later.Get(k); err != nil || !bytes.Equal(v, k) {
					t.Errorf("read back %s: %q %v", k, v, err)
					return
				}
			}
		})
	}
	wg.Wait()
	if err := later.Close(); err != nil {
		t.Fatal(err)
	}
}

// A kolo-store on it behaves as on the plain backend: a log written, read back and served.
func TestLaterBackendUnderAStore(t *testing.T) {
	st, cleanup, err := openStore(t.TempDir()+"/s", 0, &storeBudget{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	id := newIDs(t, 1)[0]
	l, err := st.Create(id)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 500 {
		if _, err := l.Append(fmt.Appendf(nil, "entry %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	for _, i := range []uint64{0, 250, 499} {
		got, err := l.Get(i)
		if err != nil || string(got) != fmt.Sprintf("entry %d", i) {
			t.Fatalf("entry %d: %q %v", i, got, err)
		}
	}
}
