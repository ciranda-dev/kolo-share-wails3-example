package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	transfersMarker = ".kolo-share"
	mainWindowName  = "main"
)

// prepareDataDir creates dir with mode 0700, or checks that an existing one is not open to other users, since it
// holds the identity and the stores of the transfers.
func prepareDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return shareErr("data-dir-open", nil, dir)
	}
	return nil
}

// resetTransfers empties the transfers folder of dir, which only this app writes: it holds the stores of the
// transfers of an earlier run that was killed. A transfers folder without the app's marker is not touched.
func resetTransfers(dir string) error {
	tmp := filepath.Join(dir, transfersDir)
	entries, err := os.ReadDir(tmp)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case len(entries) > 0:
		if _, err := os.Lstat(filepath.Join(tmp, transfersMarker)); err != nil {
			return shareErr("data-dir-foreign", nil, dir)
		}
		if err := os.RemoveAll(tmp); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(tmp, transfersMarker), nil, 0o600)
}

// fromMainWindow reports whether a bound call comes from the main window. Wails puts the calling window in the
// context; a call without one (a test) counts as another window.
func fromMainWindow(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	w, ok := ctx.Value(application.WindowKey).(interface{ Name() string })
	return ok && w.Name() == mainWindowName
}
