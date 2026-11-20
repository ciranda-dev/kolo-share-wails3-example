package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// networkWith is network with a Config and a setup for each side: the sender runs the first entry node.
func networkWith(t *testing.T, sendCfg, recvCfg Config, sendSetup, recvSetup func(*ShareService)) (sender, recv *peer) {
	t.Helper()
	first := freeUDP(t)
	sendCfg.First = first
	sender = startPeer(t, sendCfg, sendSetup)
	sender.rec.waitNetwork(t, netReady)
	recvCfg.Bootstrap = first
	recv = startPeer(t, recvCfg, recvSetup)
	recv.rec.waitNetwork(t, netReady)
	return sender, recv
}

// steps holds a send's preparation at every chunk it stores until release lets that many go.
type steps struct{ ch chan struct{} }

func newSteps() *steps { return &steps{ch: make(chan struct{}, 1<<16)} }

func (h *steps) hook(ctx context.Context, _ int) {
	select {
	case <-h.ch:
	case <-ctx.Done():
	}
}

func (h *steps) release(n int) {
	for range n {
		h.ch <- struct{}{}
	}
}

// waitPrep waits until p knows that the peer of session sid reads announcements.
func (p *peer) waitPrep(t *testing.T, sid string) {
	t.Helper()
	poll(t, "the peer's features", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.sessions[sid].peerPrep
	})
}

func (r *recorder) preparing(id string) []Transfer {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Transfer
	for _, tr := range r.transfers[id] {
		if tr.State == statePreparing {
			out = append(out, tr)
		}
	}
	return out
}

func (r *recorder) anyPreparingReceive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ts := range r.transfers {
		for _, tr := range ts {
			if tr.Direction == "receive" && tr.State == statePreparing {
				return true
			}
		}
	}
	return false
}

// preparingCard waits for the card of an item being prepared that the peer announced.
func preparingCard(t *testing.T, p *peer, name string) Transfer {
	t.Helper()
	return p.rec.find(t, "a card for "+name, func(tr Transfer) bool {
		return tr.Direction == "receive" && tr.Name == name && tr.State == statePreparing
	})
}

func TestPrepAnnouncedBeforeOffer(t *testing.T) {
	hold := newSteps()
	alice, bob := networkWith(t, Config{}, Config{}, func(s *ShareService) { s.hooks.prepStep = hold.hook }, nil)
	sa, sb := connect(t, alice, bob)
	alice.waitPrep(t, sa)
	body := randomBytes(t, 3<<20+5)
	id, err := alice.Send(sa, writeFile(t, "slow.bin", body))
	if err != nil {
		t.Fatal(err)
	}
	card := preparingCard(t, bob, "slow.bin")
	if card.Kind != kindFile || card.BytesTotal != int64(len(body)) || card.PeerName == "" || card.SessionID != sb {
		t.Fatalf("card: %+v", card)
	}
	if err := bob.SaveOffer(card.ID); err == nil {
		t.Fatal("Save works on an item that is not offered yet")
	}
	if err := bob.DeclineOffer(card.ID); err == nil {
		t.Fatal("Decline works on an item that is not offered yet")
	}
	bob.rec.waitSession(t, sb, func(v Session) bool { return v.Waiting == 0 })
	// The sender's own card shows its size at once, as preparing, and nothing is offered yet.
	if tr := alice.rec.wait(t, id, statePreparing); tr.BytesTotal != int64(len(body)) {
		t.Fatalf("sender: %+v", tr)
	}
	hold.release(2)
	if got := bob.rec.until(t, card.ID, func(tr Transfer) bool { return tr.BytesDone > 0 }); got.State != statePreparing || got.BytesDone > got.BytesTotal {
		t.Fatalf("progress: %+v", got)
	}
	hold.release(1 << 10)
	offered := bob.rec.wait(t, card.ID, stateOffered)
	if offered.Name != "slow.bin" || offered.BytesTotal != int64(len(body)) || offered.BytesDone != 0 {
		t.Fatalf("the card turned into: %+v", offered)
	}
	if s := bob.rec.states(card.ID); !slices.Equal(s, []string{statePreparing, stateOffered}) {
		t.Fatalf("states %v", s)
	}
	bob.rec.waitSession(t, sb, func(v Session) bool { return v.Waiting == 1 })
	dest := t.TempDir()
	bob.SetSessionFolder(sb, dest)
	if err := bob.SaveOffer(card.ID); err != nil {
		t.Fatal(err)
	}
	got := bob.rec.wait(t, card.ID, stateDone, stateFailed)
	if b, err := os.ReadFile(got.SavedTo); got.State != stateDone || err != nil || !bytes.Equal(b, body) {
		t.Fatalf("receive: %+v (%v)", got, err)
	}
	alice.rec.wait(t, id, stateDelivered)
	if n := len(bob.rec.states(card.ID)); n != 6 {
		t.Fatalf("one card, one set of states: %v", bob.rec.states(card.ID))
	}
	if h := bob.History(); len(h) != 1 {
		t.Fatalf("a card that became an offer is one item in the history: %+v", h)
	}
}

