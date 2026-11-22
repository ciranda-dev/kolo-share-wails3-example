package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	kolodht "github.com/ciranda-dev/kolo-dht"
	koloid "github.com/ciranda-dev/kolo-id"
	kololog "github.com/ciranda-dev/kolo-log"
	kolorelay "github.com/ciranda-dev/kolo-relay"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	kolostore "github.com/ciranda-dev/kolo-store"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
	kolosync "github.com/ciranda-dev/kolo-sync"
	kolotransport "github.com/ciranda-dev/kolo-transport"
)

// relayEntry is one connection relay in the ParseRelays form, with a fresh key, at addr.
func relayEntry(t *testing.T, addr string) (string, ed25519.PublicKey) {
	t.Helper()
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return addr + "@" + hex.EncodeToString(id.PublicKey()), id.PublicKey()
}

func TestRelaySettingSources(t *testing.T) {
	a, ka := relayEntry(t, "127.0.0.1:7001")
	b, _ := relayEntry(t, "127.0.0.1:7002")
	c, _ := relayEntry(t, "relay.example.org:7003")
	file := filepath.Join(t.TempDir(), "relays.txt")
	if err := os.WriteFile(file, []byte("# relays\n"+c+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolve := func(args []string, vars map[string]string) (settings, error) {
		t.Helper()
		cfg, err := LoadConfig(args, env(vars), io.Discard)
		if err != nil {
			return settings{}, err
		}
		cfg.DataDir = t.TempDir()
		return cfg.resolve()
	}
	s, err := resolve([]string{"-relay", a, "-relay", b + " , " + a, "-relay-file", file}, map[string]string{"KOLO_SHARE_RELAYS": b})
	if err != nil || len(s.relays) != 3 || s.relaysFrom != "flag-and-file" || s.relaysText != a+","+b+","+c || !bytes.Equal(s.relays[0].PublicKey, ka) {
		t.Fatalf("flags: %+v %q %v", s.relays, s.relaysText, err)
	}
	if s, err = resolve([]string{"-relay-file", file}, map[string]string{"KOLO_SHARE_RELAYS": a}); err != nil || s.relaysText != c || s.relaysFrom != "file" {
		t.Errorf("a file alone keeps the variable out: %q %q %v", s.relaysText, s.relaysFrom, err)
	}
	if s, err = resolve([]string{"-relay", a}, nil); err != nil || s.relaysText != a || s.relaysFrom != "flag" {
		t.Errorf("a flag alone: %q %q %v", s.relaysText, s.relaysFrom, err)
	}
	if s, err = resolve(nil, map[string]string{"KOLO_SHARE_RELAYS": b}); err != nil || s.relaysText != b || s.relaysFrom != "env" {
		t.Errorf("variable: %q %q %v", s.relaysText, s.relaysFrom, err)
	}
	if s, err = resolve(nil, nil); err != nil || len(s.relays) != 0 || s.relaysFrom != "none" {
		t.Errorf("none: %+v %q %v", s.relays, s.relaysFrom, err)
	}
	if s, err = (Config{DataDir: t.TempDir(), Relays: []string{a}}).resolve(); err != nil || s.relaysText != a || s.relaysFrom != "flag" {
		t.Errorf("set in code: %q %q %v", s.relaysText, s.relaysFrom, err)
	}
	var seventeen []string
	for i := range 17 {
		e, _ := relayEntry(t, fmt.Sprintf("127.0.0.1:%d", 8000+i))
		seventeen = append(seventeen, e)
	}
	if s, err = resolve([]string{"-relay", strings.Join(seventeen[:16], ",")}, nil); err != nil || len(s.relays) != 16 {
		t.Errorf("16 relays: %d %v", len(s.relays), err)
	}
	other, _ := relayEntry(t, "127.0.0.1:7001")
	dir := t.TempDir()
	zero, big := filepath.Join(dir, "zero"), filepath.Join(dir, "big")
	if err := os.Symlink("/dev/zero", zero); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(big, bytes.Repeat([]byte("#"), 64<<10+1), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"17 relays":            {"-relay", strings.Join(seventeen, ",")},
		"one key, two addrs":   {"-relay", a, "-relay", strings.Replace(a, "7001", "7009", 1)},
		"one addr, two keys":   {"-relay", a + "," + other},
		"IPv6":                 {"-relay", "[::1]:7001@" + hex.EncodeToString(ka)},
		"IPv4-mapped":          {"-relay", "[::ffff:127.0.0.1]:7001@" + hex.EncodeToString(ka)},
		"port 0":               {"-relay", "127.0.0.1:0@" + hex.EncodeToString(ka)},
		"short key":            {"-relay", "127.0.0.1:7001@abcd"},
		"no key":               {"-relay", "127.0.0.1:7001"},
		"a link to /dev/zero":  {"-relay-file", zero},
		"a directory":          {"-relay-file", dir},
		"64 KiB and 1 byte":    {"-relay-file", big},
		"flag and file clash":  {"-relay", strings.Replace(c, "relay.example.org", "127.0.0.9", 1), "-relay-file", file},
		"file over the others": {"-relay-file", file, "-relay", strings.Join(seventeen[:16], ",")},
	} {
		_, err := resolve(args, nil)
		var ce *ConfigError
		if !errors.As(err, &ce) || (ce.Setting != "relay" && ce.Setting != "relay-file") || !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err = resolve([]string{"-relay-file", filepath.Join(dir, "none")}, nil)
	var ce *ConfigError
	if !errors.As(err, &ce) || ce.Setting != "relay-file" || !errors.Is(err, ErrUnreadable) || !errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrMalformed) {
		t.Errorf("a missing file: %v", err)
	}
	if _, err := resolve(nil, map[string]string{"KOLO_SHARE_RELAYS": "nope"}); err == nil || !strings.Contains(err.Error(), "KOLO_SHARE_RELAYS") {
		t.Errorf("a bad variable does not say where it came from: %v", err)
	}
}

func TestFindTimeoutWithRelays(t *testing.T) {
	a, _ := relayEntry(t, "127.0.0.1:7001")
	for _, c := range []struct {
		cfg  Config
		want time.Duration
	}{
		{Config{}, defaultFindTimeout},
		{Config{Relays: []string{a}}, 72 * time.Second},
		{Config{Relays: []string{a}, FindTimeout: 9 * time.Second}, 9 * time.Second},
	} {
		c.cfg.DataDir = t.TempDir()
		s, err := c.cfg.resolve()
		if err != nil || s.FindTimeout != c.want {
			t.Errorf("%+v: %v %v", c.cfg, s.FindTimeout, err)
		}
	}
}

func TestHelpNamesTheRelayFlags(t *testing.T) {
	var out bytes.Buffer
	if _, err := LoadConfig([]string{"-h"}, env(nil), &out); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("err: %v", err)
	}
	for _, want := range []string{"-relay value", "-relay-file value", "KOLO_SHARE_RELAYS", "default 1m, 72s with connection relays"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("help has no %q: %s", want, out.String())
		}
	}
}

