package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"log"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	koloid "github.com/ciranda-dev/kolo-id"
	kololog "github.com/ciranda-dev/kolo-log"
	kolosecure "github.com/ciranda-dev/kolo-secure"
	kolostore "github.com/ciranda-dev/kolo-store"
	kolosync "github.com/ciranda-dev/kolo-sync"
	kolotransport "github.com/ciranda-dev/kolo-transport"
)

// logSink collects what the standard logger writes until the test ends.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// lines returns the logged lines that hold word.
func (l *logSink) lines(word string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, ln := range strings.Split(l.buf.String(), "\n") {
		if strings.Contains(ln, word) {
			out = append(out, ln)
		}
	}
	return out
}

func captureLog(t *testing.T) *logSink {
	t.Helper()
	sink := &logSink{}
	old, flags := log.Writer(), log.Flags()
	log.SetOutput(sink)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(old); log.SetFlags(flags) })
	return sink
}

// words reads the key=value words of a "transfer:" line.
func words(t *testing.T, line, side string) map[string]string {
	t.Helper()
	rest, ok := strings.CutPrefix(line, "transfer: "+side+" ")
	if !ok {
		t.Fatalf("not a %q line: %q", side, line)
	}
	m := map[string]string{}
	for _, w := range strings.Fields(rest) {
		k, v, ok := strings.Cut(w, "=")
		if !ok || m[k] != "" {
			t.Fatalf("bad or repeated word %q in %q", w, line)
		}
		m[k] = v
	}
	return m
}

func keysOf(m map[string]string) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

func mustFloat(t *testing.T, m map[string]string, k string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(m[k], 64)
	if err != nil {
		t.Fatalf("%s=%q: %v", k, m[k], err)
	}
	return f
}

func mustDur(t *testing.T, m map[string]string, k string) time.Duration {
	t.Helper()
	d, err := time.ParseDuration(m[k])
	if err != nil {
		t.Fatalf("%s=%q: %v", k, m[k], err)
	}
	return d
}

// A "transfer: recv" line is key=value words with the rates since the last line and since the start.
func TestRecvLineFormat(t *testing.T) {
	s := kolosync.PullStats{
		Streamed: true, Elapsed: 5 * time.Second, Bytes: 40_000_000, Answers: 40, Entries: 40,
		CommitBusy: 800 * time.Millisecond, CommitIdle: 4100 * time.Millisecond, ReaderWait: 4200 * time.Millisecond,
		FirstByteMin: 24500 * time.Microsecond, InFlightMax: 4, InFlightAvg: 3.14159,
		MemoryBlocked: 2, MemoryWait: 15 * time.Millisecond,
	}
	var tick recvTick
	line := tick.line(s, "running")
	m := words(t, line, "recv")
	want := []string{"answers", "avg_answer", "avg_mbs", "bytes", "commit_busy", "commit_idle", "elapsed", "first_byte_min",
		"inflight_avg", "inflight_max", "mem_blocked", "mem_wait", "rate_mbs", "reader_wait", "state", "streamed"}
	if got := keysOf(m); !slices.Equal(got, want) {
		t.Fatalf("words %v, want %v", got, want)
	}
	if m["state"] != "running" || m["streamed"] != "true" || m["bytes"] != "40000000" || m["answers"] != "40" || m["avg_answer"] != "1000000" ||
		m["inflight_max"] != "4" || m["inflight_avg"] != "3.1" || m["mem_blocked"] != "2" {
		t.Errorf("values: %q", line)
	}
	if mustFloat(t, m, "rate_mbs") != 8 || mustFloat(t, m, "avg_mbs") != 8 {
		t.Errorf("rates: %q", line)
	}
	if mustDur(t, m, "first_byte_min") != 25*time.Millisecond && mustDur(t, m, "first_byte_min") != 24*time.Millisecond ||
		mustDur(t, m, "commit_idle") != 4100*time.Millisecond || mustDur(t, m, "commit_busy") != 800*time.Millisecond ||
		mustDur(t, m, "mem_wait") != 15*time.Millisecond || mustDur(t, m, "elapsed") != 5*time.Second {
		t.Errorf("times: %q", line)
	}

	// The next line gives the rate over the 5 s between them, the average over the whole pull.
	s.Elapsed, s.Bytes, s.Streamed = 10*time.Second, 60_000_000, false
	m = words(t, tick.line(s, "ok"), "recv")
	if mustFloat(t, m, "rate_mbs") != 4 || mustFloat(t, m, "avg_mbs") != 6 || m["state"] != "ok" || m["streamed"] != "false" {
		t.Errorf("second line: %v", m)
	}
	// An empty pull divides by nothing.
	var zero recvTick
	m = words(t, zero.line(kolosync.PullStats{}, "error"), "recv")
	if m["rate_mbs"] != "0.00" || m["avg_answer"] != "0" {
		t.Errorf("empty: %v", m)
	}
}

