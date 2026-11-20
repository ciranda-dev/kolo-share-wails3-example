package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"io/fs"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	kolodht "github.com/ciranda-dev/kolo-dht"
	koloid "github.com/ciranda-dev/kolo-id"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
	kolosync "github.com/ciranda-dev/kolo-sync"
)

const (
	progressTick = 200 * time.Millisecond
	// rateWindow is the span the speed is measured over: short enough to show a stall, long enough not to jump.
	rateWindow   = 5 * time.Second
	transfersDir = "transfers"
)

// stallAfter is how long a transfer that should move may stay still before the window says it is waiting; tests
// shorten it.
var stallAfter = 10 * time.Second

// Network states.
const (
	netStarting = "starting"
	netReady    = "ready"
	netFailed   = "failed"
)

// Transfer states. A send goes preparing, offered, sending, then delivered; a receive goes offered, queued,
// receiving, verifying, then done. A receive starts as preparing when the sender announced the item before its
// offer. Both can end declined, cancelled, stopped or failed, a receive also withdrawn.
const (
	statePreparing = "preparing"
	stateOffered   = "offered"
	stateSending   = "sending"
	stateDelivered = "delivered"
	stateQueued    = "queued"
	stateReceiving = "receiving"
	stateVerifying = "verifying"
	stateDone      = "done"
	stateDeclined  = "declined"
	stateCancelled = "cancelled"
	stateWithdrawn = "withdrawn"
	stateStopped   = "stopped"
	stateFailed    = "failed"
)

// Kinds of item, as in an offer and in Transfer.Kind.
const (
	kindFile   = "file"
	kindFolder = "folder"
)

// Info is what Start returns.
type Info struct {
	DataDir           string  `json:"dataDir"`
	Version           string  `json:"version"`
	Network           Network `json:"network"`
	DefaultSaveFolder string  `json:"defaultSaveFolder"` // the user's Downloads folder, "" when there is none
}

// Network is the payload of the "share:network" event.
type Network struct {
	State      string        `json:"state"`
	Message    string        `json:"message"`
	Error      *ShareError   `json:"error"` // the reason of Message, to translate it; nil when Message is ""
	EntryNodes int           `json:"entryNodes"`
	Mode       string        `json:"mode"`      // "public", "own" (-bootstrap or -first) or "local" (-local)
	FirstNode  string        `json:"firstNode"` // the entry node this app runs, with -first or in local mode
	LocalNode  string        `json:"localNode"` // the address of local mode, "" in the other modes
	Since      int64         `json:"since"`     // Unix milliseconds when this state began
	Relays     []RelayStatus `json:"relays"`    // every listed relay and its state
	// Seq grows with every state, so the window keeps the newest of an event and the snapshot of Start.
	Seq uint64 `json:"seq"`
}

// Transfer is one item sent or received in a session, the payload of the "share:transfer" event.
type Transfer struct {
	ID         string `json:"id"`
	Direction  string `json:"direction"` // "send" or "receive"
	Kind       string `json:"kind"`      // "file" or "folder"
	Name       string `json:"name"`
	State      string `json:"state"`
	SessionID  string `json:"sessionId"`
	PeerName   string `json:"peerName"`
	BytesDone  int64  `json:"bytesDone"`
	BytesTotal int64  `json:"bytesTotal"`
	FilesDone  int    `json:"filesDone"`
	FilesTotal int    `json:"filesTotal"`
	Skipped    int    `json:"skipped"` // send: links and special files that are not sent
	// Stalled is true while a send or a receive moved no byte for stallAfter: the connection is slow, lost or
	// being found again. A receive then tries again by itself.
	Stalled        bool        `json:"stalled"`
	BytesPerSecond float64     `json:"bytesPerSecond"`
	SavedTo        string      `json:"savedTo"`
	Error          *ShareError `json:"error"`
	Started        int64       `json:"started"`
	Finished       int64       `json:"finished"`
}

func (t Transfer) final() bool {
	switch t.State {
	case stateDelivered, stateDone, stateDeclined, stateCancelled, stateWithdrawn, stateStopped, stateFailed:
		return true
	}
	return false
}

// picker opens the native dialogs; the app's comes from Wails, and tests have none.
type picker interface {
	open(folder bool, title string) (string, error)
}

