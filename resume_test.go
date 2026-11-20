package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kololog "github.com/ciranda-dev/kolo-log"
	kolostore "github.com/ciranda-dev/kolo-store"
	kolosync "github.com/ciranda-dev/kolo-sync"
)

// pullCall is one call of kolo-sync's Pull on a replica: the entries it held before and after.
type pullCall struct {
	id         kololog.LogID
	start, end uint64
}

// outage cuts the network between two apps in the middle of a pull: the connection closes and neither app
// accepts a new one until lift is called (or after liftAfter, when it is not zero).
type outage struct {
	w         *wall
	once      sync.Once
	mu        sync.Mutex
	calls     []pullCall
	alice     *peer
	sid       string
	liftAfter time.Duration
	cutAt     uint64 // entries of one log that start the outage
}

func (o *outage) lift() {
	o.w.mu.Lock()
	o.w.all = false
	o.w.mu.Unlock()
}

func (o *outage) cut() {
	o.once.Do(func() {
		o.w.mu.Lock()
		o.w.all = true
		o.w.mu.Unlock()
		o.alice.mu.Lock()
		l := o.alice.sessions[o.sid].link
		o.alice.mu.Unlock()
		if l != nil {
			l.conn.CloseWithError(0, "")
		}
		if o.liftAfter > 0 {
			time.AfterFunc(o.liftAfter, o.lift)
		}
	})
}

// startOutage makes every pull record what it held and cut the network once a log holds cutAt entries.
func startOutage(t *testing.T, o *outage) {
	t.Helper()
	old := pull
	t.Cleanup(func() { pull = old })
	pull = func(ctx context.Context, c kolosync.Conn, r *kolostore.Replica) error {
		start := r.Len()
		done := make(chan struct{})
		go func() {
			for {
				select {
				case <-done:
					return
				case <-time.After(time.Millisecond):
					if r.Len() >= o.cutAt {
						o.cut()
						return
					}
				}
			}
		}()
		err := old(ctx, c, r)
		close(done)
		o.mu.Lock()
		o.calls = append(o.calls, pullCall{r.ID(), start, r.Len()})
		o.mu.Unlock()
		return err
	}
}

// outagePeers starts two apps behind one test firewall, with the timings of the app shortened.
func outagePeers(t *testing.T, recvCfg Config, liftAfter time.Duration) (alice, bob *peer, o *outage) {
	t.Helper()
	oldStall, oldMin, oldMax := stallAfter, resumeMin, resumeMax
	stallAfter, resumeMin, resumeMax = 2*time.Second, 200*time.Millisecond, time.Second
	t.Cleanup(func() { stallAfter, resumeMin, resumeMax = oldStall, oldMin, oldMax })
	w := &wall{}
	alice, bob = networkWith(t, Config{}, recvCfg, w.set, w.set)
	o = &outage{w: w, alice: alice, liftAfter: liftAfter, cutAt: 8}
	t.Cleanup(o.lift)
	startOutage(t, o)
	return alice, bob, o
}

