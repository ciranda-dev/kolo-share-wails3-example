package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	koloid "github.com/ciranda-dev/kolo-id"
	invite "github.com/ciranda-dev/kolo-invite"
	kololog "github.com/ciranda-dev/kolo-log"
	kolosync "github.com/ciranda-dev/kolo-sync"
)

// fakeApprover records the answers and approves with err.
type fakeApprover struct {
	err              error
	approved, denied int
}

func (f *fakeApprover) Approve(invite.RequestID, []byte) error { f.approved++; return f.err }
func (f *fakeApprover) Deny(invite.RequestID) error            { f.denied++; return nil }

func randomKey(t *testing.T) ed25519.PublicKey {
	t.Helper()
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return id.PublicKey()
}

// openTestSession opens a session with a key nobody runs, and a connection that is there only for handle: its
// writes go nowhere.
func openTestSession(t *testing.T, p *peer) (*session, *link) {
	t.Helper()
	ss, err := p.newSession(randomKey(t), "x", [sessionSecretSize]byte{7})
	if err != nil {
		t.Fatal(err)
	}
	l := &link{ctx: context.Background(), cancel: func() {}, ready: make(chan struct{}), resent: map[uint64]bool{}}
	close(l.ready)
	p.mu.Lock()
	ss.link = l
	p.mu.Unlock()
	t.Cleanup(func() { p.mu.Lock(); ss.link = nil; p.mu.Unlock() }) // it has no real connection to close
	return ss, l
}

func TestOffersBeforeSyncedAreBounded(t *testing.T) {
	_, bob := network(t, Config{}, nil)
	ss, l := openTestSession(t, bob)
	if err := bob.handle(ss, l, ctlMsg{typ: ctlHello, tag: ss.tag}); err != nil {
		t.Fatal(err)
	}
	// An honest sender resends at most maxOpenOffers before SYNCED; a resend of one ID counts once.
	for i := range maxOpenOffers {
		for range 3 {
			if err := bob.handle(ss, l, ctlMsg{typ: ctlOffer, offer: offer{id: uint64(i + 1), kind: kindFile, name: "../x", nodes: 1, files: 1, bytes: 1}}); err != nil {
				t.Fatalf("offer %d: %v", i+1, err)
			}
		}
	}
	err := bob.handle(ss, l, ctlMsg{typ: ctlOffer, offer: offer{id: maxOpenOffers + 1, kind: kindFile, name: "../x", nodes: 1, files: 1, bytes: 1}})
	if !errors.Is(err, ErrPeerProtocol) || len(l.resent) > maxOpenOffers+1 {
		t.Fatalf("one offer too many before SYNCED: %v, %d remembered", err, len(l.resent))
	}
}

func TestMessagesBeforeHelloAndStaleHello(t *testing.T) {
	_, bob := network(t, Config{}, nil)
	ss, l := openTestSession(t, bob)
	if err := bob.handle(ss, l, ctlMsg{typ: ctlSynced}); !errors.Is(err, ErrPeerProtocol) {
		t.Fatalf("a message before HELLO: %v", err)
	}
	ss, l = openTestSession(t, bob)
	// A HELLO of another session with the same peer closes the connection, and the session goes on.
	if err := bob.handle(ss, l, ctlMsg{typ: ctlHello, tag: [sessionTagSize]byte{1}}); !errors.Is(err, errStale) {
		t.Fatalf("a HELLO with another tag: %v", err)
	}
	bob.mu.Lock()
	state, live := ss.s.State, l.live
	bob.mu.Unlock()
	if state != sessConnecting || live {
		t.Fatalf("after a stale HELLO: %s, live %v", state, live)
	}
	l.hello = false
	if err := bob.handle(ss, l, ctlMsg{typ: ctlHello, tag: ss.tag}); err != nil || ss.s.State != sessConnected {
		t.Fatalf("the right HELLO: %v %s", err, ss.s.State)
	}
}

func TestInviterAnswers(t *testing.T) {
	_, alice := network(t, Config{}, nil)
	good := func(joiner ed25519.PublicKey) invite.Event {
		return invite.Event{Kind: invite.Requested, Request: 1, Joiner: joiner, Data: joinData("Bob")}
	}
	// Its own key: denied, never approved.
	f := &fakeApprover{}
	if ss, err := alice.answer(f, good(alice.id.PublicKey())); ss != nil || err != nil || f.denied != 1 || f.approved != 0 {
		t.Fatalf("own key: %v %v %+v", ss, err, f)
	}
	f = &fakeApprover{}
	if ss, _ := alice.answer(f, invite.Event{Joiner: randomKey(t), Data: []byte("not join data")}); ss != nil || f.denied != 1 || f.approved != 0 {
		t.Fatalf("bad join data: %+v", f)
	}
	// The joiner left before the approval: the session opened for it goes again.
	f = &fakeApprover{err: invite.ErrNotFound}
	if ss, err := alice.answer(f, good(randomKey(t))); ss != nil || err != nil || f.approved != 1 {
		t.Fatalf("joiner gone: %v %v %+v", ss, err, f)
	}
	if n := len(alice.Sessions()); n != 0 {
		t.Fatalf("%d sessions after a failed approval", n)
	}
	f = &fakeApprover{}
	ss, err := alice.answer(f, good(randomKey(t)))
	if err != nil || ss == nil || f.approved != 1 || f.denied != 0 {
		t.Fatalf("a good request: %v %+v", err, f)
	}
	// While the app closes: no approval, so no joiner holds a session this side never opened.
	alice.mu.Lock()
	alice.running = false
	alice.mu.Unlock()
	t.Cleanup(func() { alice.mu.Lock(); alice.running = true; alice.mu.Unlock() })
	f = &fakeApprover{}
	if ss, _ := alice.answer(f, good(randomKey(t))); ss != nil || f.approved != 0 || f.denied != 1 {
		t.Fatalf("closing: %+v", f)
	}
}