func TestPrepProgressIsThrottled(t *testing.T) {
	slow := func(ctx context.Context, _ int) {
		select {
		case <-time.After(120 * time.Millisecond):
		case <-ctx.Done():
		}
	}
	alice, bob := networkWith(t, Config{}, Config{}, func(s *ShareService) { s.hooks.prepStep = slow }, nil)
	sa, _ := connect(t, alice, bob)
	alice.waitPrep(t, sa)
	began := time.Now()
	id, err := alice.Send(sa, writeFile(t, "paced.bin", randomBytes(t, 24<<20)))
	if err != nil {
		t.Fatal(err)
	}
	card := preparingCard(t, bob, "paced.bin")
	bob.rec.wait(t, card.ID, stateOffered)
	elapsed := time.Since(began)
	n := len(bob.rec.preparing(card.ID))
	if n < 2 {
		t.Fatalf("no progress reached the receiver: %d events", n)
	}
	// Two a second at most, and the card itself.
	if limit := int(2*elapsed.Seconds()) + 3; n > limit {
		t.Fatalf("%d card events in %v, more than %d", n, elapsed, limit)
	}
	alice.CancelTransfer(id)
}

func TestPrepThrottle(t *testing.T) {
	var p prepThrottle
	at := time.Now()
	p.at, p.total = at, 100
	for _, c := range []struct {
		after       time.Duration
		done, total int64
		want        bool
	}{
		{100 * time.Millisecond, 5, 100, false},  // changed, too soon
		{600 * time.Millisecond, 5, 100, true},   // changed, due
		{700 * time.Millisecond, 9, 100, false},  // changed, 100 ms after the last
		{1200 * time.Millisecond, 5, 100, false}, // due, but nothing changed since the last
		{1300 * time.Millisecond, 5, 0, true},    // the total changed
		{2000 * time.Millisecond, 5, 0, false},
		{32 * time.Second, 5, 0, true}, // nothing changed for a long time: a keepalive
	} {
		if got := p.due(at.Add(c.after), c.done, c.total); got != c.want {
			t.Fatalf("after %v with %d of %d: %v, want %v", c.after, c.done, c.total, got, c.want)
		}
	}
}

func TestPrepCancelBySender(t *testing.T) {
	hold := newSteps()
	alice, bob := networkWith(t, Config{}, Config{}, func(s *ShareService) { s.hooks.prepStep = hold.hook }, nil)
	sa, _ := connect(t, alice, bob)
	alice.waitPrep(t, sa)
	id, _ := alice.Send(sa, writeFile(t, "cancelled.bin", randomBytes(t, 2<<20)))
	card := preparingCard(t, bob, "cancelled.bin")
	alice.CancelTransfer(id)
	got := bob.rec.wait(t, card.ID, stateWithdrawn, stateFailed)
	if got.State != stateWithdrawn || got.Error == nil || got.Error.Reason != "offer-withdrawn" {
		t.Fatalf("card after the cancel: %+v", got)
	}
	alice.rec.wait(t, id, stateCancelled, stateStopped)
	if n := len(bob.History()); n != 0 {
		t.Fatalf("an announcement that ended is not history: %d", n)
	}
	poll(t, "no card left", func() bool { return len(bob.Transfers()) == 0 })
}

