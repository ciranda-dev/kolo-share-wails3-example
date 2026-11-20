package main

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	kololog "github.com/ciranda-dev/kolo-log"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
	kolosync "github.com/ciranda-dev/kolo-sync"
)

const (
	// maxSessions is kolo-swarm's default MaxTopics: each session joins one topic.
	maxSessions = 16
	// maxOpenOffers bounds the open offers of a session in each direction: kolo-sync serves MaxStreams streams of
	// a connection at once, so every open offer can be pulled without a busy answer.
	maxOpenOffers = kolosync.MaxStreams
	// maxReceives is how many items of a session are received at once. Each pull has its own streams and the disk
	// reservation (see spaceLedger) keeps them from promising the same room twice; more than that adds streams, not
	// speed, on a link that one pull already fills.
	maxReceives = 4
	// offerRate is how many offers a session takes per minute: kolo-invite's SourceRate, the stack's rate for the
	// requests of one source.
	offerRate = 60
	// byeWait bounds a goodbye: kolo-swarm's default HelloTimeout, one small exchange
	// on a live connection.
	byeWait = 10 * time.Second
	// prepEvery is the least time between two PREPAREs of one item that changed: two a second.
	prepEvery = 500 * time.Millisecond
	// prepKeepalive is how long a PREPARE can wait when nothing changed, so a receiver can tell a slow item from a
	// lost sender.
	prepKeepalive = 30 * time.Second
	// prepUpdateRate is how many updates of the cards of one session the app takes per minute: every open item
	// at its two a second.
	prepUpdateRate = maxOpenOffers * 120
	// A session topic that cannot be joined is tried again with the backoff of kolo-invite's Announce, from 1s to
	// 1 minute.
	joinRetryMin = time.Second
	joinRetryMax = time.Minute

	sessionTopicLabel = "kolo-share/session/v0"
	sessionTagLabel   = "kolo-share/session-tag/v0"
)

// Session states and the reasons a session ended.
const (
	sessConnecting   = "connecting"
	sessConnected    = "connected"
	sessReconnecting = "reconnecting"
	sessEnded        = "ended"

	endClosed     = "closed"
	endPeerClosed = "peer-closed"
	endReplaced   = "replaced"
	endAppClosed  = "app-closed"
	endProtocol   = "peer-protocol"
)

// Causes of the end of a transfer, from the peer or the person.
var (
	errDelivered     = errors.New("kolo-share: delivered")
	errPeerDeclined  = errors.New("kolo-share: the peer declined")
	errPeerCancelled = errors.New("kolo-share: the peer cancelled")
	errPeerFailed    = errors.New("kolo-share: the peer failed")
	errWithdrawn     = errors.New("kolo-share: the sender withdrew the offer")
	errUserDeclined  = errors.New("kolo-share: declined")
	errUserCancelled = errors.New("kolo-share: cancelled")
	errLinkLost      = errors.New("kolo-share: the connection was lost")
	errStale         = errors.New("kolo-share: a connection of another session")
	errPrepFailed    = errors.New("kolo-share: the sender could not prepare the item")
	errPrepStalled   = errors.New("kolo-share: nothing came from the sender while it prepared the item")
)

// A receive that lost its connection or stalled tries again after resumeMin, doubling the wait up to resumeMax;
// a pull that moved starts at resumeMin again. Tests shorten them.
var (
	resumeMin = time.Second
	resumeMax = 15 * time.Second
)

// prepStall is how long a card of an item being prepared waits for the next word of its sender.
var prepStall = 2 * time.Minute

// Session is a connection to one paired computer, the elements of the "share:sessions" event.
type Session struct {
	ID          string      `json:"id"`
	PeerName    string      `json:"peerName"`
	PeerKey     string      `json:"peerKey"`     // a label: the first 8 hex characters of the peer's key
	PeerID      string      `json:"peerId"`      // the peer's whole key in hex, to match its history
	Fingerprint string      `json:"fingerprint"` // 128 bits of the peer's key, to compare over a channel people trust
	State       string      `json:"state"`
	Reason      string      `json:"reason"` // why it ended
	Since       int64       `json:"since"`  // Unix milliseconds when State began
	Started     int64       `json:"started"`
	Folder      string      `json:"folder"` // where Save puts the peer's items
	AutoSave    bool        `json:"autoSave"`
	Waiting     int         `json:"waiting"`     // incoming offers waiting for an answer
	Unreachable bool        `json:"unreachable"` // the last dial to the peer failed
	Notice      *ShareError `json:"notice"`      // the last offer this app declined by itself
}