// A network that goes away for 10 seconds in the middle of a 50 MB item is no end: the receive says it waits,
// continues by itself when the sender is back, asks only for what it lacks and saves the tree it was offered.
func TestResumeAfterOutage(t *testing.T) {
	alice, bob, o := outagePeers(t, Config{}, 10*time.Second)
	sa, sb := connect(t, alice, bob)
	o.sid = sa
	body := randomBytes(t, 50<<20)
	sent := offerItem(t, alice, sa, writeFile(t, "big.bin", body))
	dest := t.TempDir()
	bob.SetSessionFolder(sb, dest)
	in := incoming(t, bob, sb, "big.bin")
	if err := bob.SaveOffer(in.ID); err != nil { // the only click
		t.Fatal(err)
	}
	waiting := bob.rec.until(t, in.ID, func(tr Transfer) bool { return tr.Stalled })
	if waiting.State != stateReceiving || waiting.BytesPerSecond != 0 || waiting.Error != nil {
		t.Fatalf("while waiting: %+v", waiting)
	}
	alice.rec.until(t, sent.ID, func(tr Transfer) bool { return tr.Stalled && tr.State == stateSending })
	got := bob.rec.wait(t, in.ID, stateDone, stateFailed)
	if got.State != stateDone || got.Stalled {
		t.Fatalf("after the outage: %+v (error %+v)", got, got.Error)
	}
	saved, err := os.ReadFile(got.SavedTo)
	if err != nil || sha256.Sum256(saved) != sha256.Sum256(body) {
		t.Fatalf("saved tree differs (%v)", err)
	}
	alice.rec.wait(t, sent.ID, stateDelivered)
	// Never back to a click, and the bytes held never went back to zero.
	bob.rec.mu.Lock()
	history := slices.Clone(bob.rec.transfers[in.ID])
	bob.rec.mu.Unlock()
	last := int64(0)
	for _, tr := range history {
		if tr.State == stateReceiving {
			if tr.BytesDone < last {
				t.Fatalf("bytes went from %d to %d: the part already received was thrown away", last, tr.BytesDone)
			}
			last = tr.BytesDone
		}
	}
	// The log with the content was pulled more than once, and every entry came once: what was verified before
	// the cut was kept.
	o.mu.Lock()
	defer o.mu.Unlock()
	var content kololog.LogID
	var most uint64
	for _, c := range o.calls {
		if c.end > most {
			content, most = c.id, c.end
		}
	}
	var fetched uint64
	var n int
	next := uint64(0)
	for _, c := range o.calls {
		if c.id != content {
			continue
		}
		n++
		if c.start != next {
			t.Fatalf("a pull started at entry %d, the one before ended at %d: %+v", c.start, next, o.calls)
		}
		fetched += c.end - c.start
		next = c.end
	}
	if n < 2 || fetched != most || o.calls[0].start != 0 {
		t.Fatalf("%d pulls of the content, %d entries fetched for %d: %+v", n, fetched, most, o.calls)
	}
}

// The person can always cancel, also while the item waits for the connection, and nothing stays behind.
func TestResumeCancelWhileWaiting(t *testing.T) {
	alice, bob, o := outagePeers(t, Config{}, 0)
	sa, sb := connect(t, alice, bob)
	o.sid = sa
	sent := offerItem(t, alice, sa, writeFile(t, "cancel.bin", randomBytes(t, 30<<20)))
	dest := t.TempDir()
	bob.SetSessionFolder(sb, dest)
	in := incoming(t, bob, sb, "cancel.bin")
	bob.SaveOffer(in.ID)
	if tr := bob.rec.until(t, in.ID, func(tr Transfer) bool { return tr.Stalled || tr.final() }); tr.final() {
		t.Fatalf("the receive ended instead of waiting: %+v (error %+v)", tr, tr.Error)
	}
	if err := bob.CancelTransfer(in.ID); err != nil {
		t.Fatal(err)
	}
	if got := bob.rec.wait(t, in.ID, stateCancelled, stateFailed); got.State != stateCancelled || got.Error != nil {
		t.Fatalf("after Cancel: %+v error %+v", got, got.Error)
	}
	assertEmpty(t, dest)
	poll(t, "the store is removed", func() bool {
		entries, _ := os.ReadDir(filepath.Join(bob.data, transfersDir))
		return len(entries) == 1 && entries[0].Name() == transfersMarker
	})
	// The sender cancels its own item too, while its peer is away.
	alice.CancelTransfer(sent.ID)
	alice.rec.wait(t, sent.ID, stateCancelled, stateStopped)
}