func TestPrepFailureEndsTheCard(t *testing.T) {
	alice, bob := networkWith(t, Config{MaxFiles: 3}, Config{}, nil, nil)
	sa, _ := connect(t, alice, bob)
	alice.waitPrep(t, sa)
	src := filepath.Join(t.TempDir(), "crowded")
	os.Mkdir(src, 0o700)
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		os.WriteFile(filepath.Join(src, n), []byte(n), 0o600)
	}
	id, err := alice.Send(sa, src)
	if err != nil {
		t.Fatal(err)
	}
	if tr := alice.rec.wait(t, id, stateFailed); tr.Error == nil || tr.Error.Reason != "source-too-many" {
		t.Fatalf("sender: %+v", tr)
	}
	card := bob.rec.find(t, "the failed card", func(tr Transfer) bool {
		return tr.Direction == "receive" && tr.Name == "crowded" && tr.State == stateFailed
	})
	if card.Error == nil || card.Error.Reason != "prepare-failed" || !slices.Equal(bob.rec.states(card.ID), []string{statePreparing, stateFailed}) {
		t.Fatalf("card: %+v, states %v", card, bob.rec.states(card.ID))
	}
	poll(t, "no card left", func() bool { return len(bob.Transfers()) == 0 })
}

func TestPrepCardStopsWhenTheSenderIsSilent(t *testing.T) {
	old := prepStall
	prepStall = 400 * time.Millisecond
	t.Cleanup(func() { prepStall = old })
	hold := newSteps()
	alice, bob := networkWith(t, Config{}, Config{}, func(s *ShareService) { s.hooks.prepStep = hold.hook }, nil)
	sa, sb := connect(t, alice, bob)
	alice.waitPrep(t, sa)
	id, _ := alice.Send(sa, writeFile(t, "silent.bin", randomBytes(t, 2<<20)))
	card := preparingCard(t, bob, "silent.bin")
	got := bob.rec.wait(t, card.ID, stateFailed)
	if got.Error == nil || got.Error.Reason != "prepare-stalled" {
		t.Fatalf("card: %+v", got)
	}
	// The sender was only slow: its offer still arrives, as a new item.
	hold.release(1 << 10)
	offered := bob.rec.find(t, "the late offer", func(tr Transfer) bool {
		return tr.Direction == "receive" && tr.Name == "silent.bin" && tr.State == stateOffered && tr.ID != card.ID
	})
	dest := t.TempDir()
	bob.SetSessionFolder(sb, dest)
	bob.SaveOffer(offered.ID)
	bob.rec.wait(t, offered.ID, stateDone)
	alice.rec.wait(t, id, stateDelivered)
}

func TestPrepOverTheLimitShowsNoCard(t *testing.T) {
	hold := newSteps()
	alice, bob := networkWith(t, Config{}, Config{MaxDownload: 1 << 20}, func(s *ShareService) { s.hooks.prepStep = hold.hook }, nil)
	sa, sb := connect(t, alice, bob)
	alice.waitPrep(t, sa)
	id, _ := alice.Send(sa, writeFile(t, "huge.bin", randomBytes(t, 2<<20)))
	hold.release(1 << 10)
	if tr := alice.rec.wait(t, id, stateDeclined); tr.State != stateDeclined {
		t.Fatalf("sender: %+v", tr)
	}
	if v := bob.rec.waitSession(t, sb, func(v Session) bool { return v.Notice != nil }); v.Notice.Reason != "too-large" {
		t.Fatalf("notice: %+v", v.Notice)
	}
	if bob.rec.anyPreparingReceive() {
		t.Fatal("an item over the limits showed a card")
	}
}

