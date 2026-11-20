package main

import "golang.org/x/sys/unix"

// physicalMemory is the installed memory in bytes, 0 when it cannot be read.
func physicalMemory() uint64 {
	var si unix.Sysinfo_t
	if err := unix.Sysinfo(&si); err != nil {
		return 0
	}
	return uint64(si.Totalram) * uint64(si.Unit)
}
