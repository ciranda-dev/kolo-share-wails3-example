package main

import (
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	kolodht "github.com/ciranda-dev/kolo-dht"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
	kolotransport "github.com/ciranda-dev/kolo-transport"
)

// connectSoon is how long after the refusals end a connection may take: a few lookup intervals of 1 s, with room for
// a loaded machine. A backoff that was never capped waits much longer after 12 s of refusals.
const connectSoon = 8 * time.Second

// refuseUntil sets a firewall on the swarms of an app that refuses every connection until the time open holds.
func refuseUntil(open *atomic.Int64) func(*ShareService) {
	return func(s *ShareService) {
		s.hooks.dht = &kolodht.Config{Secure: &kolosecure.Config{Transport: &kolotransport.Config{AcceptFunc: func(netip.AddrPort) bool {
			return time.Now().UnixNano() >= open.Load()
		}}}}
	}
}

// A dial that fails is tried again within a lookup interval, in a pairing swarm and in the session swarm, as the
// other Kolo apps do. kolo-swarm's default waits up to 10 minutes, which gave up on a computer that was there.
func TestSwarmsRetryWithinALookupInterval(t *testing.T) {
	first := freeUDP(t)
	var pairing, session []*koloswarm.Config
	alice := startPeer(t, Config{First: first, LookupInterval: 3 * time.Second}, func(s *ShareService) {
		s.hooks.pairCfg = func(c *koloswarm.Config) { pairing = append(pairing, c) }
		s.hooks.swarmCfg = func(c *koloswarm.Config) { session = append(session, c) }
	})
	alice.rec.waitNetwork(t, netReady)
	bob := startPeer(t, Config{Bootstrap: first, LookupInterval: 3 * time.Second}, func(s *ShareService) {
		s.hooks.pairCfg = func(c *koloswarm.Config) { pairing = append(pairing, c) }
	})
	bob.rec.waitNetwork(t, netReady)
	connect(t, alice, bob)
	if len(session) != 1 || len(pairing) != 2 {
		t.Fatalf("swarms opened: %d session, %d pairing", len(session), len(pairing))
	}
	for _, c := range append(pairing, session...) {
		if c.LookupInterval != 3*time.Second || c.RetryMax != 3*time.Second {
			t.Fatalf("lookup interval %v, retry max %v: want 3s for both", c.LookupInterval, c.RetryMax)
		}
	}
}

// A pasted code finds the computer that made it soon after that computer starts to accept connections: the joiner's
// failed dials are not backed off past a lookup interval. The inviter refuses every connection for 12 s, long
// enough for kolo-swarm's default backoff to grow to waits of up to 16 s and more.
func TestPairConnectsSoonAfterTheDialsStopFailing(t *testing.T) {
	first := freeUDP(t)
	entry := startPeer(t, Config{First: first}, nil)
	entry.rec.waitNetwork(t, netReady)
	var open atomic.Int64
	alice := startPeer(t, Config{Bootstrap: first}, refuseUntil(&open))
	bob := startPeer(t, Config{Bootstrap: first}, nil)
	alice.rec.waitNetwork(t, netReady)
	bob.rec.waitNetwork(t, netReady)
	code := showCode(t, alice)
	open.Store(time.Now().Add(12 * time.Second).UnixNano())
	got := pasteCode(t, bob, code.Code)
	late := time.Since(time.Unix(0, open.Load()))
	if got.State != pairConnected {
		t.Fatalf("bob: %+v (error %+v)", got, got.Error)
	}
	if late > connectSoon {
		t.Fatalf("connected %v after the inviter began to accept, want within %v (a few lookup intervals of 1 s)", late, connectSoon)
	}
}

// The session connects soon after the connections stop being refused, for the same reason.
func TestSessionConnectsSoonAfterTheDialsStopFailing(t *testing.T) {
	w := &wall{}
	first := freeUDP(t)
	entry := startPeer(t, Config{First: first}, nil)
	entry.rec.waitNetwork(t, netReady)
	alice := startPeer(t, Config{Bootstrap: first}, w.set)
	bob := startPeer(t, Config{Bootstrap: first}, w.set)
	alice.rec.waitNetwork(t, netReady)
	bob.rec.waitNetwork(t, netReady)
	w.refuseSessions(t, alice, bob)
	code := showCode(t, alice)
	jb := pasteCode(t, bob, code.Code)
	ja := alice.rec.waitPairing(t, code.ID, final)
	if ja.State != pairConnected || jb.State != pairConnected {
		t.Fatalf("pairing: %+v / %+v", ja, jb)
	}
	time.Sleep(12 * time.Second)
	w.mu.Lock()
	w.refused = nil
	w.mu.Unlock()
	lifted := time.Now()
	alice.rec.waitSession(t, ja.SessionID, connected)
	bob.rec.waitSession(t, jb.SessionID, connected)
	if d := time.Since(lifted); d > connectSoon {
		t.Fatalf("the session connected %v after the refusals ended, want within %v (a few lookup intervals of 1 s)", d, connectSoon)
	}
}

// A code whose answer was lost works again: a joiner with the same key that pastes it after the answer was sent
// gets the same answer, and the session is the one the inviter opened, not a second one.
func TestPairSameCodeAfterALostAnswer(t *testing.T) {
	first := freeUDP(t)
	alice := startPeer(t, Config{First: first, FindTimeout: 30 * time.Second}, nil)
	alice.rec.waitNetwork(t, netReady)
	bob := startPeer(t, Config{Bootstrap: first, FindTimeout: 30 * time.Second}, nil)
	bob.rec.waitNetwork(t, netReady)
	code := showCode(t, alice)
	if _, err := rawJoinAs(t, first, code.Code, bob.id); err != nil {
		t.Fatal(err)
	}
	if p := alice.rec.waitPairing(t, code.ID, final); p.State != pairConnected {
		t.Fatalf("alice: %+v", p)
	}
	jb := pasteCode(t, bob, code.Code)
	if jb.State != pairConnected {
		t.Fatalf("the same code again: %+v (error %+v)", jb, jb.Error)
	}
	sa := alice.Sessions()
	if len(sa) != 1 {
		t.Fatalf("alice has %d sessions", len(sa))
	}
	alice.rec.waitSession(t, sa[0].ID, connected)
	bob.rec.waitSession(t, jb.SessionID, connected)
}