// An app ends the session on a message it does not know, so a sender never announces to a peer that did not say
// it reads the announcements, and the offer works without them.
func TestPrepPeerWithoutFeaturesKeepsWorking(t *testing.T) {
	alice, bob := networkWith(t, Config{}, Config{}, nil, func(s *ShareService) { s.hooks.noFeatures = true })
	sa, sb := connect(t, alice, bob)
	time.Sleep(time.Second) // a features frame would have arrived by now
	alice.mu.Lock()
	known := alice.sessions[sa].peerPrep
	alice.mu.Unlock()
	if known {
		t.Fatal("alice thinks a peer without features reads announcements")
	}
	body := randomBytes(t, 1<<20)
	sent := offerItem(t, alice, sa, writeFile(t, "plain.bin", body))
	got := saveOffer(t, bob, sb, "plain.bin", t.TempDir())
	if b, err := os.ReadFile(got.SavedTo); got.State != stateDone || err != nil || !bytes.Equal(b, body) {
		t.Fatalf("receive: %+v (%v)", got, err)
	}
	alice.rec.wait(t, sent.ID, stateDelivered)
	if bob.rec.anyPreparingReceive() {
		t.Fatal("a peer without features got an announcement")
	}
	if v := bob.rec.waitSession(t, sb, func(Session) bool { return true }); v.State != sessConnected {
		t.Fatalf("session: %+v", v)
	}
}

// What an app without the feature does with the announcement messages, which is why they are never sent to it: it
// ends the session.
func TestPrepMessageEndsTheSessionOfAPeerWithoutFeatures(t *testing.T) {
	alice, bob := networkWith(t, Config{}, Config{}, nil, func(s *ShareService) { s.hooks.noFeatures = true })
	sa, sb := connect(t, alice, bob)
	alice.inject(t, sa, ctlMsg{typ: ctlPrepare, prep: prepare{id: 1, kind: kindFile, name: "x"}})
	if v := bob.rec.waitSession(t, sb, func(v Session) bool { return v.State == sessEnded }); v.Reason != endProtocol {
		t.Fatalf("session: %+v", v)
	}
}

// A peer cannot fill the window with announcements: no more cards than open offers, and no more new ones a
// minute than an honest sender makes.
func TestPrepAnnouncementsAreBounded(t *testing.T) {
	alice, bob := network(t, Config{}, nil)
	sa, sb := connect(t, alice, bob)
	for i := uint64(1); i <= 3*maxOpenOffers; i++ {
		alice.inject(t, sa, ctlMsg{typ: ctlPrepare, prep: prepare{id: i, kind: kindFile, name: "f", total: 10}})
	}
	alice.inject(t, sa, ctlMsg{typ: ctlSynced}) // a message the peer answers in order: the cards are in
	poll(t, "the cards", func() bool { return len(bob.Transfers()) >= maxOpenOffers })
	time.Sleep(300 * time.Millisecond)
	if n := len(bob.Transfers()); n != maxOpenOffers {
		t.Fatalf("%d cards, want %d", n, maxOpenOffers)
	}
	// Ended cards free their places, and the new ones a minute are bounded all the same.
	for i := uint64(1); i <= 3*maxOpenOffers; i++ {
		alice.inject(t, sa, ctlMsg{typ: ctlPrepEnd, prep: prepare{id: i}, state: endCancelled})
	}
	poll(t, "the cards end", func() bool { return len(bob.Transfers()) == 0 })
	for i := uint64(100); i < 100+2*offerRate; i++ {
		alice.inject(t, sa, ctlMsg{typ: ctlPrepare, prep: prepare{id: i, kind: kindFile, name: "g", total: 10}})
		alice.inject(t, sa, ctlMsg{typ: ctlPrepEnd, prep: prepare{id: i}, state: endCancelled})
	}
	time.Sleep(500 * time.Millisecond)
	made := 0
	bob.rec.mu.Lock()
	for _, ts := range bob.rec.transfers {
		if ts[0].Name == "g" {
			made++
		}
	}
	bob.rec.mu.Unlock()
	if made == 0 || made > offerRate {
		t.Fatalf("%d cards of announcements in a burst, the bound is %d", made, offerRate)
	}
	if v := bob.rec.waitSession(t, sb, func(Session) bool { return true }); v.State != sessConnected {
		t.Fatalf("session: %+v", v)
	}
}

