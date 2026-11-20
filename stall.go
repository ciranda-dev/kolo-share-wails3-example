package main

import (
	"sync/atomic"
	"time"
)

// stallClock records when a receive last got a byte, on the monotonic clock. That clock does not run while the
// computer sleeps, so a download that was cut by a laptop asleep for an hour goes on when it wakes, instead of
// giving up at once because a wall clock says the last byte was an hour ago.
type stallClock struct {
	base time.Time    // has a monotonic reading
	at   atomic.Int64 // when the last byte came, as time since base
}

func newStallClock() *stallClock { return &stallClock{base: time.Now()} }

// touch records a byte now.
func (c *stallClock) touch() { c.at.Store(int64(time.Since(c.base))) }

// mark is a value that changes whenever touch is called after it.
func (c *stallClock) mark() int64 { return c.at.Load() }

// idle is how long it has been since the last byte, not counting the time the computer slept.
func (c *stallClock) idle() time.Duration { return time.Since(c.base) - time.Duration(c.at.Load()) }
