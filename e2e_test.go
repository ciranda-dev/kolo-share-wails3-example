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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	kolodht "github.com/ciranda-dev/kolo-dht"
	koloid "github.com/ciranda-dev/kolo-id"
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
