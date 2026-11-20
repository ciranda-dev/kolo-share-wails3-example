package main

import (
	"context"
	"errors"
	"io"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	koloblobs "github.com/ciranda-dev/kolo-blobs"
	kolofiles "github.com/ciranda-dev/kolo-files"
	kololog "github.com/ciranda-dev/kolo-log"
	kolostore "github.com/ciranda-dev/kolo-store"
	kolosync "github.com/ciranda-dev/kolo-sync"
)

// Send offers the file or folder at path to the peer of session sessionID and returns the transfer's ID. The
// item is stored first, then offered; the peer pulls it only after its person said Save. Links and special files
// are refused.
func (s *ShareService) Send(sessionID, path string) (string, error) {
	kind, name, err := checkSource(path)
	if err != nil {
		return "", err
	}
	lim := s.limitsNow()
	j, err := s.newJob("send", kind, cleanName(name), statePreparing, func() (*session, error) {
		ss, err := s.session(sessionID)
		if err != nil {
			return nil, err
		}
		if s.openSendsLocked(ss) >= maxOpenOffers {
			return nil, shareErr("busy-offers", ErrBusy, strconv.Itoa(maxOpenOffers))
		}
		return ss, nil
	})
	if err != nil {
		return "", err
	}
	go func() {
		defer s.wg.Done()
		state, se := s.runOffer(j, path, name, lim)
		s.endPrep(j, state)
		s.finish(j, state, se, true)
	}()
	return j.t.ID, nil
}

func (s *ShareService) limitsNow() limits {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.onThisSystem(limitsOf(s.set))
}

// onThisSystem sets the rules of l to this system's, or to the ones a test plays.
func (s *ShareService) onThisSystem(l limits) limits {
	if s.hooks.rules != nil {
		l.rules = *s.hooks.rules
	}
	return l
}

func (s *ShareService) openSendsLocked(ss *session) int {
	n := 0
	for _, j := range s.jobs {
		if j.session == ss && j.t.Direction == "send" && !j.t.final() {
			n++
		}
	}
	return n
}

// runOffer stores path in a new volume, offers its two logs to the session's peer only, and waits for the
// peer's answer, a cancel or the end of the session. It returns the final state.
func (s *ShareService) runOffer(j *job, path, name string, lim limits) (string, *ShareError) {
	ss := j.session
	fail := func(err error) (string, *ShareError) {
		if cause := context.Cause(j.ctx); cause != nil {
			return sendEnd(cause)
		}
		return stateFailed, explain(err, lim)
	}
	size := cheapSize(path, j.t.Kind)
	if size > 0 {
		s.update(j, func(t *Transfer) { t.BytesTotal = size })
	}
	s.announce(j, size)
	var stored counter
	var dst io.Writer = &stored
	if h := s.hooks.prepStep; h != nil {
		dst = holdStep{ctx: j.ctx, hook: h, to: &stored}
	}
	// The watch starts before the scan, so the peer hears from a send that walks a large folder.
	stop := s.watch(j, func() int64 {
		n := stored.n.Load()
		s.prepareTick(j, n)
		return n
	})
	defer stop()
	// The scan comes first: the size it finds picks the options of the store.
	sc, err := scanSource(path, lim.nodes)
	if err != nil {
		return fail(err)
	}
	s.update(j, func(t *Transfer) { t.FilesTotal, t.BytesTotal, t.Skipped = sc.files, sc.bytes, sc.skipped })
	release, se := s.reserve(spaceNeed{dir: s.transfersPath(), bytes: storeNeed(sc.bytes), part: "prepare"})
	if se != nil {
		return fail(se)
	}
	defer release()
	st, cleanup, err := openStore(s.jobDir(j), sc.bytes, &s.tuned)
	if err != nil {
		return fail(err)
	}
	defer cleanup()
	meta, err := st.Create(s.id)
	if err != nil {
		return fail(err)
	}
	content, err := st.Create(s.id)
	if err != nil {
		return fail(err)
	}
	v, err := kolofiles.Create(meta, content)
	if err != nil {
		return fail(err)
	}
	defer v.Close()
	err = fillVolume(j.ctx, v, path, name, dst)
	stop()
	if err != nil {
		return fail(err)
	}
	// The receiver plans the same volume with the same code, so the numbers of the offer match by construction.
	p, err := planCheckout(v, j.t.Kind, limits{bytes: math.MaxInt64, nodes: lim.nodes, rules: lim.rules})
	if err != nil {
		return fail(err)
	}
	own := s.id.PublicKey()
	guards := []*guardedSource{newGuard(meta), newGuard(content)}
	for _, g := range guards {
		if err := s.srv.Offer(g); err != nil {
			return fail(err)
		}
	}
	o := offer{kind: j.t.Kind, name: p.top, nodes: p.nodes, files: p.files, bytes: p.bytes, meta: v.ID()}
	if s.hooks.tamperOffer != nil {
		s.hooks.tamperOffer(&o)
	}
	s.mu.Lock()
	if !ss.ended() {
		ss.nextOut++
		o.id = ss.nextOut
		j.offer = o
		ss.sent[o.id] = j
		delete(ss.prepOut, j.prepID)
		ss.logs[meta.ID()], ss.logs[content.ID()] = true, true
	}
	prepID := j.prepID
	s.mu.Unlock()
	if o.id != 0 {
		s.update(j, func(t *Transfer) {
			t.State, t.BytesDone, t.BytesTotal, t.FilesTotal, t.BytesPerSecond = stateOffered, 0, p.bytes, p.files, 0
		})
		if prepID != 0 {
			s.sendCtl(ss, ctlMsg{typ: ctlReady, prep: prepare{id: prepID}, offer: o})
		}
		s.sendCtl(ss, ctlMsg{typ: ctlOffer, offer: o})
		<-j.ctx.Done()
	}
	cause := context.Cause(j.ctx)
	state, se := sendEnd(cause)
	if o.id != 0 && state == stateStopped && !errors.Is(cause, ErrSessionEnded) {
		s.sendCtl(ss, ctlMsg{typ: ctlWithdraw, offer: offer{id: o.id}})
	}
	s.mu.Lock()
	delete(ss.sent, o.id)
	delete(ss.logs, meta.ID())
	delete(ss.logs, content.ID())
	s.mu.Unlock()
	for _, g := range guards {
		s.srv.Withdraw(own, g.ID())
		g.close() // after the reads in flight, so the store never closes under a stream
	}
	return state, se
}

