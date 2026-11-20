package main

import (
	"fmt"
	"time"
)

const (
	notchLinger      = 6 * time.Second // the last result stays in the notch this long, then it hides
	notchShortcut    = "CmdOrCtrl+Alt+Shift+S"
	unknownPercent   = -1
	notchWindowWidth = 660 // the library's default content area
	notchWindowH     = 92
)

// NotchState is what the notch window shows, the payload of the "share:notch" event. It never holds a pairing
// code: HasCode says that NotchCopyCode has one to copy. HasOffer says that ID is an incoming offer waiting for
// Save or Decline.
type NotchState struct {
	Active         bool        `json:"active"`
	ID             string      `json:"id"`
	Direction      string      `json:"direction"`
	Kind           string      `json:"kind"`
	Name           string      `json:"name"`
	State          string      `json:"state"`
	Percent        float64     `json:"percent"`
	BytesDone      int64       `json:"bytesDone"`
	BytesTotal     int64       `json:"bytesTotal"`
	BytesPerSecond float64     `json:"bytesPerSecond"`
	HasCode        bool        `json:"hasCode"`
	Message        string      `json:"message"`
	Error          *ShareError `json:"error"` // the reason of Message for failed, to translate it
	FilesTotal     int         `json:"filesTotal"`
	Others         int         `json:"others"`
	PeerName       string      `json:"peerName"`
	HasOffer       bool        `json:"hasOffer"`
}

// notchWindow is the part of the library's *application.NotchWindow the service uses.
type notchWindow interface {
	Show()
	Hide()
}

// pickNotch chooses what the notch shows: the oldest incoming offer waiting, else the running receive, else the
// newest running send, else a pairing code waiting to be used, else last, the transfer that finished most
// recently (nil: none).
func pickNotch(running []Transfer, last *Transfer, codeWaiting bool) NotchState {
	rank := func(t *Transfer) int {
		switch {
		case t.Direction == "receive" && t.State == stateOffered:
			return 0
		case t.Direction == "receive":
			return 1
		}
		return 2
	}
	var shown *Transfer
	for i := range running {
		t := &running[i]
		switch {
		case shown == nil, rank(t) < rank(shown):
			shown = t
		case rank(t) == rank(shown) && rank(t) == 0 && t.Started < shown.Started:
			shown = t
		case rank(t) == rank(shown) && rank(t) > 0 && t.Started > shown.Started:
			shown = t
		}
	}
	switch {
	case shown != nil:
		return notchOf(*shown, len(running)-1)
	case codeWaiting:
		return NotchState{Active: true, State: pairWaiting, HasCode: true, Percent: unknownPercent}
	case last != nil:
		return notchOf(*last, 0)
	}
	return NotchState{Percent: unknownPercent}
}

func notchOf(t Transfer, others int) NotchState {
	n := NotchState{
		Active: !t.final(), ID: t.ID, Direction: t.Direction, Kind: t.Kind, Name: t.Name, State: t.State,
		Percent: unknownPercent, BytesDone: t.BytesDone, BytesTotal: t.BytesTotal, BytesPerSecond: t.BytesPerSecond,
		FilesTotal: t.FilesTotal, Others: others, PeerName: t.PeerName,
	}
	switch t.State {
	case stateOffered:
		n.HasOffer = t.Direction == "receive"
	case statePreparing, stateSending, stateReceiving, stateVerifying:
		if t.BytesTotal > 0 {
			n.Percent = min(100, 100*float64(t.BytesDone)/float64(t.BytesTotal))
		} else if t.State == stateVerifying {
			n.Percent = 100 // nothing to write: an empty file or folder
		}
	case stateDone:
		n.Percent = 100
		n.Message = doneSentence(t)
	case stateDelivered:
		n.Percent = 100
		n.Message = fmt.Sprintf("%s saved %s.", t.PeerName, t.Name) // the receiver says so; it is not checked
	case stateDeclined:
		n.Message = "Declined."
	case stateWithdrawn:
		n.Message = "The sender cancelled it."
	case stateFailed:
		if t.Error != nil {
			n.Message, n.Error = t.Error.Message, t.Error
		}
	case stateCancelled:
		n.Message = "Cancelled."
	case stateStopped:
		n.Message = "Stopped."
	}
	return n
}

// doneSentence is the result of a receive that worked.
func doneSentence(t Transfer) string {
	if t.Kind == kindFile {
		return fmt.Sprintf("Saved %s.", t.Name)
	}
	if t.FilesTotal == 1 {
		return "Saved 1 file."
	}
	return fmt.Sprintf("Saved %d files.", t.FilesTotal)
}

// Notch returns what the notch window shows now: nothing while "Show transfers in the notch" is off.
func (s *ShareService) Notch() NotchState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.saved.showInNotch() {
		return NotchState{Percent: unknownPercent}
	}
	return s.notchLocked()
}

// NotchCopyCode puts the pairing code that waits to be used on the clipboard, here in Go, so the notch page never
// holds the code. It does nothing when no code waits.
func (s *ShareService) NotchCopyCode() {
	s.copyCode(s.waitingCode())
}

// copyCode puts a pairing code on the clipboard and remembers it, so the end of the pairing can take it back: the
// clipboard is readable by every program and clipboard managers keep a history.
func (s *ShareService) copyCode(code string) {
	if code == "" || s.copyText == nil {
		return
	}
	s.mu.Lock()
	s.copied = code
	s.mu.Unlock()
	s.copyText(code)
}

