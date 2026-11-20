//go:build !windows

package main

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// diskFree is the bytes a program that is not the superuser can still write on the volume of dir.
func diskFree(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

// volumeOf names the volume that holds dir, so two folders on one disk are told from two disks.
func volumeOf(dir string) string {
	var st syscall.Stat_t
	if err := syscall.Stat(dir, &st); err != nil {
		return ""
	}
	return fmt.Sprint(st.Dev)
}

// diskFull reports whether err says that a disk has no room left.
func diskFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}
