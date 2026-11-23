package main

import (
	"context"
	"net/netip"
	"os"
	"strconv"
	"testing"
	"time"

	kolodht "github.com/ciranda-dev/kolo-dht"
	koloid "github.com/ciranda-dev/kolo-id"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	"github.com/ciranda-dev/kolo-share-wails3-example/internal/netsim"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
	kolotransport "github.com/ciranda-dev/kolo-transport"
)

// A session swarm grows the windows of the peers Allow admits from kolo-transport's default start, with a budget for every
// paired peer, paces with BBR unless told otherwise, and has a memory budget under kolo-transport's default MaxMemory, so
// the swarm leaves MaxMemory alone.
func TestSessionSwarmConfig(t *testing.T) {
	var seen []*koloswarm.Config
	networkWith(t, Config{}, Config{}, func(s *ShareService) {
		s.hooks.swarmCfg = func(c *koloswarm.Config) { seen = append(seen, c) }
	}, nil)
	if len(seen) != 1 {
		t.Fatalf("the session swarm opened %d times", len(seen))
	}
	c := seen[0]
	if c.MaxPeers != 2*maxSessions || c.MaxHandshakes != 16 || c.DHT.Secure.MaxPending != 16 {
		t.Fatalf("peer limits: %d %d %d", c.MaxPeers, c.MaxHandshakes, c.DHT.Secure.MaxPending)
	}
	tc := c.DHT.Secure.Transport
	if !tc.WindowGrowth || tc.MaxStreamReceiveWindow != 256<<10 || tc.MaxConnReceiveWindow != 256<<10 {
		t.Fatalf("growth %v, windows %d %d: a connection nobody marked must keep the default stream window", tc.WindowGrowth, tc.MaxStreamReceiveWindow, tc.MaxConnReceiveWindow)
	}
	if tc.MaxGrownStreamReceiveWindow != 4<<20 || tc.MaxGrownConnReceiveWindow != 8<<20 || tc.MaxGrownMemory != maxSessions*(8<<20-256<<10) {
		t.Fatalf("grown: %d %d, budget %d", tc.MaxGrownStreamReceiveWindow, tc.MaxGrownConnReceiveWindow, tc.MaxGrownMemory)
	}
	if tc.CongestionControl != "bbr" {
		t.Fatalf("congestion control %q, want bbr", tc.CongestionControl)
	}
	if tc.MaxConns != maxSessions+32 || tc.MaxDialed != 2*maxSessions+32 || tc.MaxMemory != 0 {
		t.Fatalf("connections: %d %d, MaxMemory %d", tc.MaxConns, tc.MaxDialed, tc.MaxMemory)
	}
	conns := int64(tc.MaxConns + tc.MaxDialed)
	need := conns*tc.MaxConnReceiveWindow + min(tc.MaxGrownMemory, conns*(tc.MaxGrownConnReceiveWindow-tc.MaxConnReceiveWindow))
	if need > kolotransport.DefaultMaxMemory {
		t.Fatalf("the windows and their growth need %d bytes, above the default MaxMemory", need)
	}
	if more := need + 3*tc.MaxConnReceiveWindow + tc.MaxGrownConnReceiveWindow - tc.MaxConnReceiveWindow; more <= kolotransport.DefaultMaxMemory {
		t.Fatalf("%d sessions need %d bytes: one more still fits the default MaxMemory", maxSessions, need)
	}
	if c.Allow == nil || len(c.BootstrapNodes) == 0 {
		t.Fatalf("the config lost its own fields: %+v", c)
	}
}

// The session swarm paces with the controller of -congestion, bbr when it is not given.
func TestSessionCongestion(t *testing.T) {
	for _, tt := range []struct{ set, want string }{{"", "bbr"}, {"reno", "reno"}, {"cubic", "cubic"}, {"bbr", "bbr"}} {
		var seen *koloswarm.Config
		networkWith(t, Config{Congestion: tt.set}, Config{}, func(s *ShareService) {
			s.hooks.swarmCfg = func(c *koloswarm.Config) { seen = c }
		}, nil)
		if got := seen.DHT.Secure.Transport.CongestionControl; got != tt.want {
			t.Errorf("-congestion %q: the session swarm has %q, want %q", tt.set, got, tt.want)
		}
	}
}

