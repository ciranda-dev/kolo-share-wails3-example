//go:build windows

package main

// maxRSS is not measured on Windows.
func maxRSS() int64 { return 0 }