// session is an open or ended session. Every field that changes is guarded by ShareService.mu.
type session struct {
	s           Session
	id          string
	peer        ed25519.PublicKey
	topic       koloswarm.Topic
	tag         [sessionTagSize]byte
	ctx         context.Context // ends with ErrSessionEnded, and with it every transfer of the session
	cancel      context.CancelCauseFunc
	link        *link         // the current connection, nil while offline
	linkChanged chan struct{} // closed and replaced when link or its state changes
	sent        map[uint64]*job
	got         map[uint64]*job
	finals      map[uint64]uint64      // the last status of the peer's offers that ended, to answer a resend
	logs        map[kololog.LogID]bool // the logs of this side's open offers: the only ones the peer may pull
	autoLeft    int64                  // the bytes auto-save may still save: half the free space of the folder when it was turned on
	nextOut     uint64                 // the ID of this side's last offer
	peerPrep    bool                   // the peer said it reads PREPARE; it stays true for the session
	prepOut     map[uint64]*job        // this side's items being prepared that the peer knows, by transfer ID
	prepIn      map[uint64]*job        // the peer's items being prepared, by its transfer ID
	lastIn      uint64                 // the ID of the peer's last new offer
	rate        bucket                 // the peer's offers per minute
	announce    bucket                 // the peer's new items being prepared per minute
	progress    bucket                 // the peer's updates of them per minute
	slot        chan struct{}          // a place for each receive running now, maxReceives at most
	firstHello  chan struct{}          // closed at the first HELLO of the peer
	helloOnce   sync.Once
}

func (ss *session) ended() bool { return ss.s.State == sessEnded }

// link is one connection of a session: kolo-sync serves on it, and each side writes control messages on a
// unidirectional stream of its own.
type link struct {
	peer   *koloswarm.Peer
	conn   koloswarm.Conn  // direct or through a relay: the app uses both the same way
	ctx    context.Context // ends with the connection, or when a newer one replaces it
	cancel context.CancelFunc
	ready  chan struct{} // closed once HELLO, the open offers and SYNCED are written
	live   bool          // the peer's HELLO arrived, guarded by ShareService.mu
	outMu  sync.Mutex
	out    ctlStream
	// Only the reader goroutine uses these.
	hello  bool
	synced bool
	resent map[uint64]bool
	// resentPrep is the transfer IDs of the items being prepared that the peer said before SYNCED.
	resentPrep map[uint64]bool
}

// ctlStream is the sending end of a control stream.
type ctlStream interface {
	Write(p []byte) (int, error)
	SetWriteDeadline(t time.Time) error
}

// bucket is a token bucket of size rate, refilled at rate tokens per minute, as kolo-invite's limit.go.
type bucket struct {
	tokens float64
	last   time.Time
}