// startConnRelay runs a kolo-relay on 127.0.0.1 and returns it as a -relay entry. Every client of a test is
// 127.0.0.1, so the per-address limit is raised.
func startConnRelay(t *testing.T) (entry, addr string, key ed25519.PublicKey) {
	t.Helper()
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := kolorelay.Listen(context.Background(), "127.0.0.1:0", id, &kolorelay.ServerConfig{Secure: loopSecure()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	addr = srv.Addr().String()
	return addr + "@" + hex.EncodeToString(id.PublicKey()), addr, id.PublicKey()
}

func loopSecure() *kolosecure.Config {
	return &kolosecure.Config{Transport: &kolotransport.Config{MaxConns: 256, MaxConnsPerIP: 256, MaxDialed: 256}}
}

// tapRelay stands where a relay operator stands. Apps list it as their relay: it holds the relay key the apps
// check, and passes every stream on to a real kolo-relay as the same app, so it keeps every byte a relay
// operator can read. It can stand in only for the identities it is given.
type tapRelay struct {
	ids   map[string]*koloid.Identity
	wg    sync.WaitGroup
	key   ed25519.PublicKey
	entry string
	to    string
	toKey ed25519.PublicKey
	mu    sync.Mutex
	seen  []byte
}

func startTapRelay(t *testing.T, ids ...*koloid.Identity) *tapRelay {
	t.Helper()
	_, to, toKey := startConnRelay(t)
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := kolosecure.Listen(ctx, "127.0.0.1:0", id, loopSecure())
	if err != nil {
		t.Fatal(err)
	}
	tap := &tapRelay{ids: map[string]*koloid.Identity{}, key: id.PublicKey(), to: to, toKey: toKey,
		entry: ln.Addr().String() + "@" + hex.EncodeToString(id.PublicKey())}
	for _, x := range ids {
		tap.ids[string(x.PublicKey())] = x
	}
	tap.wg.Go(func() {
		for {
			c, err := ln.Accept(ctx)
			if err != nil {
				return
			}
			tap.wg.Go(func() { tap.serve(ctx, c) })
		}
	})
	t.Cleanup(func() {
		cancel()
		ln.Close()
		tap.wg.Wait()
	})
	return tap
}

func (tap *tapRelay) serve(ctx context.Context, c *kolosecure.Conn) {
	defer c.Close()
	id := tap.ids[string(c.RemotePublicKey())]
	if id == nil {
		return
	}
	rc, err := kolosecure.Dial(ctx, tap.to, id, tap.toKey, &kolosecure.Config{Transport: &kolotransport.Config{KeepAlivePeriod: 5 * time.Second}})
	if err != nil {
		return
	}
	defer rc.Close()
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	context.AfterFunc(c.Context(), cancel)
	context.AfterFunc(rc.Context(), cancel)
	var wg sync.WaitGroup
	wg.Go(func() { tap.join(cctx, c, rc) })
	wg.Go(func() { tap.join(cctx, rc, c) })
	<-cctx.Done()
	c.Close()
	rc.Close()
	wg.Wait()
}

// join passes every stream that from opens to a new stream of to, both ways.
func (tap *tapRelay) join(ctx context.Context, from, to *kolosecure.Conn) {
	for {
		a, err := from.AcceptStream(ctx)
		if err != nil {
			return
		}
		b, err := to.OpenStream(ctx)
		if err != nil {
			a.CancelRead(0)
			a.CancelWrite(0)
			return
		}
		tap.wg.Go(func() { tap.pipe(b, a) })
		tap.wg.Go(func() { tap.pipe(a, b) })
	}
}

func (tap *tapRelay) pipe(dst, src *kolotransport.Stream) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			tap.mu.Lock()
			tap.seen = append(tap.seen, buf[:n]...)
			tap.mu.Unlock()
			if _, werr := dst.Write(buf[:n]); werr != nil {
				src.CancelRead(0)
				return
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				dst.Close()
			} else {
				dst.CancelWrite(0)
			}
			return
		}
	}
}

