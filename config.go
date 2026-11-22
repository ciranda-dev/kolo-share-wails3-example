package main

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	kolodht "github.com/ciranda-dev/kolo-dht"
	koloswarm "github.com/ciranda-dev/kolo-swarm"
)

// Defaults of the settings that have one.
const (
	defaultMaxDownload = 1 << 40 // bytes a receive may download: a safety bound against a sender that never stops, not a size the app is tuned for
	// defaultMaxFiles bounds the files and folders of one receive: replaying a kolo-files volume takes about
	// 211 + 344 bytes per node and kolo-store about 250 bytes per open entry, so 100,000 keep a receive near
	// 300 MB of memory with the content log (see maxContentEntries), and as many inodes.
	defaultMaxFiles    = 100_000
	defaultFindTimeout = time.Minute
	// relayFindTimeout is the default with connection relays: kolo-swarm tries the relays once kolo-dht's Connect
	// failed or ran for its punch window of two consistent mappings, 10 probes 1 s apart plus twice RequestTimeout of
	// 1 s, and one relay dial takes up to 60 s.
	relayFindTimeout      = 12*time.Second + 60*time.Second
	defaultLookupInterval = 5 * time.Second
	// defaultPairTTL is how long a pairing code works: a short lifetime, as kolo-invite asks for in its README and
	// uses in its example, and the shortest of the board example's invite lifetimes. A code also ends at its first
	// use, Cancel or quit.
	defaultPairTTL = time.Hour
	// defaultResumeFor is how long a receive waits for bytes that stopped before it gives up: long enough for a
	// laptop to sleep, a network to change or a sender to restart its connection, short enough that a sender
	// that left does not hold a download for a day.
	defaultResumeFor = 30 * time.Minute
	maxResumeFor     = 24 * time.Hour
	// maxPairTTL is the longest invite lifetime of the Kolo apps: the chat example's maxInviteTTL and the longest
	// of the board's. kolo-invite itself has no maximum short of int64 overflow.
	maxPairTTL   = 30 * 24 * time.Hour
	localListen  = "127.0.0.1:0"
	publicListen = "0.0.0.0:0"
	// defaultLocalPort is where local mode runs its entry node, on 127.0.0.1 only. It is fixed so every window
	// finds it without a flag; next to kolo-node's 7447 and 7448, so it does not clash with a local kolo-node, and
	// below the ephemeral ranges (from 32768 on Linux, 49152 on macOS and Windows), so no random port takes it.
	defaultLocalPort = 7451
)

// ErrConfig matches every ConfigError, whatever setting it names.
var ErrConfig = errors.New("invalid configuration")

// Causes of a ConfigError.
var (
	ErrMissing    = errors.New("is required")
	ErrMalformed  = errors.New("is malformed")
	ErrUnreadable = errors.New("cannot be read")
	ErrConflict   = errors.New("cannot be used together with -bootstrap or -first")
)

// ConfigError names the setting that is wrong and why. errors.Is(err, ErrConfig) is true for every one.
type ConfigError struct {
	Setting string // the flag name, such as "bootstrap"
	Err     error
}

func (e *ConfigError) Error() string { return fmt.Sprintf("kolo-share: -%s %v", e.Setting, e.Err) }
func (e *ConfigError) Unwrap() error { return e.Err }
func (e *ConfigError) Is(target error) bool {
	return target == ErrConfig
}

