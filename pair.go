package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	kolocodec "github.com/ciranda-dev/kolo-codec"
	koloid "github.com/ciranda-dev/kolo-id"
	invite "github.com/ciranda-dev/kolo-invite"
	kolostore "github.com/ciranda-dev/kolo-store"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
)

const (
	joinTag           = "kolo-share/join/v0"
	pairTag           = "kolo-share/pair/v0"
	sessionSecretSize = 32
	oldCodePrefix     = "kolo-share:"
	// maxCodeText bounds the pasted text JoinPairing looks at, as v0.1.2 bounded a share code; a pairing code
	// is 92 characters.
	maxCodeText = 1024
	// The join data and the pairing payload hold a tag, keys and a display name; their bounds follow from the fields.
	maxJoinData    = 2*binary.MaxVarintLen64 + len(joinTag) + maxNameBytes
	maxPairPayload = 2*binary.MaxVarintLen64 + len(pairTag) + ed25519.PublicKeySize + sessionSecretSize + maxNameBytes
)

// Pairing roles and states.
const (
	roleInviter = "inviter"
	roleJoiner  = "joiner"

	pairStarting  = "starting"
	pairWaiting   = "waiting"
	pairLooking   = "looking"
	pairJoining   = "joining"
	pairConnected = "connected"
	pairExpired   = "expired"
	pairCancelled = "cancelled"
	pairFailed    = "failed"
)

// Pairing is the pairing in progress, the payload of the "share:pairing" event. Code is the pairing code, a
// secret: only the main window gets it.
type Pairing struct {
	ID        string      `json:"id"`
	Role      string      `json:"role"`
	State     string      `json:"state"`
	Code      string      `json:"code"`
	Expires   int64       `json:"expires"` // Unix milliseconds, while an inviter waits
	Error     *ShareError `json:"error"`
	SessionID string      `json:"sessionId"`
}

func (p Pairing) final() bool {
	switch p.State {
	case pairConnected, pairExpired, pairCancelled, pairFailed:
		return true
	}
	return false
}

// pairing is a running pairing: one at a time per app.
type pairing struct {
	p        Pairing // guarded by ShareService.mu
	ctx      context.Context
	cancel   context.CancelFunc
	inviteID invite.ID // inviter: its invite, so pasting the code here is refused
}

// CreatePairing makes a pairing code for another computer to paste. It returns at once in state "starting";
// the code comes in a "share:pairing" event in state "waiting". The code works once, until -pair-ttl, Cancel or
// quit, and is never written to disk.
func (s *ShareService) CreatePairing(ctx context.Context) (Pairing, error) {
	if err := s.networkUsable(); err != nil {
		return Pairing{}, err
	}
	pr, err := s.newPairing(roleInviter, pairStarting)
	if err != nil {
		return Pairing{}, err
	}
	go func() {
		defer s.wg.Done()
		s.endPairing(pr, s.runInviter(pr))
	}()
	return s.Pairing(ctx), nil
}

// JoinPairing pastes the pairing code of another computer. White space anywhere in text is ignored. It returns
// at once in state "looking"; the session it opens comes in "share:pairing" (state "connected") and
// "share:sessions".
func (s *ShareService) JoinPairing(text string) (Pairing, error) {
	code, err := parsePairingCode(text)
	if err != nil {
		return Pairing{}, explain(err, limits{})
	}
	s.mu.Lock()
	own := s.pairing != nil && s.pairing.p.Role == roleInviter && s.pairing.inviteID == code.ID()
	s.mu.Unlock()
	if own {
		return Pairing{}, explain(ErrOwnCode, limits{})
	}
	if err := s.networkUsable(); err != nil {
		return Pairing{}, err
	}
	pr, err := s.newPairing(roleJoiner, pairLooking)
	if err != nil {
		return Pairing{}, err
	}
	go func() {
		defer s.wg.Done()
		s.endPairing(pr, s.runJoiner(pr, code))
	}()
	return s.Pairing(context.Background()), nil
}

// parsePairingCode reads a pasted code. A share code of 0.1.2 and earlier gets its own error.
func parsePairingCode(text string) (invite.Code, error) {
	if len(text) > maxCodeText {
		return invite.Code{}, invite.ErrInvalidCode
	}
	text = strings.Join(strings.Fields(text), "")
	if strings.HasPrefix(strings.ToLower(text), oldCodePrefix) {
		return invite.Code{}, ErrOldCode
	}
	return invite.Parse(text)
}

