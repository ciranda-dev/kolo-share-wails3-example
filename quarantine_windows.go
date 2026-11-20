package main

import "os"

// markDownloaded writes the Zone.Identifier stream of a received file. A folder has none, and a file system
// without alternate data streams (FAT, some network shares) refuses it, which the caller only logs.
func markDownloaded(f *os.File) error {
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		return err
	}
	return os.WriteFile(f.Name()+":Zone.Identifier", []byte(zoneIdentifier), 0o666)
}