// job is a running transfer of a session. Its context ends with the cause of its end: an answer of the peer, the
// person, or the end of the session.
type job struct {
	emitMu  sync.Mutex // one emit at a time, so the window sees the states in order
	t       Transfer   // guarded by ShareService.mu
	ctx     context.Context
	cancel  context.CancelCauseFunc
	session *session
	offer   offer         // set once offered, guarded by ShareService.mu
	decide  chan decision // receive: the person's Save or Decline
	// prepID is the transfer ID the peer knows an item being prepared by, 0 when it was not announced; guarded
	// by ShareService.mu. A send gets it from its own ID, a receive from the peer's PREPARE.
	prepID  uint64
	prep    prepare      // send: the last PREPARE, to say again after a reconnect
	prog    prepThrottle // send: when the last PREPARE went out, used only by the goroutine of the send
	readyID uint64       // receive: the ID of the offer that the sender said turns this item into an offer
	// A receive that started as an announcement waits for prepared, which closes when its offer arrived; alive
	// gets a value at every word of the sender. Both are nil for a receive that started as an offer.
	prepared chan struct{}
	alive    chan struct{}
	moved    time.Time // when BytesDone or State last changed, guarded by ShareService.mu
}

// decision is the answer to an incoming offer.
type decision struct {
	save   bool
	folder string
}

// ShareService is the Go side of the window: it sends and receives files and folders. Every method is safe for
// concurrent use, and the window learns what happens through emit.
type ShareService struct {
	cfg    Config
	emit   func(name string, data any)
	pick   picker
	notch  notchWindow             // nil: no notch window
	reveal func(path string) error // shows a path in the file manager; nil: none
	// emitNotch sends an event to the notch window only; nil: no notch window. It never gets a pairing code.
	emitNotch func(name string, data any)
	copyText  func(text string) // puts text on the clipboard; nil: none
	clipText  func() string     // the text on the clipboard; nil: none
	copied    string            // the pairing code this app put on the clipboard and has not taken back
	mark      func(f *os.File) error
	pending   *pendingList
	openApp   func()      // brings the main window to the front
	tray      trayUI      // nil: no tray icon
	hooks     testHooks   // all zero in the app
	tuned     storeBudget // the tuned stores open now, for large items
	space     spaceLedger // the disk room that running transfers hold

	startMu sync.Mutex
	started bool
	set     settings
	id      *koloid.Identity
	hist    *history
	srv     *kolosync.Server // serves the logs of every open offer, each only to its session's peer
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup // the transfers, pairings and closing sessions

	netMu     sync.Mutex // one start or stop of the network at a time
	netCancel context.CancelFunc
	netWG     sync.WaitGroup
	linkWG    sync.WaitGroup // the goroutines of the session connections, started by the swarm loop

	trayMu sync.Mutex // one tray update at a time, so they arrive in order
	pairMu sync.Mutex // one pairing event at a time
	sessMu sync.Mutex // one sessions event at a time

	mu          sync.Mutex // guards the fields below, every job's Transfer and every session
	net         Network
	netChanged  chan struct{}                     // closed and replaced when net changes
	relayStates map[string]map[string]RelayStatus // what each swarm last said about each relay, by relay key
	relayLogged map[string]bool                   // a relay's key and an error reason, once that error was logged
	hosting     string                            // the local mode entry node this app runs, "" when it joined another window's
	sw          *koloswarm.Swarm
	node        *kolodht.Node
	jobs        map[string]*job
	nextID      uint64
	sessions    map[string]*session
	byKey       map[string]*session // the open session of each peer key
	nextSession uint64
	pairing     *pairing
	nextPairing uint64
	saved       savedSettings
	lastDone    *Transfer // the transfer that ended last, for the notch
	lastDoneAt  time.Time
	notchTimer  *time.Timer
	// lastOfferShow is when an incoming offer last opened the notch.
	lastOfferShow time.Time
	lastTray      TrayState
	restarting    bool // the network restarts: nothing may start
	running       bool // between Start and ServiceShutdown: pairings, sessions and transfers may start

	notchMu sync.Mutex // one send to the notch window at a time, with the setting read as it sends
}

// NewShareService returns a service that reads cfg when it starts, tells the main window what happens through
// emit and opens dialogs with pick (nil: no dialogs). The notch window gets its own events, through emitNotch.
func NewShareService(cfg Config, emit func(name string, data any), pick picker) *ShareService {
	return &ShareService{cfg: cfg, emit: emit, pick: pick, mark: markDownloaded}
}

