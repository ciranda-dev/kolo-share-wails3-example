package main

import "runtime/debug"

const (
	// minMemoryLimit is the least soft limit the app sets, so a small computer is not made to collect garbage all
	// the time.
	minMemoryLimit = 512 << 20
	// unknownMemoryLimit is the soft limit when the physical memory cannot be read.
	unknownMemoryLimit = 2 << 30
)

// memoryLimit is the soft limit to set, in bytes: half of the physical memory, never under minMemoryLimit, and
// unknownMemoryLimit when physical is 0. It is 0, no change, when gomemlimit (the GOMEMLIMIT variable) is set,
// since the runtime already applies it.
func memoryLimit(gomemlimit string, physical uint64) int64 {
	if gomemlimit != "" {
		return 0
	}
	if physical == 0 {
		return unknownMemoryLimit
	}
	return int64(max(physical/2, minMemoryLimit))
}

// limitMemory sets the soft memory limit of the Go runtime at start and returns it, 0 when GOMEMLIMIT is set.
func limitMemory(getenv func(string) string) int64 {
	n := memoryLimit(getenv("GOMEMLIMIT"), physicalMemory())
	if n > 0 {
		debug.SetMemoryLimit(n)
	}
	return n
}
