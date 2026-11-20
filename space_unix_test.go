//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestDiskFullIsATypedError(t *testing.T) {
	err := &os.PathError{Op: "write", Path: "/x/a.bin", Err: syscall.ENOSPC}
	se := explain(err, limits{})
	if se.Reason != "disk-full" || !errors.Is(se, ErrNoSpace) || se.Detail != "/x/a.bin" || se.Kind != kindDisk {
		t.Fatalf("%+v", se)
	}
}