// Start opens the data directory and the identity and starts the network in the background. It fails with a
// *ShareError of kind "config" for a bad setting. A second call returns the same Info.
func (s *ShareService) Start() (Info, error) {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if !s.started {
		if err := s.start(); err != nil {
			return Info{}, configError(err)
		}
		s.started = true
		s.pushTray()
	}
	return s.Info(), nil
}

// configSetting is the sentence of a bad setting: the flag's name and what is wrong with it.
func configSetting(ce *ConfigError) *ShareError {
	cause := ce.Err.Error()
	return &ShareError{Kind: kindConfig, Reason: "config-setting", Detail: ce.Setting, Cause: cause, Message: sentence("config-setting", ce.Setting, cause), Err: ce}
}

func configError(err error) error {
	var ce *ConfigError
	if errors.As(err, &ce) {
		return configSetting(ce)
	}
	return shareErr("data-dir", err, "")
}

func (s *ShareService) start() error {
	set, err := s.cfg.resolve()
	if err != nil {
		return err
	}
	if err := prepareDataDir(set.DataDir); err != nil {
		return err
	}
	saved, err := loadSaved(set.DataDir)
	if err != nil {
		return err
	}
	s.set, s.saved = set, saved
	if err := resetTransfers(s.set.DataDir); err != nil {
		return err
	}
	if s.pending, err = sweepPending(s.set.DataDir); err != nil {
		return err
	}
	if s.id = s.hooks.identity; s.id == nil {
		if s.id, err = loadIdentity(s.set.DataDir); err != nil {
			return err
		}
	}
	if s.hist, err = openHistory(s.set.DataDir); err != nil {
		return err
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.jobs = map[string]*job{}
	s.sessions = map[string]*session{}
	s.byKey = map[string]*session{}
	s.srv = kolosync.NewServer(s.authorize)
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	s.goTask(s.stallLoop)
	transferQuiet.Store(set.QuietTransfers)
	if !set.QuietTransfers {
		s.goTask(s.sendLoop)
	}
	s.startNetwork()
	return nil
}

// stallLoop marks a send or a receive that moved no byte for stallAfter, once a second, until the app closes.
func (s *ShareService) stallLoop() {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-tick.C:
			var still []*job
			s.mu.Lock()
			for _, j := range s.jobs {
				if (j.t.State == stateSending || j.t.State == stateReceiving) && !j.t.Stalled && now.Sub(j.moved) >= stallAfter {
					still = append(still, j)
				}
			}
			s.mu.Unlock()
			for _, j := range still {
				s.update(j, func(t *Transfer) {
					if now.Sub(j.moved) >= stallAfter { // j.moved is read under the lock of change
						t.Stalled, t.BytesPerSecond = true, 0
					}
				})
			}
		}
	}
}

func loadIdentity(dir string) (*koloid.Identity, error) {
	path := filepath.Join(dir, "identity.key")
	id, err := koloid.Load(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return id, err
	}
	if id, err = koloid.Generate(); err != nil {
		return nil, err
	}
	if err = id.SaveNew(path); err != nil && !errors.Is(err, koloid.ErrSavedNotSynced) {
		return nil, err
	}
	return id, nil
}

// ServiceShutdown says goodbye to every connected session, at most byeWait, and stops every transfer and the
// network. Wails calls it when the app quits.
func (s *ShareService) ServiceShutdown() error {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if !s.started {
		return nil
	}
	s.mu.Lock()
	s.running = false // nothing starts from now on, so wg cannot grow while it is waited for
	open := s.openSessionListLocked()
	s.mu.Unlock()
	var bye sync.WaitGroup
	for _, ss := range open {
		bye.Go(func() { s.endSession(ss, endAppClosed, true) })
	}
	bye.Wait()
	s.cancel()
	s.wg.Wait()
	s.stopNetwork()
	s.mu.Lock()
	if s.notchTimer != nil {
		s.notchTimer.Stop()
		s.notchTimer = nil
	}
	s.mu.Unlock()
	s.started = false
	return nil
}

// Info returns the data directory, the version and the network state.
func (s *ShareService) Info() Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Info{DataDir: s.set.DataDir, Version: version, Network: s.net, DefaultSaveFolder: downloadsFolder()}
}