// CancelPairing stops the pairing id: an inviter's code stops working, a joiner stops looking.
func (s *ShareService) CancelPairing(id string) {
	s.mu.Lock()
	pr := s.pairing
	s.mu.Unlock()
	if pr != nil && pr.p.ID == id {
		pr.cancel()
	}
}

// Pairing returns the pairing in progress; its code only to the main window.
func (s *ShareService) Pairing(ctx context.Context) Pairing {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pairing == nil {
		return Pairing{}
	}
	p := s.pairing.p
	if !fromMainWindow(ctx) {
		p.Code = ""
	}
	return p
}

// newPairing registers a pairing, refused while another runs or when the sessions are at their limit. The caller
// runs it in a goroutine that calls s.wg.Done.
func (s *ShareService) newPairing(role, state string) (*pairing, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.canStartLocked(); err != nil {
		return nil, err
	}
	if s.pairing != nil {
		return nil, shareErr("busy-pairing", ErrBusy, "")
	}
	if n := s.openSessionsLocked(); n >= maxSessions {
		return nil, shareErr("busy-sessions", ErrBusy, strconv.Itoa(n))
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.nextPairing++
	pr := &pairing{ctx: ctx, cancel: cancel, p: Pairing{ID: strconv.FormatUint(s.nextPairing, 10), Role: role, State: state}}
	s.pairing = pr
	s.wg.Add(1)
	return pr, nil
}

// canStartLocked refuses new work before Start, after ServiceShutdown and while the network restarts.
func (s *ShareService) canStartLocked() error {
	switch {
	case !s.running && s.ctx == nil:
		return explain(ErrNotReady, limits{})
	case !s.running:
		return shareErr("app-closed", ErrClosed, "")
	case s.restarting:
		return shareErr("busy-restarting", ErrBusy, "")
	}
	return nil
}

// updatePairing changes pr with f and tells the window, unless pr already ended.
func (s *ShareService) updatePairing(pr *pairing, f func(p *Pairing)) {
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	s.mu.Lock()
	if pr.p.final() {
		s.mu.Unlock()
		return
	}
	f(&pr.p)
	p := pr.p
	if p.final() && s.pairing == pr {
		s.pairing = nil
	}
	s.mu.Unlock()
	s.emit("share:pairing", p)
	s.sendNotch()
	s.pushTray()
	if p.final() {
		s.clearCopiedCode()
	}
}

// endPairing puts pr in its final state for err; a pairing that connected is final already.
func (s *ShareService) endPairing(pr *pairing, err error) {
	pr.cancel()
	s.updatePairing(pr, func(p *Pairing) {
		p.Code, p.Expires = "", 0
		switch {
		case err == nil, errors.Is(err, context.Canceled):
			p.State = pairCancelled
		case errors.Is(err, invite.ErrExpired):
			p.State, p.Error = pairExpired, explain(err, limits{})
		default:
			p.State, p.Error = pairFailed, explain(err, limits{})
		}
	})
}

// pairingSwarm opens a swarm of its own for one pairing, on the listen IP with a free port, as id: kolo-invite
// and kolo-sync each need to be the only reader of a connection's streams, so a pairing never runs on the
// session swarm. A dial that fails is tried again within a lookup interval, not kolo-swarm's 10 minutes at most: the
// joiner has FindTimeout and the code a short life, so a long backoff would give up on a peer that is there.
func (s *ShareService) pairingSwarm(ctx context.Context, id *koloid.Identity, maxPeers int, allow func(ed25519.PublicKey) bool) (*koloswarm.Swarm, error) {
	set := s.settingsNow()
	listen := netip.MustParseAddrPort(set.Listen)
	cfg := &koloswarm.Config{
		BootstrapNodes: set.nodes,
		LookupInterval: set.LookupInterval,
		RetryMax:       set.LookupInterval,
		MaxPeers:       maxPeers,
		Allow:          func(key ed25519.PublicKey, _ netip.AddrPort) bool { return allow(key) },
		Relays:         set.relays,
		DHT:            s.hooks.dht,
	}
	if s.hooks.pairCfg != nil {
		s.hooks.pairCfg(cfg)
	}
	sw, err := koloswarm.New(ctx, netip.AddrPortFrom(listen.Addr(), 0).String(), id, cfg)
	if err != nil {
		return nil, shareErr("network-open", err, "")
	}
	if err := sw.Bootstrap(ctx, nil); err != nil {
		sw.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if set.mode == modePublic {
			return nil, shareErr("public-unreachable", err, "")
		}
		return nil, shareErr("entry-nodes-unreachable", err, "")
	}
	return sw, nil
}

// runInviter makes the code, announces it on a pairing swarm with an identity made for this pairing, and answers
// the first request it accepts with this app's key and a new session secret. It returns nil once the session
// exists, and keeps the pairing swarm until the session connects or the joiner's FindTimeout is over, since the
// answer may still be on its way.
func (s *ShareService) runInviter(pr *pairing) error {
	ctx := pr.ctx
	if _, err := s.waitNetwork(ctx); err != nil {
		return err
	}
	pid, err := koloid.Generate()
	if err != nil {
		return err
	}
	in, err := invite.Open(kolostore.NewMemory(), pid, &invite.Options{MaxData: maxJoinData})
	if err != nil {
		return err
	}
	defer in.Close()
	sw, err := s.pairingSwarm(ctx, pid, 0, func(ed25519.PublicKey) bool { return true })
	if err != nil {
		return err
	}
	// loop ends the pairing swarm's work: at Cancel or quit, or once the session connects.
	loop, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer s.forgetRelays(swarmPairing + pr.p.ID)
	defer wg.Wait()
	defer sw.Close()
	defer stop()
	wg.Go(func() { in.Announce(loop, sw) })
	wg.Go(func() {
		for {
			ev, err := sw.Next(loop)
			if err != nil {
				return
			}
			switch ev.Kind {
			case koloswarm.Connected:
				p := ev.Peer
				wg.Go(func() { in.ServePeer(loop, p) })
			case koloswarm.RelayChanged:
				s.relayChanged(swarmPairing+pr.p.ID, ev)
			}
		}
	})
	set := s.settingsNow()
	text, info, err := in.Create(&invite.CreateOptions{TTL: set.PairTTL, MaxUses: 1, Manual: true})
	if err != nil {
		return err
	}
	s.mu.Lock()
	pr.inviteID = info.ID
	s.mu.Unlock()
	s.updatePairing(pr, func(p *Pairing) { p.State, p.Code, p.Expires = pairWaiting, text, info.Expires.UnixMilli() })
	// After the pairing, the loop keeps answering until the session connects or the joiner's FindTimeout is over:
	// kolo-invite gives a joiner that asks again the same answer, and refuses any other.
	paired := false
	for {
		ev, err := in.Next(loop)
		if err != nil {
			if paired {
				return nil
			}
			return err
		}
		switch {
		case ev.Kind == invite.Ended && ev.State == invite.Expired && !paired:
			return invite.ErrExpired
		case ev.Kind == invite.Requested:
			ss, err := s.answer(in, ev)
			if err != nil && !paired {
				return err
			}
			if ss == nil || paired {
				continue
			}
			paired = true
			s.updatePairing(pr, func(p *Pairing) { p.State, p.Code, p.Expires, p.SessionID = pairConnected, "", 0, ss.id })
			wg.Go(func() {
				t := time.NewTimer(set.FindTimeout)
				defer t.Stop()
				select {
				case <-ss.firstHello:
				case <-ss.ctx.Done():
				case <-t.C:
				case <-loop.Done():
				}
				stop()
			})
		}
	}
}

// approver answers one request; *invite.Inviter is one, tests play another.
type approver interface {
	Approve(req invite.RequestID, payload []byte) error
	Deny(req invite.RequestID) error
}

// answer opens the session first and then approves the request with this app's key and the new session secret,
// so an approval never leaves the joiner with a session this side could not open; a failed approval removes the
// session again. It denies this app's own key, join data it cannot read, and a request it cannot take (the
// session limit, the app closing). A nil session means: keep waiting.
func (s *ShareService) answer(in approver, ev invite.Event) (*session, error) {
	name, err := parseJoinData(ev.Data)
	if err != nil || ev.Joiner.Equal(s.id.PublicKey()) {
		in.Deny(ev.Request)
		return nil, nil
	}
	var secret [sessionSecretSize]byte
	rand.Read(secret[:])
	payload := pairPayload(s.id.PublicKey(), secret, s.displayName())
	ss, err := s.newSession(ev.Joiner, name, secret)
	clear(secret[:])
	if err != nil {
		in.Deny(ev.Request)
		return nil, nil
	}
	if err := in.Approve(ev.Request, payload); err != nil {
		s.dropSession(ss)
		if errors.Is(err, invite.ErrNotFound) {
			return nil, nil // the joiner left
		}
		return nil, err
	}
	return ss, nil
}

// runJoiner looks for the inviter of code on a pairing swarm that admits only the code's key, asks to join with
// this app's identity and opens the session the answer gives.
func (s *ShareService) runJoiner(pr *pairing, code invite.Code) error {
	ctx := pr.ctx
	if _, err := s.waitNetwork(ctx); err != nil {
		return err
	}
	eid, err := koloid.Generate()
	if err != nil {
		return err
	}
	sw, err := s.pairingSwarm(ctx, eid, 1, code.Matches)
	if err != nil {
		return err
	}
	defer sw.Close()
	if err := sw.Join(ctx, code.Topic(), &koloswarm.JoinOptions{Lookup: true}); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return shareErr("lookup-failed", err, "")
	}
	set := s.settingsNow()
	conn, err := findInviter(ctx, sw, code, set.FindTimeout)
	if err != nil {
		return err
	}
	s.updatePairing(pr, func(p *Pairing) { p.State = pairJoining })
	jctx, cancel := context.WithTimeout(ctx, set.FindTimeout)
	defer cancel()
	payload, err := invite.JoinConn(jctx, conn, s.id, code, &invite.JoinOptions{Data: joinData(s.displayName()), MaxPayload: maxPairPayload})
	sw.Close()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	ss, err := s.joinedSession(payload)
	if err != nil {
		return err
	}
	s.updatePairing(pr, func(p *Pairing) { p.State, p.SessionID = pairConnected, ss.id })
	return nil
}

