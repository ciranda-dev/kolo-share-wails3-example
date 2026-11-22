package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	koloblobs "github.com/ciranda-dev/kolo-blobs"
	koloid "github.com/ciranda-dev/kolo-id"
	invite "github.com/ciranda-dev/kolo-invite"
	kololog "github.com/ciranda-dev/kolo-log"
	kolostore "github.com/ciranda-dev/kolo-store"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
	kolosync "github.com/ciranda-dev/kolo-sync"
	"github.com/wailsapp/wails/v3/pkg/application"
)

var inMain = context.WithValue(context.Background(), application.WindowKey, namedWindow{mainWindowName})

// poll calls ok every 20ms until it is true, at most waitFor.
func poll(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitPairing waits until the last event of pairing id passes ok and returns it.
func (r *recorder) waitPairing(t *testing.T, id string, ok func(Pairing) bool) Pairing {
	t.Helper()
	var got Pairing
	poll(t, "pairing "+id, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		for i := len(r.pairings) - 1; i >= 0; i-- {
			if r.pairings[i].ID == id {
				got = r.pairings[i]
				return ok(got)
			}
		}
		return false
	})
	return got
}

// waitSession waits until session id, in the last sessions event, passes ok.
func (r *recorder) waitSession(t *testing.T, id string, ok func(Session) bool) Session {
	t.Helper()
	var got Session
	poll(t, "session "+id, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		if len(r.sessions) == 0 {
			return false
		}
		for _, v := range r.sessions[len(r.sessions)-1] {
			if v.ID == id {
				got = v
				return ok(v)
			}
		}
		return false
	})
	return got
}

// find waits for a transfer whose last state passes ok and returns the first one in order of appearance.
func (r *recorder) find(t *testing.T, what string, ok func(Transfer) bool) Transfer {
	t.Helper()
	var got Transfer
	poll(t, what, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, id := range r.order {
			ts := r.transfers[id]
			if last := ts[len(ts)-1]; ok(last) {
				got = last
				return true
			}
		}
		return false
	})
	return got
}

func (r *recorder) count(ok func(Transfer) bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, ts := range r.transfers {
		if ok(ts[len(ts)-1]) {
			n++
		}
	}
	return n
}

func connected(v Session) bool { return v.State == sessConnected }
func final(p Pairing) bool     { return p.final() }

// showCode makes a pairing code on p and waits for it.
func showCode(t *testing.T, p *peer) Pairing {
	t.Helper()
	pr, err := p.CreatePairing(inMain)
	if err != nil {
		t.Fatal(err)
	}
	got := p.rec.waitPairing(t, pr.ID, func(x Pairing) bool { return x.State == pairWaiting || x.final() })
	if got.State != pairWaiting || !strings.HasPrefix(got.Code, invite.Prefix) || got.Expires == 0 {
		t.Fatalf("pairing: %+v", got)
	}
	return got
}

// pasteCode pastes code on p and waits for the end of the pairing.
func pasteCode(t *testing.T, p *peer, code string) Pairing {
	t.Helper()
	pr, err := p.JoinPairing(code)
	if err != nil {
		t.Fatal(err)
	}
	return p.rec.waitPairing(t, pr.ID, final)
}

// connect pairs a and b, a showing the code, and returns the session IDs on a and on b once both are connected.
func connect(t *testing.T, a, b *peer) (string, string) {
	t.Helper()
	code := showCode(t, a)
	jb := pasteCode(t, b, code.Code)
	ja := a.rec.waitPairing(t, code.ID, final)
	if ja.State != pairConnected || jb.State != pairConnected || ja.Code != "" {
		t.Fatalf("pairing: %+v / %+v (errors %+v %+v)", ja, jb, ja.Error, jb.Error)
	}
	a.rec.waitSession(t, ja.SessionID, connected)
	b.rec.waitSession(t, jb.SessionID, connected)
	return ja.SessionID, jb.SessionID
}

// offerItem sends path in session sid and waits until it is offered.
func offerItem(t *testing.T, from *peer, sid, path string) Transfer {
	t.Helper()
	id, err := from.Send(sid, path)
	if err != nil {
		t.Fatal(err)
	}
	tr := from.rec.wait(t, id, stateOffered, stateFailed, stateDeclined)
	if tr.State != stateOffered {
		t.Fatalf("send: %+v (error %+v)", tr, tr.Error)
	}
	return tr
}

// declined sends path in session sid and waits until the peer's app declines it without asking.
func declined(t *testing.T, from *peer, sid, path string) {
	t.Helper()
	id, err := from.Send(sid, path)
	if err != nil {
		t.Fatal(err)
	}
	if tr := from.rec.wait(t, id, stateDeclined, stateDelivered, stateFailed); tr.State != stateDeclined {
		t.Fatalf("send: %+v", tr)
	}
}

// incoming waits for the offer of name in session sid.
func incoming(t *testing.T, to *peer, sid, name string) Transfer {
	t.Helper()
	return to.rec.find(t, "an offer of "+name, func(tr Transfer) bool {
		return tr.Direction == "receive" && tr.SessionID == sid && tr.Name == name && tr.State == stateOffered
	})
}

// saveOffer saves the offer of name in session sid into dest and waits for its end.
func saveOffer(t *testing.T, to *peer, sid, name, dest string) Transfer {
	t.Helper()
	if _, err := to.SetSessionFolder(sid, dest); err != nil {
		t.Fatal(err)
	}
	in := incoming(t, to, sid, name)
	if err := to.SaveOffer(in.ID); err != nil {
		t.Fatal(err)
	}
	return to.rec.wait(t, in.ID, stateDone, stateFailed, stateWithdrawn, stateCancelled, stateStopped)
}

// link returns the current connection of session sid.
func (p *peer) link(t *testing.T, sid string) *link {
	t.Helper()
	var l *link
	poll(t, "a live connection", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		l = p.sessions[sid].link
		return l != nil && l.live
	})
	return l
}

// inject writes m on p's control stream of session sid, as a peer that does what it wants.
func (p *peer) inject(t *testing.T, sid string, m ctlMsg) {
	t.Helper()
	p.link(t, sid).write(m)
}