func (s *ShareService) firstNodeLocked() string {
	if s.set.first.IsValid() {
		return s.set.first.String()
	}
	return s.hosting
}

func (s *ShareService) localNodeLocked() string {
	if s.set.local.IsValid() {
		return s.set.local.String()
	}
	return ""
}

// networkUsable refuses a transfer when the network failed or has no entry nodes. While it starts, a transfer
// is accepted and waits for it.
func (s *ShareService) networkUsable() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.net.State != netStarting && s.net.State != netReady {
		return explain(ErrNotReady, limits{})
	}
	return nil
}

// waitNetwork waits until the network is ready and returns its swarm, or the reason it did not start.
func (s *ShareService) waitNetwork(ctx context.Context) (*koloswarm.Swarm, error) {
	for {
		s.mu.Lock()
		n, sw, changed := s.net, s.sw, s.netChanged
		s.mu.Unlock()
		switch {
		case n.State == netReady && sw != nil:
			return sw, nil
		case n.State == netFailed:
			if n.Error != nil {
				return nil, n.Error
			}
			return nil, ErrNotReady
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Settings returns the settings the app runs with.
func (s *ShareService) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Settings{
		Mode: s.set.mode, EntryNodes: s.set.nodesText, EntryNodesFrom: s.set.nodesFrom, FirstNode: s.firstNodeLocked(), LocalNode: s.localNodeLocked(),
		ConnectionRelays: s.set.relaysText, ConnectionRelaysFrom: s.set.relaysFrom,
		DataDir: s.set.DataDir, Listen: s.set.Listen, MaxDownload: s.set.MaxDownload, MaxFiles: s.set.MaxFiles, ShowInNotch: s.saved.showInNotch(),
		DisplayName: s.saved.DisplayName, PairTTL: s.set.PairTTL.Milliseconds(), ResumeFor: s.set.ResumeFor.Milliseconds(),
	}
	if s.id != nil {
		st.PublicKey, st.Fingerprint = shortKey(s.id.PublicKey()), fingerprint(s.id.PublicKey())
	}
	return st
}

// settingsNow returns the settings the app runs with now.
func (s *ShareService) settingsNow() settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.set
}

// displayName is the name this app shows to its peers, "" for none.
func (s *ShareService) displayName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved.DisplayName
}

// SetDisplayName saves the name shown to the people you connect to; "" clears it. A session already open shows
// the new name after its next connection.
func (s *ShareService) SetDisplayName(name string) (Settings, error) {
	name, err := cleanDisplayName(name)
	if err != nil {
		return Settings{}, err
	}
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if !s.started {
		return Settings{}, explain(ErrNotReady, limits{})
	}
	s.mu.Lock()
	saved := s.saved
	saved.DisplayName = name
	dir := s.set.DataDir
	s.mu.Unlock()
	if err := saveSettings(dir, saved); err != nil {
		return Settings{}, explain(err, limits{})
	}
	s.mu.Lock()
	s.saved = saved
	s.mu.Unlock()
	return s.Settings(), nil
}

// Reconnect starts the network again with the same settings, after it failed. It refuses while a session or a
// pairing exists.
func (s *ShareService) Reconnect() error {
	s.startMu.Lock()
	defer s.startMu.Unlock()
	if !s.started {
		return explain(ErrNotReady, limits{})
	}
	if !s.beginRestart() {
		return shareErr("busy-reconnect", ErrBusy, "")
	}
	defer s.endRestart()
	s.stopNetwork()
	s.startNetwork()
	return nil
}

// beginRestart marks the network as restarting when no session, pairing or transfer runs, in one step, so
// nothing starts on a network that is about to close.
func (s *ShareService) beginRestart() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.jobs) > 0 || s.restarting || s.pairing != nil || s.openSessionsLocked() > 0 {
		return false
	}
	s.restarting = true
	return true
}

func (s *ShareService) endRestart() {
	s.mu.Lock()
	s.restarting = false
	s.mu.Unlock()
}

// History returns the last finished transfers, newest first.
func (s *ShareService) History() []HistoryItem {
	if s.hist == nil {
		return []HistoryItem{}
	}
	return s.hist.list()
}

// ClearHistory empties the history.
func (s *ShareService) ClearHistory() error {
	if s.hist == nil {
		return nil
	}
	if err := s.hist.clear(); err != nil {
		return explain(err, limits{})
	}
	s.emit("share:history", []HistoryItem{})
	return nil
}

