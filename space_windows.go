//go:build windows

package main

import (
	"errors"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// diskFree is the bytes the user can still write on the volume of dir.
func diskFree(dir string) (int64, error) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, err
	}
	return int64(min(free, 1<<62)), nil
}

// volumeOf names the volume of dir by its drive or share; "" when it has none, which counts as one volume.
func volumeOf(dir string) string {
	return strings.ToUpper(filepath.VolumeName(dir))
}

// diskFull reports whether err says that a disk has no room left.
func diskFull(err error) bool {
	return errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL)
}