// The announcement, its progress and its end survive a lost connection: the sender says them again, and a card
// the sender no longer says goes away.
func TestPrepSurvivesAReconnect(t *testing.T) {
	hold := newSteps()
	alice, bob := networkWith(t, Config{}, Config{}, func(s *ShareService) { s.hooks.prepStep = hold.hook }, nil)
	sa, sb := connect(t, alice, bob)
	alice.waitPrep(t, sa)
	idKeep, _ := alice.Send(sa, writeFile(t, "keep.bin", randomBytes(t, 2<<20)))
	idGone, _ := alice.Send(sa, writeFile(t, "gone.bin", randomBytes(t, 2<<20)))
	keep := preparingCard(t, bob, "keep.bin")
	gone := preparingCard(t, bob, "gone.bin")
	// The PREPEND of gone.bin is lost with the connection: alice forgets it while bob still shows it.
	alice.mu.Lock()
	for id, j := range alice.sessions[sa].prepOut {
		if j.t.ID == idGone {
			delete(alice.sessions[sa].prepOut, id)
		}
	}
	alice.mu.Unlock()
	alice.link(t, sa).conn.CloseWithError(0, "")
	alice.rec.waitSession(t, sa, func(v Session) bool { return v.State == sessReconnecting })
	alice.rec.waitSession(t, sa, connected)
	bob.rec.waitSession(t, sb, connected)
	if tr := bob.rec.wait(t, gone.ID, stateWithdrawn); tr.Error == nil {
		t.Fatalf("a card that was not said again: %+v", tr)
	}
	hold.release(1 << 10)
	offered := bob.rec.wait(t, keep.ID, stateOffered)
	if !slices.Equal(bob.rec.states(keep.ID), []string{statePreparing, stateOffered}) || offered.Name != "keep.bin" {
		t.Fatalf("states %v", bob.rec.states(keep.ID))
	}
	alice.CancelTransfer(idKeep)
	alice.CancelTransfer(idGone)
}

// A new message that breaks the protocol ends the session as the old ones do.
func TestMalformedPrepEndsSession(t *testing.T) {
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
		{"prepare with more done than total", raw(frameOf(encodePrepareFields(1, 1, []byte("a"), 11, 10)))},
		{"prepend with a reason that does not exist", raw(frameOf([]byte{ctlPrepEnd, 1, 9}))},
		{"transfer ID used again for another item", func(l *link) {
			l.write(ctlMsg{typ: ctlPrepare, prep: prepare{id: 5, kind: kindFile, name: "a"}})
			l.write(ctlMsg{typ: ctlPrepare, prep: prepare{id: 5, kind: kindFolder, name: "a"}})
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

func TestTrayAndNotchSayWhoPrepares(t *testing.T) {
	card := Transfer{ID: "1", Direction: "receive", Kind: kindFile, Name: "big.iso", PeerName: "Ana", State: statePreparing, BytesTotal: 100, BytesDone: 25}
	if st := trayOf([]Transfer{card}, "", 1); st.Status != "Ana is preparing big.iso" {
		t.Fatalf("tray: %q", st.Status)
	}
	n := pickNotch([]Transfer{card}, nil, false)
	if n.State != statePreparing || n.HasOffer || n.Percent != 25 || n.Direction != "receive" {
		t.Fatalf("notch: %+v", n)
	}
	card.BytesTotal = 0
	if n := pickNotch([]Transfer{card}, nil, false); n.Percent != unknownPercent {
		t.Fatalf("a card without a size shows no percent: %+v", n)
	}
	own := card
	own.Direction = "send"
	if st := trayOf([]Transfer{own}, "", 1); st.Status != "Preparing big.iso" {
		t.Fatalf("own send: %q", st.Status)
	}
}

// A scan or a store that takes minutes still tells the peer every prepKeepalive, so a card never reaches prepStall
// while the sender works: a folder of 100,000 files is walked with no number changing for a long time.
func TestHeartbeatCoversALongScan(t *testing.T) {
	if prepKeepalive*2 > prepStall {
		t.Fatalf("a heartbeat every %v against a receiver that gives up after %v", prepKeepalive, prepStall)
	}
	var p prepThrottle
	start := time.Now()
	p.due(start, 0, 0) // the first announcement
	last := start
	sent := 0
	for s := 1; s <= 3600; s++ { // an hour of scanning with nothing to report
		now := start.Add(time.Duration(s) * time.Second)
		if p.due(now, 0, 0) {
			if gap := now.Sub(last); gap > prepStall/2 {
				t.Fatalf("%v of silence before a heartbeat", gap)
			}
			last = now
			sent++
		}
	}
	if sent != 3600/int(prepKeepalive/time.Second) {
		t.Fatalf("%d heartbeats in an hour of scanning", sent)
	}
}