// connStats is a connection that has measured a path: 24.5 ms, a 1 MiB window with 300 KB in flight, 12 of 4000 packets lost.
func connStats() kolotransport.ConnStats {
	return kolotransport.ConnStats{
		SmoothedRTT: 24600 * time.Microsecond, MinRTT: 20 * time.Millisecond, CongestionWindow: 1 << 20, BytesInFlight: 300_000,
		PacketsSent: 4000, PacketsLost: 12, BytesLost: 14_400, CongestionController: "bbr", BandwidthEstimate: 12_500_000,
		PathChanges: 1, BytesSent: 5_000_000,
	}
}

// The connection's numbers are key=value words after the line; BBR's bandwidth is there only when it measured one.
func TestNetWords(t *testing.T) {
	line := "transfer: recv state=ok" + netWords(connStats())
	m := words(t, line, "recv")
	want := []string{"bw_mbs", "bytes_lost", "cc", "cwnd", "in_flight", "min_rtt", "path_changes", "pkts_lost", "pkts_sent", "rtt", "state"}
	if got := keysOf(m); !slices.Equal(got, want) {
		t.Fatalf("words %v, want %v", got, want)
	}
	if m["rtt"] != "24.6ms" || m["min_rtt"] != "20ms" || m["cwnd"] != "1048576" ||
		m["in_flight"] != "300000" || m["pkts_sent"] != "4000" || m["pkts_lost"] != "12" || m["bytes_lost"] != "14400" ||
		m["cc"] != "bbr" || m["bw_mbs"] != "12.50" || m["path_changes"] != "1" {
		t.Errorf("values: %q", line)
	}
	s := connStats()
	s.CongestionController, s.BandwidthEstimate = "cubic", 0
	m = words(t, "transfer: recv state=ok"+netWords(s), "recv")
	if _, ok := m["bw_mbs"]; ok || m["cc"] != "cubic" {
		t.Errorf("cubic: %v", m)
	}
	// A connection that has ended gives the zero value, and no word.
	if got := netWords(kolotransport.ConnStats{}); got != "" {
		t.Errorf("ended connection: %q", got)
	}
}

// A connection without statistics adds nothing; one with them adds its words.
func TestConnWords(t *testing.T) {
	if got := connWords(struct{}{}); got != "" {
		t.Errorf("no Stats method: %q", got)
	}
	if got := connWords(fakeStats(connStats())); got != netWords(connStats()) || got == "" {
		t.Errorf("with Stats: %q", got)
	}
}

type fakeStats kolotransport.ConnStats

func (f fakeStats) Stats() kolotransport.ConnStats { return kolotransport.ConnStats(f) }

// The sender's line names how many connections there are and gives the numbers of the one that has sent the most.
func TestSendWords(t *testing.T) {
	if got := sendWords(nil); got != "" {
		t.Errorf("no connection: %q", got)
	}
	idle, busy := connStats(), connStats()
	idle.BytesSent, idle.CongestionController = 10, "reno"
	line := "transfer: send state=running" + sendWords([]kolotransport.ConnStats{idle, busy})
	m := words(t, line, "send")
	if m["conns"] != "2" || m["cc"] != "bbr" || m["rtt"] != "24.6ms" {
		t.Errorf("two connections: %q", line)
	}
	m = words(t, "transfer: send state=idle"+sendWords([]kolotransport.ConnStats{idle}), "send")
	if m["conns"] != "1" || m["cc"] != "reno" {
		t.Errorf("one connection: %v", m)
	}
}

// With no session the sender has no connection to ask, and the loop writes no word of it.
func TestSendStatsNoSession(t *testing.T) {
	s := &ShareService{sessions: map[string]*session{}}
	if got := s.sendStats(); len(got) != 0 {
		t.Errorf("%v", got)
	}
}

