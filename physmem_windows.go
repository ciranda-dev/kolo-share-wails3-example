package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// memoryStatusEx is the MEMORYSTATUSEX of GlobalMemoryStatusEx.
type memoryStatusEx struct {
	length               uint32
	memoryLoad           uint32
	totalPhys            uint64
	availPhys            uint64
	totalPageFile        uint64
	availPageFile        uint64
	totalVirtual         uint64
	availVirtual         uint64
	availExtendedVirtual uint64
}

// physicalMemory is the installed memory in bytes, 0 when it cannot be read.
func physicalMemory() uint64 {
	st := memoryStatusEx{length: uint32(unsafe.Sizeof(memoryStatusEx{}))}
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
	if r, _, _ := proc.Call(uintptr(unsafe.Pointer(&st))); r == 0 {
		return 0
	}
	return st.totalPhys
}