func (tap *tapRelay) captured() []byte {
	tap.mu.Lock()
	defer tap.mu.Unlock()
	return bytes.Clone(tap.seen)
}

// wall is a test firewall on every swarm of the apps it is set on, as a strict NAT: it refuses connections from
// the listed addresses, or from everyone. A relayed connection is not refused: the app dialed the relay.
type wall struct {
	mu      sync.Mutex
	all     bool
	refused map[netip.AddrPort]bool
}

func (w *wall) set(s *ShareService) {
	s.hooks.dht = &kolodht.Config{Secure: &kolosecure.Config{Transport: &kolotransport.Config{AcceptFunc: func(a netip.AddrPort) bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return !w.all && !w.refused[a]
	}}}}
}

// refuseSessions refuses direct connections between the session swarms of ps; pairing swarms still connect.
func (w *wall) refuseSessions(t *testing.T, ps ...*peer) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.refused == nil {
		w.refused = map[netip.AddrPort]bool{}
	}
	for _, p := range ps {
		p.mu.Lock()
		sw := p.sw
		p.mu.Unlock()
		a, err := netip.ParseAddrPort(sw.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		w.refused[a] = true
	}
}

// relayPeers starts two apps that list relays, behind w, on a private loopback network.
func relayPeers(t *testing.T, w *wall, relays []string, ids ...*koloid.Identity) (a, b *peer) {
	t.Helper()
	first := freeUDP(t)
	setup := func(id *koloid.Identity) func(*ShareService) {
		return func(s *ShareService) {
			w.set(s)
			s.hooks.identity = id
		}
	}
	a = startPeer(t, Config{First: first, Relays: relays}, setup(ids[0]))
	a.rec.waitNetwork(t, netReady)
	b = startPeer(t, Config{Bootstrap: first, Relays: relays}, setup(ids[1]))
	b.rec.waitNetwork(t, netReady)
	return a, b
}

