package main

import (
	"encoding/hex"
	"errors"
	"log"
	"maps"
	"reflect"
	"slices"

	kolorelay "github.com/ciranda-dev/kolo-relay"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
)

// The app's swarms that hold a place at the relays: the sessions' one, and one per pairing code that waits, named
// swarmPairing and the pairing's ID, so a pairing that ends forgets only its own states.
const (
	swarmSessions = "sessions"
	swarmPairing  = "pairing:"
)

// States of a relay, as Settings shows them.
const (
	relayIdle        = "idle"         // no code shown and no session open: the app holds no place there
	relayReserved    = "reserved"     // a computer that cannot connect directly can reach this app through it
	relayNotReserved = "not-reserved" // the relay gave no place; Error says why
)

// RelayStatus is one relay in Network.Relays.
type RelayStatus struct {
	Addr  string      `json:"addr"`
	Key   string      `json:"key"` // the relay's kolo-id key in hex
	State string      `json:"state"`
	Error *ShareError `json:"error"` // why it holds no place; nil otherwise
}

// relayChanged records a RelayChanged event of one of the app's swarms, logs an error the first time it happens
// with that relay and tells the window when a state changed.
func (s *ShareService) relayChanged(which string, ev koloswarm.Event) {
	if errors.Is(ev.Err, kolorelay.ErrReservationEnded) {
		return // a place ends with the relay's lifetime and kolo-swarm takes a new one: not a failure
	}
	s.mu.Lock()
	addr := ""
	for _, r := range s.set.relays {
		if r.PublicKey.Equal(ev.PublicKey) {
			addr = r.Addr
		}
	}
	st := RelayStatus{State: relayReserved}
	switch {
	case ev.Err == nil:
	case errors.Is(ev.Err, koloswarm.ErrClosed):
		st.State = relayIdle
	default:
		st = RelayStatus{State: relayNotReserved, Error: relayError(ev.Err, addr, which != swarmSessions)}
	}
	if s.relayStates == nil {
		s.relayStates, s.relayLogged = map[string]map[string]RelayStatus{}, map[string]bool{}
	}
	if s.relayStates[which] == nil {
		s.relayStates[which] = map[string]RelayStatus{}
	}
	s.relayStates[which][string(ev.PublicKey)] = st
	logIt := false
	if st.Error != nil {
		seen := string(ev.PublicKey) + st.Error.Reason
		logIt, s.relayLogged[seen] = !s.relayLogged[seen], true
	}
	s.mu.Unlock()
	if logIt {
		log.Print("relay ", addr, ": ", st.Error.Message)
	}
	s.pushRelays()
}

// forgetRelays drops what a swarm said, once it is closed.
func (s *ShareService) forgetRelays(which string) {
	s.mu.Lock()
	delete(s.relayStates, which)
	s.mu.Unlock()
	s.pushRelays()
}

// pushRelays sends the network again when the relays' states changed.
func (s *ShareService) pushRelays() {
	s.mu.Lock()
	rs := s.relayStatusesLocked()
	if reflect.DeepEqual(rs, s.net.Relays) {
		s.mu.Unlock()
		return
	}
	s.net.Relays, s.net.Seq = rs, s.net.Seq+1
	n := s.net
	s.mu.Unlock()
	s.emit("share:network", n)
}

// relayStatusesLocked is every listed relay with its state: the sessions' one when it holds or was refused a place,
// since a code that cannot wait at a relay does not change what the sessions have there; else the worst state of a
// waiting code.
func (s *ShareService) relayStatusesLocked() []RelayStatus {
	out := make([]RelayStatus, 0, len(s.set.relays))
	for _, r := range s.set.relays {
		st, ok := s.relayStates[swarmSessions][string(r.PublicKey)]
		if !ok || st.State == relayIdle {
			st = RelayStatus{State: relayIdle}
			for _, which := range slices.Sorted(maps.Keys(s.relayStates)) {
				x := s.relayStates[which][string(r.PublicKey)]
				if which != swarmSessions && (x.State == relayNotReserved || x.State == relayReserved && st.State == relayIdle) {
					st = x
				}
			}
		}
		st.Addr, st.Key = r.Addr, hex.EncodeToString(r.PublicKey)
		out = append(out, st)
	}
	return out
}

// relayError is the reason a relay holds no place for this app. A pairing's key is made for its code, so no allow
// list names it.
func relayError(err error, addr string, pairing bool) *ShareError {
	switch {
	case errors.Is(err, kolorelay.ErrNotAllowed) && pairing:
		return shareErr("relay-pairing-not-allowed", err, addr)
	case errors.Is(err, kolorelay.ErrNotAllowed):
		return shareErr("relay-not-allowed", err, addr)
	case errors.Is(err, kolorelay.ErrLimit):
		return shareErr("relay-full", err, addr)
	case errors.Is(err, kolosecure.ErrUnexpectedPeer):
		return shareErr("relay-wrong-key", err, addr)
	case errors.Is(err, koloswarm.ErrInvalidConfig):
		return shareErr("relay-bad-key", err, addr)
	}
	return shareErr("relay-unreachable", err, addr)
}
