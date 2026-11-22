package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	kolodht "github.com/ciranda-dev/kolo-dht"
	koloid "github.com/ciranda-dev/kolo-id"
	"github.com/ciranda-dev/kolo-share-wails3-example/internal/netsim"
)

var waitFor = 60 * time.Second

// recorder keeps what a ShareService tells the window.
type recorder struct {
	mu        sync.Mutex
	transfers map[string][]Transfer
	order     []string // transfer IDs in the order they first appeared
	network   []Network
	history   [][]HistoryItem
	pairings  []Pairing
	sessions  [][]Session
	all       []string
}

func (r *recorder) emit(name string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch v := data.(type) {
	case Transfer:
		if r.transfers[v.ID] == nil {
			r.order = append(r.order, v.ID)
		}
		r.transfers[v.ID] = append(r.transfers[v.ID], v)
	case Network:
		r.network = append(r.network, v)
	case []HistoryItem:
		r.history = append(r.history, v)
	case Pairing:
		r.pairings = append(r.pairings, v)
	case []Session:
		r.sessions = append(r.sessions, v)
	}
	if b, err := json.Marshal(data); err == nil {
		r.all = append(r.all, name+" "+string(b))
	}
}

func (r *recorder) states(id string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, t := range r.transfers[id] {
		if len(out) == 0 || out[len(out)-1] != t.State {
			out = append(out, t.State)
		}
	}
	return out
}