// limitSession keeps what the test firewall already put in the kolo-dht settings.
func TestLimitSessionKeepsHooks(t *testing.T) {
	accept := func(netip.AddrPort) bool { return false }
	var cfg koloswarm.Config
	cfg.DHT = &kolodht.Config{Server: true, Secure: &kolosecure.Config{Transport: &kolotransport.Config{AcceptFunc: accept}}}
	limitSession(&cfg, "reno")
	tc := cfg.DHT.Secure.Transport
	if tc.AcceptFunc == nil || !cfg.DHT.Server || tc.MaxConnReceiveWindow != startWindow || tc.CongestionControl != "reno" {
		t.Fatalf("limitSession dropped a setting: %+v", cfg.DHT)
	}
}

// linkMode is a transport setting of the benchmark link.
type linkMode struct {
	name  string
	apply func(*kolotransport.Config) // nil: the settings of the app
}

var (
	// modeDefaults is kolo-transport with no setting of the app: 256 KiB and 512 KiB windows, no growth, reno.
	modeDefaults = linkMode{"defaults_reno", func(t *kolotransport.Config) {
		t.WindowGrowth, t.CongestionControl = false, ""
		t.MaxStreamReceiveWindow, t.MaxConnReceiveWindow, t.MaxMemory = 0, 0, 0
		t.MaxGrownStreamReceiveWindow, t.MaxGrownConnReceiveWindow, t.MaxGrownMemory = 0, 0, 0
	}}
	// modeFlat is the flat windows of 4 MiB and 8 MiB for every connection, with reno.
	modeFlat = linkMode{"flat_4_8_MiB_reno", func(t *kolotransport.Config) {
		modeDefaults.apply(t)
		t.MaxStreamReceiveWindow, t.MaxConnReceiveWindow = 4<<20, 8<<20
	}}
	// modeApp is what the app sets: growth for the paired peer and BBR.
	modeApp = linkMode{"growth_bbr", nil}
)

// linkedPair starts two apps that pair and connect through a netsim link with the profile half in each direction,
// and returns them with the session IDs on alice and on bob. mode changes the transport settings of both swarms.
func linkedPair(t *testing.T, half netsim.Profile, mode linkMode) (alice, bob *peer, sa, sb string, link *netsim.Link) {
	t.Helper()
	ids := newIDs(t, 2)
	w := &wall{}
	var bobTC kolotransport.Config
	set := func(id *koloid.Identity, tc *kolotransport.Config) func(*ShareService) {
		return func(s *ShareService) {
			w.set(s)
			s.hooks.identity = id
			s.hooks.swarmCfg = func(c *koloswarm.Config) {
				t := c.DHT.Secure.Transport
				if mode.apply != nil {
					mode.apply(t)
				}
				*tc = *t
			}
		}
	}
	var aliceTC kolotransport.Config
	first := freeUDP(t)
	alice = startPeer(t, Config{First: first}, set(ids[0], &aliceTC))
	alice.rec.waitNetwork(t, netReady)
	bob = startPeer(t, Config{Bootstrap: first}, set(ids[1], &bobTC))
	bob.rec.waitNetwork(t, netReady)
	w.refuseSessions(t, alice, bob)

	code := showCode(t, alice)
	jb := pasteCode(t, bob, code.Code)
	ja := alice.rec.waitPairing(t, code.ID, final)
	if ja.State != pairConnected || jb.State != pairConnected {
		t.Fatalf("pairing: %+v / %+v", ja, jb)
	}
	link, err := netsim.New(alice.sw.Addr().String(), half, half)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { link.Close() })
	// Bob dials alice through the link with his swarm's settings and hands the connection to his swarm.
	dial := bobTC
	dial.MaxConns, dial.MaxDialed = 0, 0
	conn, err := kolosecure.Dial(context.Background(), link.Addr(), ids[1], ids[0].PublicKey(), &kolosecure.Config{Transport: &dial})
	if err != nil {
		t.Fatal(err)
	}
	if err := bob.sw.Attach(context.Background(), conn, true); err != nil {
		t.Fatal(err)
	}
	bob.rec.waitSession(t, jb.SessionID, connected)
	alice.rec.waitSession(t, ja.SessionID, connected)
	return alice, bob, ja.SessionID, jb.SessionID, link
}