// A sender that does not come back ends the receive after -resume-for, with a sentence that says so, and nothing
// is saved. The sender's offer stays open for as long as it is not cancelled.
func TestResumeGivesUp(t *testing.T) {
	alice, bob, o := outagePeers(t, Config{ResumeFor: 3 * time.Second}, 0)
	sa, sb := connect(t, alice, bob)
	o.sid = sa
	sent := offerItem(t, alice, sa, writeFile(t, "gone.bin", randomBytes(t, 30<<20)))
	dest := t.TempDir()
	bob.SetSessionFolder(sb, dest)
	in := incoming(t, bob, sb, "gone.bin")
	began := time.Now()
	bob.SaveOffer(in.ID)
	bob.rec.until(t, in.ID, func(tr Transfer) bool { return tr.Stalled })
	got := bob.rec.wait(t, in.ID, stateFailed, stateDone)
	if got.State != stateFailed || got.Error == nil || got.Error.Reason != "resume-gave-up" || got.Error.Detail != "3 s" {
		t.Fatalf("after the bound: %+v (error %+v)", got, got.Error)
	}
	if took := time.Since(began); took < 3*time.Second {
		t.Fatalf("gave up after %v, before the bound", took)
	}
	assertEmpty(t, dest)
	if tr := alice.rec.until(t, sent.ID, func(tr Transfer) bool { return tr.Stalled }); tr.final() {
		t.Fatalf("the sender's offer ended with its peer away: %+v", tr)
	}
	alice.CancelTransfer(sent.ID)
	alice.rec.wait(t, sent.ID, stateCancelled, stateStopped)
}

func TestRateOf(t *testing.T) {
	at := time.Now()
	s := []sample{{at, 0}}
	var r float64
	for i := 1; i <= 10; i++ {
		s = append(s, sample{at.Add(time.Duration(i) * time.Second), int64(i) * 1000})
		r, s = rateOf(s, at.Add(time.Duration(i)*time.Second))
	}
	if r != 1000 || len(s) > 7 {
		t.Fatalf("steady 1000 a second: %v over %d samples", r, len(s))
	}
	// Nothing for the whole window: the speed is zero, not the average of the past.
	for i := 11; i <= 16; i++ {
		s = append(s, sample{at.Add(time.Duration(i) * time.Second), 10_000})
		r, s = rateOf(s, at.Add(time.Duration(i)*time.Second))
	}
	if r != 0 {
		t.Fatalf("a stalled transfer: %v a second", r)
	}
	if r, _ := rateOf(nil, at); r != 0 {
		t.Fatalf("no samples: %v", r)
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		3 * time.Second: "3 s", 30 * time.Minute: "30 min", time.Hour: "1 h", 90 * time.Minute: "1 h 30 min", 24 * time.Hour: "24 h",
	} {
		if got := humanDuration(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

// Several items of one session are received at once: every pull is inside kolo-sync's Pull before any of them
// goes on, which one receive at a time could never do.
func TestSeveralReceivesRunAtOnce(t *testing.T) {
	const n = 3
	old := pull
	t.Cleanup(func() { pull = old })
	var inside atomic.Int32
	together := make(chan struct{})
	var once sync.Once
	pull = func(ctx context.Context, c kolosync.Conn, r *kolostore.Replica) error {
		if inside.Add(1) >= n {
			once.Do(func() { close(together) })
		}
		select {
		case <-together:
		case <-time.After(20 * time.Second):
			return errors.New("the pulls never ran together")
		}
		return old(ctx, c, r)
	}
	alice, bob := network(t, Config{}, nil)
	sa, sb := connect(t, alice, bob)
	bob.SetSessionFolder(sb, t.TempDir())
	names := []string{"one.bin", "two.bin", "three.bin"}
	for _, name := range names {
		offerItem(t, alice, sa, writeFile(t, name, randomBytes(t, 1<<20)))
	}
	var ids []string
	for _, name := range names {
		in := incoming(t, bob, sb, name)
		if err := bob.SaveOffer(in.ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, in.ID)
	}
	for _, id := range ids {
		if tr := bob.rec.wait(t, id, stateDone, stateFailed); tr.State != stateDone {
			t.Fatalf("receive: %+v", tr.Error)
		}
	}
}

func TestStallClock(t *testing.T) {
	c := newStallClock()
	if !strings.Contains(c.base.String(), "m=") {
		t.Fatal("the clock has no monotonic reading, so a sleeping computer would count as idle time")
	}
	m := c.mark()
	time.Sleep(30 * time.Millisecond)
	if d := c.idle(); d < 25*time.Millisecond || d > time.Second {
		t.Fatalf("idle %v after 30ms", d)
	}
	c.touch()
	if c.mark() == m || c.idle() > 20*time.Millisecond {
		t.Fatalf("after a byte: mark %d to %d, idle %v", m, c.mark(), c.idle())
	}
}