// Config is everything the app needs from outside. A zero field takes its default, if it has one.
// LoadConfig fills it from flags and environment variables.
type Config struct {
	DataDir        string        // -data, KOLO_SHARE_DATA. Default: <user config dir>/kolo-share
	Bootstrap      string        // -bootstrap, KOLO_BOOTSTRAP: entry nodes of your own network, a comma list of host:port. Default: the public network
	Local          bool          // -local, KOLO_SHARE_LOCAL: a network on this computer only, instead of the public network
	First          string        // -first, KOLO_SHARE_FIRST: run the first entry node of a test network on this loopback IP:PORT
	Listen         string        // -listen, KOLO_SHARE_LISTEN. Default 127.0.0.1:0 on a loopback network, else 0.0.0.0:0
	MaxDownload    int64         // -max-download, KOLO_SHARE_MAX_DOWNLOAD: bytes one receive may download. Default 1 TiB
	MaxFiles       int           // -max-files, KOLO_SHARE_MAX_FILES: files and folders one item may hold. Default 100000
	FindTimeout    time.Duration // -find-timeout, KOLO_SHARE_FIND_TIMEOUT: how long a pasted code looks for the other computer. Default 1m, 72s with connection relays
	LookupInterval time.Duration // -lookup-interval, KOLO_SHARE_LOOKUP_INTERVAL: time between lookups of a topic. Default 5s
	PairTTL        time.Duration // -pair-ttl, KOLO_SHARE_PAIR_TTL: how long a pairing code works. Default 1h
	ResumeFor      time.Duration // -resume-for, KOLO_SHARE_RESUME_FOR: how long a receive waits for bytes that stopped before it fails. Default 30m
	Congestion     string        // -congestion, KOLO_SHARE_CONGESTION: how the session connection paces what it sends, "reno", "cubic" or "bbr". Default bbr
	QuietTransfers bool          // -quiet-transfers, KOLO_SHARE_QUIET_TRANSFERS: no "transfer:" line every few seconds while a transfer runs; the one at its end stays
	// Relays and RelayFiles are the connection relays: -relay (repeatable, a comma list of host:port@key) and
	// -relay-file (repeatable), or KOLO_SHARE_RELAYS when neither flag is given.
	Relays     []string
	RelayFiles []string

	source        entrySource // the entry nodes of -local; nil: loopbackSource(defaultLocalPort)
	bootstrapFrom string      // "flag", "env" or "" (set in code, which counts as a flag)
	localFrom     string      // "flag", "env" or "" (set in code)
	relaysFrom    string      // "flag", "env" or "" (set in code, which counts as a flag)
	explicitZero  string      // the first setting given as 0 that has no meaning
}