// linkScenario is a path of the benchmark; half is one direction.
type linkScenario struct {
	name string
	half netsim.Profile
}

// linkScenarios are the paths of TestBenchLink; the name gives the round trip. Rate is in bytes per second.
var linkScenarios = []linkScenario{
	{"100ms_100Mbit_noloss", netsim.Profile{Rate: 100_000_000 / 8, Delay: 50 * time.Millisecond, Seed: 1}},
	{"100ms_20Mbit_1pct_loss", netsim.Profile{Rate: 20_000_000 / 8, Delay: 50 * time.Millisecond, Loss: 0.01, Seed: 1}},
	{"30ms_100Mbit_noloss", netsim.Profile{Rate: 100_000_000 / 8, Delay: 15 * time.Millisecond, Seed: 1}},
	{"30ms_20Mbit_1pct_loss", netsim.Profile{Rate: 20_000_000 / 8, Delay: 15 * time.Millisecond, Loss: 0.01, Seed: 1}},
	{"60ms_20Mbit_3pct_loss", netsim.Profile{Rate: 20_000_000 / 8, Delay: 30 * time.Millisecond, Loss: 0.03, Seed: 1}},
}

// linkRun sends size bytes from alice to bob over a netsim link that carries the session connection, with the transport
// mode, and returns how long the receive into the store took.
func linkRun(t *testing.T, half netsim.Profile, mode linkMode, size int) time.Duration {
	t.Helper()
	old := waitFor
	waitFor = 40 * time.Minute
	t.Cleanup(func() { waitFor = old })
	alice, bob, sa, sb, link := linkedPair(t, half, mode)
	bob.SetSessionFolder(sb, t.TempDir())
	id, err := alice.Send(sa, writeFile(t, "link.bin", randomBytes(t, size)))
	if err != nil {
		t.Fatal(err)
	}
	alice.rec.wait(t, id, stateOffered)
	in := incoming(t, bob, sb, "link.bin")
	start := time.Now()
	if err := bob.SaveOffer(in.ID); err != nil {
		t.Fatal(err)
	}
	if tr := bob.rec.wait(t, in.ID, stateVerifying, stateDone, stateFailed); tr.State == stateFailed {
		t.Fatalf("receive: %+v (%+v)", tr, tr.Error)
	}
	took := time.Since(start)
	if tr := bob.rec.wait(t, in.ID, stateDone, stateFailed); tr.State != stateDone {
		t.Fatalf("receive: %+v (%+v)", tr, tr.Error)
	}
	down := link.Down()
	t.Logf("link: %d bytes and %d packets delivered to bob, dropped %d (queue) %d (loss)", down.Bytes, down.Packets, down.DroppedQ, down.DroppedLoss)
	return took
}

// TestBenchLink pulls KOLO_SHARE_BENCH_MIB MiB (default 100) through the app over five simulated paths with kolo-transport's
// defaults, with flat 4 MiB and 8 MiB windows, and with the growth and BBR the app sets:
// KOLO_SHARE_BENCH=1 go test -run BenchLink -v. -run BenchLink/100ms_100Mbit_noloss/growth_bbr picks one cell.
func TestBenchLink(t *testing.T) {
	if os.Getenv("KOLO_SHARE_BENCH") != "1" {
		t.Skip("set KOLO_SHARE_BENCH=1")
	}
	mib := 100
	if v := os.Getenv("KOLO_SHARE_BENCH_MIB"); v != "" {
		var err error
		if mib, err = strconv.Atoi(v); err != nil || mib < 1 {
			t.Fatalf("KOLO_SHARE_BENCH_MIB=%q", v)
		}
	}
	size := mib << 20
	for _, sc := range linkScenarios {
		t.Run(sc.name, func(t *testing.T) {
			for _, mode := range []linkMode{modeDefaults, modeFlat, modeApp} {
				t.Run(mode.name, func(t *testing.T) {
					d := linkRun(t, sc.half, mode, size)
					t.Logf("%s %s: %d MiB in %v, %.2f MB/s (%.1f Mbit/s)", sc.name, mode.name, mib, d.Round(time.Millisecond),
						float64(size)/1e6/d.Seconds(), float64(size)*8/1e6/d.Seconds())
				})
			}
		})
	}
}
