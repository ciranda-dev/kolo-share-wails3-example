//go:build !windows

package main

import (
	"runtime"
	"syscall"
)

// maxRSS is the peak resident size of this process in bytes.
func maxRSS() int64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss) // bytes
	}
	return int64(ru.Maxrss) << 10 // KiB
}
