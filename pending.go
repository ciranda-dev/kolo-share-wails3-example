package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const (
	pendingFile    = "pending.json"
	maxPendingFile = 64 << 10
)

// pendingEntry is a receive being written: in Folder, the tree under Part and the name Top reserved for it.
type pendingEntry struct {
	Folder string `json:"folder"`
	Part   string `json:"part"`
	Top    string `json:"top"`
}

// pendingList keeps the receives being written in a file of the data directory, so that the next start removes
// what a crash, a kill or a power loss left.
type pendingList struct {
	mu    sync.Mutex
	path  string
	items []pendingEntry
}

func (l *pendingList) add(e pendingEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, e)
	return l.save()
}

func (l *pendingList) remove(e pendingEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = slices.DeleteFunc(l.items, func(x pendingEntry) bool { return x == e })
	l.save()
}

func (l *pendingList) save() error {
	b, err := json.Marshal(l.items)
	if err != nil {
		return err
	}
	return writeFileAtomic(l.path, b)
}

// sweepPending removes what the receives in the pending file of dir left: the ".kolo-share-*.part" entry, and the
// reserved name when it is still an empty placeholder. It touches nothing else, and only inside each folder.
func sweepPending(dir string) (*pendingList, error) {
	l := &pendingList{path: filepath.Join(dir, pendingFile)}
	info, err := os.Lstat(l.path)
	if errors.Is(err, fs.ErrNotExist) {
		return l, nil
	}
	if err != nil {
		return nil, err
	}
	var items []pendingEntry
	if info.Mode().IsRegular() && info.Size() <= maxPendingFile {
		if b, err := os.ReadFile(l.path); err == nil {
			json.Unmarshal(b, &items)
		}
	}
	for _, e := range items {
		e.sweep()
	}
	return l, l.save()
}

func (e pendingEntry) sweep() {
	if !filepath.IsAbs(e.Folder) || !ownPart(e.Part) || !oneName(e.Top) {
		return
	}
	root, err := os.OpenRoot(e.Folder)
	if err != nil {
		return
	}
	defer root.Close()
	if _, err := root.Lstat(e.Part); err != nil {
		return // renamed onto Top before the crash: the receive was done
	}
	root.RemoveAll(e.Part)
	if info, err := root.Lstat(e.Top); err == nil && (info.Mode().IsRegular() && info.Size() == 0 || info.IsDir()) {
		root.Remove(e.Top) // a folder goes only when it is empty
	}
}

// ownPart reports whether name is a part name this app makes.
func ownPart(name string) bool {
	return oneName(name) && strings.HasPrefix(name, partPrefix) && strings.HasSuffix(name, partSuffix)
}

// oneName reports whether name is one plain name: no separator, not "." or "..".
func oneName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`) && filepath.Base(name) == name
}
