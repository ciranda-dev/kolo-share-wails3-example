//go:build !windows

package main

import "os"

// syncDir makes the entries of dir, a rename in it for one, durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