func newIDs(t *testing.T, n int) []*koloid.Identity {
	t.Helper()
	var out []*koloid.Identity
	for range n {
		id, err := koloid.Generate()
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// relayOf returns the key of the relay that carries session sid of p, nil for a direct connection.
func relayOf(t *testing.T, p *peer, sid string) ed25519.PublicKey {
	t.Helper()
	if rc, ok := p.link(t, sid).conn.(*kolorelay.Conn); ok {
		return rc.RelayPublicKey()
	}
	return nil
}

// sendThrough sends a file of marker lines from a to b in their session and checks that it arrives intact.
func sendThrough(t *testing.T, a, b *peer, sa, sb string, marker []byte) []byte {
	t.Helper()
	body := bytes.Repeat(marker, (1<<20)/len(marker))
	sent := offerItem(t, a, sa, writeFile(t, "marked.txt", body))
	got := saveOffer(t, b, sb, "marked.txt", t.TempDir())
	saved, err := os.ReadFile(got.SavedTo)
	if got.State != stateDone || err != nil || sha256.Sum256(saved) != sha256.Sum256(body) {
		t.Fatalf("receive through the relay: %+v (%v)", got, err)
	}
	a.rec.wait(t, sent.ID, stateDelivered)
	return body
}

// keepKeys makes every pull keep the entry keys it received, the keys that open the file's entries.
func keepKeys(t *testing.T) func() []kololog.EntryKey {
	var mu sync.Mutex
	var keys []kololog.EntryKey
	old := pull
	t.Cleanup(func() { pull = old })
	pull = func(ctx context.Context, c kolosync.Conn, r *kolostore.Replica, m *kolosync.PullMeter) error {
		err := old(ctx, c, r, m)
		for i := range r.Len() {
			if k, kerr := r.Key(i); kerr == nil {
				mu.Lock()
				keys = append(keys, bytes.Clone(k))
				mu.Unlock()
			}
		}
		return err
	}
	return func() []kololog.EntryKey {
		mu.Lock()
		defer mu.Unlock()
		return keys
	}
}

// Two apps whose session swarms cannot connect directly get their session through the relay both list, and a
// file arrives intact. The relay operator's view holds the file's bytes, encrypted end to end: neither the
// file's text nor a key of its entries.
func TestSessionFileThroughRelay(t *testing.T) {
	keys := keepKeys(t)
	ids := newIDs(t, 2)
	tap := startTapRelay(t, ids...)
	w := &wall{}
	alice, bob := relayPeers(t, w, []string{tap.entry}, ids...)
	w.refuseSessions(t, alice, bob)
	sa, sb := connect(t, alice, bob)
	if k := relayOf(t, alice, sa); !k.Equal(tap.key) {
		t.Fatalf("alice's session is not relayed by the tap: %x", k)
	}
	if k := relayOf(t, bob, sb); !k.Equal(tap.key) {
		t.Fatalf("bob's session is not relayed by the tap: %x", k)
	}
	marker := []byte("kolo-share relay marker: this text must stay end to end.\n")
	body := sendThrough(t, alice, bob, sa, sb, marker)

	seen := tap.captured()
	if len(seen) < len(body) {
		t.Fatalf("the relay passed %d bytes, less than the %d of the file", len(seen), len(body))
	}
	if bytes.Contains(seen, marker[:16]) {
		t.Fatal("the relay saw the file's text")
	}
	ks := keys()
	if len(ks) == 0 {
		t.Fatal("no entry keys were pulled")
	}
	for _, k := range ks {
		if len(k) > 0 && bytes.Contains(seen, k) {
			t.Fatalf("the relay saw an entry key")
		}
	}
}

// With every direct connection refused, pasting a pairing code finds the other app through the relay, and the
// session it opens moves a file intact through the same relay.
func TestPairingAndFileThroughRelay(t *testing.T) {
	entry, _, rkey := startConnRelay(t)
	w := &wall{all: true}
	alice, bob := relayPeers(t, w, []string{entry}, newIDs(t, 2)...)
	if st := alice.Settings(); st.ConnectionRelays != entry || st.ConnectionRelaysFrom != "flag" {
		t.Fatalf("settings: %q %q", st.ConnectionRelays, st.ConnectionRelaysFrom)
	}
	sa, sb := connect(t, alice, bob)
	if k := relayOf(t, bob, sb); !k.Equal(rkey) {
		t.Fatalf("bob's session is not relayed: %x", k)
	}
	sendThrough(t, bob, alice, sb, sa, []byte("from bob, through the relay\n"))
	alice.rec.waitRelays(t, "the session's place at the relay", func(rs []RelayStatus) bool {
		return len(rs) == 1 && rs[0].State == relayReserved
	})
	if err := alice.CloseSession(sa); err != nil {
		t.Fatal(err)
	}
	alice.rec.waitRelays(t, "no place once the last session is closed", func(rs []RelayStatus) bool {
		return len(rs) == 1 && rs[0].State == relayIdle && rs[0].Error == nil
	})
}

// waitRelays waits until the relays of the last network event pass ok.
func (r *recorder) waitRelays(t *testing.T, what string, ok func([]RelayStatus) bool) {
	t.Helper()
	poll(t, what, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.network) > 0 && ok(r.network[len(r.network)-1].Relays)
	})
}