// wait polls until the transfer id is in one of states and returns it.
func (r *recorder) wait(t *testing.T, id string, states ...string) Transfer {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		ts := r.transfers[id]
		r.mu.Unlock()
		if len(ts) > 0 && slices.Contains(states, ts[len(ts)-1].State) {
			return ts[len(ts)-1]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("transfer %s never reached %v; states %v", id, states, r.states(id))
	return Transfer{}
}

// until polls until the last state of transfer id passes ok.
func (r *recorder) until(t *testing.T, id string, ok func(Transfer) bool) Transfer {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		ts := r.transfers[id]
		r.mu.Unlock()
		if len(ts) > 0 && ok(ts[len(ts)-1]) {
			return ts[len(ts)-1]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("transfer %s never got there; states %v", id, r.states(id))
	return Transfer{}
}

func (r *recorder) waitNetwork(t *testing.T, state string) {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		n := len(r.network)
		last := Network{}
		if n > 0 {
			last = r.network[n-1]
		}
		r.mu.Unlock()
		if last.State == state {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("network never %s: %+v", state, r.network)
}

// freeUDP returns a loopback UDP address that was free a moment ago, for the first entry node.
func freeUDP(t *testing.T) string {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().String()
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	return netip.MustParseAddrPort(freeUDP(t)).Port()
}

type peer struct {
	*ShareService
	rec  *recorder
	data string
}

// startPeer starts a service with its own data directory and an identity made in memory, on 127.0.0.1.
func startPeer(t *testing.T, cfg Config, setup func(*ShareService)) *peer {
	t.Helper()
	rec := &recorder{transfers: map[string][]Transfer{}}
	t.Setenv("HOME", t.TempDir()) // Info looks for the user's Downloads folder there
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.Listen = "127.0.0.1:0"
	if cfg.Local && cfg.source == nil {
		cfg.source = loopbackSource(freePort(t)) // never the app's default, which a running app may hold
	}
	if cfg.LookupInterval == 0 {
		cfg.LookupInterval = time.Second
	}
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	s := NewShareService(cfg, rec.emit, nil)
	s.hooks.identity = id
	if setup != nil {
		setup(s)
	}
	if set, err := cfg.resolve(); err == nil && set.mode == modePublic && s.hooks.netGate == nil {
		t.Fatal("this test would use the public network: pass Local, First or Bootstrap") // no test sends to a public address
	}
	if _, err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.ServiceShutdown() })
	return &peer{ShareService: s, rec: rec, data: cfg.DataDir}
}

// network starts the sender as the first entry node of a private loopback network and a receiver bootstrapped to it.
func network(t *testing.T, receiver Config, setup func(*ShareService)) (sender, recv *peer) {
	t.Helper()
	first := freeUDP(t)
	sender = startPeer(t, Config{First: first}, nil)
	sender.rec.waitNetwork(t, netReady)
	receiver.Bootstrap = first
	recv = startPeer(t, receiver, setup)
	recv.rec.waitNetwork(t, netReady)
	return sender, recv
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func assertEmpty(t *testing.T, dir string) {
	t.Helper()
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("%s holds %v (%v)", dir, entries, err)
	}
}

func TestSettingsAndNetwork(t *testing.T) {
	first := freeUDP(t)
	a := startPeer(t, Config{First: first}, nil)
	a.rec.waitNetwork(t, netReady)

	c := startPeer(t, Config{Local: true}, nil)
	c.rec.waitNetwork(t, netReady)
	if st := c.Settings(); st.EntryNodesFrom != "local" || st.Mode != modeLocal {
		t.Fatalf("settings without entry nodes: %+v", st)
	}
	if st, err := c.SetShowInNotch(false); err != nil || st.ShowInNotch {
		t.Fatalf("notch setting: %+v %v", st, err)
	}
	if st, err := c.SetDisplayName("  Ana\u202e  Lima "); err != nil || st.DisplayName != "Ana Lima" || st.PublicKey != shortKey(c.id.PublicKey()) || st.PairTTL != defaultPairTTL.Milliseconds() {
		t.Fatalf("name: %+v %v", st, err)
	}
	var se *ShareError
	if _, err := c.SetDisplayName(strings.Repeat("x", maxNameRunes+1)); !errors.As(err, &se) || se.Reason != "name-too-long" {
		t.Fatalf("long name: %v", err)
	}
	saved, err := loadSaved(c.data)
	if err != nil || saved.showInNotch() || saved.DisplayName != "Ana Lima" {
		t.Fatalf("saved file: %+v %v", saved, err)
	}
	if err := c.Reconnect(); err != nil {
		t.Fatal(err)
	}
	c.rec.waitNetwork(t, netReady)

	// Entry nodes in the settings file are ignored: the network comes from flags only.
	f := startPeer(t, Config{Local: true}, func(s *ShareService) {
		os.MkdirAll(s.cfg.DataDir, 0o700)
		os.WriteFile(filepath.Join(s.cfg.DataDir, settingsFile), []byte(`{"entryNodes":"`+first+`"}`), 0o600)
	})
	f.rec.waitNetwork(t, netReady)
	if st := f.Settings(); st.EntryNodes != "" || st.EntryNodesFrom != "local" {
		t.Fatalf("settings file: %+v", st)
	}

	b := startPeer(t, Config{Bootstrap: first}, nil)
	b.rec.waitNetwork(t, netReady)
	if st := b.Settings(); st.EntryNodes != first || st.EntryNodesFrom != "flag" || st.Mode != modeOwn || st.LocalNode != "" {
		t.Fatalf("entry nodes from the flag: %+v", st)
	}
	if info := b.Info(); info.Network.EntryNodes != 1 || info.DataDir != b.data || info.Version != version {
		t.Fatalf("info: %+v", info)
	}
	if fi, err := os.Stat(b.data); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("data directory: %v %v", fi.Mode(), err)
	}
	if _, err := os.Stat(filepath.Join(b.data, "identity.key")); !os.IsNotExist(err) {
		t.Fatalf("tests keep identities in memory: %v", err)
	}
}

// gated starts a peer whose network stays "starting" until the returned function is called.
func gated(t *testing.T, cfg Config) (*peer, func()) {
	t.Helper()
	gate := make(chan struct{})
	p := startPeer(t, cfg, func(s *ShareService) { s.hooks.netGate = gate })
	var once sync.Once
	return p, func() { once.Do(func() { close(gate) }) }
}

