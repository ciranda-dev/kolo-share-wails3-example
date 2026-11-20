//go:build !darwin && !linux && !windows

package main

// physicalMemory is 0 where the app does not read it: the limit is then unknownMemoryLimit.
func physicalMemory() uint64 { return 0 }