// clearCopiedCode empties the clipboard when it still holds the code this app copied; anything else the person
// copied since stays.
func (s *ShareService) clearCopiedCode() {
	s.mu.Lock()
	code := s.copied
	s.copied = ""
	s.mu.Unlock()
	if code != "" && s.copyText != nil && s.clipText != nil && s.clipText() == code {
		s.copyText("")
	}
}

// waitingCode is the pairing code this app shows, "" when none waits.
func (s *ShareService) waitingCode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pr := s.pairing; pr != nil && pr.p.Role == roleInviter && pr.p.State == pairWaiting {
		return pr.p.Code
	}
	return ""
}

// NotchSaveOffer saves the offer id, the one the notch page showed when the person clicked: never another one that
// took its place meanwhile. When it cannot (no usable folder, the session offline, the offer gone), it opens the
// app instead.
func (s *ShareService) NotchSaveOffer(id string) {
	if s.SaveOffer(id) != nil {
		s.NotchOpenApp()
	}
}

// NotchDeclineOffer declines the offer id, the one the notch page showed.
func (s *ShareService) NotchDeclineOffer(id string) {
	s.DeclineOffer(id)
}

// sendNotch sends the notch window what it shows now, while the setting is on. notchMu orders the sends with
// the setting's changes, and the setting is read under it, so no state follows the empty one of "off".
func (s *ShareService) sendNotch() {
	s.notchMu.Lock()
	defer s.notchMu.Unlock()
	s.mu.Lock()
	n, on := s.notchLocked(), s.saved.showInNotch()
	s.mu.Unlock()
	if s.hooks.notchHook != nil {
		s.hooks.notchHook()
	}
	if on && s.emitNotch != nil {
		s.emitNotch("share:notch", n)
	}
}

func (s *ShareService) notchLocked() NotchState {
	running := make([]Transfer, 0, len(s.jobs))
	for _, j := range s.jobs {
		running = append(running, j.t)
	}
	var last *Transfer
	if s.lastDone != nil && time.Since(s.lastDoneAt) < notchLinger {
		last = s.lastDone
	}
	pr := s.pairing
	return pickNotch(running, last, pr != nil && pr.p.Role == roleInviter && pr.p.State == pairWaiting)
}

// NotchShow shows the notch window.
func (s *ShareService) NotchShow() {
	if s.notch != nil {
		s.notch.Show()
	}
}

// NotchHide hides the notch window; its page stays loaded.
func (s *ShareService) NotchHide() {
	if s.notch != nil {
		s.notch.Hide()
	}
}

// NotchOpenApp brings the main window to the front.
func (s *ShareService) NotchOpenApp() {
	if s.openApp != nil {
		s.openApp()
	}
}

// SetShowInNotch turns the setting "Show in the notch" on or off and saves it.
func (s *ShareService) SetShowInNotch(on bool) (Settings, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if !s.started {
		return Settings{}, explain(ErrNotReady, limits{})
	}
	s.mu.Lock()
	saved := s.saved
	saved.ShowInNotch = &on
	s.mu.Unlock()
	if err := saveSettings(s.set.DataDir, saved); err != nil {
		return Settings{}, explain(err, limits{})
	}
	s.notchMu.Lock()
	s.mu.Lock()
	s.saved = saved
	s.mu.Unlock()
	if !on && s.emitNotch != nil {
		s.emitNotch("share:notch", NotchState{Percent: unknownPercent}) // the page forgets what it showed
	}
	s.notchMu.Unlock()
	if on {
		s.sendNotch()
	} else {
		s.NotchHide()
	}
	s.pushTray()
	return s.Settings(), nil
}

// notchChanged tells the notch page what it shows, opens the window when a transfer starts and hides it a while
// after the last one ends. started is true for the first update of a transfer.
func (s *ShareService) notchChanged(t Transfer, started bool) {
	s.mu.Lock()
	if t.final() {
		s.lastDone, s.lastDoneAt = &t, time.Now()
	}
	show := s.saved.showInNotch()
	if started && t.Direction == "receive" {
		// A peer's offers open the notch at most once per notchLinger, so a stream of offers cannot keep popping it.
		show = show && time.Since(s.lastOfferShow) >= notchLinger
		if show {
			s.lastOfferShow = time.Now()
		}
	}
	idle := len(s.jobs) == 0 || (len(s.jobs) == 1 && t.final() && s.jobs[t.ID] != nil)
	if started && s.notchTimer != nil {
		s.notchTimer.Stop()
		s.notchTimer = nil
	}
	if t.final() && idle {
		if s.notchTimer != nil {
			s.notchTimer.Stop()
		}
		s.notchTimer = time.AfterFunc(notchLinger, s.notchExpired)
	}
	s.mu.Unlock()
	s.sendNotch()
	s.pushTray()
	if started && show {
		s.NotchShow()
	}
}

// notchExpired runs notchLinger after the last transfer ended: the notch forgets it and hides, unless a new
// transfer started meanwhile.
func (s *ShareService) notchExpired() {
	s.mu.Lock()
	if len(s.jobs) > 0 {
		s.mu.Unlock()
		return
	}
	s.lastDone, s.notchTimer = nil, nil
	s.mu.Unlock()
	s.sendNotch()
	s.NotchHide()
}
