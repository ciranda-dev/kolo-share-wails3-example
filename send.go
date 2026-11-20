package main

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	kolofiles "github.com/ciranda-dev/kolo-files"
)

// checkSource says whether path can be sent, and as what.
func checkSource(path string) (kind, name string, err error) {
	if path == "" || !filepath.IsAbs(path) {
		return "", "", shareErr("source-missing", ErrSource, "")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", "", shareErr("source-unreadable", err, path)
	}
	name = filepath.Base(path)
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return "", "", shareErr("source-link", ErrSource, path)
	case !info.IsDir() && !info.Mode().IsRegular():
		return "", "", shareErr("source-special", ErrSource, path)
	case !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, `/\`):
		return "", "", shareErr("source-root", ErrSource, "")
	case info.IsDir():
		return kindFolder, name, nil
	}
	return kindFile, name, nil
}

// scan is what a folder holds: files and their bytes, and the links and special files it skips.
type scan struct {
	files, nodes, skipped int
	bytes                 int64
}

// scanSource counts what path holds and refuses more than maxNodes files and folders, the limit a receiver of
// this app applies too. Links and special files are not sent. Hidden files are.
func scanSource(path string, maxNodes int) (scan, error) {
	var sc scan
	err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return sourceErr(p, err)
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			sc.skipped++
			return nil
		}
		if sc.nodes++; sc.nodes > maxNodes {
			return shareErr("source-too-many", ErrTooManyFiles, strconv.Itoa(maxNodes))
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return sourceErr(p, err)
			}
			sc.files++
			sc.bytes += info.Size()
		}
		return nil
	})
	return sc, err
}

func sourceErr(p string, err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	return shareErr("source-unreadable", err, p)
}

// openRegular opens the file p, and refuses it when p is a link by now: the walk saw a regular file, and a program
// that can write in the folder may have put a link in its place since. The opened file must be the one p names.
func openRegular(p string) (*os.File, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, sourceErr(p, err)
	}
	fi, err := f.Stat()
	if err == nil {
		var li os.FileInfo
		if li, err = os.Lstat(p); err == nil && (!li.Mode().IsRegular() || !fi.Mode().IsRegular() || !os.SameFile(fi, li)) {
			f.Close()
			return nil, shareErr("source-link", ErrSource, p)
		}
	}
	if err != nil {
		f.Close()
		return nil, sourceErr(p, err)
	}
	return f, nil
}

// fillVolume writes path into v under name: a file as the one file of the volume, a folder as its one top folder
// with everything below it. Links and special files are skipped.
func fillVolume(ctx context.Context, v *kolofiles.Volume, path, name string, stored io.Writer) error {
	return filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return sourceErr(p, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return sourceErr(p, err)
		}
		vname := name
		if rel != "." {
			vname = name + "/" + filepath.ToSlash(rel)
		}
		switch {
		case d.IsDir():
			err = v.Mkdir(vname)
		case d.Type().IsRegular():
			if _, serr := v.Stat(vname); serr == nil {
				// kolo-files stores names in NFC: two names on this disk that differ only in normalization.
				return shareErr("source-normalization", ErrSource, p)
			}
			var f *os.File
			if f, err = openRegular(p); err != nil {
				return err
			}
			err = v.WriteFile(ctx, vname, io.TeeReader(f, stored))
			f.Close()
		default:
			return nil
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			return shareErr("source-store", err, p)
		}
		return err
	})
}
