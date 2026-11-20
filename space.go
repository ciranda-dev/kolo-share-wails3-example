package main

import (
	"errors"
	"sync"
)

// ErrNoSpace means a disk has less free space than an item needs.
var ErrNoSpace = errors.New("kolo-share: not enough free disk space")

// An item is stored twice before it is saved: the sender keeps an encrypted copy while the peer pulls it, and the
// receiver keeps another while it downloads and then writes the files. The checks below ask for that room before
// the first byte, so a 100 GB item does not fail after hours.
const (
	// spaceShare is the part of an item's size, as 1 over this, that a store holds besides the item: 48 bytes of
	// encryption and a record per chunk of at least 256 KiB (1.2 to 1.4 percent, kolo-blobs docs/limits.md).
	spaceShare = 16
	// tunedSpaceMargin is what a tuned store holds besides that, about 1 GiB whatever the size of the item: its WAL,
	// its flushed memtables and the files a compaction has not removed yet. Measured with items of 3 and 4 GiB, a
	// store was 0.97 and 0.95 GiB over the item and its share. smallSpaceMargin is that of a default store, with
	// room for the system.
	tunedSpaceMargin = 1 << 30
	smallSpaceMargin = 64 << 20
)

func spaceMargin(size int64) int64 {
	if size >= tunedStoreFrom {
		return tunedSpaceMargin
	}
	return smallSpaceMargin
}

// storeNeed is the room a store needs on disk for an item of size bytes.
func storeNeed(size int64) int64 { return size + size/spaceShare + spaceMargin(size) }

// saveNeed is the room the saved files of an item of size bytes take.
func saveNeed(size int64) int64 { return size + smallSpaceMargin/4 }

// storeBound is the most a store of an item that holds size bytes may grow to before the receive stops it: twice
// the room it needs besides the item, so an item exactly at the limit is not cut off by its own overhead.
func storeBound(size int64) int64 { return size + 2*(size/spaceShare+spaceMargin(size)) }

// spaceNeed is room wanted in the volume of dir, for the part of a transfer that the sentence names.
type spaceNeed struct {
	dir   string
	bytes int64
	part  string // "prepare", "download" or "save"
}

// spaceLedger keeps the room that running transfers asked for and have not finished writing, per volume, so two
// 100 GB items started together are not both promised the same free space.
type spaceLedger struct {
	mu   sync.Mutex
	held map[string]int64
}

// reserve holds every need of needs, and returns the function that gives the room back. Needs on one volume are
// added up. It returns a *ShareError of ErrNoSpace, with the bytes needed and the bytes free after the other
// transfers' reservations, when a volume cannot take them; nothing stays held then.
func (s *ShareService) reserve(needs ...spaceNeed) (func(), *ShareError) {
	free := s.hooks.freeSpace
	if free == nil {
		free = diskFree
	}
	type group struct {
		dir   string
		bytes int64
		parts map[string]bool
	}
	volume := s.hooks.volume
	if volume == nil {
		volume = volumeOf
	}
	groups := map[string]*group{}
	var order []string
	for _, n := range needs {
		v := volume(n.dir)
		g := groups[v]
		if g == nil {
			g = &group{dir: n.dir, parts: map[string]bool{}}
			groups[v], order = g, append(order, v)
		}
		g.bytes += n.bytes
		g.parts[n.part] = true
	}
	l := &s.space
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, v := range order {
		g := groups[v]
		f, err := free(g.dir)
		if err != nil {
			continue // a volume that cannot be asked is not refused: the write reports a full disk
		}
		avail := max(f-l.held[v], 0)
		if g.bytes > avail {
			reason := "no-space-" + needPart(g.parts)
			return nil, noSpace(reason, g.bytes, avail)
		}
	}
	for _, v := range order {
		if l.held == nil {
			l.held = map[string]int64{}
		}
		l.held[v] += groups[v].bytes
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			for _, v := range order {
				if l.held[v] -= groups[v].bytes; l.held[v] <= 0 {
					delete(l.held, v)
				}
			}
		})
	}, nil
}

// needPart is the part of a volume's needs that picks its sentence: one part, or "both" when a download and a
// save share the disk.
func needPart(parts map[string]bool) string {
	if len(parts) > 1 {
		return "both"
	}
	for p := range parts {
		return p
	}
	return "download"
}

// noSpace is the error for reason: need bytes are wanted and only avail are free. Detail holds the first and Cause
// the second, as text.
func noSpace(reason string, need, avail int64) *ShareError {
	n, a := humanBytes(need), humanBytes(avail)
	return &ShareError{Kind: kindDisk, Reason: reason, Detail: n, Cause: a, Message: sentence(reason, n, a), Err: ErrNoSpace}
}