// sendEnd is the final state of an offer for the cause of its end.
func sendEnd(cause error) (string, *ShareError) {
	switch {
	case errors.Is(cause, errDelivered):
		return stateDelivered, nil
	case errors.Is(cause, errPeerDeclined):
		return stateDeclined, nil
	case errors.Is(cause, errPeerCancelled):
		return stateCancelled, nil
	case errors.Is(cause, errPeerFailed):
		return stateFailed, shareErr("peer-failed", errPeerFailed, "") // the peer's own reason is text from another computer
	case errors.Is(cause, ErrSessionEnded):
		return stateStopped, shareErr("session-ended", ErrSessionEnded, "")
	}
	return stateStopped, nil
}

// gotOffer takes an OFFER of the peer. A resent offer gets the answer it had; a new one is checked against this
// app's limits and either declined at once, with one line in the session, or shown to the person. Nothing is
// written to disk for it before Save.
func (s *ShareService) gotOffer(ss *session, l *link, o offer) error {
	if !l.synced {
		// An honest sender resends at most its open offers before SYNCED, so more is abuse, not memory to spend.
		if l.resent[o.id] = true; len(l.resent) > maxOpenOffers {
			return protoErr("more offers before SYNCED than a sender holds open", nil)
		}
	}
	s.mu.Lock()
	if ss.ended() {
		s.mu.Unlock()
		return nil
	}
	if j := ss.got[o.id]; j != nil {
		same, st, done := j.offer == o, statusOf(j.t.State), j.t.BytesDone
		s.mu.Unlock()
		if !same {
			return protoErr("an offer ID used again for another item", nil)
		}
		if st != 0 {
			s.sendStatus(ss, o.id, st, done)
		}
		return nil
	}
	if st, ok := ss.finals[o.id]; ok {
		s.mu.Unlock()
		s.sendStatus(ss, o.id, st, 0)
		return nil
	}
	if o.id <= ss.lastIn {
		s.mu.Unlock()
		s.sendStatus(ss, o.id, stFailed, 0) // ended and forgotten
		return nil
	}
	ss.lastIn = o.id
	card := takeReadyLocked(ss, o.id) // the item of this offer that the peer announced, if it did
	if se := s.refuseOfferLocked(ss, o); se != nil {
		ss.finals[o.id] = stDeclined
		pruneFinals(ss)
		ss.s.Notice = se
		s.mu.Unlock()
		if card != nil {
			card.cancel(errUserDeclined)
		}
		s.sendStatus(ss, o.id, stDeclined, 0)
		s.emitSessions()
		return nil
	}
	j := card
	if j == nil {
		var err error
		if j, err = s.addJobLocked(ss, "receive", o.kind, cleanName(o.name), stateOffered); err != nil {
			s.mu.Unlock()
			return nil
		}
		j.decide = make(chan decision, 1)
	}
	j.offer = o
	j.t.BytesTotal, j.t.FilesTotal = o.bytes, o.files
	ss.got[o.id] = j
	auto, folder := ss.s.AutoSave, ss.s.Folder
	if auto && o.bytes > ss.autoLeft {
		auto, ss.s.AutoSave = false, false
		ss.s.Notice = shareErr("autosave-limit", ErrNoSpace, "")
	} else if auto {
		ss.autoLeft -= o.bytes
	}
	s.mu.Unlock()
	if card == nil {
		s.change(j, true, func(*Transfer) {})
		go s.runIncoming(j)
	} else {
		s.change(j, false, func(t *Transfer) {
			t.State, t.Kind, t.Name, t.BytesDone, t.BytesTotal, t.FilesTotal = stateOffered, o.kind, cleanName(o.name), 0, o.bytes, o.files
		})
		close(j.prepared)
	}
	if auto && checkFolder(folder) == nil {
		j.decide <- decision{save: true, folder: folder}
	}
	s.emitSessions()
	return nil
}