// The window sees each relay's state: a code that waits holds a place at a good relay, a relay that answers with
// another key is named with its reason, and the error is logged once. Without a code or a session, none is used.
func TestRelayStateInNetwork(t *testing.T) {
	good, _, _ := startConnRelay(t)
	_, addr, _ := startConnRelay(t)
	wrong, _ := relayEntry(t, addr)
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	p := startPeer(t, Config{First: freeUDP(t), Relays: []string{good, wrong}}, nil)
	p.rec.waitNetwork(t, netReady)
	if rs := p.Info().Network.Relays; len(rs) != 2 || rs[0].State != relayIdle || rs[1].State != relayIdle || rs[1].Addr != addr {
		t.Fatalf("before a code: %+v", rs)
	}
	code := showCode(t, p)
	p.rec.waitRelays(t, "a place at the good relay, none at the wrong one", func(rs []RelayStatus) bool {
		return len(rs) == 2 && rs[0].State == relayReserved && rs[1].State == relayNotReserved &&
			rs[1].Error != nil && rs[1].Error.Reason == "relay-wrong-key" && rs[1].Error.Detail == addr
	})
	p.CancelPairing(code.ID)
	p.rec.waitRelays(t, "no place once the code ends", func(rs []RelayStatus) bool {
		return len(rs) == 2 && rs[0].State == relayIdle && rs[1].State == relayIdle
	})
	if n := strings.Count(logged.String(), "relay "+addr+": The relay at "+addr+" answered with another key"); n != 1 {
		t.Fatalf("logged %d times: %s", n, logged.String())
	}
}

