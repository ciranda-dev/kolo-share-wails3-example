package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	kolostore "github.com/ciranda-dev/kolo-store"
	kolosync "github.com/ciranda-dev/kolo-sync"
	kolotransport "github.com/ciranda-dev/kolo-transport"
)

// transferEvery is the time between the lines of a running transfer; tests shorten it.
var transferEvery = 5 * time.Second

// transferQuiet is -quiet-transfers: no line while a transfer runs. A pull still logs one line when it ends.
var transferQuiet atomic.Bool

// meteredPull is kolosync.Pull with the meter m that logs where the time went: a "transfer: recv" line every
// transferEvery while it runs and one when it ends, with the connection's own numbers at the end of the line. The lines
// hold counts, sizes and times only, never a name, an address or a key.
func meteredPull(ctx context.Context, conn kolosync.Conn, rep *kolostore.Replica, m *kolosync.PullMeter) error {
	stop := make(chan struct{})
	var wg sync.WaitGroup
	prev := recvTick{}
	if !transferQuiet.Load() {
		wg.Go(func() {
			tick := time.NewTicker(transferEvery)
			defer tick.Stop()
			for {
				select {
				case <-stop:
					return
				case <-tick.C:
					log.Print(prev.line(m.Stats(), "running") + connWords(conn))
				}
			}
		})
	}
	res, err := kolosync.PullWith(ctx, conn, rep, &kolosync.PullOptions{Meter: m})
	close(stop)
	wg.Wait()
	state := "ok"
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		state = "canceled"
	case err != nil:
		state = "error"
	}
	log.Print(prev.line(res.Stats, state) + connWords(conn))
	return err
}

// recvTick remembers the last line of a pull, so the next one can give the rate over the time between them.
type recvTick struct {
	bytes   uint64
	elapsed time.Duration
}

// line is the "transfer: recv" line of s, read as key=value words: rate_mbs is MB/s since the last line and avg_mbs
// since the pull started; commit_idle is the time the store waited for an answer and commit_busy the time it spent
// verifying and storing, over the whole pull.
func (t *recvTick) line(s kolosync.PullStats, state string) string {
	rate := mbs(s.Bytes-t.bytes, s.Elapsed-t.elapsed)
	t.bytes, t.elapsed = s.Bytes, s.Elapsed
	var b strings.Builder
	fmt.Fprintf(&b, "transfer: recv state=%s rate_mbs=%.2f avg_mbs=%.2f", state, rate, mbs(s.Bytes, s.Elapsed))
	fmt.Fprintf(&b, " bytes=%d answers=%d avg_answer=%d streamed=%t", s.Bytes, s.Answers, s.AvgAnswer(), s.Streamed)
	fmt.Fprintf(&b, " inflight_max=%d inflight_avg=%.1f first_byte_min=%s", s.InFlightMax, s.InFlightAvg, round(s.FirstByteMin))
	fmt.Fprintf(&b, " commit_idle=%s commit_busy=%s reader_wait=%s", round(s.CommitIdle), round(s.CommitBusy), round(s.ReaderWait))
	fmt.Fprintf(&b, " mem_blocked=%d mem_wait=%s elapsed=%s", s.MemoryBlocked, round(s.MemoryWait), round(s.Elapsed))
	return b.String()
}

// statser is what the connections of the swarm (kolo-secure's and kolo-relay's) give: kolo-transport's statistics.
type statser interface {
	Stats() kolotransport.ConnStats
}

// connWords is the " rtt=... cc=..." words of conn's statistics, "" when conn keeps none or has ended.
func connWords(conn any) string {
	c, ok := conn.(statser)
	if !ok {
		return ""
	}
	return netWords(c.Stats())
}

