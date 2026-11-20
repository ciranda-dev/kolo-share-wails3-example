package main

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// markDownloaded sets the quarantine attribute on a received file or folder, through its open descriptor, so
// Gatekeeper checks it the first time it is opened, as for a file from a browser.
func markDownloaded(f *os.File) error {
	return unix.Fsetxattr(int(f.Fd()), "com.apple.quarantine", []byte(quarantineValue(time.Now())), 0)
}
