package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"io"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kolodht "github.com/ciranda-dev/kolo-dht"
	koloid "github.com/ciranda-dev/kolo-id"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	"github.com/ciranda-dev/kolo-share-wails3-example/internal/netsim"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
	kolotransport "github.com/ciranda-dev/kolo-transport"
)

// growthHalf is one direction of the path of the growth tests: at a 50 ms round trip, auto-tuning needs a reader of
// only about 5 MB/s to double a 1 MiB window, well under what loopback gives.
var growthHalf = netsim.Profile{Delay: 25 * time.Millisecond}

var growthTopic = koloswarm.Topic(sha256.Sum256([]byte("kolo-share growth test")))

// growthSwarm is a swarm on loopback that has joined growthTopic and records its peers and what it found.
type growthSwarm struct {
	*koloswarm.Swarm
	id    *koloid.Identity
	found atomic.Int64

	mu    sync.Mutex
	peers map[string]*koloswarm.Peer
}

func (g *growthSwarm) peer(k ed25519.PublicKey) *koloswarm.Peer {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.peers[string(k)]
}

func newGrowthSwarm(t *testing.T, node *kolodht.Node, id *koloid.Identity, cfg *koloswarm.Config, join koloswarm.JoinOptions) *growthSwarm {
	t.Helper()
	cfg.BootstrapNodes = []string{node.Addr().String()}
	cfg.LookupInterval = time.Second
	cfg.HelloTimeout = 2 * time.Second
	if cfg.DHT == nil {
		cfg.DHT = &kolodht.Config{}
	}
	cfg.DHT.RequestTimeout, cfg.DHT.PunchTimeout = 300*time.Millisecond, time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sw, err := koloswarm.New(ctx, "127.0.0.1:0", id, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sw.Close() })
	if err := sw.Bootstrap(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := sw.Join(ctx, growthTopic, &join); err != nil {
		t.Fatal(err)
	}
	g := &growthSwarm{Swarm: sw, id: id, peers: map[string]*koloswarm.Peer{}}
	go func() {
		for {
			ev, err := sw.Next(context.Background())
			if err != nil {
				return
			}
			g.mu.Lock()
			switch ev.Kind {
			case koloswarm.Found:
				g.found.Add(1)
			case koloswarm.Connected:
				g.peers[string(ev.PublicKey)] = ev.Peer
			case koloswarm.Lost:
				delete(g.peers, string(ev.PublicKey))
			}
			g.mu.Unlock()
		}
	}()
	return g
}

// growthHub starts the session swarm as the app builds it, with Allow admitting only the key paired, on a loopback entry node.
func growthHub(t *testing.T, paired ed25519.PublicKey) (*growthSwarm, *kolodht.Node) {
	t.Helper()
	node, err := kolodht.New(context.Background(), "127.0.0.1:0", newIDs(t, 1)[0], &kolodht.Config{Server: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { node.Close() })
	cfg := &koloswarm.Config{
		Allow: func(key ed25519.PublicKey, _ netip.AddrPort) bool { return string(key) == string(paired) },
		DHT:   &kolodht.Config{Secure: &kolosecure.Config{Transport: &kolotransport.Config{MaxConnsPerIP: 16}}},
	}
	limitSession(cfg, defaultCongestion)
	return newGrowthSwarm(t, node, newIDs(t, 1)[0], cfg, koloswarm.JoinOptions{Announce: true}), node
}

// growthPeer starts a swarm for id that looks the topic up and has found hub, whose address its Allow refuses, so the swarm
// never dials hub and the test attaches the connection it makes.
func growthPeer(t *testing.T, node *kolodht.Node, hub *growthSwarm, id *koloid.Identity) *growthSwarm {
	t.Helper()
	hubAddr := hub.Addr().String()
	cfg := &koloswarm.Config{Allow: func(_ ed25519.PublicKey, addr netip.AddrPort) bool { return addr.String() != hubAddr }}
	p := newGrowthSwarm(t, node, id, cfg, koloswarm.JoinOptions{Lookup: true})
	poll(t, "the peer found the hub", func() bool { return p.found.Load() > 0 })
	return p
}

// reach attaches a connection from peer, made through a delayed link to hub's socket, and returns it with the connection
// hub holds for it once hub admitted it, or the error of Attach.
func reach(t *testing.T, hub, peer *growthSwarm) (mine, theirs koloswarm.Conn, err error) {
	t.Helper()
	link, lerr := netsim.New(hub.Addr().String(), growthHalf, growthHalf)
	if lerr != nil {
		t.Fatal(lerr)
	}
	t.Cleanup(func() { link.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, derr := kolosecure.Dial(ctx, link.Addr(), peer.id, hub.PublicKey(), nil)
	if derr != nil {
		t.Fatal(derr)
	}
	t.Cleanup(func() { c.Close() })
	if err := peer.Attach(ctx, c, true); err != nil {
		return nil, nil, err
	}
	poll(t, "hub admitted the peer", func() bool { return hub.peer(peer.id.PublicKey()) != nil })
	return c, hub.peer(peer.id.PublicKey()).Conn(), nil
}

// pump sends size bytes from sender on a new stream and has receiver read them all at once, the traffic that makes a
// trusted connection's windows grow.
func pump(t *testing.T, sender, receiver koloswarm.Conn, size int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st, err := sender.OpenStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 64<<10)
		for left := size; left > 0; left -= len(buf) {
			if _, err := st.Write(buf[:min(len(buf), left)]); err != nil {
				return
			}
		}
		st.Close()
	}()
	rs, err := receiver.AcceptStream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := io.Copy(io.Discard, rs); err != nil || int(n) != size {
		t.Fatalf("read %d of %d bytes: %v", n, size, err)
	}
}

// probe writes on six streams at once, each with its own credit, until the peer's connection window blocks them, and
// returns what they took and the open streams.
func probe(t *testing.T, writer koloswarm.Conn) (int, []*kolotransport.Stream) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var streams []*kolotransport.Stream
	for range 6 {
		w, err := writer.OpenStream(ctx)
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, w)
	}
	var sent atomic.Int64
	var wg sync.WaitGroup
	deadline := time.Now().Add(time.Second)
	for _, w := range streams {
		wg.Go(func() {
			w.SetWriteDeadline(deadline)
			n, err := w.Write(make([]byte, 1<<20))
			if err != os.ErrDeadlineExceeded {
				t.Errorf("write: %v", err)
			}
			sent.Add(int64(n))
		})
	}
	wg.Wait()
	return int(sent.Load()), streams
}