// netWords turns the statistics of a connection into key=value words, each after a space: the smoothed and the minimum
// round trip, to 100 microseconds (rtt, min_rtt), the congestion window and the bytes in flight (cwnd, in_flight), the packets sent and
// lost and the bytes lost (pkts_sent, pkts_lost, bytes_lost), the controller (cc), BBR's bandwidth estimate in MB/s
// (bw_mbs, only when it measured one) and the moves to a new path (path_changes). A connection that has ended, or whose
// controller has no name, gives "".
func netWords(s kolotransport.ConnStats) string {
	if s.CongestionController == "" {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, " rtt=%s min_rtt=%s cwnd=%d in_flight=%d", fine(s.SmoothedRTT), fine(s.MinRTT), s.CongestionWindow, s.BytesInFlight)
	fmt.Fprintf(&b, " pkts_sent=%d pkts_lost=%d bytes_lost=%d cc=%s", s.PacketsSent, s.PacketsLost, s.BytesLost, s.CongestionController)
	if s.BandwidthEstimate > 0 {
		fmt.Fprintf(&b, " bw_mbs=%.2f", float64(s.BandwidthEstimate)/1e6)
	}
	fmt.Fprintf(&b, " path_changes=%d", s.PathChanges)
	return b.String()
}

// sendWords is " conns=N" and the netWords of the connection that has sent the most of the N, "" without a connection:
// a sender may serve several sessions, and its line is about the transfer that moves the most.
func sendWords(all []kolotransport.ConnStats) string {
	if len(all) == 0 {
		return ""
	}
	top := all[0]
	for _, c := range all[1:] {
		if c.BytesSent > top.BytesSent {
			top = c
		}
	}
	return fmt.Sprintf(" conns=%d", len(all)) + netWords(top)
}

// sendStats is the statistics of the connections of the open sessions.
func (s *ShareService) sendStats() []kolotransport.ConnStats {
	s.mu.Lock()
	var conns []statser
	for _, ss := range s.openSessionListLocked() {
		if ss.link != nil {
			if st, ok := ss.link.conn.(statser); ok {
				conns = append(conns, st)
			}
		}
	}
	s.mu.Unlock()
	out := make([]kolotransport.ConnStats, 0, len(conns))
	for _, c := range conns {
		out = append(out, c.Stats())
	}
	return out
}

// sendTick turns the totals of kolosync.Server into the "transfer: send" lines of the sender.
type sendTick struct {
	bytes   uint64    // sent at the last tick
	moved   uint64    // sent since the transfer began
	began   time.Time // the tick before the first one that saw it move
	last    time.Time // the last tick; set before the first step
	started bool
}

// step is called with the totals at now. It returns the line to log: one while streams are served or bytes moved
// since the last tick, and one with state=idle when that stops; "" when nothing happens.
func (t *sendTick) step(now time.Time, s kolosync.ServerStats) string {
	busy := s.ActiveStreams > 0 || s.BytesSent != t.bytes
	if !busy && !t.started {
		t.last = now
		return ""
	}
	if !t.started {
		t.started, t.began, t.moved = true, t.last, 0
	}
	delta := s.BytesSent - t.bytes
	t.moved += delta
	state, end := "running", now
	if !busy {
		state, end, t.started = "idle", t.last, false // the quiet interval is not part of the transfer
	}
	line := fmt.Sprintf("transfer: send state=%s rate_mbs=%.2f avg_mbs=%.2f sent=%d streams=%d elapsed=%s",
		state, mbs(delta, now.Sub(t.last)), mbs(t.moved, end.Sub(t.began)), t.moved, s.ActiveStreams, round(end.Sub(t.began)))
	t.bytes, t.last = s.BytesSent, now
	return line
}

// sendLoop logs a "transfer: send" line every transferEvery while the app serves a transfer, until the app closes.
func (s *ShareService) sendLoop() {
	tick := time.NewTicker(transferEvery)
	defer tick.Stop()
	var st sendTick
	st.last = time.Now()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-tick.C:
			if line := st.step(now, s.srv.Stats()); line != "" {
				log.Print(line + sendWords(s.sendStats()))
			}
		}
	}
}

// mbs is n bytes over d in MB/s, 0 for an empty interval.
func mbs(n uint64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(n) / 1e6 / d.Seconds()
}

func round(d time.Duration) time.Duration { return d.Round(time.Millisecond) }

// fine rounds a round-trip time to 100 microseconds, since loopback and a LAN answer in less than a millisecond.
func fine(d time.Duration) time.Duration { return d.Round(100 * time.Microsecond) }