// The wall really refuses direct connections: without a relay, the code is never found.
func TestWallWithoutRelayNeverPairs(t *testing.T) {
	w := &wall{all: true}
	first := freeUDP(t)
	alice := startPeer(t, Config{First: first}, w.set)
	alice.rec.waitNetwork(t, netReady)
	bob := startPeer(t, Config{Bootstrap: first, FindTimeout: 3 * time.Second}, w.set)
	bob.rec.waitNetwork(t, netReady)
	code := showCode(t, alice)
	if got := pasteCode(t, bob, code.Code); got.State != pairFailed || got.Error == nil || got.Error.Reason != "pair-not-found" {
		t.Fatalf("pairing behind the wall: %+v (%+v)", got, got.Error)
	}
	if b, err := json.Marshal(alice.Info().Network); err != nil || !strings.Contains(string(b), `"relays":[]`) {
		t.Fatalf("no relays must be a list: %s %v", b, err)
	}
}

// A key without a session cannot reach an app through its relay, and the session stays with its peer.
func TestStrangerRefusedThroughRelay(t *testing.T) {
	entry, addr, rkey := startConnRelay(t)
	w := &wall{all: true}
	alice, bob := relayPeers(t, w, []string{entry}, newIDs(t, 2)...)
	sa, _ := connect(t, alice, bob)
	stranger, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), waitFor)
	defer cancel()
	c, err := kolorelay.Dial(ctx, addr, rkey, stranger, alice.id.PublicKey(), nil)
	if err == nil {
		c.Close()
	}
	if !errors.Is(err, kolorelay.ErrUnavailable) {
		t.Fatalf("a stranger through the relay: %v", err)
	}
	if got := alice.link(t, sa).conn.RemotePublicKey(); !got.Equal(bob.id.PublicKey()) {
		t.Fatalf("the session's peer is %x", got)
	}
	if n := len(alice.Sessions()); n != 1 {
		t.Fatalf("%d sessions", n)
	}
}

// relayService is a service that lists relays, without a network, and counts its network events.
func relayService(t *testing.T, relays ...string) (*ShareService, *int) {
	t.Helper()
	events := new(int)
	s := NewShareService(Config{}, func(name string, _ any) {
		if name == "share:network" {
			*events++
		}
	}, nil)
	var err error
	if s.set, err = (Config{DataDir: t.TempDir(), Relays: relays}).resolve(); err != nil {
		t.Fatal(err)
	}
	return s, events
}

// logTo sends the log to a buffer for the rest of the test.
func logTo(t *testing.T) *bytes.Buffer {
	var b bytes.Buffer
	log.SetOutput(&b)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &b
}

func relayEvent(key ed25519.PublicKey, err error) koloswarm.Event {
	return koloswarm.Event{Kind: koloswarm.RelayChanged, PublicKey: key, Err: err}
}

// An error is logged the first time a relay gives it, not each time the swarm says it again, and every reason has
// its sentence.
func TestRelayErrorsLoggedOnce(t *testing.T) {
	a, ka := relayEntry(t, "127.0.0.1:7001")
	s, _ := relayService(t, a)
	logged := logTo(t)
	for _, e := range []error{kolorelay.ErrLimit, kolorelay.ErrLimit, nil, kolorelay.ErrLimit, kolorelay.ErrNotAllowed, kolorelay.ErrNotAllowed} {
		s.relayChanged(swarmSessions, relayEvent(ka, e))
	}
	full, denied := "relay 127.0.0.1:7001: The relay 127.0.0.1:7001 is full.", "relay 127.0.0.1:7001: The relay 127.0.0.1:7001 does not let"
	if n, m := strings.Count(logged.String(), full), strings.Count(logged.String(), denied); n != 1 || m != 1 {
		t.Fatalf("logged %d and %d times: %s", n, m, logged.String())
	}
	for err, reason := range map[error]string{
		kolorelay.ErrNotAllowed: "relay-not-allowed", kolorelay.ErrLimit: "relay-full",
		kolosecure.ErrUnexpectedPeer: "relay-wrong-key", koloswarm.ErrInvalidConfig: "relay-bad-key", koloswarm.ErrLost: "relay-unreachable",
	} {
		if se := relayError(err, "r:1", false); se.Reason != reason || se.Detail != "r:1" {
			t.Errorf("%v: %+v", err, se)
		}
	}
	if se := relayError(kolorelay.ErrNotAllowed, "r:1", true); se.Reason != "relay-pairing-not-allowed" {
		t.Errorf("a pairing's refusal: %+v", se)
	}
}