// LoadConfig reads the flags in args. A setting that is not given as a flag comes from its environment variable
// (read with getenv, such as os.Getenv), and a flag wins over its variable.
func LoadConfig(args []string, getenv func(string) string, stderr io.Writer) (Config, error) {
	var c Config
	fs := flag.NewFlagSet("kolo-share", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var quietFlag bool // -quiet-transfers was given, so its variable is not read
	text := func(p *string) func(string) error {
		return func(v string) error { *p = v; return nil }
	}
	dur := func(name string, p *time.Duration) func(string) error {
		return func(v string) (err error) {
			if *p, err = time.ParseDuration(v); err == nil && *p == 0 && (name == "pair-ttl" || name == "resume-for") && c.explicitZero == "" {
				c.explicitZero = name // a code that never expires is not offered
			}
			return err
		}
	}
	size := func(name string, p *int64) func(string) error {
		return func(v string) (err error) {
			if *p, err = strconv.ParseInt(v, 10, 64); err == nil && *p == 0 && c.explicitZero == "" {
				c.explicitZero = name
			}
			return err
		}
	}
	count := func(name string, p *int) func(string) error {
		return func(v string) (err error) {
			if *p, err = strconv.Atoi(v); err == nil && *p == 0 && c.explicitZero == "" {
				c.explicitZero = name
			}
			return err
		}
	}
	for _, s := range []struct {
		name, env, help string
		set             func(string) error
	}{
		{"data", "KOLO_SHARE_DATA", "data directory (default <user config dir>/kolo-share)", text(&c.DataDir)},
		{"bootstrap", "KOLO_BOOTSTRAP", "entry nodes of your own network, a comma list of host:port (default: the public network)", text(&c.Bootstrap)},
		{"first", "KOLO_SHARE_FIRST", "run the first entry node of a test network on this loopback IP:PORT, such as 127.0.0.1:4000", text(&c.First)},
		{"listen", "KOLO_SHARE_LISTEN", "local IP:PORT (default 127.0.0.1:0 on a loopback network, else 0.0.0.0:0)", text(&c.Listen)},
		{"max-download", "KOLO_SHARE_MAX_DOWNLOAD", "bytes one received item may download (default 1099511627776, 1 TiB)", size("max-download", &c.MaxDownload)},
		{"max-files", "KOLO_SHARE_MAX_FILES", "files and folders one item may hold (default 100000)", count("max-files", &c.MaxFiles)},
		{"find-timeout", "KOLO_SHARE_FIND_TIMEOUT", "how long a pasted code looks for the other computer (default 1m, 72s with connection relays)", dur("find-timeout", &c.FindTimeout)},
		{"lookup-interval", "KOLO_SHARE_LOOKUP_INTERVAL", "time between lookups of a topic (default 5s)", dur("lookup-interval", &c.LookupInterval)},
		{"pair-ttl", "KOLO_SHARE_PAIR_TTL", "how long a pairing code works, from 1s to 720h (default 1h)", dur("pair-ttl", &c.PairTTL)},
		{"resume-for", "KOLO_SHARE_RESUME_FOR", "how long a download waits for bytes that stopped before it fails, from 1s to 24h (default 30m)", dur("resume-for", &c.ResumeFor)},
		{"congestion", "KOLO_SHARE_CONGESTION", "how the connection to a paired computer paces what it sends: reno, cubic or bbr; bbr holds a standing queue and takes bandwidth from other flows on the link (default bbr)", text(&c.Congestion)},
	} {
		if v := getenv(s.env); v != "" {
			if err := s.set(v); err != nil {
				return c, &ConfigError{Setting: s.name, Err: fmt.Errorf("%w: %s: %w", ErrMalformed, s.env, err)}
			}
			if s.name == "bootstrap" {
				c.bootstrapFrom = "env"
			}
		}
		set := s.set
		if s.name == "bootstrap" {
			set = func(v string) error { c.bootstrapFrom = "flag"; return s.set(v) }
		}
		fs.Func(s.name, s.help+" ("+s.env+")", set)
	}
	fs.BoolFunc("local", "use a network on this computer only, instead of the public network (KOLO_SHARE_LOCAL)", func(v string) (err error) {
		if c.Local, err = strconv.ParseBool(v); err == nil {
			c.localFrom = "flag"
		}
		return err
	})
	fs.BoolFunc("quiet-transfers", "log no transfer line every few seconds while a transfer runs; the line at its end stays (KOLO_SHARE_QUIET_TRANSFERS)", func(v string) (err error) {
		c.QuietTransfers, err = strconv.ParseBool(v)
		quietFlag = err == nil
		return err
	})
	fs.Func("relay", "connection relay, host:port@key; repeat it or give a comma list (KOLO_SHARE_RELAYS when no -relay or -relay-file is given)",
		func(v string) error { c.Relays = append(c.Relays, v); c.relaysFrom = "flag"; return nil })
	fs.Func("relay-file", "file of connection relays, one host:port@key per line; repeatable",
		func(v string) error { c.RelayFiles = append(c.RelayFiles, v); c.relaysFrom = "flag"; return nil })
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if v := getenv("KOLO_SHARE_LOCAL"); v != "" && c.localFrom == "" {
		on, err := strconv.ParseBool(v)
		if err != nil {
			return c, &ConfigError{Setting: "local", Err: fmt.Errorf("%w: KOLO_SHARE_LOCAL: want 1 or 0", ErrMalformed)}
		}
		c.Local, c.localFrom = on, "env"
	}
	if v := getenv("KOLO_SHARE_QUIET_TRANSFERS"); v != "" && !quietFlag {
		on, err := strconv.ParseBool(v)
		if err != nil {
			return c, &ConfigError{Setting: "quiet-transfers", Err: fmt.Errorf("%w: KOLO_SHARE_QUIET_TRANSFERS: want 1 or 0", ErrMalformed)}
		}
		c.QuietTransfers = on
	}
	if v := getenv("KOLO_SHARE_RELAYS"); v != "" && c.relaysFrom == "" {
		c.Relays, c.relaysFrom = []string{v}, "env"
	}
	return c, nil
}

// entrySource gives the entry nodes of -local, and the one of them this app may run itself when it is free.
// The startup tries to run host and joins the nodes when it cannot.
type entrySource func() (nodes []string, host netip.AddrPort)

// loopbackSource is the network of -local: one entry node on 127.0.0.1:port, run by the first window of this
// computer that takes the port. Windows on other computers cannot reach it.
func loopbackSource(port uint16) entrySource {
	return func() ([]string, netip.AddrPort) {
		host := netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), port)
		return []string{host.String()}, host
	}
}

// The three networks: the public one by default, your own with -bootstrap or -first, and this computer only with -local.
const (
	modePublic = "public"
	modeOwn    = "own"
	modeLocal  = "local"
)

// settings is a Config after defaults and checks: values the code can use as they are.
type settings struct {
	Config
	first      netip.AddrPort    // valid when this app runs the first entry node
	local      netip.AddrPort    // valid in local mode: the host of the entrySource
	mode       string            // modePublic, modeOwn or modeLocal
	nodes      []string          // entry nodes given to kolo-swarm, from ParseNodes; none in public mode, where kolo-swarm uses its own list
	nodesText  string            // the entry nodes as shown: "" in public mode, whose addresses the window does not list
	nodesFrom  string            // "flag", "env", "local", "public" or "" (only -first)
	relays     []koloswarm.Relay // connection relays, from ParseRelays and LoadRelays
	relaysText string            // relays as a comma list of host:port@key
	relaysFrom string            // "flag", "file", "flag-and-file", "env" or "none"
}