// No line names a file, a path or a key: only the words above.
func TestTransferLinesCarryNoNames(t *testing.T) {
	var tick recvTick
	line := tick.line(kolosync.PullStats{Bytes: 1, Elapsed: time.Second}, "ok") + netWords(connStats())
	if strings.ContainsAny(line, "/\\") || strings.Contains(line, "err") {
		t.Errorf("recv line: %q", line)
	}
	var st sendTick
	st.step(time.Unix(0, 0), kolosync.ServerStats{})
	line = st.step(time.Unix(5, 0), kolosync.ServerStats{BytesSent: 1, ActiveStreams: 1}) + sendWords([]kolotransport.ConnStats{connStats()})
	if strings.ContainsAny(line, "/\\") {
		t.Errorf("send line: %q", line)
	}
}

// The sender logs while it serves, and one idle line when it stops; nothing before or after.
func TestSendTick(t *testing.T) {
	var st sendTick
	t0 := time.Unix(1000, 0)
	st.last = t0
	if l := st.step(t0.Add(5*time.Second), kolosync.ServerStats{}); l != "" {
		t.Fatalf("idle: %q", l)
	}
	l := st.step(t0.Add(10*time.Second), kolosync.ServerStats{BytesSent: 40_000_000, ActiveStreams: 2})
	m := words(t, l, "send")
	if m["state"] != "running" || m["sent"] != "40000000" || m["streams"] != "2" || mustFloat(t, m, "rate_mbs") != 8 || mustFloat(t, m, "avg_mbs") != 8 || mustDur(t, m, "elapsed") != 5*time.Second {
		t.Fatalf("first: %q", l)
	}
	l = st.step(t0.Add(15*time.Second), kolosync.ServerStats{BytesSent: 60_000_000, ActiveStreams: 1})
	m = words(t, l, "send")
	if m["state"] != "running" || mustFloat(t, m, "rate_mbs") != 4 || mustFloat(t, m, "avg_mbs") != 6 || m["sent"] != "60000000" {
		t.Fatalf("second: %q", l)
	}
	// The last streams ended with a few bytes still counted in this interval.
	l = st.step(t0.Add(20*time.Second), kolosync.ServerStats{BytesSent: 60_000_000, ActiveStreams: 0})
	if m = words(t, l, "send"); m["state"] != "idle" || m["streams"] != "0" || mustFloat(t, m, "rate_mbs") != 0 || mustFloat(t, m, "avg_mbs") != 6 || mustDur(t, m, "elapsed") != 10*time.Second {
		t.Fatalf("idle line: %q", l)
	}
	if l := st.step(t0.Add(25*time.Second), kolosync.ServerStats{BytesSent: 60_000_000}); l != "" {
		t.Fatalf("after idle: %q", l)
	}
	// A new transfer starts its own count.
	l = st.step(t0.Add(30*time.Second), kolosync.ServerStats{BytesSent: 65_000_000, ActiveStreams: 1})
	if m = words(t, l, "send"); m["sent"] != "5000000" || mustFloat(t, m, "avg_mbs") != 1 {
		t.Fatalf("second transfer: %q", l)
	}
}

// goroutinesBack waits for the goroutine count to fall to want, and fails if it does not.
func goroutinesBack(t *testing.T, want int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		n := runtime.NumGoroutine()
		if n <= want {
			return
		}
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<16)
			t.Fatalf("%d goroutines, want %d:\n%s", n, want, buf[:runtime.Stack(buf, true)])
		}
	}
}

// slowConn makes every stream the pull opens wait first, so the pull lasts for a few lines.
type slowConn struct {
	kolosync.Conn
	delay time.Duration
}

// Stats lets the wrapped connection's statistics through, as the connections of the swarm give them.
func (c slowConn) Stats() kolotransport.ConnStats { return c.Conn.(statser).Stats() }

