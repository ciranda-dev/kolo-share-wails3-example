package main

import (
	"context"
	"os"
	"strconv"
	"time"

	kolofiles "github.com/ciranda-dev/kolo-files"
)

// An item is stored before it is offered, which takes a while for a large one. When the peer reads PREPARE, the
// sender tells it at once and says how far it got, so the peer shows the item while it waits. The item turns into
// the usual offer when READY and the OFFER arrive, and goes away with PREPEND when it ends before that.

// prepThrottle decides when a PREPARE goes out for an item being stored.
type prepThrottle struct {
	at          time.Time
	done, total int64
}

// due reports whether a PREPARE for done and total goes out at now, and records it when it does: at most one
// every prepEvery, and only when a number changed, or after prepKeepalive of silence.
func (p *prepThrottle) due(now time.Time, done, total int64) bool {
	since := now.Sub(p.at)
	if since < prepEvery || done == p.done && total == p.total && since < prepKeepalive {
		return false
	}
	p.at, p.done, p.total = now, done, total
	return true
}

// cheapSize is the size of path when it is a file, which a stat gives at once, and 0 for a folder, whose size
// takes a walk of the whole tree.
func cheapSize(path, kind string) int64 {
	if kind != kindFile {
		return 0
	}
	if info, err := os.Lstat(path); err == nil {
		return info.Size()
	}
	return 0
}

// announce tells the peer that j is being prepared, when it reads PREPARE. The item's size is known for a file.
func (s *ShareService) announce(j *job, size int64) {
	ss := j.session
	id, _ := strconv.ParseUint(j.t.ID, 10, 64)
	s.mu.Lock()
	if !ss.peerPrep || ss.ended() || id == 0 || len(j.t.Name) > kolofiles.DefaultMaxNameLength {
		s.mu.Unlock()
		return
	}
	j.prepID = id
	j.prep = prepare{id: id, kind: j.t.Kind, name: j.t.Name, total: size}
	ss.prepOut[id] = j
	p := j.prep
	s.mu.Unlock()
	j.prog = prepThrottle{at: time.Now(), total: size}
	s.sendCtl(ss, ctlMsg{typ: ctlPrepare, prep: p})
}

// prepareTick sends the progress of j's preparation, as often as prepThrottle lets it.
func (s *ShareService) prepareTick(j *job, done int64) {
	s.mu.Lock()
	id, total := j.prepID, j.t.BytesTotal
	s.mu.Unlock()
	if id == 0 {
		return
	}
	if total > 0 {
		done = min(done, total)
	}
	if !j.prog.due(time.Now(), done, total) {
		return
	}
	s.mu.Lock()
	j.prep.done, j.prep.total = done, total
	p := j.prep
	s.mu.Unlock()
	s.sendCtl(j.session, ctlMsg{typ: ctlPrepare, prep: p})
}

// endPrep tells the peer that j, which it knows as being prepared, ended in state before it was offered.
func (s *ShareService) endPrep(j *job, state string) {
	ss := j.session
	s.mu.Lock()
	id, offered := j.prepID, j.offer.id != 0
	delete(ss.prepOut, id)
	s.mu.Unlock()
	if id == 0 || offered || ss.ctx.Err() != nil {
		return
	}
	reason := uint64(endCancelled)
	if state == stateFailed {
		reason = endFailed
	}
	s.sendCtl(ss, ctlMsg{typ: ctlPrepEnd, prep: prepare{id: id}, state: reason})
}