// resolve applies the defaults and checks every setting. It returns a *ConfigError for the first bad setting.
func (c Config) resolve() (settings, error) {
	s := settings{Config: c}
	if s.DataDir == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return s, &ConfigError{Setting: "data", Err: fmt.Errorf("%w: no user config directory: %w", ErrMissing, err)}
		}
		s.DataDir = filepath.Join(dir, "kolo-share")
	}
	if c.explicitZero != "" {
		return s, &ConfigError{Setting: c.explicitZero, Err: fmt.Errorf("%w: must be positive; leave it out for the default", ErrMalformed)}
	}
	if s.MaxDownload == 0 {
		s.MaxDownload = defaultMaxDownload
	}
	if s.MaxDownload < 0 {
		return s, &ConfigError{Setting: "max-download", Err: fmt.Errorf("%w: must be positive", ErrMalformed)}
	}
	if s.MaxFiles == 0 {
		s.MaxFiles = defaultMaxFiles
	}
	if s.MaxFiles < 0 {
		return s, &ConfigError{Setting: "max-files", Err: fmt.Errorf("%w: must be positive", ErrMalformed)}
	}
	if err := s.resolveRelays(); err != nil {
		return s, err
	}
	if s.FindTimeout == 0 {
		s.FindTimeout = defaultFindTimeout
		if len(s.relays) > 0 {
			s.FindTimeout = relayFindTimeout
		}
	}
	if s.FindTimeout < time.Second {
		return s, &ConfigError{Setting: "find-timeout", Err: fmt.Errorf("%w: at least 1s", ErrMalformed)}
	}
	if s.PairTTL == 0 {
		s.PairTTL = defaultPairTTL
	}
	if s.PairTTL < time.Second || s.PairTTL > maxPairTTL {
		return s, &ConfigError{Setting: "pair-ttl", Err: fmt.Errorf("%w: from 1s to 720h (30 days)", ErrMalformed)}
	}
	if s.ResumeFor == 0 {
		s.ResumeFor = defaultResumeFor
	}
	if s.ResumeFor < time.Second || s.ResumeFor > maxResumeFor {
		return s, &ConfigError{Setting: "resume-for", Err: fmt.Errorf("%w: from 1s to 24h", ErrMalformed)}
	}
	if s.Congestion == "" {
		s.Congestion = defaultCongestion
	}
	if !validCongestion(s.Congestion) {
		return s, &ConfigError{Setting: "congestion", Err: fmt.Errorf("%w: want reno, cubic or bbr", ErrMalformed)}
	}
	if s.LookupInterval == 0 {
		s.LookupInterval = defaultLookupInterval
	}
	if s.LookupInterval < time.Second || s.LookupInterval > time.Hour {
		return s, &ConfigError{Setting: "lookup-interval", Err: fmt.Errorf("%w: from 1s to 1h", ErrMalformed)}
	}
	if s.First != "" {
		a, err := netip.ParseAddrPort(s.First)
		if err != nil || !a.Addr().Is4() || !a.Addr().IsLoopback() || a.Port() == 0 {
			return s, &ConfigError{Setting: "first", Err: fmt.Errorf("%w: want a loopback IPv4 IP:PORT such as 127.0.0.1:4000, for local testing only", ErrMalformed)}
		}
		s.first = a
	}

	given := c.Bootstrap != "" || c.bootstrapFrom != ""
	if c.Local && (given || s.First != "") {
		return s, &ConfigError{Setting: "local", Err: ErrConflict}
	}
	s.nodesText, s.nodesFrom = c.Bootstrap, c.bootstrapFrom
	if s.nodesFrom == "" && c.Bootstrap != "" {
		s.nodesFrom = "flag"
	}
	nodes, err := parseEntryNodes(s.nodesText)
	if err != nil {
		return s, &ConfigError{Setting: "bootstrap", Err: err}
	}
	if given && len(nodes) == 0 {
		return s, &ConfigError{Setting: "bootstrap", Err: fmt.Errorf("%w: give the entry nodes of your network, or leave it out for the public network", ErrMissing)}
	}
	s.nodes = nodes
	if s.first.IsValid() && !slices.Contains(nodes, s.first.String()) {
		s.nodes = append([]string{s.first.String()}, nodes...)
	}
	switch {
	case c.Local:
		src := c.source
		if src == nil {
			src = loopbackSource(defaultLocalPort)
		}
		s.mode, s.nodesFrom = modeLocal, "local"
		s.nodes, s.local = src()
		s.nodesText = ""
	case len(s.nodes) == 0:
		s.mode, s.nodesFrom = modePublic, "public"
	default:
		s.mode = modeOwn
	}

	if s.Listen == "" {
		s.Listen = publicListen
		if loopbackOnly(s.nodes) {
			s.Listen = localListen
		}
	}
	if a, err := netip.ParseAddrPort(s.Listen); err != nil || !a.Addr().Is4() {
		return s, &ConfigError{Setting: "listen", Err: fmt.Errorf("%w: want an IPv4 IP:PORT such as 0.0.0.0:0", ErrMalformed)}
	}
	return s, nil
}