// joinedSession opens the session an inviter's answer gives, refusing an answer that names this app's own key.
// The answer is cleared once read: the app does not keep the session secret.
func (s *ShareService) joinedSession(payload []byte) (*session, error) {
	peer, secret, name, err := parsePairPayload(payload)
	clear(payload)
	if err != nil {
		return nil, err
	}
	if peer.Equal(s.id.PublicKey()) {
		return nil, ErrOwnCode
	}
	ss, err := s.newSession(peer, name, secret)
	clear(secret[:])
	return ss, err
}

// findInviter waits at most timeout for a connection to the code's key.
func findInviter(ctx context.Context, sw *koloswarm.Swarm, code invite.Code, timeout time.Duration) (koloswarm.Conn, error) {
	fctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		ev, err := sw.Next(fctx)
		if err != nil {
			if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if ev.Kind == koloswarm.Connected && code.Matches(ev.Peer.PublicKey()) {
			return ev.Peer.Conn(), nil
		}
	}
}

func joinData(name string) []byte {
	var e kolocodec.Encoder
	e.PutString(joinTag)
	e.PutString(name)
	return e.Bytes()
}

// parseJoinData reads the joiner's name from its join data.
func parseJoinData(b []byte) (string, error) {
	d := kolocodec.NewDecoder(b)
	tag, err := d.String(len(joinTag))
	if err != nil || tag != joinTag {
		return "", protoErr("join tag", err)
	}
	name, err := d.String(maxNameBytes)
	if err != nil {
		return "", protoErr("join name", err)
	}
	if err := d.Done(); err != nil {
		return "", protoErr("join data", err)
	}
	return name, nil
}

func pairPayload(key ed25519.PublicKey, secret [sessionSecretSize]byte, name string) []byte {
	var e kolocodec.Encoder
	e.PutString(pairTag)
	e.PutFixed(key)
	e.PutFixed(secret[:])
	e.PutString(name)
	return e.Bytes()
}

// parsePairPayload reads the inviter's app key, the session secret and the inviter's name.
func parsePairPayload(b []byte) (ed25519.PublicKey, [sessionSecretSize]byte, string, error) {
	var secret [sessionSecretSize]byte
	d := kolocodec.NewDecoder(b)
	tag, err := d.String(len(pairTag))
	if err != nil || tag != pairTag {
		return nil, secret, "", invite.ErrProtocol
	}
	key, err := d.Fixed(ed25519.PublicKeySize)
	if err != nil {
		return nil, secret, "", invite.ErrProtocol
	}
	sec, err := d.Fixed(sessionSecretSize)
	if err != nil {
		return nil, secret, "", invite.ErrProtocol
	}
	name, err := d.String(maxNameBytes)
	if err != nil || d.Done() != nil {
		return nil, secret, "", invite.ErrProtocol
	}
	copy(secret[:], sec)
	return ed25519.PublicKey(key), secret, name, nil
}