// refuseOfferLocked says why a new offer is declined without asking: too many at once, a name this computer
// cannot save, or more than the limits of this app.
func (s *ShareService) refuseOfferLocked(ss *session, o offer) *ShareError {
	lim := s.onThisSystem(limitsOf(s.set))
	if !ss.rate.take(time.Now(), offerRate) || len(ss.got) >= maxOpenOffers {
		return shareErr("offer-limit", ErrBusy, "")
	}
	if !oneName(o.name) {
		return shareErr("unsafe-name", kolofiles.ErrUnsafeName, cleanName(o.name))
	}
	if _, err := savedTop(lim.rules, o.name); err != nil {
		var se *ShareError
		if errors.As(err, &se) {
			return se
		}
		return shareErr("unsafe-name", err, cleanName(o.name))
	}
	if o.bytes > lim.bytes {
		return shareErr("too-large", ErrTooLarge, humanBytes(lim.bytes))
	}
	if o.nodes > lim.nodes {
		return shareErr("too-many-files", ErrTooManyFiles, strconv.Itoa(lim.nodes))
	}
	return nil
}

// pruneFinals keeps the statuses of the last maxOpenOffers offers that ended: the sender holds at most that many
// open, so an older one is never asked for again.
func pruneFinals(ss *session) {
	for len(ss.finals) > maxOpenOffers {
		oldest := uint64(math.MaxUint64)
		for id := range ss.finals {
			oldest = min(oldest, id)
		}
		delete(ss.finals, oldest)
	}
}

// statusOf is the STATUS a receive in state sends, 0 for none.
func statusOf(state string) uint64 {
	switch state {
	case stateQueued:
		return stAccepted
	case stateReceiving:
		return stReceiving
	case stateVerifying:
		return stVerifying
	case stateDone:
		return stDone
	case stateDeclined:
		return stDeclined
	case stateCancelled:
		return stCancelled
	case stateFailed:
		return stFailed
	}
	return 0
}

// runIncoming waits for the person's answer to an incoming offer and receives it, then tells the sender how it
// ended. A card of an item still being prepared waits for its offer first.
func (s *ShareService) runIncoming(j *job) {
	defer s.wg.Done()
	ss := j.session
	state, se, answered := s.incoming(j)
	st := statusOf(state)
	s.mu.Lock()
	id := j.offer.id
	if id == 0 && ss.prepIn[j.prepID] == j {
		delete(ss.prepIn, j.prepID) // it never became an offer
	}
	delete(ss.got, id)
	if st != 0 && id != 0 {
		ss.finals[id] = st
		pruneFinals(ss)
	}
	done := j.t.BytesDone
	s.mu.Unlock()
	if st != 0 && id != 0 {
		s.sendStatus(ss, id, st, done)
	}
	s.finish(j, state, se, answered)
	s.emitSessions()
}

