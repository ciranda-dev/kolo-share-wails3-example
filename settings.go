package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	settingsFile    = "settings.json"
	maxSettingsFile = 64 << 10
)

// Settings is what the Settings view shows. The network comes only from flags or environment variables, so
// the network is read only; DisplayName changes with SetDisplayName and ShowInNotch with SetShowInNotch.
type Settings struct {
	Mode           string `json:"mode"`           // "public", "own" (-bootstrap or -first) or "local" (-local)
	EntryNodes     string `json:"entryNodes"`     // a comma list of your own entry nodes: "" in public and local mode
	EntryNodesFrom string `json:"entryNodesFrom"` // "flag", "env", "local", "public" or "" (only -first)
	FirstNode      string `json:"firstNode"`      // the entry node this app runs, with -first or in local mode
	LocalNode      string `json:"localNode"`      // the address of local mode, "" in the other modes
	DataDir        string `json:"dataDir"`
	Listen         string `json:"listen"`
	MaxDownload    int64  `json:"maxDownload"`
	MaxFiles       int    `json:"maxFiles"`
	ShowInNotch    bool   `json:"showInNotch"`
	DisplayName    string `json:"displayName"` // shown to the people you connect to; "" for none
	PublicKey      string `json:"publicKey"`   // your short key, the first 8 hex characters: a label
	Fingerprint    string `json:"fingerprint"` // 128 bits of your key, for the other person to compare
	PairTTL        int64  `json:"pairTTL"`     // milliseconds a pairing code lives, from -pair-ttl
	ResumeFor      int64  `json:"resumeFor"`   // milliseconds a download waits for bytes that stopped, from -resume-for

	// ConnectionRelays are the relays from -relay, -relay-file or KOLO_SHARE_RELAYS, a comma list of host:port@key.
	ConnectionRelays     string `json:"connectionRelays"`
	ConnectionRelaysFrom string `json:"connectionRelaysFrom"` // "flag", "file", "flag-and-file", "env" or "none"
}

// savedSettings is the file kept in the data directory.
type savedSettings struct {
	ShowInNotch *bool  `json:"showInNotch,omitempty"` // nil: on
	DisplayName string `json:"displayName,omitempty"`
}

func (s savedSettings) showInNotch() bool { return s.ShowInNotch == nil || *s.ShowInNotch }

// loadSaved reads the saved settings of dir; a missing, oversized or unreadable file gives the zero value.
func loadSaved(dir string) (savedSettings, error) {
	var s savedSettings
	path := filepath.Join(dir, settingsFile)
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSettingsFile {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	if json.Unmarshal(b, &s) != nil {
		return savedSettings{}, nil
	}
	return s, nil
}

func saveSettings(dir string, s savedSettings) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, settingsFile), b)
}
