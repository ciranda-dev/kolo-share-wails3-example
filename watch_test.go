package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// The bar of a receive stops at its total while the bytes past it, the logs' own overhead, still give a speed.
func TestWatchClampsToTheTotal(t *testing.T) {
	rec := &recorder{transfers: map[string][]Transfer{}}
	s := NewShareService(Config{}, rec.emit, nil)
	j := &job{t: Transfer{ID: "1", Direction: "receive", State: stateReceiving, BytesTotal: 1000}}
	var n atomic.Int64
	stop := s.watch(j, func() int64 { return n.Add(100) })
	time.Sleep(25 * progressTick)
	stop()
	if tr := rec.last("1"); tr.BytesDone != 1000 || tr.BytesPerSecond <= 0 {
		t.Fatalf("%+v: want 1000 bytes shown and a speed", tr)
	}
}

// Bytes that arrive while the count shown does not move, as when an answer cut off is fetched again, keep a receive
// from being marked stalled, and end a stall already shown.
func TestBytesMovedEndsAStall(t *testing.T) {
	rec := &recorder{transfers: map[string][]Transfer{}}
	s := NewShareService(Config{}, rec.emit, nil)
	j := &job{t: Transfer{ID: "1", Direction: "receive", State: stateReceiving, BytesTotal: 1000, BytesDone: 10, Stalled: true}}
	before := time.Now()
	s.bytesMoved(j)
	s.mu.Lock()
	moved := j.moved
	s.mu.Unlock()
	if tr := rec.last("1"); tr.Stalled || tr.BytesDone != 10 || moved.Before(before) {
		t.Fatalf("%+v, moved %v: want a receive no longer stalled, its count unchanged", tr, moved)
	}
}
