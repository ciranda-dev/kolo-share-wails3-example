package main

import (
	"os"
	"path/filepath"
	"testing"

	koloblobs "github.com/ciranda-dev/kolo-blobs"
	kolostore "github.com/ciranda-dev/kolo-store"
)

func (b *storeBudget) inUse() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.open
}

func tunedOptions() kolostore.PebbleOptions {
	return kolostore.PebbleOptions{MemTableSize: tunedMemTable, NoCompression: true}
}

func TestStoreOptionsBySize(t *testing.T) {
	var b storeBudget
	for _, size := range []int64{0, 1, tunedStoreFrom - 1} {
		o, release := b.options(size)
		if o != (kolostore.PebbleOptions{}) || b.inUse() != 0 {
			t.Fatalf("%d bytes: %+v, %d in use", size, o, b.inUse())
		}
		release()
	}
	for _, size := range []int64{tunedStoreFrom, 1 << 30, 32 << 30} {
		o, release := b.options(size)
		if o != tunedOptions() || b.inUse() != 1 {
			t.Fatalf("%d bytes: %+v, %d in use", size, o, b.inUse())
		}
		release()
		if b.inUse() != 0 {
			t.Fatalf("%d bytes: place not given back", size)
		}
	}
}

func TestStoreBudgetAllowsTwoTunedStores(t *testing.T) {
	var b storeBudget
	_, r1 := b.options(tunedStoreFrom)
	_, r2 := b.options(tunedStoreFrom)
	if o, r3 := b.options(tunedStoreFrom); o != (kolostore.PebbleOptions{}) || b.inUse() != maxTunedStores {
		t.Fatalf("a third large item: %+v, %d in use", o, b.inUse())
	} else {
		r3() // a store of defaults owns no place
	}
	if b.inUse() != maxTunedStores {
		t.Fatalf("releasing the defaults of a third item gave a place back: %d", b.inUse())
	}
	r1()
	r1() // twice is once
	if b.inUse() != 1 {
		t.Fatalf("after one release: %d", b.inUse())
	}
	if o, r := b.options(tunedStoreFrom); o != tunedOptions() || b.inUse() != 2 {
		t.Fatalf("the freed place: %+v, %d in use", o, b.inUse())
	} else {
		r()
	}
	r2()
	if b.inUse() != 0 {
		t.Fatalf("%d in use", b.inUse())
	}
}

func TestOpenStoreCleansUp(t *testing.T) {
	var b storeBudget
	dir := filepath.Join(t.TempDir(), "job")
	_, cleanup, err := openStore(dir, tunedStoreFrom, &b)
	if err != nil || b.inUse() != 1 {
		t.Fatalf("open: %v, %d in use", err, b.inUse())
	}
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) || b.inUse() != 0 {
		t.Fatalf("after cleanup: %v, %d in use", err, b.inUse())
	}

	// A store of defaults for a small item takes no place.
	_, cleanup, err = openStore(dir, 1<<20, &b)
	if err != nil || b.inUse() != 0 {
		t.Fatalf("small: %v, %d in use", err, b.inUse())
	}
	cleanup()
}

func TestOpenStoreFailureGivesThePlaceBack(t *testing.T) {
	var b storeBudget
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openStore(file, tunedStoreFrom, &b); err == nil || b.inUse() != 0 {
		t.Fatalf("a directory that is a file: %v, %d in use", err, b.inUse())
	}
	if _, _, err := openStore(filepath.Join(file, "sub"), tunedStoreFrom, &b); err == nil || b.inUse() != 0 {
		t.Fatalf("a directory under a file: %v, %d in use", err, b.inUse())
	}
}

// bigFile is a file of the size from which an item gets a tuned store, sparse so it costs no disk.
func bigFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(tunedStoreFrom); err != nil {
		t.Fatal(err)
	}
	return path
}

// jobDirs is the number of stores on p's disk; the directory also holds a marker file.
func jobDirs(p *peer) int {
	n := 0
	entries, _ := os.ReadDir(filepath.Join(p.data, transfersDir))
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}

func TestTunedStoreSendEnds(t *testing.T) {
	hold := newSteps()
	alice, bob := networkWith(t, Config{}, Config{}, func(s *ShareService) { s.hooks.prepStep = hold.hook }, nil)
	sa, _ := connect(t, alice, bob)
	alice.waitPrep(t, sa)

	// A file: its size is known at once. Cancelled while it is prepared.
	id, err := alice.Send(sa, bigFile(t, filepath.Join(t.TempDir(), "big.bin")))
	if err != nil {
		t.Fatal(err)
	}
	poll(t, "a tuned store for the file", func() bool { return alice.tuned.inUse() == 1 })
	alice.CancelTransfer(id)
	alice.rec.wait(t, id, stateCancelled, stateStopped)
	poll(t, "the place and the directory given back", func() bool { return alice.tuned.inUse() == 0 && jobDirs(alice) == 0 })

	// A folder: its size comes from the scan. It fails while it is prepared, after its store opened.
	src := filepath.Join(t.TempDir(), "folder")
	if err := os.Mkdir(src, 0o700); err != nil {
		t.Fatal(err)
	}
	locked := bigFile(t, filepath.Join(src, "locked.bin"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	id, err = alice.Send(sa, src)
	if err != nil {
		t.Fatal(err)
	}
	if tr := alice.rec.wait(t, id, stateFailed); tr.Error == nil || tr.Error.Reason != "source-unreadable" {
		t.Fatalf("send: %+v", tr)
	}
	poll(t, "the place and the directory given back", func() bool { return alice.tuned.inUse() == 0 && jobDirs(alice) == 0 })

	// A small file never takes a place.
	hold.release(1 << 10)
	small := offerItem(t, alice, sa, writeFile(t, "small.txt", []byte("small")))
	if alice.tuned.inUse() != 0 {
		t.Fatalf("a small item took a tuned place")
	}
	alice.CancelTransfer(small.ID)
}

func TestTunedStoreReceiveEnds(t *testing.T) {
	alice, bob := network(t, Config{}, func(s *ShareService) {
		s.hooks.wrapContent = func(b koloblobs.Backend) koloblobs.Backend { return slow{b} }
	})
	sa, sb := connect(t, alice, bob)
	sent := offerItem(t, alice, sa, bigFile(t, filepath.Join(t.TempDir(), "big.bin")))
	if alice.tuned.inUse() != 1 {
		t.Fatalf("the sender's store while offered: %d in use", alice.tuned.inUse())
	}
	bob.SetSessionFolder(sb, t.TempDir())
	in := incoming(t, bob, sb, "big.bin")
	if err := bob.SaveOffer(in.ID); err != nil {
		t.Fatal(err)
	}
	bob.rec.wait(t, in.ID, stateVerifying)
	if bob.tuned.inUse() != 1 {
		t.Fatalf("the receiver's store: %d in use", bob.tuned.inUse())
	}
	bob.CancelTransfer(in.ID)
	bob.rec.wait(t, in.ID, stateCancelled)
	alice.rec.wait(t, sent.ID, stateCancelled)
	poll(t, "every place and directory given back", func() bool {
		return alice.tuned.inUse() == 0 && bob.tuned.inUse() == 0 && jobDirs(alice) == 0 && jobDirs(bob) == 0
	})
}