// TestNetworkSequence checks what lets the window keep the newest network state: Start returns a snapshot that
// can arrive after a newer event, so every state carries a number that only grows.
func TestNetworkSequence(t *testing.T) {
	first := freeUDP(t)
	p, open := gated(t, Config{First: first})
	snap := p.Info().Network
	if snap.State != netStarting || snap.Seq == 0 || snap.Since == 0 || snap.EntryNodes != 1 || snap.FirstNode != first {
		t.Fatalf("snapshot while starting: %+v", snap)
	}
	open()
	p.rec.waitNetwork(t, netReady)
	p.rec.mu.Lock()
	events := slices.Clone(p.rec.network)
	p.rec.mu.Unlock()
	for i := 1; i < len(events); i++ {
		if events[i].Seq <= events[i-1].Seq {
			t.Fatalf("sequence does not grow: %+v", events)
		}
	}
	last := events[len(events)-1]
	if last.Seq <= snap.Seq || !reflect.DeepEqual(p.Info().Network, last) {
		t.Fatalf("ready %+v, snapshot %+v, now %+v", last, snap, p.Info().Network)
	}
	if err := p.Reconnect(); err != nil {
		t.Fatal(err)
	}
	p.rec.waitNetwork(t, netReady)
	if again := p.Info().Network; again.Seq <= last.Seq {
		t.Fatalf("sequence after a restart: %+v then %+v", last, again)
	}
}

// sendOne connects two peers and checks that a small file goes from one to the other.
func sendOne(t *testing.T, from, to *peer) {
	t.Helper()
	sf, st := connect(t, from, to)
	src := filepath.Join(t.TempDir(), "note.txt")
	body := randomBytes(t, 4096)
	os.WriteFile(src, body, 0o600)
	sent := offerItem(t, from, sf, src)
	got := saveOffer(t, to, st, sent.Name, t.TempDir())
	if b, err := os.ReadFile(got.SavedTo); got.State != stateDone || err != nil || !bytes.Equal(b, body) {
		t.Fatalf("receive: %+v (%v)", got, err)
	}
	from.rec.wait(t, sent.ID, stateDelivered)
}

// TestPublicModeSettings holds the network at "starting" so nothing is sent: the public network is the default.
func TestPublicModeSettings(t *testing.T) {
	p, _ := gated(t, Config{})
	n, st := p.Info().Network, p.Settings()
	if n.Mode != modePublic || n.EntryNodes != len(kolodht.PublicServers()) || n.LocalNode != "" || n.FirstNode != "" {
		t.Fatalf("network: %+v", n)
	}
	if st.Mode != modePublic || st.EntryNodesFrom != "public" || st.EntryNodes != "" || st.LocalNode != "" || st.FirstNode != "" {
		t.Fatalf("settings: %+v", st)
	}
	b, err := json.Marshal(struct {
		Network
		Settings Settings
	}{n, st})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range kolodht.PublicServers() {
		if bytes.Contains(b, []byte(a.Addr().String())) {
			t.Fatalf("the payload lists the public entry node %s: %s", a, b)
		}
	}
}

func TestLocalModeFirstThenSecond(t *testing.T) {
	port := freePort(t)
	addr := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), port).String()
	alice := startPeer(t, Config{Local: true, source: loopbackSource(port)}, nil)
	alice.rec.waitNetwork(t, netReady)
	bob := startPeer(t, Config{Local: true, source: loopbackSource(port)}, nil)
	bob.rec.waitNetwork(t, netReady)
	if a, b := alice.Info().Network, bob.Info().Network; a.LocalNode != addr || a.FirstNode != addr || b.LocalNode != addr || b.FirstNode != "" || b.EntryNodes != 1 || a.Mode != modeLocal || b.Mode != modeLocal {
		t.Fatalf("alice %+v, bob %+v", a, b)
	}
	if st := bob.Settings(); st.EntryNodesFrom != "local" || st.EntryNodes != "" || st.LocalNode != addr || st.FirstNode != "" {
		t.Fatalf("settings: %+v", st)
	}
	sendOne(t, alice, bob)
	sendOne(t, bob, alice)
}