// take spends one token if there is one.
func (b *bucket) take(now time.Time, rate int) bool {
	if b.last.IsZero() {
		b.tokens = float64(rate)
	} else if now.After(b.last) {
		b.tokens = min(float64(rate), b.tokens+float64(now.Sub(b.last))*float64(rate)/float64(time.Minute))
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sessionKeys derives the topic and the tag of a session from its secret, one-way, so the network sees neither
// the secret nor the tag.
func sessionKeys(secret [sessionSecretSize]byte) (koloswarm.Topic, [sessionTagSize]byte) {
	topic := sha256.Sum256(append([]byte(sessionTopicLabel), secret[:]...))
	sum := sha256.Sum256(append([]byte(sessionTagLabel), secret[:]...))
	var tag [sessionTagSize]byte
	copy(tag[:], sum[:])
	return koloswarm.Topic(topic), tag
}

// newSession opens the session with peer that a pairing made: both apps join the topic of secret on their main
// swarm, announce and look it up, and admit only each other. An open session with the same key ends first.
func (s *ShareService) newSession(peer ed25519.PublicKey, name string, secret [sessionSecretSize]byte) (*session, error) {
	topic, tag := sessionKeys(secret)
	clear(secret[:])
	s.mu.Lock()
	if err := s.canStartLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	old := s.byKey[string(peer)]
	n := s.openSessionsLocked()
	if old != nil {
		n--
	}
	if n >= maxSessions {
		s.mu.Unlock()
		return nil, shareErr("busy-sessions", ErrBusy, strconv.Itoa(n))
	}
	s.nextSession++
	ctx, cancel := context.WithCancelCause(s.ctx)
	now := time.Now().UnixMilli()
	ss := &session{
		id: strconv.FormatUint(s.nextSession, 10), peer: peer, topic: topic, tag: tag, ctx: ctx, cancel: cancel,
		linkChanged: make(chan struct{}), sent: map[uint64]*job{}, got: map[uint64]*job{}, prepOut: map[uint64]*job{}, prepIn: map[uint64]*job{}, finals: map[uint64]uint64{},
		logs: map[kololog.LogID]bool{}, slot: make(chan struct{}, maxReceives), firstHello: make(chan struct{}),
	}
	ss.s = Session{ID: ss.id, PeerName: peerName(name, peer), PeerKey: shortKey(peer), PeerID: hex.EncodeToString(peer),
		Fingerprint: fingerprint(peer), State: sessConnecting, Since: now, Started: now, Folder: downloadsFolder()}
	s.sessions[ss.id] = ss
	s.mu.Unlock()
	if old != nil {
		s.endSession(old, endReplaced, false) // leaves its topic first, so the connection belongs to one session
	}
	s.mu.Lock()
	s.byKey[string(peer)] = ss
	s.mu.Unlock()
	s.emitSessions()
	s.goTask(func() { s.joinSession(ss) })
	return ss, nil
}

// joinSession joins the session's topic, trying again with backoff while the session is open.
func (s *ShareService) joinSession(ss *session) {
	delay := joinRetryMin
	for {
		s.mu.Lock()
		sw := s.sw
		s.mu.Unlock()
		if sw == nil {
			return
		}
		err := sw.Join(ss.ctx, ss.topic, &koloswarm.JoinOptions{Announce: true, Lookup: true, MaxPeers: 1})
		if err == nil {
			if ss.ctx.Err() != nil {
				sw.Leave(ss.topic)
			}
			return
		}
		if ss.ctx.Err() != nil {
			return
		}
		log.Print("session topic: ", err)
		select {
		case <-time.After(delay):
		case <-ss.ctx.Done():
			return
		}
		delay = min(2*delay, joinRetryMax)
	}
}

// goTask runs f in a goroutine that ServiceShutdown waits for; false when the app is closing and f did not run.
func (s *ShareService) goTask(f func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return false
	}
	s.wg.Go(f)
	return true
}

// sessionOf returns the open session of key, nil for any other key: the main swarm admits only those.
func (s *ShareService) sessionOf(key ed25519.PublicKey) *session {
	if len(key) != ed25519.PublicKeySize {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byKey[string(key)]
}

// authorize gives a peer the logs of the open offers of its own session, and nothing else.
func (s *ShareService) authorize(peer, owner ed25519.PublicKey, logID kololog.LogID) kolosync.Access {
	if s.id == nil || !owner.Equal(s.id.PublicKey()) {
		return kolosync.NoAccess
	}
	ss := s.sessionOf(peer)
	if ss == nil {
		return kolosync.NoAccess
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss.ended() || !ss.logs[logID] {
		return kolosync.NoAccess
	}
	return kolosync.Keys
}

func (s *ShareService) openSessionsLocked() int {
	n := 0
	for _, ss := range s.sessions {
		if !ss.ended() {
			n++
		}
	}
	return n
}

func (s *ShareService) openSessionListLocked() []*session {
	var out []*session
	for _, ss := range s.sessions {
		if !ss.ended() {
			out = append(out, ss)
		}
	}
	return out
}

// Sessions returns the sessions, open and ended, oldest first.
func (s *ShareService) Sessions() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionListLocked()
}

func (s *ShareService) sessionListLocked() []Session {
	out := []Session{}
	for _, ss := range s.sessions {
		v := ss.s
		for _, j := range ss.got {
			if j.t.State == stateOffered {
				v.Waiting++
			}
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b Session) int {
		x, _ := strconv.ParseUint(a.ID, 10, 64)
		y, _ := strconv.ParseUint(b.ID, 10, 64)
		return int(x) - int(y)
	})
	return out
}

// emitSessions tells the window the whole list; sessMu keeps the events in order.
func (s *ShareService) emitSessions() {
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	list := s.Sessions()
	s.emit("share:sessions", list)
	s.pushTray()
}

func (s *ShareService) session(id string) (*session, error) {
	ss := s.sessions[id]
	if ss == nil || ss.ended() {
		return nil, shareErr("session-ended", ErrSessionEnded, "")
	}
	return ss, nil
}

// CloseSession ends the session id: it says goodbye to the peer in the background and stops every transfer of
// the session. On a session that already ended, it removes it from the list.
func (s *ShareService) CloseSession(id string) error {
	s.mu.Lock()
	ss := s.sessions[id]
	if ss != nil && ss.ended() {
		delete(s.sessions, id)
		s.mu.Unlock()
		s.emitSessions()
		return nil
	}
	s.mu.Unlock()
	if ss != nil {
		s.endSession(ss, endClosed, true)
	}
	return nil
}

// dropSession ends ss and removes it from the list, for a session whose pairing did not complete.
func (s *ShareService) dropSession(ss *session) {
	s.endSession(ss, endClosed, false)
	s.mu.Lock()
	delete(s.sessions, ss.id)
	s.mu.Unlock()
	s.emitSessions()
}

// SetSessionFolder sets where Save puts the items of the session's peer.
func (s *ShareService) SetSessionFolder(id, folder string) (Session, error) {
	if err := checkFolder(folder); err != nil {
		return Session{}, err
	}
	return s.changeSession(id, func(v *Session) error { v.Folder = folder; return nil })
}

// SetAutoSave turns on or off saving every item the peer offers from now on into the session's folder, without
// asking. It is off at the start of every session and never saved to disk.
func (s *ShareService) SetAutoSave(id string, on bool) (Session, error) {
	return s.changeSession(id, func(v *Session) error {
		if on {
			if err := checkFolder(v.Folder); err != nil {
				return err
			}
		}
		v.AutoSave = on
		if on {
			s.sessions[id].autoLeft = s.autoBudget(v.Folder)
		}
		return nil
	})
}

// autoBudget is how much auto-save may save into folder: half of its free space now, so a run of items that each
// fit cannot fill the disk. When the free space cannot be read there is no bound but the disk itself.
func (s *ShareService) autoBudget(folder string) int64 {
	free := s.hooks.freeSpace
	if free == nil {
		free = diskFree
	}
	n, err := free(folder)
	if err != nil {
		return math.MaxInt64
	}
	return max(n, 0) / 2
}

func (s *ShareService) changeSession(id string, f func(v *Session) error) (Session, error) {
	s.mu.Lock()
	ss, err := s.session(id)
	if err == nil {
		err = f(&ss.s)
	}
	var v Session
	if err == nil {
		v = ss.s
	}
	s.mu.Unlock()
	if err != nil {
		return Session{}, err
	}
	s.emitSessions()
	return v, nil
}

// endSession ends ss for reason: its transfers stop, its peer is no longer admitted, and it leaves its topic. With
// bye, a connected peer is told first, and the end waits at most byeWait for the peer to close the connection,
// since closing it here could drop the goodbye.
func (s *ShareService) endSession(ss *session, reason string, bye bool) {
	s.mu.Lock()
	if ss.ended() {
		s.mu.Unlock()
		return
	}
	ss.s.State, ss.s.Reason, ss.s.Since, ss.s.Unreachable = sessEnded, reason, time.Now().UnixMilli(), false
	if s.byKey[string(ss.peer)] == ss {
		delete(s.byKey, string(ss.peer))
	}
	l, sw := ss.link, s.sw
	s.changedLocked(ss)
	s.mu.Unlock()
	ss.cancel(ErrSessionEnded)
	s.emitSessions()
	leave := func() {
		if bye && l != nil {
			ctx, cancel := context.WithTimeout(context.Background(), byeWait)
			if l.writeBefore(ctx, ctlMsg{typ: ctlBye}) == nil {
				select {
				case <-l.conn.Context().Done():
				case <-ctx.Done():
				}
			}
			cancel()
		}
		if sw != nil {
			sw.Leave(ss.topic)
		}
		if l != nil {
			l.cancel()
			l.conn.Close()
		}
	}
	if !bye || !s.goTask(leave) {
		leave()
	}
}

// changedLocked wakes whoever waits for a change of ss's connection.
func (s *ShareService) changedLocked(ss *session) {
	close(ss.linkChanged)
	ss.linkChanged = make(chan struct{})
}

// attach makes p the session's connection: kolo-sync serves on it, and the control streams start.
func (s *ShareService) attach(ss *session, p *koloswarm.Peer) {
	conn := p.Conn()
	// Not under the session's context, so a goodbye can still be written after the session ended.
	ctx, cancel := context.WithCancel(s.ctx)
	stop := context.AfterFunc(conn.Context(), cancel)
	l := &link{peer: p, conn: conn, ctx: ctx, cancel: func() { stop(); cancel() }, ready: make(chan struct{}), resent: map[uint64]bool{}, resentPrep: map[uint64]bool{}}
	s.mu.Lock()
	if ss.ended() {
		s.mu.Unlock()
		l.cancel()
		conn.Close()
		return
	}
	old := ss.link
	ss.link = l
	s.changedLocked(ss)
	s.mu.Unlock()
	if old != nil {
		old.cancel()
	}
	s.linkWG.Go(func() { s.srv.Serve(ctx, conn) })
	s.linkWG.Go(func() { s.readControl(ss, l) })
	s.linkWG.Go(func() { s.writeHello(ss, l) })
}

// detach forgets p when it is the session's connection. A replaced connection (ErrDuplicate) is followed by its
// successor at once, so the session stays connected for it.
func (s *ShareService) detach(ss *session, p *koloswarm.Peer, why error) {
	s.mu.Lock()
	l := ss.link
	if l == nil || l.peer != p {
		s.mu.Unlock()
		return
	}
	ss.link = nil
	if !ss.ended() && !errors.Is(why, koloswarm.ErrDuplicate) {
		ss.s.State, ss.s.Since = sessReconnecting, time.Now().UnixMilli()
	}
	s.changedLocked(ss)
	s.mu.Unlock()
	l.cancel()
	s.emitSessions()
}

func (s *ShareService) markUnreachable(ss *session) {
	s.mu.Lock()
	changed := !ss.ended() && !ss.s.Unreachable
	if changed {
		ss.s.Unreachable = true
	}
	s.mu.Unlock()
	if changed {
		s.emitSessions()
	}
}

// linkChanged is the channel that closes when ss's connection or its state changes.
func (s *ShareService) linkChanged(ss *session) <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ss.linkChanged
}

// waitLink waits until the session is connected and returns its connection.
func (s *ShareService) waitLink(ss *session, ctx context.Context) (*link, error) {
	for {
		s.mu.Lock()
		l, changed := ss.link, ss.linkChanged
		live := l != nil && l.live
		s.mu.Unlock()
		if live {
			return l, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

// writeHello opens this side's control stream and writes HELLO, every open offer of this side and SYNCED, before
// any other message.
func (s *ShareService) writeHello(ss *session, l *link) {
	defer close(l.ready)
	out, err := l.conn.OpenUniStream(l.ctx)
	if err != nil {
		return
	}
	l.outMu.Lock()
	defer l.outMu.Unlock()
	l.out = out
	deadline := time.Now().Add(kolosync.Timeout)
	err = l.writeLocked(ctlMsg{typ: ctlHello, tag: ss.tag, name: s.displayName()}, deadline)
	s.mu.Lock()
	var preps []prepare
	var open []ctlMsg
	for _, j := range ss.prepOut {
		preps = append(preps, j.prep)
	}
	for _, j := range ss.sent {
		if j.prepID != 0 {
			open = append(open, ctlMsg{typ: ctlReady, prep: prepare{id: j.prepID}, offer: j.offer})
		}
		open = append(open, ctlMsg{typ: ctlOffer, offer: j.offer})
	}
	s.mu.Unlock()
	slices.SortFunc(preps, func(a, b prepare) int { return cmp.Compare(a.id, b.id) })
	// An offer's READY comes first: the sort keeps each pair together by the offer's ID, READY before OFFER.
	slices.SortStableFunc(open, func(a, b ctlMsg) int { return cmp.Compare(a.offer.id, b.offer.id) })
	for _, p := range preps {
		if err == nil {
			err = l.writeLocked(ctlMsg{typ: ctlPrepare, prep: p}, deadline)
		}
	}
	for _, m := range open {
		if err == nil {
			err = l.writeLocked(m, deadline)
		}
	}
	if err == nil {
		err = l.writeLocked(ctlMsg{typ: ctlSynced}, deadline)
	}
	if err != nil {
		l.conn.CloseWithError(0, "")
		return
	}
	s.writeFeatures(l)
}

// writeFeatures says what this app reads, in one frame on a second unidirectional stream: an app that does not
// know the stream refuses it without reading, and then never gets a message it cannot read.
func (s *ShareService) writeFeatures(l *link) {
	if s.hooks.oldPeer {
		return
	}
	st, err := l.conn.OpenUniStream(l.ctx)
	if err != nil {
		return
	}
	st.SetWriteDeadline(time.Now().Add(kolosync.Timeout))
	writeBody(st, encodeFeatures(featPrepare))
	st.Close()
}

func (l *link) writeLocked(m ctlMsg, deadline time.Time) error {
	l.out.SetWriteDeadline(deadline)
	return writeFrame(l.out, m)
}

// write sends m after the opening messages. A peer that does not read for kolosync.Timeout loses the connection,
// so a writer is never held longer.
func (l *link) write(m ctlMsg) {
	ctx, cancel := context.WithTimeout(l.ctx, kolosync.Timeout)
	defer cancel()
	l.writeBefore(ctx, m)
}

func (l *link) writeBefore(ctx context.Context, m ctlMsg) error {
	select {
	case <-l.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	l.outMu.Lock()
	defer l.outMu.Unlock()
	if l.out == nil {
		return ErrOffline
	}
	deadline, _ := ctx.Deadline()
	err := l.writeLocked(m, deadline)
	if err != nil && l.ctx.Err() == nil {
		l.conn.CloseWithError(0, "")
	}
	return err
}

// sendCtl writes m on the session's current connection; nothing when it is offline, since each side resends
// what matters after a reconnect.
func (s *ShareService) sendCtl(ss *session, m ctlMsg) {
	s.mu.Lock()
	l := ss.link
	s.mu.Unlock()
	if l != nil {
		l.write(m)
	}
}

func (s *ShareService) sendStatus(ss *session, id, state uint64, done int64) {
	s.sendCtl(ss, ctlMsg{typ: ctlStatus, offer: offer{id: id}, state: state, done: done})
}

// readControl reads the peer's control stream, the first unidirectional stream it opens; later ones are refused.
// A message that breaks the protocol ends the session.
func (s *ShareService) readControl(ss *session, l *link) {
	in, err := l.conn.AcceptUniStream(l.ctx)
	if err != nil {
		return
	}
	s.linkWG.Go(func() {
		for first := true; ; first = false {
			extra, err := l.conn.AcceptUniStream(l.ctx)
			if err != nil {
				return
			}
			if first && !s.hooks.oldPeer {
				s.readFeatures(ss, extra)
			} else {
				extra.CancelRead(0)
			}
		}
	})
	for {
		body, err := readFrame(in)
		var m ctlMsg
		if err == nil {
			m, err = decodeControl(body)
		}
		if err == nil && s.hooks.oldPeer && m.typ > ctlBye {
			err = protoErr("message type", nil) // what an app of an older version does
		}
		if err == nil {
			err = s.handle(ss, l, m)
		}
		switch {
		case err == nil:
			continue
		case errors.Is(err, ErrPeerProtocol) && l.ctx.Err() == nil:
			log.Print("session ", ss.id, ": ", err)
			s.endSession(ss, endProtocol, false)
			l.conn.CloseWithError(0, "")
		case errors.Is(err, errStale):
			l.conn.CloseWithError(0, "")
		}
		return
	}
}

// readFeatures reads the one frame of the peer's features stream, and nothing else from it. A frame that is
// wrong only means the peer gets none of the features.
func (s *ShareService) readFeatures(ss *session, in readStream) {
	defer in.CancelRead(0)
	in.SetReadDeadline(time.Now().Add(kolosync.Timeout))
	body, err := readFrame(in)
	if err != nil {
		return
	}
	if bits, err := decodeFeatures(body); err == nil && bits&featPrepare != 0 {
		s.mu.Lock()
		ss.peerPrep = true
		s.mu.Unlock()
	}
}

// readStream is the receiving end of a stream.
type readStream interface {
	Read(p []byte) (int, error)
	SetReadDeadline(t time.Time) error
	CancelRead(code uint64) error
}

// handle applies one message of the peer.
func (s *ShareService) handle(ss *session, l *link, m ctlMsg) error {
	if !l.hello {
		if m.typ != ctlHello {
			return protoErr("a message before HELLO", nil)
		}
		l.hello = true
		return s.gotHello(ss, l, m)
	}
	switch m.typ {
	case ctlHello:
		return protoErr("a second HELLO", nil)
	case ctlOffer:
		return s.gotOffer(ss, l, m.offer)
	case ctlWithdraw:
		s.mu.Lock()
		j := ss.got[m.offer.id]
		s.mu.Unlock()
		if j != nil {
			j.cancel(errWithdrawn)
		}
	case ctlStatus:
		s.gotStatus(ss, m)
	case ctlPrepare:
		return s.gotPrepare(ss, l, m.prep)
	case ctlReady:
		return s.gotReady(ss, l, m.prep.id, m.offer.id)
	case ctlPrepEnd:
		s.gotPrepEnd(ss, m.prep.id, m.state)
	case ctlSynced:
		s.gotSynced(ss, l)
	case ctlBye:
		s.endSession(ss, endPeerClosed, false)
		return errStale
	}
	return nil
}

// gotHello makes the session connected; a HELLO with another session's tag is a connection of an older session
// with this peer, and is closed.
func (s *ShareService) gotHello(ss *session, l *link, m ctlMsg) error {
	if m.tag != ss.tag {
		return errStale
	}
	s.mu.Lock()
	if ss.link != l || ss.ended() {
		s.mu.Unlock()
		return errStale
	}
	l.live = true
	ss.s.State, ss.s.Since, ss.s.Unreachable = sessConnected, time.Now().UnixMilli(), false
	if tidyName(m.name) != "" {
		ss.s.PeerName = peerName(m.name, ss.peer)
	}
	s.changedLocked(ss)
	s.mu.Unlock()
	ss.helloOnce.Do(func() { close(ss.firstHello) })
	s.emitSessions()
	return nil
}

// gotStatus applies the receiver's answer to one of this side's offers. Its bytes are shown, clamped to the
// offer's, and never trusted.
func (s *ShareService) gotStatus(ss *session, m ctlMsg) {
	s.mu.Lock()
	j := ss.sent[m.offer.id]
	s.mu.Unlock()
	if j == nil {
		return // it can cross a WITHDRAW
	}
	switch m.state {
	case stAccepted, stReceiving, stVerifying:
		s.update(j, func(t *Transfer) {
			if t.State == stateOffered || t.State == stateSending {
				t.State, t.BytesDone = stateSending, min(max(m.done, 0), t.BytesTotal)
			}
		})
	case stDone:
		j.cancel(errDelivered)
	case stDeclined:
		j.cancel(errPeerDeclined)
	case stCancelled:
		j.cancel(errPeerCancelled)
	case stFailed:
		j.cancel(errPeerFailed)
	}
}

// gotSynced ends what the peer no longer offers after a reconnect: an offer it did not send again was withdrawn
// while the session was offline.
func (s *ShareService) gotSynced(ss *session, l *link) {
	if l.synced {
		return
	}
	l.synced = true
	s.mu.Lock()
	var gone []*job
	for id, j := range ss.got {
		if !l.resent[id] {
			gone = append(gone, j)
		}
	}
	for id, j := range ss.prepIn {
		if !l.resentPrep[id] {
			gone = append(gone, j) // the sender ended it while the session was offline
			delete(ss.prepIn, id)
		}
	}
	for id := range ss.finals {
		if !l.resent[id] {
			delete(ss.finals, id)
		}
	}
	s.mu.Unlock()
	l.resent, l.resentPrep = nil, nil
	for _, j := range gone {
		j.cancel(errWithdrawn)
	}
}