// incoming returns the final state of an incoming offer, and whether the person answered it. A receive that
// loses the connection goes on by itself when the connection is back (see receiveOffer).
func (s *ShareService) incoming(j *job) (string, *ShareError, bool) {
	ss := j.session
	if j.prepared != nil {
		if state, se, ended := s.waitPrepared(j); ended {
			return state, se, false
		}
	}
	var d decision
	select {
	case d = <-j.decide:
	case <-j.ctx.Done():
		state, se := s.receiveEnd(j, nil)
		return state, se, false
	}
	if !d.save {
		return stateDeclined, nil, true
	}
	s.update(j, func(t *Transfer) { t.State, t.Error = stateQueued, nil })
	s.emitSessions()
	s.sendStatus(ss, j.offer.id, stAccepted, 0)
	savedTo, err := s.receiveOffer(j, d.folder)
	if err == nil {
		s.update(j, func(t *Transfer) { t.SavedTo = savedTo })
		return stateDone, nil, true
	}
	state, se := s.receiveEnd(j, err)
	return state, se, true
}

// receiveEnd is the final state of a receive for err and the cause of its end.
func (s *ShareService) receiveEnd(j *job, err error) (string, *ShareError) {
	cause := context.Cause(j.ctx)
	switch {
	case errors.Is(cause, errUserDeclined):
		return stateDeclined, nil
	case errors.Is(cause, errWithdrawn), cause == nil && errors.Is(err, kolosync.ErrNotFound):
		return stateWithdrawn, shareErr("offer-withdrawn", errWithdrawn, "")
	case errors.Is(cause, errPrepFailed):
		return stateFailed, shareErr("prepare-failed", errPrepFailed, "")
	case errors.Is(cause, errPrepStalled):
		return stateFailed, shareErr("prepare-stalled", errPrepStalled, "")
	case errors.Is(cause, ErrSessionEnded):
		return stateStopped, shareErr("session-ended", ErrSessionEnded, "")
	case cause != nil:
		return stateCancelled, nil
	}
	return stateFailed, explain(err, s.limitsNow())
}

// receiveOffer pulls the offered volume on the session's connection, checks the verified tree against the
// offer, and saves it into folder. The pull is bounded by the offer before a byte is pulled: the signed heads
// may hold no more entries than an item of the offered size, and the tree must be the one offered.
//
// A pull that loses its connection or stalls is not an end: the entries verified so far stay in the store, and
// the next pull, with the same replicas, asks only for the ones missing. It starts again by itself when the
// connection is back and after a backoff when it is not, until the person cancels, the sender ends the offer, a
// limit or a check fails, or nothing has arrived for -resume-for.
func (s *ShareService) receiveOffer(j *job, folder string) (string, error) {
	ss, o := j.session, j.offer
	select {
	case ss.slot <- struct{}{}:
	case <-j.ctx.Done():
		return "", context.Cause(j.ctx)
	}
	defer func() { <-ss.slot }()
	set := s.settingsNow()
	lim := s.onThisSystem(limitsOf(set))
	dir := s.jobDir(j)
	// Both copies are checked before the first byte: the store while it downloads and the saved files, which share
	// a disk when the folder is on the data folder's volume.
	release, se := s.reserve(spaceNeed{dir: s.transfersPath(), bytes: storeNeed(o.bytes), part: "download"}, spaceNeed{dir: folder, bytes: saveNeed(o.bytes), part: "save"})
	if se != nil {
		return "", se
	}
	defer release()
	st, cleanup, err := openStore(dir, o.bytes, &s.tuned)
	if err != nil {
		return "", err
	}
	defer cleanup()
	s.update(j, func(t *Transfer) { t.State = stateReceiving })
	s.sendStatus(ss, o.id, stReceiving, 0)
	offered := limits{bytes: min(lim.bytes, o.bytes), nodes: min(lim.nodes, o.nodes)}
	var pulling atomic.Pointer[kolostore.Replica]
	var cur atomic.Pointer[context.CancelCauseFunc] // ends the pull running now
	abort := func(err error) {
		if c := cur.Load(); c != nil {
			(*c)(err)
		}
	}
	moved := newStallClock() // when the store last grew
	var seen int64
	stop := s.watch(j, func() int64 {
		// The store holds ciphertext and tree nodes, more than the content; a compaction can shrink it for a
		// moment, and the bytes shown never go back.
		n := max(dirSize(dir), seen)
		if n > storeBound(lim.bytes) {
			abort(ErrTooLarge)
		}
		if rep := pulling.Load(); rep != nil && rep.Len() > offered.contentEntries() {
			abort(ErrTooManyEntries)
		}
		if n != seen {
			seen = n
			moved.touch()
		}
		n = min(n, o.bytes)
		s.sendStatus(ss, o.id, stReceiving, n)
		return n
	})
	var vol volume
	var p plan
	// The tree is checked as soon as the metadata is whole: against the offer, the limits and this system's
	// names, before the content, which is nearly all of the item, is pulled.
	vol.check = func(v *kolofiles.Volume) error {
		if v.Len() != vol.meta.Len() {
			return ErrIncomplete // an entry the sender did not give a key for
		}
		var err error
		p, err = planCheckout(v, o.kind, lim)
		if errors.Is(err, ErrWrongKind) {
			return ErrOfferMismatch
		}
		if err != nil {
			return err
		}
		// The person agreed to this name and size: a different tree is refused.
		if p.top != o.name || p.files != o.files || p.nodes != o.nodes || p.bytes != o.bytes {
			return ErrOfferMismatch
		}
		return p.fits(folder)
	}
	defer func() {
		if vol.tree != nil {
			vol.tree.Close()
		}
	}()
	err = s.pullResuming(j, st, &vol, offered, &pulling, &cur, moved, set.ResumeFor, lim)
	stop()
	if err != nil {
		return "", err
	}
	var backend koloblobs.Backend = koloblobs.NewLogBackend(vol.content)
	if s.hooks.wrapContent != nil {
		backend = s.hooks.wrapContent(backend)
	}
	vol.late.b.Store(&backend)
	v := vol.tree
	s.update(j, func(t *Transfer) {
		t.State, t.Name, t.FilesTotal, t.BytesTotal, t.BytesDone, t.BytesPerSecond = stateVerifying, p.local, p.files, p.bytes, 0, 0
	})
	var written counter
	var files atomic.Int64
	stop = s.watch(j, func() int64 {
		n := int(files.Load())
		s.update(j, func(t *Transfer) { t.FilesDone = n })
		w := written.n.Load()
		s.sendStatus(ss, o.id, stVerifying, w)
		return w
	})
	savedTo, err := s.writer(folder).checkout(j.ctx, v, p, &written, &files)
	stop()
	if err != nil {
		return "", err
	}
	s.update(j, func(t *Transfer) { t.FilesDone, t.BytesDone = p.files, p.bytes })
	return savedTo, nil
}