func TestLocalModeAtOnce(t *testing.T) {
	port := freePort(t)
	alice, openAlice := gated(t, Config{Local: true, source: loopbackSource(port)})
	bob, openBob := gated(t, Config{Local: true, source: loopbackSource(port)})
	openAlice()
	openBob()
	alice.rec.waitNetwork(t, netReady)
	bob.rec.waitNetwork(t, netReady)
	if a, b := alice.Info().Network.FirstNode != "", bob.Info().Network.FirstNode != ""; a == b {
		t.Fatalf("want exactly one entry node: alice %+v, bob %+v", alice.Info().Network, bob.Info().Network)
	}
	sendOne(t, alice, bob)
}

func TestLocalPortTakenByAnotherProgram(t *testing.T) {
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	port := netip.MustParseAddrPort(pc.LocalAddr().String()).Port()
	p := startPeer(t, Config{Local: true, source: loopbackSource(port)}, nil)
	p.rec.waitNetwork(t, netFailed)
	n := p.Info().Network
	if n.Error == nil || n.Error.Reason != "local-port-taken" || n.Error.Detail != pc.LocalAddr().String() || !strings.Contains(n.Message, "-bootstrap") || !strings.Contains(n.Message, "without -local") {
		t.Fatalf("network: %+v", n)
	}
}

// TestLocalModeShutdownOrder closes a window that only joined, then the one that runs the entry node: the
// others keep working, and Reconnect makes one of them the entry node.
func TestLocalModeShutdownOrder(t *testing.T) {
	port := freePort(t)
	alice := startPeer(t, Config{Local: true, source: loopbackSource(port)}, nil)
	alice.rec.waitNetwork(t, netReady)
	bob := startPeer(t, Config{Local: true, source: loopbackSource(port)}, nil)
	bob.rec.waitNetwork(t, netReady)
	carol := startPeer(t, Config{Local: true, source: loopbackSource(port)}, nil)
	carol.rec.waitNetwork(t, netReady)

	bob.ServiceShutdown()
	sendOne(t, alice, carol)

	alice.ServiceShutdown()
	if err := carol.Reconnect(); err != nil {
		t.Fatal(err)
	}
	carol.rec.waitNetwork(t, netReady)
	if n := carol.Info().Network; n.FirstNode == "" {
		t.Fatalf("carol did not take over the entry node: %+v", n)
	}
	dave := startPeer(t, Config{Local: true, source: loopbackSource(port)}, nil)
	dave.rec.waitNetwork(t, netReady)
	sendOne(t, carol, dave)
}

// last is the newest state of transfer id.
func (r *recorder) last(id string) Transfer {
	r.mu.Lock()
	defer r.mu.Unlock()
	ts := r.transfers[id]
	if len(ts) == 0 {
		return Transfer{}
	}
	return ts[len(ts)-1]
}

// pathSend pairs two apps through a netsim link with half in each direction, offers a file of size random bytes from
// alice and saves it on bob. It returns the IDs of the send and of the receive, the file and when Save was pressed.
func pathSend(t *testing.T, half netsim.Profile, size int) (alice, bob *peer, link *netsim.Link, sendID, recvID string, body []byte, saved time.Time) {
	t.Helper()
	alice, bob, sa, sb, link := linkedPair(t, half, modeApp)
	bob.SetSessionFolder(sb, t.TempDir())
	body = randomBytes(t, size)
	id, err := alice.Send(sa, writeFile(t, "path.bin", body))
	if err != nil {
		t.Fatal(err)
	}
	in := incoming(t, bob, sb, "path.bin")
	saved = time.Now()
	if err := bob.SaveOffer(in.ID); err != nil {
		t.Fatal(err)
	}
	return alice, bob, link, id, in.ID, body, saved
}