// growCredit pumps from sender to receiver, read as fast as it comes, and measures sender's credit after each round
// until it is above min or a minute has passed. A window never shrinks, so a slow round only costs another round.
func growCredit(t *testing.T, sender, receiver koloswarm.Conn, min int) int {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		pump(t, sender, receiver, 8<<20)
		n, streams := probe(t, sender)
		for _, w := range streams {
			w.Close()
		}
		if n > min || !time.Now().Before(deadline) {
			return n
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		for range streams {
			rs, err := receiver.AcceptStream(ctx)
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			io.Copy(io.Discard, rs)
		}
		cancel()
	}
}

// The session swarm grows the window of the connection of the paired peer under fast reading, past what the default
// windows allow, and refuses a key that is not paired, so no connection of a stranger exists to grow.
func TestSessionSwarmGrowsOnlyForThePairedPeer(t *testing.T) {
	ids := newIDs(t, 2)
	hub, node := growthHub(t, ids[0].PublicKey())
	paired, stranger := growthPeer(t, node, hub, ids[0]), growthPeer(t, node, hub, ids[1])
	if _, _, err := reach(t, hub, stranger); err == nil {
		t.Fatal("the swarm admitted a key that is not paired")
	}
	if hub.peer(ids[1].PublicKey()) != nil {
		t.Fatal("a key that is not paired is a peer")
	}
	mine, theirs, err := reach(t, hub, paired)
	if err != nil {
		t.Fatal(err)
	}
	if n := growCredit(t, mine, theirs, 1<<20); n <= 1<<20 {
		t.Fatalf("the connection of the paired peer took only %d bytes: its window did not grow", n)
	}
}

// spy is a connection that counts AllowWindowGrowth calls, which kolo-swarm makes for a connection Allow admitted.
type spy struct {
	koloswarm.Conn
	marked atomic.Int64
}

func (s *spy) AllowWindowGrowth() bool { s.marked.Add(1); return true }

// Only the paired key is marked for growth.
func TestSessionSwarmMarksOnlyThePairedPeer(t *testing.T) {
	ids := newIDs(t, 3)
	hub, node := growthHub(t, ids[0].PublicKey())
	attach := func(id *koloid.Identity) (*spy, error) {
		peer := growthPeer(t, node, hub, id)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		l, err := kolosecure.Listen(ctx, "127.0.0.1:0", hub.id, &kolosecure.Config{Transport: &kolotransport.Config{}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { l.Close() })
		accepted := make(chan *kolosecure.Conn, 1)
		go func() {
			c, err := l.Accept(ctx)
			if err != nil {
				close(accepted)
				return
			}
			accepted <- c
		}()
		ca, err := kolosecure.Dial(ctx, l.Addr().String(), id, hub.PublicKey(), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ca.Close() })
		cb, ok := <-accepted
		if !ok {
			t.Fatal("no connection accepted")
		}
		sp := &spy{Conn: cb}
		errc := make(chan error, 1)
		go func() { errc <- peer.Attach(ctx, ca, true) }()
		err = hub.Attach(ctx, sp, false)
		<-errc
		return sp, err
	}
	if sp, err := attach(ids[0]); err != nil || sp.marked.Load() != 1 {
		t.Fatalf("paired key: Attach %v, marked %d times, want nil and 1", err, sp.marked.Load())
	}
	if sp, err := attach(ids[1]); err == nil || sp.marked.Load() != 0 {
		t.Fatalf("key not paired: Attach %v, marked %d times, want an error and 0", err, sp.marked.Load())
	}
}

// The Allow of the session swarm admits the key of an open session and no other, so the swarm marks nothing else, and the
// config always has an Allow, since without one the swarm marks nothing and no window grows at all.
func TestSessionAllowAdmitsOnlyPairedKeys(t *testing.T) {
	ids := newIDs(t, 3)
	var cfg *koloswarm.Config
	alice, bob := networkWith(t, Config{}, Config{}, func(s *ShareService) {
		s.hooks.identity = ids[0]
		s.hooks.swarmCfg = func(c *koloswarm.Config) { cfg = c }
	}, func(s *ShareService) { s.hooks.identity = ids[1] })
	if cfg == nil || cfg.Allow == nil {
		t.Fatal("the session swarm has no Allow")
	}
	allow := func(id *koloid.Identity) bool { return cfg.Allow(id.PublicKey(), netip.AddrPort{}) }
	if allow(ids[1]) || allow(ids[2]) {
		t.Fatal("Allow admitted a key before any pairing")
	}
	code := showCode(t, alice)
	jb := pasteCode(t, bob, code.Code)
	if ja := alice.rec.waitPairing(t, code.ID, final); ja.State != pairConnected || jb.State != pairConnected {
		t.Fatalf("pairing: %+v / %+v", ja, jb)
	}
	if !allow(ids[1]) || allow(ids[2]) {
		t.Fatalf("after the pairing Allow admits bob %v and a stranger %v, want true and false", allow(ids[1]), allow(ids[2]))
	}
}