// A place that ends with the relay's lifetime is taken again by kolo-swarm: no log, no "no place", no event.
func TestRelayPlaceRenewalIsQuiet(t *testing.T) {
	a, ka := relayEntry(t, "127.0.0.1:7001")
	s, events := relayService(t, a)
	logged := logTo(t)
	for _, e := range []error{nil, kolorelay.ErrReservationEnded, nil, kolorelay.ErrReservationEnded, nil} {
		s.relayChanged(swarmSessions, relayEvent(ka, e))
		if rs := s.net.Relays; len(rs) != 1 || rs[0].State != relayReserved {
			t.Fatalf("after %v: %+v", e, rs)
		}
	}
	if *events != 1 || logged.Len() != 0 {
		t.Fatalf("%d events, log %q", *events, logged.String())
	}
}

// Each waiting code has its own states: a code whose swarm ends forgets only its own. A code that cannot wait at a
// relay does not hide the place the sessions hold there.
func TestPairingRelayStates(t *testing.T) {
	a, ka := relayEntry(t, "127.0.0.1:7001")
	s, _ := relayService(t, a)
	logTo(t)
	state := func() RelayStatus { return s.net.Relays[0] }
	s.relayChanged(swarmPairing+"1", relayEvent(ka, nil))
	s.relayChanged(swarmPairing+"2", relayEvent(ka, nil))
	s.forgetRelays(swarmPairing + "1")
	if st := state(); st.State != relayReserved {
		t.Fatalf("the second code lost its place: %+v", st)
	}
	s.forgetRelays(swarmPairing + "2")
	if st := state(); st.State != relayIdle {
		t.Fatalf("no code waits: %+v", st)
	}
	s.relayChanged(swarmSessions, relayEvent(ka, nil))
	s.relayChanged(swarmPairing+"3", relayEvent(ka, kolorelay.ErrNotAllowed))
	if st := state(); st.State != relayReserved || st.Error != nil {
		t.Fatalf("a code's refusal hides the sessions' place: %+v", st)
	}
	s.relayChanged(swarmSessions, relayEvent(ka, koloswarm.ErrClosed))
	if st := state(); st.State != relayNotReserved || st.Error.Reason != "relay-pairing-not-allowed" {
		t.Fatalf("with no session: %+v", st)
	}
}

// A code shown with a relay that lets in only the app's own key is told it cannot wait there; the advice is not to
// allow a key, since the code's key is made for it.
func TestAllowListedRelayRefusesACode(t *testing.T) {
	id, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	rid, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	srv, err := kolorelay.Listen(context.Background(), "127.0.0.1:0", rid, &kolorelay.ServerConfig{
		Secure: loopSecure(), AllowReserve: func(k ed25519.PublicKey) bool { return k.Equal(id.PublicKey()) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	entry := srv.Addr().String() + "@" + hex.EncodeToString(rid.PublicKey())
	logTo(t)
	p := startPeer(t, Config{First: freeUDP(t), Relays: []string{entry}}, func(s *ShareService) { s.hooks.identity = id })
	p.rec.waitNetwork(t, netReady)
	showCode(t, p)
	p.rec.waitRelays(t, "a code that cannot wait at the relay", func(rs []RelayStatus) bool {
		return len(rs) == 1 && rs[0].State == relayNotReserved && rs[0].Error.Reason == "relay-pairing-not-allowed"
	})
}
