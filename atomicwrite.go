package main

import (
	"os"
	"path/filepath"
)

// fsyncFile and fsyncDir are the two syncs of writeFileAtomic; tests replace them to see the order and to fail.
var (
	fsyncFile = func(f *os.File) error { return f.Sync() }
	fsyncDir  = syncDir
)

// writeFileAtomic replaces path with b, mode 0600, so that after a crash or a power loss path holds the old
// content or the new one, never an empty file: write a temporary file next to it, sync it, rename it over path,
// then sync the folder so the rename itself is on disk.
func writeFileAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = fsyncFile(f)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
		return err
	}
	return fsyncDir(dir)
}
