package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteFileAtomicSyncsTheFileThenTheFolder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.json")
	os.WriteFile(path, []byte("old"), 0o600)
	defer func(f func(*os.File) error, d func(string) error) { fsyncFile, fsyncDir = f, d }(fsyncFile, fsyncDir)
	var events []string
	read := func() string { b, _ := os.ReadFile(path); return string(b) }
	fsyncFile = func(f *os.File) error {
		// The new bytes are in the temporary file and the name still holds the old content.
		b, _ := os.ReadFile(f.Name())
		events = append(events, "file:"+string(b)+":"+read())
		return f.Sync()
	}
	fsyncDir = func(d string) error {
		events = append(events, "dir:"+filepath.Base(d)+":"+read())
		return nil
	}
	if err := writeFileAtomic(path, []byte("new")); err != nil {
		t.Fatal(err)
	}
	want := []string{"file:new:old", "dir:" + filepath.Base(dir) + ":new"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events %q, want %q", events, want)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 && runtime.GOOS != "windows" {
		t.Fatalf("mode %v %v", info.Mode(), err)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".tmp-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestWriteFileAtomicKeepsTheOldFileWhenASyncFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	os.WriteFile(path, []byte("old"), 0o600)
	defer func(f func(*os.File) error) { fsyncFile = f }(fsyncFile)
	boom := errors.New("disk fell over")
	fsyncFile = func(*os.File) error { return boom }
	if err := writeFileAtomic(path, []byte("new")); !errors.Is(err, boom) {
		t.Fatalf("error %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "old" {
		t.Fatalf("a failed write changed the file: %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".tmp-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
}

func TestWriteFileAtomicReportsAFolderSyncError(t *testing.T) {
	dir := t.TempDir()
	defer func(d func(string) error) { fsyncDir = d }(fsyncDir)
	boom := errors.New("folder sync")
	fsyncDir = func(string) error { return boom }
	if err := writeFileAtomic(filepath.Join(dir, "a.json"), []byte("x")); !errors.Is(err, boom) {
		t.Fatalf("error %v", err)
	}
}

func TestSyncDirOnARealFolder(t *testing.T) {
	if err := syncDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := syncDir(filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Fatal("a missing folder is an error")
		}
	}
}
