package main

import (
	kolodht "github.com/ciranda-dev/kolo-dht"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
	kolotransport "github.com/ciranda-dev/kolo-transport"
)

const (
	// startWindow is the receive window of every connection of the session swarm, per stream and per connection: kolo-transport's
	// default stream window, the same for both so a connection nobody marked never grows (kolo-transport docs/guide.md,
	// "Faster transfers for trusted peers"). A marked connection grows from there to grownStreamWindow and grownConnWindow:
	// a pull moves at most a window per round trip, so 4 MiB is about 335 Mbit/s at 100 ms and 84 at 400 ms; 256 KiB is 21 at 100 ms.
	startWindow       = kolotransport.DefaultMaxStreamReceiveWindow
	grownStreamWindow = 4 << 20
	grownConnWindow   = 8 << 20
	// maxSessions is the most sessions whose windows fit kolo-transport's default MaxMemory, the budget kolo-swarm derives
	// its own places from: each session takes three windows (an accepted and two dialed connections) and the growth of
	// one, next to the windows of the hellos and handshakes.
	maxSessions = (kolotransport.DefaultMaxMemory/startWindow - 2*(sessionHandshakes+sessionPending)) / (3 + (grownConnWindow-startWindow)/startWindow)
	// grownMemory is the growth all marked connections hold together: every paired peer at the ceiling.
	grownMemory = maxSessions * (grownConnWindow - startWindow)
	// sessionMaxPeers is two peers per session, so a peer that reconnects while its old connection is closing still
	// has a place. Only the paired peers pass Allow.
	sessionMaxPeers = 2 * maxSessions
	// sessionHandshakes is kolo-swarm's MaxHandshakes default, and sessionPending as many: each kolo-secure handshake
	// that ends becomes a hello.
	sessionHandshakes = 16
	sessionPending    = 16
	// sessionConns and sessionDialed are what kolo-swarm computes from them: the
	// inbound peers (half of sessionMaxPeers), hellos and handshakes; and the peers, hellos and handshakes.
	sessionConns  = sessionMaxPeers/2 + sessionHandshakes + sessionPending
	sessionDialed = sessionMaxPeers + sessionHandshakes + sessionPending
)

// Congestion controllers of -congestion.
const (
	congestionReno  = "reno"
	congestionCubic = "cubic"
	congestionBBR   = "bbr"
	// defaultCongestion is BBR because the session connection only moves files; it fills a link that random loss
	// leaves empty under reno (kolo-transport docs/guide.md, "Congestion control").
	defaultCongestion = congestionBBR
)

// validCongestion reports whether name is a controller kolo-transport takes.
func validCongestion(name string) bool {
	return name == congestionReno || name == congestionCubic || name == congestionBBR
}

// limitSession sets the peer limits, the window growth and the congestion controller of the session swarm on cfg, over
// the kolo-dht settings it already has. The initial windows apply to every connection of the swarm's socket,
// strangers included, so the connection counts are the app's own and not kolo-swarm's defaults, which it derives for
// many more peers; growth is only for the connections the swarm marks after Allow admitted the paired peer, and never without Allow.
func limitSession(cfg *koloswarm.Config, congestion string) {
	var dht kolodht.Config
	if cfg.DHT != nil {
		dht = *cfg.DHT
	}
	var sec kolosecure.Config
	if dht.Secure != nil {
		sec = *dht.Secure
	}
	var tc kolotransport.Config
	if sec.Transport != nil {
		tc = *sec.Transport
	}
	tc.WindowGrowth = true
	tc.MaxStreamReceiveWindow = startWindow
	tc.MaxConnReceiveWindow = startWindow
	tc.MaxGrownStreamReceiveWindow = grownStreamWindow
	tc.MaxGrownConnReceiveWindow = grownConnWindow
	tc.MaxGrownMemory = grownMemory
	tc.CongestionControl = congestion
	tc.MaxConns = sessionConns
	tc.MaxDialed = sessionDialed
	sec.MaxPending = sessionPending
	sec.Transport = &tc
	dht.Secure = &sec
	cfg.DHT = &dht
	cfg.MaxPeers = sessionMaxPeers
	cfg.MaxHandshakes = sessionHandshakes
}