// filesDropped tells the window the paths dropped on it; Wails gives nil for none, which would reach it as null.
func (s *ShareService) filesDropped(paths []string) {
	if paths == nil {
		paths = []string{}
	}
	s.emit("share:dropped", paths)
}

// Transfers returns the transfers that are still running, newest first.
func (s *ShareService) Transfers() []Transfer {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Transfer{}
	for _, j := range s.jobs {
		out = append(out, j.t)
	}
	slices.SortFunc(out, func(a, b Transfer) int {
		x, _ := strconv.ParseUint(a.ID, 10, 64)
		y, _ := strconv.ParseUint(b.ID, 10, 64)
		return int(y) - int(x)
	})
	return out
}

// PickFile opens the native dialog to choose a file to send; "" when the user cancels.
func (s *ShareService) PickFile() (string, error) { return s.open(false, "Choose a file to send") }

// PickFolder opens the native dialog to choose a folder to send; "" when the user cancels.
func (s *ShareService) PickFolder() (string, error) { return s.open(true, "Choose a folder to send") }

// PickSaveFolder opens the native dialog to choose the folder a session saves into; "" when the user cancels.
func (s *ShareService) PickSaveFolder() (string, error) { return s.open(true, "Choose where to save") }

func (s *ShareService) open(folder bool, title string) (string, error) {
	if s.pick == nil {
		return "", shareErr("no-dialogs", nil, "")
	}
	p, err := s.pick.open(folder, title)
	if err != nil {
		return "", shareErr("dialog-failed", err, "")
	}
	return p, nil
}

// startNetwork starts the first entry node, if configured, and the swarm, in the background.
func (s *ShareService) startNetwork() {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	ctx, cancel := context.WithCancel(s.ctx)
	s.netCancel = cancel
	s.setNetwork(netStarting, nil)
	s.netWG.Go(func() { s.runNetwork(ctx) })
}

func (s *ShareService) stopNetwork() {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	if s.netCancel != nil {
		s.netCancel()
	}
	s.mu.Lock()
	sw, node := s.sw, s.node
	s.sw, s.node, s.hosting = nil, nil, ""
	s.mu.Unlock()
	if sw != nil {
		sw.Close()
	}
	s.netWG.Wait()
	s.linkWG.Wait() // its goroutines start in the swarm loop, which netWG waited for
	if node != nil {
		node.Close()
	}
}

func (s *ShareService) runNetwork(ctx context.Context) {
	s.mu.Lock()
	set := s.set
	s.mu.Unlock()
	if s.hooks.netGate != nil {
		select {
		case <-s.hooks.netGate:
		case <-ctx.Done():
			return
		}
	}
	var node *kolodht.Node
	first := set.first
	if set.local.IsValid() {
		first = set.local
	}
	if first.IsValid() {
		nid, err := koloid.Generate()
		if err == nil {
			node, err = kolodht.New(ctx, first.String(), nid, &kolodht.Config{Server: true})
		}
		switch {
		case err != nil && set.local.IsValid():
			// The port is taken, most likely by the window of this app that opened first: join its entry node.
			node = nil
		case err != nil:
			s.setNetwork(netFailed, shareErr("first-node-failed", err, set.first.String()))
			return
		}
	}
	cfg := &koloswarm.Config{
		BootstrapNodes: set.nodes,
		LookupInterval: set.LookupInterval,
		RetryMax:       set.LookupInterval, // a dial that failed is tried again within a lookup interval, as in a pairing
		Allow:          func(key ed25519.PublicKey, _ netip.AddrPort) bool { return s.sessionOf(key) != nil },
		Relays:         set.relays,
		DHT:            s.hooks.dht,
	}
	limitSession(cfg, set.Congestion)
	if s.hooks.swarmCfg != nil {
		s.hooks.swarmCfg(cfg)
	}
	sw, err := koloswarm.New(ctx, set.Listen, s.id, cfg)
	opened := err == nil
	if opened {
		if err = sw.Bootstrap(ctx, nil); err != nil {
			sw.Close()
		}
	}
	if err != nil {
		if node != nil {
			node.Close()
		}
		if ctx.Err() == nil {
			reason, detail := "network-open", ""
			if opened {
				reason, detail = bootstrapFailure(set, node != nil, err)
			}
			s.setNetwork(netFailed, shareErr(reason, err, detail))
		}
		return
	}
	s.mu.Lock()
	if ctx.Err() != nil {
		s.mu.Unlock()
		sw.Close()
		if node != nil {
			node.Close()
		}
		return
	}
	s.sw, s.node = sw, node
	if node != nil && set.local.IsValid() {
		s.hosting = set.local.String()
	}
	s.mu.Unlock()
	log.Print("kolo-id ", s.id.PublicKeyString())
	s.setNetwork(netReady, nil)
	s.swarmLoop(ctx, sw)
}

