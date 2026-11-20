//go:build !darwin && !windows

package main

import "os"

// markDownloaded does nothing: Linux desktops have no common mark for downloaded files.
func markDownloaded(*os.File) error { return nil }
