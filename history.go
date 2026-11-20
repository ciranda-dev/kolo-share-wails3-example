package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

const (
	maxHistory     = 50
	maxHistoryFile = 1 << 20 // bytes; 50 items take a few KiB, so a larger file is not ours
	historyFile    = "history.json"
)

// HistoryItem is one finished transfer, as the window lists it. It never holds a code or a session secret.
type HistoryItem struct {
	ID        string `json:"id"`
	Direction string `json:"direction"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Result    string `json:"result"` // a final Transfer state
	Message   string `json:"message"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
	SavedTo   string `json:"savedTo"`
	At        int64  `json:"at"` // ms since epoch
	// Error is the reason of Message, to translate it; nil when the transfer did not fail.
	Error    *ShareError `json:"error"`
	PeerName string      `json:"peerName"`
	PeerKey  string      `json:"peerKey"` // the peer's short key, a label
	PeerID   string      `json:"peerId"`  // the peer's whole key in hex, to list the history of one peer
}

// history keeps the last maxHistory items, newest first, in a file of the data directory.
type history struct {
	mu    sync.Mutex
	path  string
	items []HistoryItem
}

// openHistory reads the history file of dir. A missing file is an empty history; a file that cannot be parsed is
// replaced by an empty history at the next write, since it holds nothing the app needs.
func openHistory(dir string) (*history, error) {
	h := &history{path: filepath.Join(dir, historyFile)}
	info, err := os.Lstat(h.path)
	if errors.Is(err, fs.ErrNotExist) {
		return h, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxHistoryFile {
		return h, nil
	}
	b, err := os.ReadFile(h.path)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(b, &h.items) != nil {
		h.items = nil
	}
	if len(h.items) > maxHistory {
		h.items = h.items[:maxHistory]
	}
	return h, nil
}

// add puts it first, drops the oldest past maxHistory and saves.
func (h *history) add(it HistoryItem) ([]HistoryItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.items = append([]HistoryItem{it}, h.items...)
	if len(h.items) > maxHistory {
		h.items = h.items[:maxHistory]
	}
	return h.listLocked(), h.save()
}

func (h *history) clear() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.items = nil
	return h.save()
}

func (h *history) list() []HistoryItem {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.listLocked()
}

func (h *history) listLocked() []HistoryItem {
	return append([]HistoryItem{}, h.items...)
}

func (h *history) save() error {
	b, err := json.Marshal(h.items)
	if err != nil {
		return err
	}
	return writeFileAtomic(h.path, b)
}