// bootstrapFailure names why the network could not start: a reason of the error catalog and its detail. hosted is
// whether this app runs the entry node of local mode.
func bootstrapFailure(set settings, hosted bool, err error) (reason, detail string) {
	switch {
	case set.mode == modePublic:
		return "public-unreachable", "" // a swarm without entry nodes of its own bootstraps from the public list
	case set.mode == modeLocal && !hosted && errors.Is(err, kolodht.ErrNoNodes):
		return "local-port-taken", set.local.String() // held by a program that is not this app
	case set.mode == modeLocal:
		return "bootstrap-failed", set.local.String()
	case errors.Is(err, kolodht.ErrNoNodes) && !set.first.IsValid() && loopbackOnly(set.nodes):
		return "no-local-entry-node", set.nodesText // a test network whose -first window is not running
	}
	return "bootstrap-failed", set.nodesText
}

func (s *ShareService) setNetwork(state string, why *ShareError) {
	s.mu.Lock()
	s.net = Network{State: state, Mode: s.set.mode, EntryNodes: s.set.entryCount(), FirstNode: s.firstNodeLocked(), LocalNode: s.localNodeLocked(), Error: why, Since: time.Now().UnixMilli(), Seq: s.net.Seq + 1, Relays: s.relayStatusesLocked()}
	if why != nil {
		s.net.Message = why.Message
	}
	n := s.net
	if s.netChanged != nil {
		close(s.netChanged)
	}
	s.netChanged = make(chan struct{})
	s.mu.Unlock()
	s.emit("share:network", n)
}

// swarmLoop gives every connection to a session's peer to its session, and closes any other.
func (s *ShareService) swarmLoop(ctx context.Context, sw *koloswarm.Swarm) {
	defer s.forgetRelays(swarmSessions)
	for {
		ev, err := sw.Next(ctx)
		if err != nil {
			return
		}
		switch ev.Kind {
		case koloswarm.Connected:
			if ss := s.sessionOf(ev.Peer.PublicKey()); ss != nil {
				s.attach(ss, ev.Peer)
			} else {
				ev.Peer.Conn().Close()
			}
		case koloswarm.Lost:
			if ss := s.sessionOf(ev.Peer.PublicKey()); ss != nil {
				s.detach(ss, ev.Peer, ev.Err)
			}
		case koloswarm.Unreachable:
			if ss := s.sessionOf(ev.PublicKey); ss != nil {
				s.markUnreachable(ss)
			}
		case koloswarm.RelayChanged:
			s.relayChanged(swarmSessions, ev)
		}
	}
}