func writeFile(t *testing.T, name string, body []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSessionFile(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	code := showCode(t, alice)
	jb := pasteCode(t, bob, code.Code)
	ja := alice.rec.waitPairing(t, code.ID, final)
	sa, sb := ja.SessionID, jb.SessionID
	va := alice.rec.waitSession(t, sa, connected)
	vb := bob.rec.waitSession(t, sb, connected)
	// Each side's session peer is the other's app key, from Bob's signed request and Alice's sealed answer.
	if va.PeerKey != shortKey(bob.id.PublicKey()) || vb.PeerKey != shortKey(alice.id.PublicKey()) || va.AutoSave || vb.AutoSave {
		t.Fatalf("sessions: %+v / %+v", va, vb)
	}
	alice.mu.Lock()
	peerOfAlice := alice.sessions[sa].peer
	alice.mu.Unlock()
	if !peerOfAlice.Equal(bob.id.PublicKey()) {
		t.Fatal("alice's session is not bound to bob's key")
	}

	body := randomBytes(t, 3<<20+17)
	src := writeFile(t, "photo.jpg", body)
	sent := offerItem(t, alice, sa, src)
	if sent.Kind != kindFile || sent.Name != "photo.jpg" || sent.FilesTotal != 1 || sent.BytesTotal != int64(len(body)) || sent.SessionID != sa {
		t.Fatalf("send: %+v", sent)
	}
	dest := t.TempDir()
	if _, err := bob.SetSessionFolder(sb, dest); err != nil {
		t.Fatal(err)
	}
	in := incoming(t, bob, sb, "photo.jpg")
	if in.BytesTotal != int64(len(body)) || in.PeerName == "" {
		t.Fatalf("incoming: %+v", in)
	}
	bob.rec.waitSession(t, sb, func(v Session) bool { return v.Waiting == 1 })
	time.Sleep(500 * time.Millisecond)
	assertEmpty(t, dest) // nothing is written before Save
	if err := bob.SaveOffer(in.ID); err != nil {
		t.Fatal(err)
	}
	got := bob.rec.wait(t, in.ID, stateDone, stateFailed)
	if got.State != stateDone || got.SavedTo != filepath.Join(dest, "photo.jpg") || got.FilesDone != 1 || got.BytesDone != int64(len(body)) {
		t.Fatalf("receive: %+v (error %+v)", got, got.Error)
	}
	if b, err := os.ReadFile(got.SavedTo); err != nil || !bytes.Equal(b, body) {
		t.Fatalf("saved bytes differ (%v)", err)
	}
	want := []string{stateOffered, stateQueued, stateReceiving, stateVerifying, stateDone}
	s := bob.rec.states(got.ID)
	if len(s) > 0 && s[0] == statePreparing {
		s = s[1:] // the announcement comes first when alice knew that bob reads it
	}
	if !slices.Equal(s, want) {
		t.Fatalf("receive states %v, want %v", s, want)
	}
	if tr := alice.rec.wait(t, sent.ID, stateDelivered, stateFailed); tr.State != stateDelivered || !slices.Contains(alice.rec.states(sent.ID), stateSending) {
		t.Fatalf("send: %+v, states %v", tr, alice.rec.states(sent.ID))
	}

	// The session stays open: a second item needs no code, and lands next to the first.
	sent2 := offerItem(t, alice, sa, src)
	if again := saveOffer(t, bob, sb, "photo.jpg", dest); again.State != stateDone || again.SavedTo != filepath.Join(dest, "photo (2).jpg") {
		t.Fatalf("second receive: %+v", again)
	}
	alice.rec.wait(t, sent2.ID, stateDelivered)

	var shown []string
	bob.reveal = func(p string) error { shown = append(shown, p); return nil }
	if err := bob.Reveal(got.SavedTo); err != nil || len(shown) != 1 {
		t.Fatalf("reveal: %v %v", err, shown)
	}
	if err := bob.Reveal(src); !errors.Is(err, ErrNotSaved) {
		t.Fatalf("reveal of a path bob did not save: %v", err)
	}
	h := bob.History()
	if len(h) != 2 || h[0].Result != stateDone || h[0].Name != "photo.jpg" || h[0].PeerKey != shortKey(alice.id.PublicKey()) || h[0].PeerName == "" {
		t.Fatalf("bob's history: %+v", h)
	}
	if h := alice.History(); len(h) != 2 || h[0].Result != stateDelivered || h[0].PeerKey != shortKey(bob.id.PublicKey()) {
		t.Fatalf("alice's history: %+v", h)
	}
	for _, p := range []*peer{alice, bob} {
		if entries, _ := os.ReadDir(filepath.Join(p.data, transfersDir)); len(entries) != 1 || entries[0].Name() != transfersMarker {
			t.Fatalf("transfer stores left behind: %v", entries)
		}
	}
	// The code is a secret of alice's window: bob never emits it, and it is gone from alice's state.
	bob.rec.mu.Lock()
	all := slices.Clone(bob.rec.all)
	bob.rec.mu.Unlock()
	for _, e := range all {
		if strings.Contains(e, code.Code) {
			t.Fatalf("bob emitted the code: %s", e)
		}
	}
	if alice.Pairing(inMain).Code != "" {
		t.Fatal("the code outlived its pairing")
	}
}

func TestSessionFolder(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	sa, sb := connect(t, alice, bob)
	src := filepath.Join(t.TempDir(), "share")
	files := map[string][]byte{
		"top.txt":              []byte("top"),
		"empty.txt":            {},
		"a/b/c/deep.bin":       randomBytes(t, 1<<20+3),
		"a/CON.txt":            []byte("a device name on Windows"),
		"a/..dots":             []byte("dots in a name"),
		"a/trailing.":          []byte("a trailing dot"),
		"names/café.txt":       []byte("nfc"),
		"names/with space.txt": []byte("space"),
	}
	for name, body := range files {
		p := filepath.Join(src, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, body, 0o600)
	}
	os.Mkdir(filepath.Join(src, "empty-folder"), 0o700)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("not sent"), 0o600)
	if err := os.Symlink(outside, filepath.Join(src, "link-out")); err != nil {
		t.Fatal(err)
	}
	sent := offerItem(t, alice, sa, src)
	if sent.Kind != kindFolder || sent.FilesTotal != len(files) || sent.Skipped != 1 {
		t.Fatalf("send: %+v", sent)
	}
	dest := t.TempDir()
	// A link under the item's name waits in the folder, pointing outside: it must not be followed or replaced.
	victim := t.TempDir()
	os.Symlink(victim, filepath.Join(dest, "share"))
	got := saveOffer(t, bob, sb, "share", dest)
	if got.State != stateDone || got.SavedTo != filepath.Join(dest, "share (2)") || got.FilesTotal != len(files) {
		t.Fatalf("receive: %+v (error %+v)", got, got.Error)
	}
	for name, body := range files {
		b, err := os.ReadFile(filepath.Join(got.SavedTo, filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(b, body) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if info, err := os.Stat(filepath.Join(got.SavedTo, "empty-folder")); err != nil || !info.IsDir() {
		t.Errorf("empty folder: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(got.SavedTo, "link-out")); err == nil {
		t.Error("a link was sent")
	}
	assertEmpty(t, victim)
	assertInside(t, dest, map[string]bool{filepath.Join(dest, "share"): true})
	if n := pickNotch(nil, &got, false); n.Message != "Saved 8 files." {
		t.Fatalf("result sentence %q", n.Message)
	}
}

func TestBothDirectionsAtOnce(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	sa, sb := connect(t, alice, bob)
	da, db := t.TempDir(), t.TempDir()
	alice.SetSessionFolder(sa, da)
	bob.SetSessionFolder(sb, db)
	toBob, toAlice := randomBytes(t, 2<<20), randomBytes(t, 2<<20+5)
	a := offerItem(t, alice, sa, writeFile(t, "to-bob.bin", toBob))
	b := offerItem(t, bob, sb, writeFile(t, "to-alice.bin", toAlice))
	inB := incoming(t, bob, sb, "to-bob.bin")
	inA := incoming(t, alice, sa, "to-alice.bin")
	var wg sync.WaitGroup
	wg.Go(func() { bob.SaveOffer(inB.ID) })
	wg.Go(func() { alice.SaveOffer(inA.ID) })
	wg.Wait()
	gb := bob.rec.wait(t, inB.ID, stateDone, stateFailed)
	ga := alice.rec.wait(t, inA.ID, stateDone, stateFailed)
	for _, c := range []struct {
		tr   Transfer
		body []byte
	}{{gb, toBob}, {ga, toAlice}} {
		if b, err := os.ReadFile(c.tr.SavedTo); c.tr.State != stateDone || err != nil || !bytes.Equal(b, c.body) {
			t.Fatalf("%+v (%v)", c.tr, err)
		}
	}
	alice.rec.wait(t, a.ID, stateDelivered)
	bob.rec.wait(t, b.ID, stateDelivered)
}

// flipper is a content backend that plays a peer sending a bad chunk: from the third object on, it flips a bit.
type flipper struct {
	koloblobs.Backend
	n atomic.Int64
}

func (f *flipper) Get(ctx context.Context, h koloblobs.Hash) ([]byte, error) {
	b, err := f.Backend.Get(ctx, h)
	if err == nil && f.n.Add(1) > 2 && len(b) > 0 {
		b = bytes.Clone(b)
		b[len(b)/2] ^= 1
	}
	return b, err
}

// slow is a content backend that takes its time, so a receive can be cancelled while it writes.
type slow struct{ koloblobs.Backend }

func (s slow) Get(ctx context.Context, h koloblobs.Hash) ([]byte, error) {
	select {
	case <-time.After(300 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.Backend.Get(ctx, h)
}

func TestTamperedChunkIsRejected(t *testing.T) {
	alice, bob := network(t, Config{}, func(s *ShareService) {
		s.hooks.wrapContent = func(b koloblobs.Backend) koloblobs.Backend { return &flipper{Backend: b} }
	})
	sa, sb := connect(t, alice, bob)
	sent := offerItem(t, alice, sa, writeFile(t, "data.bin", randomBytes(t, 3<<20)))
	dest := t.TempDir()
	got := saveOffer(t, bob, sb, "data.bin", dest)
	if got.State != stateFailed || got.Error == nil || got.Error.Kind != kindCorrupt {
		t.Fatalf("receive: %+v", got)
	}
	assertEmpty(t, dest)
	if h := bob.History(); len(h) != 1 || h[0].Result != stateFailed || h[0].Error.Reason != "corrupt" {
		t.Fatalf("history: %+v", h)
	}
	if tr := alice.rec.wait(t, sent.ID, stateFailed); tr.Error == nil || tr.Error.Reason != "peer-failed" {
		t.Fatalf("send: %+v", tr)
	}
}

func TestDeclineFreesSender(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	sa, sb := connect(t, alice, bob)
	sent := offerItem(t, alice, sa, writeFile(t, "no.txt", []byte("no thanks")))
	in := incoming(t, bob, sb, "no.txt")
	if err := bob.DeclineOffer(in.ID); err != nil {
		t.Fatal(err)
	}
	if tr := bob.rec.wait(t, in.ID, stateDeclined); tr.Error != nil {
		t.Fatalf("receive: %+v", tr)
	}
	alice.rec.wait(t, sent.ID, stateDeclined)
	poll(t, "the sender's store removed", func() bool {
		entries, _ := os.ReadDir(filepath.Join(alice.data, transfersDir))
		return len(entries) == 1
	})
	if err := bob.SaveOffer(in.ID); !errors.Is(err, ErrOfferGone) {
		t.Fatalf("save after decline: %v", err)
	}
	if h := bob.History(); len(h) != 1 || h[0].Result != stateDeclined {
		t.Fatalf("a declined offer is an answer, kept in the history: %+v", h)
	}
}

func TestCancelBySender(t *testing.T) {
	alice, bob := network(t, Config{}, func(s *ShareService) {
		s.hooks.wrapContent = func(b koloblobs.Backend) koloblobs.Backend { return slow{b} }
	})
	sa, sb := connect(t, alice, bob)

	// Withdrawn before anyone answered: not in the history.
	early := offerItem(t, alice, sa, writeFile(t, "early.txt", []byte("e")))
	in := incoming(t, bob, sb, "early.txt")
	alice.CancelTransfer(early.ID)
	if tr := bob.rec.wait(t, in.ID, stateWithdrawn); tr.Error == nil || tr.Error.Reason != "offer-withdrawn" {
		t.Fatalf("withdrawn offer: %+v", tr)
	}
	alice.rec.wait(t, early.ID, stateStopped)

	// Withdrawn while it is written.
	src := filepath.Join(t.TempDir(), "big")
	for i := range 3 {
		os.MkdirAll(src, 0o700)
		os.WriteFile(filepath.Join(src, string(rune('a'+i))), randomBytes(t, 1<<20), 0o600)
	}
	sent := offerItem(t, alice, sa, src)
	dest := t.TempDir()
	bob.SetSessionFolder(sb, dest)
	in = incoming(t, bob, sb, "big")
	bob.SaveOffer(in.ID)
	bob.rec.wait(t, in.ID, stateVerifying)
	alice.CancelTransfer(sent.ID)
	if tr := bob.rec.wait(t, in.ID, stateWithdrawn, stateDone, stateFailed); tr.State != stateWithdrawn {
		t.Fatalf("withdrawn while writing: %+v (error %+v)", tr, tr.Error)
	}
	alice.rec.wait(t, sent.ID, stateStopped)
	assertEmpty(t, dest)
	if h := bob.History(); len(h) != 1 || h[0].Result != stateWithdrawn {
		t.Fatalf("history: %+v", h)
	}
}

func TestCancelByReceiver(t *testing.T) {
	alice, bob := network(t, Config{}, func(s *ShareService) {
		s.hooks.wrapContent = func(b koloblobs.Backend) koloblobs.Backend { return slow{b} }
	})
	sa, sb := connect(t, alice, bob)
	src := filepath.Join(t.TempDir(), "big")
	for i := range 3 {
		os.MkdirAll(src, 0o700)
		os.WriteFile(filepath.Join(src, string(rune('a'+i))), randomBytes(t, 1<<20), 0o600)
	}
	sent := offerItem(t, alice, sa, src)
	dest := t.TempDir()
	bob.SetSessionFolder(sb, dest)
	in := incoming(t, bob, sb, "big")
	bob.SaveOffer(in.ID)
	bob.rec.wait(t, in.ID, stateVerifying)
	bob.CancelTransfer(in.ID)
	if tr := bob.rec.wait(t, in.ID, stateCancelled, stateDone, stateFailed); tr.State != stateCancelled {
		t.Fatalf("cancel while writing: %+v", tr)
	}
	alice.rec.wait(t, sent.ID, stateCancelled)
	assertEmpty(t, dest)
}

func TestOfferMismatchIsRejected(t *testing.T) {
	var tamper atomic.Pointer[func(*offer)]
	alice, bob := network(t, Config{}, nil)
	alice.hooks.tamperOffer = func(o *offer) {
		if f := tamper.Load(); f != nil {
			(*f)(o)
		}
	}
	sa, sb := connect(t, alice, bob)
	file := writeFile(t, "one.txt", []byte("one"))
	folder := filepath.Join(t.TempDir(), "two")
	os.MkdirAll(folder, 0o700)
	os.WriteFile(filepath.Join(folder, "a"), []byte("a"), 0o600)
	os.WriteFile(filepath.Join(folder, "b"), []byte("b"), 0o600)
	for _, c := range []struct {
		name, src, shown, reason string
		f                        func(*offer)
	}{
		{"another name", file, "two.txt", "offer-mismatch", func(o *offer) { o.name = "two.txt" }},
		{"more bytes", file, "one.txt", "offer-mismatch", func(o *offer) { o.bytes++ }},
		{"fewer bytes", file, "one.txt", "offer-mismatch", func(o *offer) { o.bytes-- }},
		{"a folder for a file", file, "one.txt", "offer-mismatch", func(o *offer) { o.kind, o.nodes = kindFolder, 2 }},
		// One node offered: the signed head of three is refused before a byte is pulled.
		{"a file for a folder", folder, "two", "too-many-entries", func(o *offer) { o.kind, o.nodes, o.files = kindFile, 1, 1 }},
		{"fewer files", folder, "two", "offer-mismatch", func(o *offer) { o.files-- }},
		{"more folders", folder, "two", "offer-mismatch", func(o *offer) { o.nodes++ }},
	} {
		tamper.Store(&c.f)
		sent := offerItem(t, alice, sa, c.src)
		dest := t.TempDir()
		got := saveOffer(t, bob, sb, c.shown, dest)
		if got.State != stateFailed || got.Error == nil || got.Error.Reason != c.reason {
			t.Fatalf("%s: %+v (error %+v)", c.name, got, got.Error)
		}
		assertEmpty(t, dest)
		alice.rec.wait(t, sent.ID, stateFailed)
	}
}

func TestOfferLimits(t *testing.T) {
	alice, bob := network(t, Config{MaxDownload: 1 << 20, MaxFiles: 10}, nil)
	sa, sb := connect(t, alice, bob)
	notice := func(reason string) {
		t.Helper()
		bob.rec.waitSession(t, sb, func(v Session) bool { return v.Notice != nil && v.Notice.Reason == reason })
	}

	declined(t, alice, sa, writeFile(t, "big.bin", randomBytes(t, 2<<20)))
	notice("too-large")
	many := filepath.Join(t.TempDir(), "many")
	os.Mkdir(many, 0o700)
	for i := range 12 {
		os.WriteFile(filepath.Join(many, fmt.Sprint(i)), nil, 0o600)
	}
	// Bob's limit of 10 is his; alice's own limit of 100,000 lets her offer it.
	declined(t, alice, sa, many)
	notice("too-many-files")
	// A folder's size is not announced, so its card shows until the offer is declined; nothing is ever offered.
	if n := bob.rec.count(func(tr Transfer) bool { return tr.Direction == "receive" && tr.State != stateDeclined }); n != 0 {
		t.Fatalf("%d receives for offers over the limits", n)
	}

	// A peer that lies about its offers, past what alice's app would send.
	var meta kololog.LogID
	id := uint64(1000)
	hostile := func(name string, nodes, files int, bytes int64) {
		id++
		kind := kindFile
		if nodes > 1 {
			kind = kindFolder
		}
		alice.inject(t, sa, ctlMsg{typ: ctlOffer, offer: offer{id: id, kind: kind, name: name, nodes: nodes, files: files, bytes: bytes, meta: meta}})
	}
	for _, name := range []string{"../escape", "a/b", "..", ".", "", `a\b`} {
		bob.mu.Lock()
		bob.sessions[sb].s.Notice = nil
		bob.mu.Unlock()
		bob.emitSessions()
		hostile(name, 1, 1, 1)
		notice("unsafe-name")
	}
	if n := bob.rec.count(func(tr Transfer) bool { return tr.Direction == "receive" && tr.State != stateDeclined }); n != 0 {
		t.Fatalf("%d offers with unsafe names shown", n)
	}
	hostile("huge", 1, 1, math.MaxInt64)
	notice("too-large")
	hostile("wide", 1_000_000, 0, 1)
	notice("too-many-files")

	// A flood: the first maxOpenOffers wait for an answer, the others are declined without a word to the person.
	for i := range 30 {
		hostile(fmt.Sprintf("flood-%02d", i), 1, 1, 1)
	}
	bob.rec.waitSession(t, sb, func(v Session) bool {
		return v.Waiting == maxOpenOffers && v.Notice != nil && v.Notice.Reason == "offer-limit"
	})
	time.Sleep(300 * time.Millisecond)
	if n := bob.rec.count(func(tr Transfer) bool { return tr.Direction == "receive" && tr.State != stateDeclined }); n != maxOpenOffers {
		t.Fatalf("%d offers shown, want %d", n, maxOpenOffers)
	}
	bob.mu.Lock()
	finals := len(bob.sessions[sb].finals)
	bob.mu.Unlock()
	if finals > maxOpenOffers {
		t.Fatalf("%d final statuses kept: an endless session would grow without bound", finals)
	}
	if h := bob.History(); len(h) != 0 {
		t.Fatalf("a flood reached the history: %+v", h)
	}
}

func TestOfferRate(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	sa, sb := connect(t, alice, bob)
	// Every offer is declined for its name, until the rate runs out: then the reason is the rate.
	for i := range offerRate {
		alice.inject(t, sa, ctlMsg{typ: ctlOffer, offer: offer{id: uint64(i + 1), kind: kindFile, name: "../x", nodes: 1, files: 1, bytes: 1}})
	}
	bob.rec.waitSession(t, sb, func(v Session) bool { return v.Notice != nil && v.Notice.Reason == "unsafe-name" })
	time.Sleep(300 * time.Millisecond)
	alice.inject(t, sa, ctlMsg{typ: ctlOffer, offer: offer{id: offerRate + 1, kind: kindFile, name: "fine.txt", nodes: 1, files: 1, bytes: 1}})
	bob.rec.waitSession(t, sb, func(v Session) bool { return v.Notice != nil && v.Notice.Reason == "offer-limit" })
	if n := bob.rec.count(func(tr Transfer) bool { return tr.Direction == "receive" }); n != 0 {
		t.Fatalf("%d offers past the rate", n)
	}
}

func TestBucket(t *testing.T) {
	var b bucket
	now := time.Unix(1000, 0)
	for i := range offerRate {
		if !b.take(now, offerRate) {
			t.Fatalf("token %d refused", i)
		}
	}
	if b.take(now, offerRate) {
		t.Fatal("a token past the rate")
	}
	if !b.take(now.Add(time.Second), offerRate) || b.take(now.Add(time.Second), offerRate) {
		t.Fatal("one token per second")
	}
}

func TestMalformedControlEndsSession(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	raw := func(b []byte) func(*link) {
		return func(l *link) {
			l.outMu.Lock()
			defer l.outMu.Unlock()
			l.out.Write(b)
		}
	}
	frameOf := func(body []byte) []byte {
		return append([]byte{byte(len(body)), byte(len(body) >> 8), 0, 0}, body...)
	}
	for _, c := range []struct {
		name string
		do   func(*link)
	}{
		{"oversize length", raw([]byte{0xff, 0xff, 0xff, 0x7f})},
		{"zero length", raw([]byte{0, 0, 0, 0})},
		{"unknown type", raw(frameOf([]byte{99}))},
		{"second hello", func(l *link) { l.write(ctlMsg{typ: ctlHello}) }},
		{"bad UTF-8 name", raw(frameOf(append([]byte{ctlOffer, 5, 1, 2, 0xff, 0xfe, 1, 1, 1}, make([]byte, kololog.LogIDSize)...)))},
		{"trailing bytes", raw(frameOf([]byte{ctlSynced, 0}))},
		{"offer ID used again for another item", func(l *link) {
			l.write(ctlMsg{typ: ctlOffer, offer: offer{id: 7, kind: kindFile, name: "a", nodes: 1, files: 1, bytes: 1}})
			l.write(ctlMsg{typ: ctlOffer, offer: offer{id: 7, kind: kindFile, name: "b", nodes: 1, files: 1, bytes: 1}})
		}},
	} {
		sa, sb := connect(t, alice, bob)
		c.do(alice.link(t, sa))
		if v := bob.rec.waitSession(t, sb, func(v Session) bool { return v.State == sessEnded }); v.Reason != endProtocol {
			t.Fatalf("%s: %+v", c.name, v)
		}
		alice.rec.waitSession(t, sa, func(v Session) bool { return v.State != sessConnected })
	}
}

func TestLostPullResumesByItself(t *testing.T) {
	defer func(f func(context.Context, kolosync.Conn, *kolostore.Replica, *kolosync.PullMeter) error) { pull = f }(pull)
	var dropped atomic.Bool
	pull = func(ctx context.Context, c kolosync.Conn, r *kolostore.Replica, m *kolosync.PullMeter) error {
		if dropped.CompareAndSwap(false, true) {
			c.(koloswarm.Conn).CloseWithError(0, "") // the network goes away during the first pull
		}
		return kolosync.Pull(ctx, c, r)
	}
	alice, bob := network(t, Config{}, nil)
	sa, sb := connect(t, alice, bob)
	body := randomBytes(t, 1<<20)
	sent := offerItem(t, alice, sa, writeFile(t, "retry.bin", body))
	dest := t.TempDir()
	bob.SetSessionFolder(sb, dest)
	in := incoming(t, bob, sb, "retry.bin")
	bob.SaveOffer(in.ID) // one click: the rest is by itself
	got := bob.rec.wait(t, in.ID, stateDone, stateFailed)
	if b, err := os.ReadFile(got.SavedTo); got.State != stateDone || err != nil || !bytes.Equal(b, body) {
		t.Fatalf("receive: %+v (%v)", got, err)
	}
	if s := bob.rec.states(in.ID); slices.Contains(s[slices.Index(s, stateQueued):], stateOffered) {
		t.Fatalf("the item went back to waiting for a click: %v", s)
	}
	alice.rec.wait(t, sent.ID, stateDelivered)
}

func TestSessionReconnectsAfterDrop(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	sa, sb := connect(t, alice, bob)
	alice.link(t, sa).conn.CloseWithError(0, "")
	alice.rec.waitSession(t, sa, func(v Session) bool { return v.State == sessReconnecting })
	bob.rec.waitSession(t, sb, func(v Session) bool { return v.State == sessReconnecting })
	began := time.Now()
	alice.rec.waitSession(t, sa, connected)
	bob.rec.waitSession(t, sb, connected)
	t.Logf("both reconnected by themselves in %v", time.Since(began))
	body := randomBytes(t, 4096)
	offerItem(t, alice, sa, writeFile(t, "after.txt", body))
	got := saveOffer(t, bob, sb, "after.txt", t.TempDir())
	if b, err := os.ReadFile(got.SavedTo); got.State != stateDone || err != nil || !bytes.Equal(b, body) {
		t.Fatalf("receive after the reconnect: %+v (%v)", got, err)
	}
}

func TestResentOffersAfterReconnect(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	sa, sb := connect(t, alice, bob)
	keep := offerItem(t, alice, sa, writeFile(t, "keep.txt", []byte("k")))
	gone := offerItem(t, alice, sa, writeFile(t, "gone.txt", []byte("g")))
	inKeep := incoming(t, bob, sb, "keep.txt")
	inGone := incoming(t, bob, sb, "gone.txt")
	// The WITHDRAW of gone.txt is lost: alice forgets the offer while bob still shows it.
	alice.mu.Lock()
	for id, j := range alice.sessions[sa].sent {
		if j.t.ID == gone.ID {
			delete(alice.sessions[sa].sent, id)
		}
	}
	alice.mu.Unlock()
	alice.link(t, sa).conn.CloseWithError(0, "")
	alice.rec.waitSession(t, sa, func(v Session) bool { return v.State == sessReconnecting })
	alice.rec.waitSession(t, sa, connected)
	if tr := bob.rec.wait(t, inGone.ID, stateWithdrawn); tr.Error == nil || tr.Error.Reason != "offer-withdrawn" {
		t.Fatalf("an offer that was not resent: %+v", tr)
	}
	bob.rec.waitSession(t, sb, connected)
	time.Sleep(300 * time.Millisecond)
	if tr := bob.rec.until(t, inKeep.ID, func(Transfer) bool { return true }); tr.State != stateOffered {
		t.Fatalf("a resent offer: %+v", tr)
	}
	got := saveOffer(t, bob, sb, "keep.txt", t.TempDir())
	if got.State != stateDone {
		t.Fatalf("receive: %+v", got.Error)
	}
	alice.rec.wait(t, keep.ID, stateDelivered)
	alice.CancelTransfer(gone.ID)
}

func TestAutoSaveOffByDefault(t *testing.T) {
	alice, bob := network(t, Config{MaxDownload: 1 << 20}, nil)
	sa, sb := connect(t, alice, bob)
	dest := t.TempDir()
	v, err := bob.SetSessionFolder(sb, dest)
	if err != nil || v.AutoSave {
		t.Fatalf("a new session saves by itself: %+v %v", v, err)
	}
	offerItem(t, alice, sa, writeFile(t, "ask.txt", []byte("ask first")))
	in := incoming(t, bob, sb, "ask.txt")
	time.Sleep(time.Second)
	if tr := bob.rec.until(t, in.ID, func(Transfer) bool { return true }); tr.State != stateOffered {
		t.Fatalf("an offer moved without an answer: %+v", tr)
	}
	assertEmpty(t, dest)

	if _, err := bob.SetAutoSave(sb, true); err != nil {
		t.Fatal(err)
	}
	// The limits still apply: an item over the download limit is declined, not saved.
	declined(t, alice, sa, writeFile(t, "big.bin", randomBytes(t, 2<<20)))
	small, err := alice.Send(sa, writeFile(t, "auto.txt", []byte("saved without asking")))
	if err != nil {
		t.Fatal(err)
	}
	got := bob.rec.find(t, "auto.txt saved", func(tr Transfer) bool { return tr.Name == "auto.txt" && tr.final() })
	if got.State != stateDone || got.SavedTo != filepath.Join(dest, "auto.txt") {
		t.Fatalf("auto-save: %+v", got)
	}
	alice.rec.wait(t, small, stateDelivered)
	if entries, _ := os.ReadDir(dest); len(entries) != 1 {
		t.Fatalf("folder holds %v", entries)
	}
	if _, err := bob.SetAutoSave(sb, false); err != nil {
		t.Fatal(err)
	}
	var se *ShareError
	bob.mu.Lock()
	bob.sessions[sb].s.Folder = ""
	bob.mu.Unlock()
	if _, err := bob.SetAutoSave(sb, true); !errors.As(err, &se) || se.Reason != "folder-missing" {
		t.Fatalf("auto-save without a folder: %v", err)
	}
}

func TestGuardedSourceWaitsForReads(t *testing.T) {
	st := kolostore.New(kolostore.NewMemory())
	defer st.Close()
	id, _ := koloid.Generate()
	l, err := st.Create(id)
	if err != nil {
		t.Fatal(err)
	}
	l.Append([]byte("x"))
	g := newGuard(l)
	if _, err := g.Head(); err != nil {
		t.Fatal(err)
	}
	g.mu.RLock() // a read in flight
	closed := make(chan struct{})
	go func() { g.close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("close did not wait for the read in flight")
	case <-time.After(200 * time.Millisecond):
	}
	g.mu.RUnlock()
	<-closed
	if _, err := g.Head(); !errors.Is(err, ErrClosed) {
		t.Fatalf("a read after close: %v", err)
	}
	if _, err := g.Entry(0); !errors.Is(err, ErrClosed) {
		t.Fatalf("an entry after close: %v", err)
	}
	if g.ID() != l.ID() {
		t.Fatal("ID")
	}
}

func TestCloseSendsBye(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	sa, sb := connect(t, alice, bob)
	began := time.Now()
	if err := alice.CloseSession(sa); err != nil {
		t.Fatal(err)
	}
	if v := alice.Sessions()[0]; v.State != sessEnded || v.Reason != endClosed {
		t.Fatalf("alice: %+v", v)
	}
	if v := bob.rec.waitSession(t, sb, func(v Session) bool { return v.State == sessEnded }); v.Reason != endPeerClosed {
		t.Fatalf("bob: %+v", v)
	}
	if d := time.Since(began); d >= byeWait {
		t.Fatalf("the goodbye took %v", d)
	}
	if bob.sessionOf(alice.id.PublicKey()) != nil || alice.sessionOf(bob.id.PublicKey()) != nil {
		t.Fatal("an ended session still admits its peer")
	}
	// A second Close dismisses the ended session.
	alice.CloseSession(sa)
	if n := len(alice.Sessions()); n != 0 {
		t.Fatalf("%d sessions after dismiss", n)
	}
	if _, err := alice.Send(sa, writeFile(t, "late.txt", nil)); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("send on a closed session: %v", err)
	}
}

func TestShutdownSendsBye(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	_, sb := connect(t, alice, bob)
	began := time.Now()
	alice.ServiceShutdown()
	if d := time.Since(began); d >= byeWait+5*time.Second {
		t.Fatalf("shutdown took %v", d)
	}
	if v := bob.rec.waitSession(t, sb, func(v Session) bool { return v.State == sessEnded }); v.Reason != endPeerClosed {
		t.Fatalf("bob: %+v", v)
	}
}

func TestPeerGoneKeepsReconnecting(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	_, sb := connect(t, alice, bob)
	// Bob vanishes without a goodbye, as a crash or a power loss.
	bob.mu.Lock()
	sw := bob.sw
	bob.mu.Unlock()
	sw.Close()
	_ = sb
	sa := alice.Sessions()[0].ID
	alice.rec.waitSession(t, sa, func(v Session) bool { return v.State == sessReconnecting })
	time.Sleep(3 * time.Second)
	if v := alice.Sessions()[0]; v.State != sessReconnecting {
		t.Fatalf("alice gave up on a session nobody closed: %+v", v)
	}
	if alice.sessionOf(bob.id.PublicKey()) == nil {
		t.Fatal("alice stopped admitting bob")
	}
	alice.CloseSession(sa) // no goodbye can arrive: bounded by byeWait
	if v := alice.Sessions()[0]; v.State != sessEnded {
		t.Fatalf("close: %+v", v)
	}
}

func TestSessionEndStopsTransfers(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	sa, sb := connect(t, alice, bob)
	sent := offerItem(t, alice, sa, writeFile(t, "wait.txt", []byte("w")))
	in := incoming(t, bob, sb, "wait.txt")
	alice.CloseSession(sa)
	if tr := alice.rec.wait(t, sent.ID, stateStopped); tr.Error == nil || tr.Error.Reason != "session-ended" {
		t.Fatalf("send: %+v", tr)
	}
	if tr := bob.rec.wait(t, in.ID, stateStopped); tr.Error == nil || tr.Error.Reason != "session-ended" {
		t.Fatalf("receive: %+v", tr)
	}
	if h := bob.History(); len(h) != 0 {
		t.Fatalf("an offer nobody answered is in the history: %+v", h)
	}
	if n := len(alice.Transfers()) + len(bob.Transfers()); n != 0 {
		t.Fatalf("%d transfers after the end", n)
	}
}

func TestNoRestartWithSessions(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	sa, _ := connect(t, alice, bob)
	var se *ShareError
	if err := alice.Reconnect(); !errors.As(err, &se) || se.Reason != "busy-reconnect" {
		t.Fatalf("reconnect with a session: %v", err)
	}
	pr := showCode(t, alice)
	alice.CloseSession(sa)
	if err := alice.Reconnect(); !errors.As(err, &se) || se.Reason != "busy-reconnect" {
		t.Fatalf("reconnect with a pairing: %v", err)
	}
	alice.CancelPairing(pr.ID)
	alice.rec.waitPairing(t, pr.ID, final)
	if err := alice.Reconnect(); err != nil {
		t.Fatalf("reconnect with only ended sessions: %v", err)
	}
	alice.rec.waitNetwork(t, netReady)
}

func TestBusyOffers(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	sa, _ := connect(t, alice, bob)
	src := writeFile(t, "f", []byte("x"))
	var wg sync.WaitGroup
	var ok atomic.Int32
	for range maxOpenOffers + 4 {
		wg.Go(func() {
			if _, err := alice.Send(sa, src); err == nil {
				ok.Add(1)
			} else if !errors.Is(err, ErrBusy) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if ok.Load() != maxOpenOffers {
		t.Fatalf("%d offers open, want %d", ok.Load(), maxOpenOffers)
	}
}

func TestStrangerRefused(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	sa, _ := connect(t, alice, bob)
	alice.mu.Lock()
	topic := alice.sessions[sa].topic
	alice.mu.Unlock()
	// Someone who learned the topic still cannot connect: alice admits only bob's key.
	x, _ := koloid.Generate()
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	sw, err := koloswarm.New(ctx, "127.0.0.1:0", x, &koloswarm.Config{BootstrapNodes: []string{alice.Info().Network.FirstNode}, LookupInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer sw.Close()
	if err := sw.Bootstrap(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := sw.Join(ctx, topic, &koloswarm.JoinOptions{Announce: true, Lookup: true}); err != nil {
		t.Fatal(err)
	}
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	found := false
	for {
		ev, err := sw.Next(wctx)
		if err != nil {
			break
		}
		if ev.Kind == koloswarm.Found && ev.PublicKey.Equal(alice.id.PublicKey()) {
			found = true
		}
		if ev.Kind == koloswarm.Connected && ev.Peer.PublicKey().Equal(alice.id.PublicKey()) {
			t.Fatal("a stranger connected to a session")
		}
	}
	if !found {
		t.Fatal("the stranger never found alice: the test proves nothing")
	}
	if v := alice.Sessions()[0]; v.State != sessConnected {
		t.Fatalf("alice's session: %+v", v)
	}
}

// rawJoin pastes code as a joiner that is not this app: it pairs and never opens the session.
func rawJoin(t *testing.T, node, code string) ([]byte, error) {
	t.Helper()
	j, _ := koloid.Generate()
	return rawJoinAs(t, node, code, j)
}

// rawJoinAs is rawJoin with the key j as the joiner.
func rawJoinAs(t *testing.T, node, code string, j *koloid.Identity) ([]byte, error) {
	t.Helper()
	c, err := invite.Parse(code)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := koloid.Generate()
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	sw, err := koloswarm.New(ctx, "127.0.0.1:0", e, &koloswarm.Config{BootstrapNodes: []string{node}, LookupInterval: time.Second, MaxPeers: 1,
		Allow: func(k ed25519.PublicKey, _ netip.AddrPort) bool { return c.Matches(k) }})
	if err != nil {
		t.Fatal(err)
	}
	defer sw.Close()
	if err := sw.Bootstrap(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := sw.Join(ctx, c.Topic(), &koloswarm.JoinOptions{Lookup: true}); err != nil {
		t.Fatal(err)
	}
	conn, err := findInviter(ctx, sw, c, waitFor)
	if err != nil {
		t.Fatal(err)
	}
	return invite.JoinConn(ctx, conn, j, c, &invite.JoinOptions{Data: joinData("raw"), MaxPayload: maxPairPayload})
}

func TestPairSingleUse(t *testing.T) {
	first := freeUDP(t)
	alice := startPeer(t, Config{First: first, FindTimeout: 30 * time.Second}, nil)
	alice.rec.waitNetwork(t, netReady)
	code := showCode(t, alice)
	payload, err := rawJoin(t, first, code.Code)
	if err != nil {
		t.Fatal(err)
	}
	if key, _, _, err := parsePairPayload(payload); err != nil || !key.Equal(alice.id.PublicKey()) {
		t.Fatalf("payload: %v", err)
	}
	if p := alice.rec.waitPairing(t, code.ID, final); p.State != pairConnected {
		t.Fatalf("alice: %+v", p)
	}
	// The raw joiner never connects, so alice still answers on the code's topic: a second paste is refused.
	carol := startPeer(t, Config{Bootstrap: first, FindTimeout: 10 * time.Second}, nil)
	carol.rec.waitNetwork(t, netReady)
	if p := pasteCode(t, carol, code.Code); p.State != pairFailed || p.Error.Reason != "pair-used" {
		t.Fatalf("second paste: %+v (error %+v)", p, p.Error)
	}
	if n := len(alice.Sessions()); n != 1 {
		t.Fatalf("a single-use code opened %d sessions", n)
	}
}

func TestPairCancelRevokes(t *testing.T) {
	alice, bob := network(t, Config{FindTimeout: 3 * time.Second}, nil)
	code := showCode(t, alice)
	alice.CancelPairing(code.ID)
	if p := alice.rec.waitPairing(t, code.ID, final); p.State != pairCancelled || p.Code != "" {
		t.Fatalf("cancel: %+v", p)
	}
	if p := alice.Pairing(inMain); p.ID != "" {
		t.Fatalf("a pairing after cancel: %+v", p)
	}
	if p := pasteCode(t, bob, code.Code); p.State != pairFailed || p.Error.Reason != "pair-not-found" {
		t.Fatalf("paste after cancel: %+v (error %+v)", p, p.Error)
	}
}

func TestPairExpires(t *testing.T) {
	first := freeUDP(t)
	alice := startPeer(t, Config{First: first, PairTTL: time.Second}, nil)
	alice.rec.waitNetwork(t, netReady)
	if st := alice.Settings(); st.PairTTL != 1000 {
		t.Fatalf("settings: %+v", st)
	}
	code := showCode(t, alice)
	if left := time.Until(time.UnixMilli(code.Expires)); left > time.Second || left < 0 {
		t.Fatalf("expires in %v", left)
	}
	if p := alice.rec.waitPairing(t, code.ID, final); p.State != pairExpired || p.Error == nil || p.Error.Reason != "pair-expired" {
		t.Fatalf("expiry: %+v", p)
	}
	bob := startPeer(t, Config{Bootstrap: first, FindTimeout: 3 * time.Second}, nil)
	bob.rec.waitNetwork(t, netReady)
	if p := pasteCode(t, bob, code.Code); p.State != pairFailed {
		t.Fatalf("paste after expiry: %+v", p)
	}
}

func TestPairOwnCode(t *testing.T) {
	alice, _ := network(t, Config{}, nil)
	code := showCode(t, alice)
	var se *ShareError
	if _, err := alice.JoinPairing(code.Code); !errors.As(err, &se) || se.Reason != "pair-own-code" {
		t.Fatalf("own code: %v", err)
	}
	// The inviter refuses its own key too, for two windows of one data directory.
	key, secret, _, _ := parsePairPayload(pairPayload(alice.id.PublicKey(), [sessionSecretSize]byte{1}, "x"))
	if !key.Equal(alice.id.PublicKey()) || secret[0] != 1 {
		t.Fatal("payload round trip")
	}
}

func TestPairDeniedAtMaxSessions(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	code := showCode(t, alice)
	alice.mu.Lock()
	for i := range maxSessions {
		id := fmt.Sprint("fake-", i)
		alice.sessions[id] = &session{id: id, s: Session{ID: id, State: sessConnected}}
	}
	alice.mu.Unlock()
	t.Cleanup(func() {
		alice.mu.Lock()
		for i := range maxSessions {
			delete(alice.sessions, fmt.Sprint("fake-", i))
		}
		alice.mu.Unlock()
	})
	if p := pasteCode(t, bob, code.Code); p.State != pairFailed || p.Error.Reason != "pair-denied" {
		t.Fatalf("paste at the limit: %+v (error %+v)", p, p.Error)
	}
	var se *ShareError
	alice.CancelPairing(code.ID)
	alice.rec.waitPairing(t, code.ID, final)
	if _, err := alice.CreatePairing(inMain); !errors.As(err, &se) || se.Reason != "busy-sessions" {
		t.Fatalf("a code at the limit: %v", err)
	}
	if _, err := bob.JoinPairing(code.Code); err != nil {
		t.Fatalf("bob has room: %v", err)
	}
}

func TestPairReplacesSessionWithSameKey(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	sa, sb := connect(t, alice, bob)
	na, nb := connect(t, alice, bob)
	if v := alice.rec.waitSession(t, sa, func(v Session) bool { return v.State == sessEnded }); v.Reason != endReplaced {
		t.Fatalf("alice's old session: %+v", v)
	}
	if v := bob.rec.waitSession(t, sb, func(v Session) bool { return v.State == sessEnded }); v.Reason != endReplaced {
		t.Fatalf("bob's old session: %+v", v)
	}
	body := randomBytes(t, 4096)
	offerItem(t, alice, na, writeFile(t, "new.txt", body))
	if got := saveOffer(t, bob, nb, "new.txt", t.TempDir()); got.State != stateDone {
		t.Fatalf("receive in the new session: %+v", got.Error)
	}
}

func TestPairNothingOnDisk(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	code := showCode(t, alice)
	pasteCode(t, bob, code.Code)
	ja := alice.rec.waitPairing(t, code.ID, final)
	alice.rec.waitSession(t, ja.SessionID, connected)
	alice.mu.Lock()
	ss := alice.sessions[ja.SessionID]
	secrets := [][]byte{[]byte(code.Code), ss.tag[:], ss.topic[:]}
	alice.mu.Unlock()
	for _, p := range []*peer{alice, bob} {
		filepath.WalkDir(p.data, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return err
			}
			b, _ := os.ReadFile(path)
			for _, x := range secrets {
				if bytes.Contains(b, x) {
					t.Errorf("%s holds a pairing secret", path)
				}
			}
			return nil
		})
	}
}

func TestPairingSwarmClosesWhenSessionConnects(t *testing.T) {
	alice, bob := network(t, Config{FindTimeout: 3 * time.Second}, nil)
	code := showCode(t, alice)
	pasteCode(t, bob, code.Code)
	ja := alice.rec.waitPairing(t, code.ID, final)
	alice.rec.waitSession(t, ja.SessionID, connected)
	// Once the session is up, nothing answers on the code's topic any more: not even "used".
	poll(t, "the pairing swarm closed", func() bool {
		alice.mu.Lock()
		defer alice.mu.Unlock()
		return alice.pairing == nil
	})
	time.Sleep(500 * time.Millisecond)
	carol := startPeer(t, Config{Bootstrap: alice.Info().Network.FirstNode, FindTimeout: 3 * time.Second}, nil)
	carol.rec.waitNetwork(t, netReady)
	if p := pasteCode(t, carol, code.Code); p.State != pairFailed || p.Error.Reason != "pair-not-found" {
		t.Fatalf("paste after the session connected: %+v (error %+v)", p, p.Error)
	}
}

func TestPairingRefusals(t *testing.T) {
	_, bob := network(t, Config{}, nil)
	for name, c := range map[string]struct{ text, reason string }{
		"garbage":    {"hello", "pair-invalid-code"},
		"empty":      {"", "pair-invalid-code"},
		"huge":       {invite.Prefix + strings.Repeat("a", maxCodeText), "pair-invalid-code"},
		"other app":  {"  KOLO-SHARE:AAAA", "pair-invalid-code"},
		"mixed case": {"koloINV0" + strings.Repeat("a", 84), "pair-invalid-code"},
	} {
		_, err := bob.JoinPairing(c.text)
		var se *ShareError
		if !errors.As(err, &se) || se.Reason != c.reason {
			t.Errorf("%s: %v", name, err)
		}
	}
	if p := bob.Pairing(inMain); p.ID != "" {
		t.Fatalf("a refused code started a pairing: %+v", p)
	}
	// A code wrapped by a mail program still works: white space anywhere is ignored.
	if _, err := parsePairingCode(" koloinv0\n" + strings.Repeat("a", 3)); !errors.Is(err, invite.ErrInvalidCode) {
		t.Fatalf("short code: %v", err)
	}
}

func TestConcurrentPairingsAreSerialized(t *testing.T) {
	alice, _ := network(t, Config{}, nil)
	var wg sync.WaitGroup
	var ok, busy atomic.Int32
	for range 4 {
		wg.Go(func() {
			_, err := alice.CreatePairing(inMain)
			var se *ShareError
			switch {
			case err == nil:
				ok.Add(1)
			case errors.As(err, &se) && se.Reason == "busy-pairing":
				busy.Add(1)
			default:
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 1 || busy.Load() != 3 {
		t.Fatalf("%d pairings started, %d busy", ok.Load(), busy.Load())
	}
}

func TestPairWhileStarting(t *testing.T) {
	first := freeUDP(t)
	alice, open := gated(t, Config{First: first})
	pr, err := alice.CreatePairing(inMain)
	if err != nil {
		t.Fatalf("a code asked while the network starts is refused: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if p := alice.Pairing(inMain); p.State != pairStarting || p.Code != "" {
		t.Fatalf("before the network: %+v", p)
	}
	open()
	code := alice.rec.waitPairing(t, pr.ID, func(p Pairing) bool { return p.State == pairWaiting })
	bob, openBob := gated(t, Config{Bootstrap: first})
	jp, err := bob.JoinPairing(code.Code)
	if err != nil {
		t.Fatalf("a paste while the network starts is refused: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if p := bob.Pairing(inMain); p.State != pairLooking {
		t.Fatalf("bob before the network: %+v", p)
	}
	openBob()
	if p := bob.rec.waitPairing(t, jp.ID, final); p.State != pairConnected {
		t.Fatalf("bob: %+v (error %+v)", p, p.Error)
	}
}

// TestPairingFailsWithTheNetwork starts a window before the first one, as the owner did: its network fails, the
// pairing fails with it, and Try again connects once the first window runs.
func TestPairingFailsWithTheNetwork(t *testing.T) {
	first := freeUDP(t)
	p, open := gated(t, Config{Bootstrap: first}) // nobody answers there yet
	pr, err := p.CreatePairing(inMain)
	if err != nil {
		t.Fatal(err)
	}
	open()
	if got := p.rec.waitPairing(t, pr.ID, final); got.State != pairFailed || got.Error == nil || got.Error.Reason != "no-local-entry-node" || got.Error.Detail == "" {
		t.Fatalf("pairing: %+v (error %+v)", got, got.Error)
	}
	p.rec.waitNetwork(t, netFailed)
	if n := p.Info().Network; n.Error == nil || !strings.Contains(n.Message, "-first") {
		t.Fatalf("no next step for a loopback entry node that is not running: %+v", n)
	}
	if _, err := p.CreatePairing(inMain); !errors.Is(err, ErrNotReady) {
		t.Fatalf("a code on a failed network: %v", err)
	}
	if _, err := p.JoinPairing(showCodeText(t)); !errors.Is(err, ErrNotReady) {
		t.Fatalf("a paste on a failed network: %v", err)
	}
	failed := p.Info().Network
	a := startPeer(t, Config{First: first}, nil)
	a.rec.waitNetwork(t, netReady)
	if err := p.Reconnect(); err != nil {
		t.Fatalf("reconnect after the pairing failed: %v", err)
	}
	p.rec.waitNetwork(t, netReady)
	if n := p.Info().Network; n.State != netReady || n.Seq <= failed.Seq {
		t.Fatalf("after Try again: %+v, failed at %+v", n, failed)
	}
}

// showCodeText makes a well-formed pairing code of an inviter nobody runs.
func showCodeText(t *testing.T) string {
	t.Helper()
	id, _ := koloid.Generate()
	in, err := invite.Open(kolostore.NewMemory(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	text, _, err := in.Create(nil)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestShutdownWhileTransfersStart(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	sa, _ := connect(t, alice, bob)
	src := writeFile(t, "f", []byte("x"))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { alice.Send(sa, src) })
	}
	wg.Go(func() { alice.CreatePairing(inMain) })
	alice.ServiceShutdown()
	wg.Wait()
	if n := len(alice.Transfers()); n != 0 {
		t.Fatalf("%d transfers after shutdown", n)
	}
}

func TestSaveNeedsLiveSession(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	sa, sb := connect(t, alice, bob)
	offerItem(t, alice, sa, writeFile(t, "later.txt", []byte("l")))
	in := incoming(t, bob, sb, "later.txt")
	dest := t.TempDir()
	bob.SetSessionFolder(sb, dest)
	alice.mu.Lock()
	sw := alice.sw
	alice.mu.Unlock()
	sw.Close() // alice is gone without a goodbye
	bob.rec.waitSession(t, sb, func(v Session) bool { return v.State == sessReconnecting })
	var se *ShareError
	if err := bob.SaveOffer(in.ID); !errors.As(err, &se) || se.Reason != "session-offline" {
		t.Fatalf("save while offline: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if tr := bob.rec.until(t, in.ID, func(Transfer) bool { return true }); tr.State != stateOffered {
		t.Fatalf("the offer after a refused save: %+v", tr)
	}
	assertEmpty(t, dest)
}