// pullResuming pulls the volume into vol until it is whole, with the loop that receiveOffer describes. It
// returns the error that ends the receive, and nil when vol holds both logs.
func (s *ShareService) pullResuming(j *job, st *kolostore.Store, vol *volume, offered limits, pulling *atomic.Pointer[kolostore.Replica], cur *atomic.Pointer[context.CancelCauseFunc], moved *stallClock, resumeFor time.Duration, lim limits) error {
	ss, o := j.session, j.offer
	left := func() time.Duration { return resumeFor - moved.idle() }
	wait := resumeMin
	for {
		wctx, wcancel := context.WithTimeout(j.ctx, max(left(), 0))
		l, err := s.waitLink(ss, wctx)
		wcancel()
		if err != nil {
			if j.ctx.Err() != nil {
				return context.Cause(j.ctx)
			}
			return gaveUp(resumeFor)
		}
		changed := s.linkChanged(ss)
		pctx, cancel := context.WithCancelCause(j.ctx)
		stopLink := context.AfterFunc(l.ctx, func() { cancel(errLinkLost) })
		cur.Store(&cancel)
		before := moved.mark()
		err = pullVolume(pctx, l.conn, st, ss.peer, o.meta, offered, pulling, vol)
		cur.Store(nil)
		stopLink()
		cause := context.Cause(pctx)
		cancel(nil)
		if errors.Is(cause, ErrTooLarge) || errors.Is(cause, ErrTooManyEntries) {
			return cause
		}
		if err == nil {
			return nil
		}
		if j.ctx.Err() != nil {
			return context.Cause(j.ctx)
		}
		// The connection's end can reach its context after the pull's error.
		if l.ctx.Err() == nil && l.conn.Context().Err() == nil && explain(err, lim).Kind != kindLost {
			return err
		}
		s.update(j, func(t *Transfer) { t.Stalled, t.BytesPerSecond = true, 0 })
		if moved.mark() != before {
			wait = resumeMin
		}
		if left() <= 0 {
			return gaveUp(resumeFor)
		}
		timer := time.NewTimer(min(wait, left()))
		select {
		case <-timer.C:
		case <-changed: // the connection changed: try at once
		case <-j.ctx.Done():
		}
		timer.Stop()
		wait = min(2*wait, resumeMax)
	}
}

