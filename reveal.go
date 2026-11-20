package main

import (
	"os"
	"path/filepath"
)

// command is a program and its arguments, run without a shell.
type command struct {
	name string
	args []string
}

// revealCommand is how goos shows path, an absolute path, selected in its file manager, without opening it: a
// received "x.app" folder must never launch. The path is one argument, never parsed by a shell and never
// expanded, so names such as "$HOME", "%PATH%", "-x" or "a --flag" stay names; an absolute path cannot start with
// "-". Linux has no common "select" call outside D-Bus, so its file manager opens the parent folder.
func revealCommand(goos, path string) command {
	switch goos {
	case "darwin":
		return command{"open", []string{"-R", path}}
	case "windows":
		// Explorer parses its own command line: "/select," and the path in quotes, which a Windows name never holds.
		return command{"explorer.exe", []string{`/select,"` + path + `"`}}
	}
	return command{"xdg-open", []string{filepath.Dir(path)}}
}

// Reveal shows a file or folder that a receive saved, selected in the system file manager. It refuses every other
// path: only a savedTo of a transfer in the history is accepted.
func (s *ShareService) Reveal(path string) error {
	if path == "" || !filepath.IsAbs(path) || !s.savedByUs(filepath.Clean(path)) {
		return shareErr("reveal-refused", ErrNotSaved, "")
	}
	path = filepath.Clean(path)
	if _, err := os.Lstat(path); err != nil {
		return shareErr("reveal-failed", err, path)
	}
	if s.reveal == nil {
		return shareErr("no-dialogs", nil, "")
	}
	if err := s.reveal(path); err != nil {
		return shareErr("reveal-failed", err, path)
	}
	return nil
}

func (s *ShareService) savedByUs(path string) bool {
	for _, it := range s.History() {
		if it.Direction == "receive" && it.Result == stateDone && it.SavedTo != "" && filepath.Clean(it.SavedTo) == path {
			return true
		}
	}
	return false
}

// downloadsFolder is the user's Downloads folder when it exists, the usual place to save into.
func downloadsFolder() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, "Downloads")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	return dir
}