// entryCount is how many entry nodes the network starts from.
func (s settings) entryCount() int {
	if s.mode == modePublic {
		return len(kolodht.PublicServers())
	}
	return len(s.nodes)
}

// resolveRelays reads the connection relays: the flags and files, else KOLO_SHARE_RELAYS.
func (s *settings) resolveRelays() error {
	s.relaysFrom = s.Config.relaysFrom
	if s.relaysFrom != "env" {
		s.relaysFrom = "flag" // set in code: the same as a flag
		switch {
		case len(s.Relays) == 0 && len(s.RelayFiles) > 0:
			s.relaysFrom = "file"
		case len(s.Relays) > 0 && len(s.RelayFiles) > 0:
			s.relaysFrom = "flag-and-file"
		}
	}
	parts := slices.Clone(s.Relays)
	where := ""
	if s.relaysFrom == "env" {
		where = "KOLO_SHARE_RELAYS: "
	}
	for _, path := range s.RelayFiles {
		rs, err := koloswarm.LoadRelays(path)
		var pe *fs.PathError
		switch {
		case errors.As(err, &pe): // missing or unreadable, not malformed
			return &ConfigError{Setting: "relay-file", Err: fmt.Errorf("%w: %w", ErrUnreadable, pe)}
		case err != nil:
			return &ConfigError{Setting: "relay-file", Err: plainSwarmErr(err)}
		}
		parts = append(parts, relaysText(rs))
	}
	relays, err := parseRelayList(strings.Join(parts, ","))
	if err != nil {
		if where != "" {
			err = fmt.Errorf("%w: %s%s", ErrMalformed, where, strings.TrimPrefix(err.Error(), ErrMalformed.Error()+": "))
		}
		return &ConfigError{Setting: "relay", Err: err}
	}
	s.relays, s.relaysText = relays, relaysText(relays)
	if len(relays) == 0 {
		s.relaysFrom = "none"
	}
	return nil
}

// parseRelayList parses a comma list with kolo-swarm's ParseRelays and turns its error into a sentence.
func parseRelayList(text string) ([]koloswarm.Relay, error) {
	relays, err := koloswarm.ParseRelays(text)
	if err != nil {
		return nil, plainSwarmErr(err)
	}
	return relays, nil
}

// relaysText writes relays in the form ParseRelays reads.
func relaysText(relays []koloswarm.Relay) string {
	out := make([]string, len(relays))
	for i, r := range relays {
		out[i] = r.Addr + "@" + hex.EncodeToString(r.PublicKey)
	}
	return strings.Join(out, ",")
}

// plainSwarmErr is a kolo-swarm parse error without its prefixes, as ErrMalformed.
func plainSwarmErr(err error) error {
	msg := strings.TrimPrefix(err.Error(), "koloswarm: ")
	msg = strings.TrimPrefix(msg, "invalid argument: ")
	return fmt.Errorf("%w: %s", ErrMalformed, msg)
}

// parseEntryNodes parses a comma list with kolo-swarm's ParseNodes and turns its error into a sentence.
func parseEntryNodes(s string) ([]string, error) {
	nodes, err := koloswarm.ParseNodes(s)
	if err != nil {
		return nil, plainSwarmErr(err)
	}
	return nodes, nil
}

// loopbackOnly reports whether every entry node is a loopback IPv4 literal, so the app can stay on 127.0.0.1.
func loopbackOnly(nodes []string) bool {
	if len(nodes) == 0 {
		return false
	}
	for _, n := range nodes {
		a, err := netip.ParseAddrPort(n)
		if err != nil || !a.Addr().IsLoopback() {
			return false
		}
	}
	return true
}