// gotPrepare takes a PREPARE of the peer: a new item shows as a card that says it is being prepared, a known one
// shows its progress. The peer may keep at most maxOpenOffers items open, so a card more than that, or more than
// the announcements a minute an honest sender makes, or an item over the limits of this app, shows nothing: its
// offer, if it comes, is declined as usual.
func (s *ShareService) gotPrepare(ss *session, l *link, p prepare) error {
	if !l.synced {
		if l.resentPrep[p.id] = true; len(l.resentPrep) > maxOpenOffers {
			return protoErr("more items being prepared before SYNCED than a sender holds open", nil)
		}
	}
	now := time.Now()
	s.mu.Lock()
	if ss.ended() {
		s.mu.Unlock()
		return nil
	}
	if j := ss.prepIn[p.id]; j != nil {
		if j.t.Kind != p.kind {
			s.mu.Unlock()
			return protoErr("a transfer ID used again for another item", nil)
		}
		take := ss.progress.take(now, prepUpdateRate)
		s.mu.Unlock()
		select {
		case j.alive <- struct{}{}:
		default:
		}
		if take {
			s.update(j, func(t *Transfer) { t.BytesDone, t.BytesTotal = progressOf(p) })
		}
		return nil
	}
	if len(ss.prepIn)+len(ss.got) >= maxOpenOffers || p.total > limitsOf(s.set).bytes || !ss.announce.take(now, offerRate) {
		s.mu.Unlock()
		return nil
	}
	j, err := s.addJobLocked(ss, "receive", p.kind, cleanName(p.name), statePreparing)
	if err != nil {
		s.mu.Unlock()
		return nil
	}
	j.prepID, j.prepared, j.alive = p.id, make(chan struct{}), make(chan struct{}, 1)
	j.decide = make(chan decision, 1)
	j.t.BytesDone, j.t.BytesTotal = progressOf(p)
	ss.prepIn[p.id] = j
	s.mu.Unlock()
	s.change(j, true, func(*Transfer) {})
	go s.runIncoming(j)
	s.emitSessions()
	return nil
}

// progressOf is the bytes done and the total a card shows for p, never more done than the total.
func progressOf(p prepare) (done, total int64) {
	if p.total > 0 {
		return min(p.done, p.total), p.total
	}
	return p.done, 0
}

// gotReady takes a READY: the OFFER with offer ID is the item of the card of transfer ID id.
func (s *ShareService) gotReady(ss *session, l *link, id, offerID uint64) error {
	if !l.synced {
		if l.resentPrep[id] = true; len(l.resentPrep) > maxOpenOffers {
			return protoErr("more items being prepared before SYNCED than a sender holds open", nil)
		}
	}
	s.mu.Lock()
	j := ss.prepIn[id]
	if j != nil {
		j.readyID = offerID
	}
	s.mu.Unlock()
	if j != nil {
		select {
		case j.alive <- struct{}{}:
		default:
		}
	}
	return nil
}

// gotPrepEnd takes a PREPEND: the card of transfer ID id ends, as cancelled or failed by the sender.
func (s *ShareService) gotPrepEnd(ss *session, id, reason uint64) {
	s.mu.Lock()
	j := ss.prepIn[id]
	delete(ss.prepIn, id)
	s.mu.Unlock()
	if j == nil {
		return
	}
	if reason == endFailed {
		j.cancel(errPrepFailed)
	} else {
		j.cancel(errWithdrawn)
	}
}

// takeReadyLocked removes and returns the card that READY linked to the offer id, nil when there is none or its
// end already began.
func takeReadyLocked(ss *session, offerID uint64) *job {
	for id, j := range ss.prepIn {
		if j.readyID == offerID && j.ctx.Err() == nil {
			delete(ss.prepIn, id)
			return j
		}
	}
	return nil
}

// waitPrepared holds a card that is still an announcement until its offer arrives (it returns "", nil, false),
// or it ends: the sender said so, the session ended, or prepStall passed without a word of the sender. The state
// is the final one then.
func (s *ShareService) waitPrepared(j *job) (state string, se *ShareError, ended bool) {
	ss := j.session
	timer := time.NewTimer(prepStall)
	defer timer.Stop()
	for {
		select {
		case <-j.prepared:
			return "", nil, false
		case <-j.alive:
			timer.Reset(prepStall)
		case <-timer.C:
			s.mu.Lock()
			stalled := ss.prepIn[j.prepID] == j // else its offer took it just now
			if stalled {
				delete(ss.prepIn, j.prepID)
			}
			s.mu.Unlock()
			if stalled {
				j.cancel(errPrepStalled)
				state, se = s.receiveEnd(j, nil)
				return state, se, true
			}
		case <-j.ctx.Done():
			s.mu.Lock()
			if ss.prepIn[j.prepID] == j {
				delete(ss.prepIn, j.prepID)
			}
			s.mu.Unlock()
			state, se = s.receiveEnd(j, nil)
			return state, se, true
		}
	}
}

// holdStep calls the test hook at every chunk a send stores.
type holdStep struct {
	ctx  context.Context
	hook func(ctx context.Context, n int)
	to   *counter
}

func (h holdStep) Write(p []byte) (int, error) {
	h.to.Write(p)
	h.hook(h.ctx, len(p))
	return len(p), h.ctx.Err()
}
