package main

import (
	"os"
	"sync"

	kolostore "github.com/ciranda-dev/kolo-store"
)

const (
	// tunedStoreFrom is the size from which an item gets a tuned store. Below it the store keeps Pebble's
	// defaults, so a session with many small items stays light.
	tunedStoreFrom = 64 << 20
	// tunedMemTable is the memtable of a tuned store. A store can hold up to twice this in memory.
	tunedMemTable = 256 << 20
	// maxTunedStores bounds the tuned stores open at once in the app, so the memory they add stays under
	// maxTunedStores * 2 * tunedMemTable (1 GiB). A large item that finds them all taken uses the defaults.
	maxTunedStores = 2
)

// storeBudget counts the tuned stores that are open. The zero value has none open.
type storeBudget struct {
	mu   sync.Mutex
	open int
}

// options returns the Pebble options for a store of size bytes, and the function that gives its place back.
// A large item gets the tuned options while a place is free; every other item gets the defaults.
func (b *storeBudget) options(size int64) (kolostore.PebbleOptions, func()) {
	if size < tunedStoreFrom {
		return kolostore.PebbleOptions{}, func() {}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open >= maxTunedStores {
		return kolostore.PebbleOptions{}, func() {}
	}
	b.open++
	var once sync.Once
	return kolostore.PebbleOptions{MemTableSize: tunedMemTable, NoCompression: true}, func() {
		once.Do(func() {
			b.mu.Lock()
			b.open--
			b.mu.Unlock()
		})
	}
}

// deferWrites says whether a store groups its commits (see laterBackend). Benchmarks turn it off to measure what it
// gives; nothing else does.
var deferWrites = true

// openStore opens a kolo-store on Pebble in dir, tuned when size and the budget allow, and returns it with the
// function that closes and removes it and gives its place in the budget back.
func openStore(dir string, size int64, b *storeBudget) (*kolostore.Store, func(), error) {
	o, release := b.options(size)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		release()
		return nil, nil, err
	}
	db, err := kolostore.OpenPebbleWith(dir, o)
	if err != nil {
		os.RemoveAll(dir)
		release()
		return nil, nil, err
	}
	var be kolostore.Backend = db
	if deferWrites {
		be = newLaterBackend(db)
	}
	st := kolostore.New(be)
	return st, func() {
		st.Close()
		os.RemoveAll(dir)
		release()
	}, nil
}