// savedBody waits for the receive id to end and returns what it saved.
func savedBody(t *testing.T, p *peer, id string) []byte {
	t.Helper()
	tr := p.rec.wait(t, id, stateDone, stateFailed, stateCancelled, stateStopped, stateWithdrawn)
	if tr.State != stateDone {
		t.Fatalf("receive: %+v (%+v)", tr, tr.Error)
	}
	b, err := os.ReadFile(tr.SavedTo)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A small item over a path of 470 ms round trip, the best round trip of a relayed transfer between two countries in
// the field, takes round trips, not bandwidth: the pull of its two logs needs about 10, so Save to done stays within 12.
func TestPathRoundTripsAt470ms(t *testing.T) {
	const rtt = 470 * time.Millisecond
	_, bob, link, _, in, body, saved := pathSend(t, netsim.Profile{Delay: rtt / 2}, 1<<10)
	got := savedBody(t, bob, in)
	took := time.Since(saved)
	if !bytes.Equal(got, body) {
		t.Fatal("the saved file differs")
	}
	if trips := took.Seconds() / rtt.Seconds(); trips > 12 {
		t.Fatalf("Save to done took %v, %.1f round trips; want at most 12", took, trips)
	}
	t.Logf("1 KiB: Save to done in %v (%.1f round trips), %d packets to bob", took.Round(time.Millisecond), took.Seconds()/rtt.Seconds(), link.Down().Packets)
	bob.rec.mu.Lock()
	defer bob.rec.mu.Unlock()
	for _, tr := range bob.rec.transfers[in] {
		if tr.Slow {
			t.Fatalf("a path of 470 ms with no rate limit was called slow: %+v", tr)
		}
	}
}

// The progress of a receive counts the bytes as they arrive, not the size of the store: on a path of 50 KB/s a
// receive shows tens of KiB within seconds, long before its first answer of about 1 MiB is whole, and is not slow.
func TestProgressMovesWithinAnAnswer(t *testing.T) {
	_, bob, _, _, in, _, saved := pathSend(t, netsim.Profile{Rate: 50_000, Delay: 50 * time.Millisecond}, 3<<20)
	tr := bob.rec.until(t, in, func(tr Transfer) bool { return tr.BytesDone >= 64<<10 || tr.final() })
	if took := time.Since(saved); took > 8*time.Second || tr.State != stateReceiving || tr.Stalled || tr.Slow {
		t.Fatalf("after %v: %+v; want 64 KiB received within 8 s, before the first answer of 1 MiB", took, tr)
	}
	if tr.BytesDone >= 1<<20 {
		t.Fatalf("progress %d already past the first answer", tr.BytesDone)
	}
	bob.CancelTransfer(in)
}

// On a path below the 8.5 KB/s at which an answer of about 1 MiB used to be cut off, a receive moves, says the path is
// slow with the speed it measures, and finishes. The item is 200 KiB, one entry, over 2 KB/s: the old floor of 16 KiB/s
// cut it off after 72 s, the item takes about 2 minutes.
func TestSlowPathCompletes(t *testing.T) {
	if testing.Short() {
		t.Skip("about 2 minutes")
	}
	old := waitFor
	waitFor = 5 * time.Minute
	t.Cleanup(func() { waitFor = old })
	_, bob, _, _, in, body, saved := pathSend(t, netsim.Profile{Rate: 2_000, Delay: 50 * time.Millisecond}, 200<<10)
	tr := bob.rec.until(t, in, func(tr Transfer) bool { return (tr.Slow && tr.BytesDone >= 32<<10) || tr.final() })
	if tr.State != stateReceiving || tr.Stalled || tr.BytesPerSecond < 500 || tr.BytesPerSecond > 4_000 {
		t.Fatalf("after %v: %+v; want a slow receive at about 2 KB/s", time.Since(saved), tr)
	}
	if got := savedBody(t, bob, in); !bytes.Equal(got, body) {
		t.Fatal("the saved file differs")
	}
	t.Logf("200 KiB at 2 KB/s in %v", time.Since(saved).Round(time.Second))
}

// A path whose packets are capped below the 1500 bytes of Ethernet still carries a transfer from the first packet.
// KOLO_SHARE_MTU_DROP=1 also runs a cap that appears after the connection found a larger size, as on a path that
// changes: the transport must see the loss of its larger packets and fall back to 1200 bytes, which kolo-transport
// v0.1.0 does not do (its CHANGELOG has the fix under Unreleased).
func TestPacketSizeCaps(t *testing.T) {
	if testing.Short() {
		t.Skip("about a minute")
	}
	t.Run("1240 from the start", func(t *testing.T) {
		_, bob, link, _, in, body, _ := pathSend(t, netsim.Profile{Delay: 20 * time.Millisecond, MTU: 1240}, 3<<20)
		if got := savedBody(t, bob, in); !bytes.Equal(got, body) {
			t.Fatal("the saved file differs")
		}
		if link.Down().DroppedMTU == 0 {
			t.Fatal("no packet was over the cap: the test does not test it")
		}
	})
	t.Run("1280 after a large transfer", func(t *testing.T) {
		if os.Getenv("KOLO_SHARE_MTU_DROP") != "1" {
			t.Skip("needs a kolo-transport that lowers its packet size after a black hole (newer than v0.1.0); set KOLO_SHARE_MTU_DROP=1")
		}
		alice, bob, sa, sb, link := linkedPair(t, netsim.Profile{Delay: 20 * time.Millisecond}, modeApp)
		dest := t.TempDir()
		for i, cap := range []int{0, 1280} {
			link.SetMTU(cap)
			name := "big" + strconv.Itoa(i) + ".bin"
			body := randomBytes(t, 3<<20)
			offerItem(t, alice, sa, writeFile(t, name, body))
			got := saveOffer(t, bob, sb, name, dest)
			if b, err := os.ReadFile(got.SavedTo); got.State != stateDone || err != nil || !bytes.Equal(b, body) {
				t.Fatalf("cap %d: %+v (%v)", cap, got, err)
			}
		}
		if link.Down().DroppedMTU == 0 {
			t.Fatal("no packet was over the cap: the connection never used larger packets")
		}
	})
}

// Cancel ends a transfer at once on a slow path and on a path that carries nothing, from either card; a call that
// does not end it fails the test with the stack of every goroutine, to show where it waits.
func TestCancelOnStuckPaths(t *testing.T) {
	for _, tc := range []struct {
		name string
		half netsim.Profile
		cut  bool
		who  string
	}{
		{"slow path, receiver cancels", netsim.Profile{Rate: 4_000, Delay: 100 * time.Millisecond}, false, "bob"},
		{"dead path, sender cancels", netsim.Profile{Rate: 50_000, Delay: 100 * time.Millisecond}, true, "alice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alice, bob, link, sendID, in, _, _ := pathSend(t, tc.half, 4<<20)
			bob.rec.until(t, in, func(tr Transfer) bool { return tr.BytesDone >= 16<<10 || tr.final() })
			if tc.cut {
				link.Cut(time.Hour)
				time.Sleep(3 * time.Second)
			}
			p, id, other, otherID := bob, in, alice, sendID
			if tc.who == "alice" {
				p, id, other, otherID = alice, sendID, bob, in
			}
			start := time.Now()
			if err := p.CancelTransfer(id); err != nil {
				t.Fatal(err)
			}
			for !p.rec.last(id).final() {
				if time.Since(start) > 2*time.Second {
					buf := make([]byte, 8<<20)
					t.Fatalf("Cancel did not end the transfer in 2 s: %+v\n%s", p.rec.last(id), buf[:runtime.Stack(buf, true)])
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !tc.cut {
				other.rec.until(t, otherID, func(tr Transfer) bool { return tr.final() })
			}
		})
	}
}