// newJob registers a transfer of the session that admit returns, in its first state, and tells the window. admit
// runs under the lock that registers it, so two calls cannot both pass a check of the running transfers; its error
// refuses the transfer. The caller runs the transfer in a goroutine that calls s.wg.Done.
func (s *ShareService) newJob(direction, kind, name, state string, admit func() (*session, error)) (*job, error) {
	s.mu.Lock()
	ss, err := admit()
	var j *job
	if err == nil {
		j, err = s.addJobLocked(ss, direction, kind, name, state)
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	s.change(j, true, func(*Transfer) {})
	return j, nil
}

// addJobLocked registers a transfer of ss without telling the window. It adds to s.wg under the lock that
// ServiceShutdown takes before it waits.
func (s *ShareService) addJobLocked(ss *session, direction, kind, name, state string) (*job, error) {
	if err := s.canStartLocked(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancelCause(ss.ctx)
	j := &job{ctx: ctx, cancel: cancel, session: ss}
	s.wg.Add(1)
	s.nextID++
	j.t = Transfer{ID: strconv.FormatUint(s.nextID, 10), Direction: direction, Kind: kind, Name: name, State: state,
		SessionID: ss.id, PeerName: ss.s.PeerName, Started: time.Now().UnixMilli()}
	j.moved = time.Now()
	s.jobs[j.t.ID] = j
	return j, nil
}

// update changes j's Transfer with f and emits it. A final Transfer is not changed again.
func (s *ShareService) update(j *job, f func(t *Transfer)) { s.change(j, false, f) }

// change is update; started is true for the first state of a transfer, which may open the notch window.
func (s *ShareService) change(j *job, started bool, f func(t *Transfer)) {
	j.emitMu.Lock()
	defer j.emitMu.Unlock()
	s.mu.Lock()
	if j.t.final() {
		s.mu.Unlock()
		return
	}
	state, done := j.t.State, j.t.BytesDone
	f(&j.t)
	if j.t.State != state || j.t.BytesDone != done {
		j.moved, j.t.Stalled = time.Now(), false
	}
	t := j.t
	s.mu.Unlock()
	s.emit("share:transfer", t)
	s.notchChanged(t, started)
}

// finish puts j in state, a final one, and forgets it. record writes it to the history: an offer that ended
// before anyone answered it is not recorded, so a flood of offers never fills the history.
func (s *ShareService) finish(j *job, state string, se *ShareError, record bool) {
	s.update(j, func(t *Transfer) {
		t.Finished, t.BytesPerSecond, t.State, t.Error = time.Now().UnixMilli(), 0, state, se
	})
	s.mu.Lock()
	t := j.t
	delete(s.jobs, t.ID)
	s.mu.Unlock()
	s.pushTray()
	if !record {
		return
	}
	it := HistoryItem{ID: t.ID, Direction: t.Direction, Kind: t.Kind, Name: t.Name, Result: t.State, Files: t.FilesTotal, Bytes: t.BytesTotal,
		SavedTo: t.SavedTo, At: t.Finished, Error: t.Error, PeerName: t.PeerName, PeerKey: shortKey(j.session.peer),
		PeerID: hex.EncodeToString(j.session.peer)}
	if t.Error != nil {
		it.Message = t.Error.Message
	}
	list, herr := s.hist.add(it)
	if herr != nil {
		log.Print("history: ", herr)
	}
	s.emit("share:history", list)
}

// sample is a measure of a transfer at a time.
type sample struct {
	at time.Time
	n  int64
}

// rateOf is the bytes a second between the oldest of samples that is within rateWindow of now and the newest;
// it drops the older ones.
func rateOf(samples []sample, now time.Time) (float64, []sample) {
	for len(samples) > 1 && now.Sub(samples[1].at) >= rateWindow {
		samples = samples[1:]
	}
	if len(samples) < 2 {
		return 0, samples
	}
	first, last := samples[0], samples[len(samples)-1]
	dt := last.at.Sub(first.at).Seconds()
	if dt <= 0 {
		return 0, samples
	}
	return float64(max(last.n-first.n, 0)) / dt, samples
}

// watch calls measure every progressTick until the returned function is called, and puts the result in
// BytesDone with the speed of the last rateWindow.
func (s *ShareService) watch(j *job, measure func() int64) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		tick := time.NewTicker(progressTick)
		defer tick.Stop()
		samples := []sample{{time.Now(), measure()}}
		for {
			select {
			case <-done:
				return
			case now := <-tick.C:
				n := measure()
				var rate float64
				samples = append(samples, sample{now, n})
				rate, samples = rateOf(samples, now)
				s.update(j, func(t *Transfer) {
					if t.BytesDone != n || t.BytesPerSecond != rate {
						t.BytesDone, t.BytesPerSecond = n, rate
					}
				})
			}
		}
	})
	var once sync.Once
	return func() { once.Do(func() { close(done); wg.Wait() }) }
}

// jobDir is the private directory of a transfer's kolo-store, removed when it ends.
func (s *ShareService) jobDir(j *job) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filepath.Join(s.set.DataDir, transfersDir, j.t.ID)
}

// transfersPath is the folder that holds the stores of the transfers.
func (s *ShareService) transfersPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return filepath.Join(s.set.DataDir, transfersDir)
}

// dirSize sums the sizes of the regular files below dir.
func dirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// counter counts the bytes that go through it.
type counter struct{ n atomic.Int64 }

func (c *counter) Write(p []byte) (int, error) { c.n.Add(int64(len(p))); return len(p), nil }