func TestJoinerRefusesItsOwnKey(t *testing.T) {
	_, bob := network(t, Config{}, nil)
	payload := pairPayload(bob.id.PublicKey(), [sessionSecretSize]byte{3}, "me")
	if _, err := bob.joinedSession(payload); !errors.Is(err, ErrOwnCode) {
		t.Fatalf("an answer that names this app's key: %v", err)
	}
	payload = pairPayload(randomKey(t), [sessionSecretSize]byte{3}, "Alice")
	ss, err := bob.joinedSession(payload)
	if err != nil || ss == nil {
		t.Fatal(err)
	}
	if strings.Trim(string(payload), "\x00") != "" {
		t.Fatal("the answer, which holds the session secret, was kept")
	}
}

func TestAuthorizeOnlyOwnSessionsOpenOffers(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	carol := startPeer(t, Config{Bootstrap: alice.Info().Network.FirstNode}, nil)
	carol.rec.waitNetwork(t, netReady)
	sa, sb := connect(t, alice, bob)
	connect(t, alice, carol)
	sent := offerItem(t, alice, sa, writeFile(t, "for-bob.txt", []byte("b")))
	incoming(t, bob, sb, "for-bob.txt")
	alice.mu.Lock()
	var logs []kololog.LogID
	for id := range alice.sessions[sa].logs {
		logs = append(logs, id)
	}
	alice.mu.Unlock()
	if len(logs) != 2 {
		t.Fatalf("%d logs offered", len(logs))
	}
	own := alice.id.PublicKey()
	for _, id := range logs {
		if alice.authorize(bob.id.PublicKey(), own, id) != kolosync.Keys {
			t.Fatal("bob cannot pull his own offer")
		}
		if alice.authorize(carol.id.PublicKey(), own, id) != kolosync.NoAccess {
			t.Fatal("carol, in another session, may pull bob's offer")
		}
		if alice.authorize(bob.id.PublicKey(), randomKey(t), id) != kolosync.NoAccess {
			t.Fatal("a log of another owner")
		}
	}
	alice.CancelTransfer(sent.ID)
	alice.rec.wait(t, sent.ID, stateStopped)
	for _, id := range logs {
		if alice.authorize(bob.id.PublicKey(), own, id) != kolosync.NoAccess {
			t.Fatal("an ended offer can still be pulled")
		}
	}
}

func TestFingerprint(t *testing.T) {
	key := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
	fp := fingerprint(key)
	digits := strings.ReplaceAll(fp, " ", "")
	if b, err := hex.DecodeString(digits); err != nil || len(b)*8 != 128 || len(strings.Fields(fp)) != 8 {
		t.Fatalf("fingerprint %q: want 128 bits in 8 groups", fp)
	}
	other := make(ed25519.PublicKey, ed25519.PublicKeySize)
	other[31] = 1
	if fingerprint(other) == fp || strings.HasPrefix(digits, hex.EncodeToString(key[:4])) {
		t.Fatal("the fingerprint must depend on the whole key, not be its first bytes")
	}
	_, bob := network(t, Config{}, nil)
	if st := bob.Settings(); st.Fingerprint != fingerprint(bob.id.PublicKey()) {
		t.Fatalf("settings: %+v", st)
	}
	ss, _ := openTestSession(t, bob)
	if v := ss.s; v.Fingerprint != fingerprint(ss.peer) || v.PeerID != hex.EncodeToString(ss.peer) {
		t.Fatalf("session: %+v", v)
	}
}

func TestPairTTLBounds(t *testing.T) {
	for _, c := range []struct {
		arg string
		ok  bool
	}{{"720h", true}, {"720h1s", false}, {"100000h", false}, {"1s", true}} {
		cfg, err := LoadConfig([]string{"-pair-ttl", c.arg}, env(nil), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cfg.resolve(); (err == nil) != c.ok {
			t.Errorf("-pair-ttl %s: %v", c.arg, err)
		}
	}
	if maxPairTTL != 30*24*time.Hour {
		t.Fatal(maxPairTTL)
	}
}