// gaveUp is the end of a receive that got nothing for resumeFor.
func gaveUp(resumeFor time.Duration) error {
	return shareErr("resume-gave-up", errLinkLost, humanDuration(resumeFor))
}

// SaveOffer saves the incoming offer id into its session's folder. It needs the session connected.
func (s *ShareService) SaveOffer(id string) error {
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil || j.t.Direction != "receive" || j.t.State != stateOffered {
		s.mu.Unlock()
		return shareErr("offer-gone", ErrOfferGone, "")
	}
	ss := j.session
	folder, live := ss.s.Folder, ss.link != nil && ss.link.live
	s.mu.Unlock()
	if !live {
		return shareErr("session-offline", ErrOffline, "")
	}
	if err := checkFolder(folder); err != nil {
		return err
	}
	select {
	case j.decide <- decision{save: true, folder: folder}:
		return nil
	default:
		return shareErr("offer-gone", ErrOfferGone, "")
	}
}

// DeclineOffer declines the incoming offer id, before its receive started.
func (s *ShareService) DeclineOffer(id string) error {
	s.mu.Lock()
	j := s.jobs[id]
	state := ""
	if j != nil && j.t.Direction == "receive" {
		state = j.t.State
	}
	s.mu.Unlock()
	switch state {
	case stateOffered:
		select {
		case j.decide <- decision{}:
			return nil
		default:
		}
	case stateQueued:
		j.cancel(errUserDeclined)
		return nil
	}
	return shareErr("offer-gone", ErrOfferGone, "")
}

// CancelTransfer stops the transfer id: a send is withdrawn, a receive stops and leaves nothing in the folder.
// An offer nobody answered yet is declined.
func (s *ShareService) CancelTransfer(id string) error {
	s.mu.Lock()
	j := s.jobs[id]
	s.mu.Unlock()
	switch {
	case j == nil:
	case j.t.Direction == "send":
		j.cancel(nil)
	case s.DeclineOffer(id) == nil:
	default:
		j.cancel(errUserCancelled)
	}
	return nil
}

// guardedSource is a log offered to kolo-sync until close: every read holds a read lock and close takes the
// write lock, so a store is never closed under a stream that reads it. One Serve serves every offer of a
// connection, so the guard is per offer, not per connection.
type guardedSource struct {
	mu     sync.RWMutex
	log    *kolostore.Log
	id     kololog.LogID
	header kololog.LogHeader
	closed bool
}

func newGuard(l *kolostore.Log) *guardedSource {
	return &guardedSource{log: l, id: l.ID(), header: l.Header()}
}

func (g *guardedSource) close() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
}

// read runs f under the read lock, unless the source is closed.
func read[T any](g *guardedSource, f func(*kolostore.Log) (T, error)) (T, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.closed {
		var zero T
		return zero, ErrClosed
	}
	return f(g.log)
}

func (g *guardedSource) ID() kololog.LogID         { return g.id }
func (g *guardedSource) Header() kololog.LogHeader { return g.header }
func (g *guardedSource) Head() (kololog.Head, error) {
	return read(g, func(l *kolostore.Log) (kololog.Head, error) { return l.Head() })
}
func (g *guardedSource) Record(i uint64) (kololog.Record, error) {
	return read(g, func(l *kolostore.Log) (kololog.Record, error) { return l.Record(i) })
}
func (g *guardedSource) Entry(i uint64) (kololog.Entry, error) {
	return read(g, func(l *kolostore.Log) (kololog.Entry, error) { return l.Entry(i) })
}
func (g *guardedSource) EntryInfo(i uint64) (kololog.EntryInfo, error) {
	return read(g, func(l *kolostore.Log) (kololog.EntryInfo, error) { return l.EntryInfo(i) })
}
func (g *guardedSource) Key(i uint64) (kololog.EntryKey, error) {
	return read(g, func(l *kolostore.Log) (kololog.EntryKey, error) { return l.Key(i) })
}
func (g *guardedSource) InclusionProof(i, size uint64) (kololog.InclusionProof, error) {
	return read(g, func(l *kolostore.Log) (kololog.InclusionProof, error) { return l.InclusionProof(i, size) })
}
func (g *guardedSource) ConsistencyProof(oldSize, newSize uint64) (kololog.ConsistencyProof, error) {
	return read(g, func(l *kolostore.Log) (kololog.ConsistencyProof, error) { return l.ConsistencyProof(oldSize, newSize) })
}