func (c slowConn) OpenStream(ctx context.Context) (*kolotransport.Stream, error) {
	select {
	case <-time.After(c.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return c.Conn.OpenStream(ctx)
}

// pullFixture serves a log of entries entries of size bytes over loopback and returns what a pull needs.
type pullFixture struct {
	conn  kolosync.Conn
	st    *kolostore.Store
	owner ed25519.PublicKey
	id    kololog.LogID
	rep   func() *kolostore.Replica
	size  uint64
}

func newPullFixture(t *testing.T, entries, size int) *pullFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	owner, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	puller, err := koloid.Generate()
	if err != nil {
		t.Fatal(err)
	}
	src := kolostore.New(kolostore.NewMemory())
	t.Cleanup(func() { src.Close() })
	l, err := src.Create(owner)
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, size)
	for i := range entries {
		payload[0] = byte(i)
		if _, err := l.Append(payload); err != nil {
			t.Fatal(err)
		}
	}
	srv := kolosync.NewServer(func(peer, own ed25519.PublicKey, id kololog.LogID) kolosync.Access { return kolosync.Keys })
	if err := srv.Offer(l); err != nil {
		t.Fatal(err)
	}
	ln, err := kolosecure.Listen(ctx, "127.0.0.1:0", owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		if c, err := ln.Accept(ctx); err == nil {
			srv.Serve(ctx, c)
		}
	}()
	conn, err := kolosecure.Dial(ctx, ln.Addr().String(), puller, owner.PublicKey(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	dst := kolostore.New(kolostore.NewMemory())
	t.Cleanup(func() { dst.Close() })
	head, err := kolosync.FetchHead(ctx, conn, owner.PublicKey(), l.ID())
	if err != nil || head.Size() != uint64(entries) {
		t.Fatalf("head: %v %v", head, err)
	}
	header, err := kolosync.FetchHeader(ctx, conn, owner.PublicKey(), l.ID())
	if err != nil {
		t.Fatal(err)
	}
	rep, err := dst.OpenReplica(owner.PublicKey(), l.ID(), header)
	if err != nil {
		t.Fatal(err)
	}
	return &pullFixture{conn: conn, st: dst, owner: owner.PublicKey(), id: l.ID(), rep: func() *kolostore.Replica { return rep }, size: uint64(entries)}
}

// The app's pull gives kolo-sync a meter and logs lines that tell how it went: running lines while it works, one at
// the end, and no goroutine is left behind.
func TestMeteredPullLogs(t *testing.T) {
	sink := captureLog(t)
	defer func(d time.Duration) { transferEvery = d }(transferEvery)
	transferEvery = 10 * time.Millisecond
	f := newPullFixture(t, 12, 200<<10)
	before := runtime.NumGoroutine()

	if err := meteredPull(context.Background(), slowConn{f.conn, 120 * time.Millisecond}, f.rep(), new(kolosync.PullMeter)); err != nil {
		t.Fatal(err)
	}
	if f.rep().Len() != f.size {
		t.Fatalf("the replica holds %d of %d entries", f.rep().Len(), f.size)
	}
	lines := sink.lines("transfer: recv")
	if len(lines) < 3 {
		t.Fatalf("%d lines: %q", len(lines), lines)
	}
	for _, l := range lines[:len(lines)-1] {
		if m := words(t, l, "recv"); m["state"] != "running" {
			t.Errorf("a line before the end: %q", l)
		}
	}
	last := words(t, lines[len(lines)-1], "recv")
	if last["state"] != "ok" || last["streamed"] != "true" || last["bytes"] == "0" || last["answers"] == "0" || last["inflight_max"] == "0" {
		t.Errorf("last line: %q", lines[len(lines)-1])
	}
	if mustFloat(t, last, "avg_mbs") <= 0 || mustDur(t, last, "elapsed") < 100*time.Millisecond {
		t.Errorf("last line: %q", lines[len(lines)-1])
	}
	// The connection's own numbers come from the real loopback connection.
	if mustDur(t, last, "rtt") < 0 || mustDur(t, last, "min_rtt") < 0 || mustFloat(t, last, "cwnd") <= 0 || mustFloat(t, last, "pkts_sent") <= 0 ||
		!validCongestion(last["cc"]) || last["path_changes"] != "0" {
		t.Errorf("connection words: %q", lines[len(lines)-1])
	}
	goroutinesBack(t, before)
}

// -quiet-transfers keeps the line at the end and drops the ones while the pull runs.
func TestMeteredPullQuiet(t *testing.T) {
	sink := captureLog(t)
	defer func(d time.Duration) { transferEvery = d }(transferEvery)
	transferEvery = 10 * time.Millisecond
	transferQuiet.Store(true)
	defer transferQuiet.Store(false)
	f := newPullFixture(t, 4, 100<<10)
	before := runtime.NumGoroutine()
	if err := meteredPull(context.Background(), slowConn{f.conn, 80 * time.Millisecond}, f.rep(), new(kolosync.PullMeter)); err != nil {
		t.Fatal(err)
	}
	lines := sink.lines("transfer:")
	if len(lines) != 1 || words(t, lines[0], "recv")["state"] != "ok" {
		t.Fatalf("lines: %q", lines)
	}
	goroutinesBack(t, before)
}

// A pull that is canceled returns its error, logs the end with state=canceled and stops its goroutine.
func TestMeteredPullCanceled(t *testing.T) {
	sink := captureLog(t)
	defer func(d time.Duration) { transferEvery = d }(transferEvery)
	transferEvery = 10 * time.Millisecond
	f := newPullFixture(t, 4, 100<<10)
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(60*time.Millisecond, cancel)
	err := meteredPull(ctx, slowConn{f.conn, time.Hour}, f.rep(), new(kolosync.PullMeter))
	if err == nil {
		t.Fatal("a canceled pull returned no error")
	}
	lines := sink.lines("transfer: recv")
	if len(lines) == 0 || words(t, lines[len(lines)-1], "recv")["state"] != "canceled" {
		t.Fatalf("lines: %q", lines)
	}
	goroutinesBack(t, before)
}

// A pull that fails says so without its error text, which can hold a name.
func TestMeteredPullError(t *testing.T) {
	sink := captureLog(t)
	f := newPullFixture(t, 2, 1<<10)
	f.conn = deadConn{f.conn}
	if err := meteredPull(context.Background(), f.conn, f.rep(), new(kolosync.PullMeter)); err == nil {
		t.Fatal("no error")
	}
	lines := sink.lines("transfer: recv")
	if len(lines) != 1 || words(t, lines[0], "recv")["state"] != "error" {
		t.Fatalf("lines: %q", lines)
	}
}

type deadConn struct{ kolosync.Conn }

func (deadConn) OpenStream(context.Context) (*kolotransport.Stream, error) {
	return nil, io.ErrClosedPipe
}

// The sender's loop logs through the standard logger, only while a transfer is served, and ends with the app.
func TestSendLoop(t *testing.T) {
	sink := captureLog(t)
	defer func(d time.Duration) { transferEvery = d }(transferEvery)
	transferEvery = 10 * time.Millisecond
	s := &ShareService{srv: kolosync.NewServer(nil)}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	var done atomic.Bool
	go func() { s.sendLoop(); done.Store(true) }()
	time.Sleep(60 * time.Millisecond)
	if l := sink.lines("transfer:"); len(l) != 0 {
		t.Fatalf("a line with nothing served: %q", l)
	}
	s.cancel()
	for deadline := time.Now().Add(2 * time.Second); !done.Load(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("sendLoop did not stop with the app")
		}
	}
}

// -quiet-transfers and KOLO_SHARE_QUIET_TRANSFERS: off by default, the flag over the variable, a bad variable refused.
func TestQuietTransfersSetting(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		env  map[string]string
		want bool
	}{
		"none":              {nil, nil, false},
		"flag":              {[]string{"-quiet-transfers"}, nil, true},
		"flag false":        {[]string{"-quiet-transfers=false"}, nil, false},
		"env 1":             {nil, map[string]string{"KOLO_SHARE_QUIET_TRANSFERS": "1"}, true},
		"env 0":             {nil, map[string]string{"KOLO_SHARE_QUIET_TRANSFERS": "0"}, false},
		"flag over env":     {[]string{"-quiet-transfers=false"}, map[string]string{"KOLO_SHARE_QUIET_TRANSFERS": "1"}, false},
		"flag with env off": {[]string{"-quiet-transfers"}, map[string]string{"KOLO_SHARE_QUIET_TRANSFERS": "0"}, true},
	} {
		cfg, err := LoadConfig(c.args, env(c.env), io.Discard)
		if err != nil || cfg.QuietTransfers != c.want {
			t.Errorf("%s: %v, %v, want %v", name, cfg.QuietTransfers, err, c.want)
		}
	}
	_, err := LoadConfig(nil, env(map[string]string{"KOLO_SHARE_QUIET_TRANSFERS": "maybe"}), io.Discard)
	var ce *ConfigError
	if !errors.Is(err, ErrMalformed) || !errors.As(err, &ce) || ce.Setting != "quiet-transfers" {
		t.Errorf("a bad variable: %v", err)
	}
}
