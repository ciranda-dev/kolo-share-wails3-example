package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"time"

	kolofiles "github.com/ciranda-dev/kolo-files"
)

func controlSamples() []ctlMsg {
	meta := [32]byte{1, 2, 3}
	return []ctlMsg{
		{typ: ctlHello, tag: [sessionTagSize]byte{9, 8, 7}, name: "Ana Lima"},
		{typ: ctlHello, name: strings.Repeat("é", maxNameRunes)},
		{typ: ctlOffer, offer: offer{id: 1, kind: kindFile, name: "photo.jpg", nodes: 1, files: 1, bytes: 3 << 20, meta: meta}},
		{typ: ctlOffer, offer: offer{id: math.MaxUint64, kind: kindFolder, name: strings.Repeat("n", kolofiles.DefaultMaxNameLength), nodes: math.MaxInt32, files: math.MaxInt32 - 1, bytes: math.MaxInt64, meta: meta}},
		{typ: ctlOffer, offer: offer{id: 2, kind: kindFolder, name: "empty", nodes: 1, files: 0}},
		{typ: ctlWithdraw, offer: offer{id: 5}},
		{typ: ctlStatus, offer: offer{id: 5}, state: stReceiving, done: 12345},
		{typ: ctlStatus, offer: offer{id: 5}, state: stFailed},
		{typ: ctlSynced},
		{typ: ctlBye},
		{typ: ctlPrepare, prep: prepare{id: 3, kind: kindFile, name: "big.iso", done: 5, total: 100}},
		{typ: ctlPrepare, prep: prepare{id: math.MaxUint64, kind: kindFolder, name: strings.Repeat("n", kolofiles.DefaultMaxNameLength), done: math.MaxInt64, total: math.MaxInt64}},
		{typ: ctlPrepare, prep: prepare{id: 4, kind: kindFolder, name: "unknown size", done: 7}},
		{typ: ctlReady, prep: prepare{id: 3}, offer: offer{id: 9}},
		{typ: ctlPrepEnd, prep: prepare{id: 3}, state: endCancelled},
		{typ: ctlPrepEnd, prep: prepare{id: 3}, state: endFailed},
	}
}

func TestControlRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	for _, m := range controlSamples() {
		if err := writeFrame(&buf, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range controlSamples() {
		body, err := readFrame(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) > maxControlFrame {
			t.Fatalf("a %d byte body over the bound %d", len(body), maxControlFrame)
		}
		got, err := decodeControl(body)
		if err != nil || got != want {
			t.Fatalf("got %+v (%v), want %+v", got, err, want)
		}
	}
	if _, err := readFrame(&buf); !errors.Is(err, io.EOF) {
		t.Fatalf("after the last frame: %v", err)
	}
}

// encodeOfferFields writes an OFFER body field by field, so a test can put in what encodeControl never writes.
func encodeOfferFields(id, kind uint64, name []byte, nodes, files, size uint64, metaLen int) []byte {
	b := binary.AppendUvarint(nil, ctlOffer)
	b = binary.AppendUvarint(b, id)
	b = binary.AppendUvarint(b, kind)
	b = binary.AppendUvarint(b, uint64(len(name)))
	b = append(b, name...)
	b = binary.AppendUvarint(b, nodes)
	b = binary.AppendUvarint(b, files)
	b = binary.AppendUvarint(b, size)
	return append(b, make([]byte, metaLen)...)
}

// encodePrepareFields writes a PREPARE body field by field, so a test can put in what encodeControl never writes.
func encodePrepareFields(id, kind uint64, name []byte, done, total uint64) []byte {
	b := binary.AppendUvarint(nil, ctlPrepare)
	b = binary.AppendUvarint(b, id)
	b = binary.AppendUvarint(b, kind)
	b = binary.AppendUvarint(b, uint64(len(name)))
	b = append(b, name...)
	b = binary.AppendUvarint(b, done)
	return binary.AppendUvarint(b, total)
}

func TestFeaturesFrame(t *testing.T) {
	bits, err := decodeFeatures(encodeFeatures(featPrepare))
	if err != nil || bits != featPrepare {
		t.Fatalf("round trip: %d %v", bits, err)
	}
	if bits, err := decodeFeatures(encodeFeatures(featPrepare | 1<<40)); err != nil || bits != featPrepare {
		t.Fatalf("a bit this app does not know is dropped: %d %v", bits, err)
	}
	for name, body := range map[string][]byte{
		"empty":     {},
		"other tag": append([]byte{21}, "kolo-share/features/v9"...),
		"no bits":   append([]byte{byte(len(featuresTag))}, featuresTag...),
		"trailing":  append(encodeFeatures(1), 0),
	} {
		if _, err := decodeFeatures(body); !errors.Is(err, ErrPeerProtocol) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestControlHostile(t *testing.T) {
	long := bytes.Repeat([]byte("n"), kolofiles.DefaultMaxNameLength+1)
	for name, body := range map[string][]byte{
		"empty":                    {},
		"unknown type":             {99},
		"type zero":                {0},
		"hello wrong protocol":     append(append([]byte{ctlHello, 21}, "kolo-share/control/v9"...), make([]byte, sessionTagSize+1)...),
		"hello short tag":          append(append([]byte{ctlHello, 21}, controlTag...), 1, 2, 3),
		"offer ID zero":            encodeOfferFields(0, 1, []byte("a"), 1, 1, 1, 32),
		"offer kind 3":             encodeOfferFields(1, 3, []byte("a"), 1, 1, 1, 32),
		"offer bad UTF-8":          encodeOfferFields(1, 1, []byte{0xff, 0xfe}, 1, 1, 1, 32),
		"offer name too long":      encodeOfferFields(1, 1, long, 1, 1, 1, 32),
		"file of two nodes":        encodeOfferFields(1, 1, []byte("a"), 2, 1, 1, 32),
		"file of no file":          encodeOfferFields(1, 1, []byte("a"), 1, 0, 1, 32),
		"folder of no node":        encodeOfferFields(1, 2, []byte("a"), 0, 0, 1, 32),
		"more files than room":     encodeOfferFields(1, 2, []byte("a"), 3, 3, 1, 32),
		"nodes over int32":         encodeOfferFields(1, 2, []byte("a"), math.MaxInt32+1, 1, 1, 32),
		"bytes over int64":         encodeOfferFields(1, 1, []byte("a"), 1, 1, math.MaxInt64+1, 32),
		"short log ID":             encodeOfferFields(1, 1, []byte("a"), 1, 1, 1, 31),
		"trailing byte":            encodeOfferFields(1, 1, []byte("a"), 1, 1, 1, 33),
		"withdraw ID zero":         {ctlWithdraw, 0},
		"status unknown":           {ctlStatus, 1, 8, 0},
		"status zero":              {ctlStatus, 1, 0, 0},
		"status done too big":      append([]byte{ctlStatus, 1, stDone}, binary.AppendUvarint(nil, math.MaxInt64+1)...),
		"synced with a body":       {ctlSynced, 0},
		"non-minimal varint":       {0x85, 0x00},
		"prepare ID zero":          encodePrepareFields(0, 1, []byte("a"), 0, 0),
		"prepare kind 3":           encodePrepareFields(1, 3, []byte("a"), 0, 0),
		"prepare bad UTF-8":        encodePrepareFields(1, 1, []byte{0xff, 0xfe}, 0, 0),
		"prepare name too long":    encodePrepareFields(1, 1, long, 0, 0),
		"prepare done over total":  encodePrepareFields(1, 1, []byte("a"), 11, 10),
		"prepare done over int64":  encodePrepareFields(1, 1, []byte("a"), math.MaxInt64+1, 0),
		"prepare total over int64": encodePrepareFields(1, 1, []byte("a"), 0, math.MaxInt64+1),
		"prepare trailing byte":    append(encodePrepareFields(1, 1, []byte("a"), 0, 0), 0),
		"prepare cut short":        encodePrepareFields(1, 1, []byte("a"), 0, 0)[:6],
		"ready transfer zero":      {ctlReady, 0, 1},
		"ready offer zero":         {ctlReady, 1, 0},
		"ready cut short":          {ctlReady, 1},
		"prepend reason zero":      {ctlPrepEnd, 1, 0},
		"prepend reason 3":         {ctlPrepEnd, 1, 3},
		"prepend ID zero":          {ctlPrepEnd, 0, 1},
		"prepend trailing":         {ctlPrepEnd, 1, 1, 0},
	} {
		if _, err := decodeControl(body); !errors.Is(err, ErrPeerProtocol) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A length over the bound is refused before its body is read: the reader holds only the length.
	hdr := binary.LittleEndian.AppendUint32(nil, uint32(maxControlFrame+1))
	if _, err := readFrame(bytes.NewReader(hdr)); !errors.Is(err, ErrPeerProtocol) {
		t.Fatalf("oversize length: %v", err)
	}
	if _, err := readFrame(bytes.NewReader([]byte{0, 0, 0, 0})); !errors.Is(err, ErrPeerProtocol) {
		t.Fatalf("zero length: %v", err)
	}
	ok := binary.LittleEndian.AppendUint32(nil, uint32(maxControlFrame))
	if _, err := readFrame(bytes.NewReader(ok)); !errors.Is(err, io.EOF) {
		t.Fatalf("a length at the bound waits for its body: %v", err)
	}
	if se := explain(protoErr("x", nil), limits{}); se.Reason != "peer-protocol" {
		t.Fatalf("explain: %+v", se)
	}
}

func FuzzReadControl(f *testing.F) {
	for _, m := range controlSamples() {
		var buf bytes.Buffer
		writeFrame(&buf, m)
		f.Add(buf.Bytes())
	}
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		r := bytes.NewReader(b)
		for {
			body, err := readFrame(r)
			if err != nil {
				return
			}
			m, err := decodeControl(body)
			if err != nil {
				continue
			}
			// Canonical: a body that decodes is the one encoding of its message.
			if again := encodeControl(m); !bytes.Equal(again, body) {
				t.Fatalf("%x decodes to %+v, which encodes to %x", body, m, again)
			}
		}
	})
}

func TestPairPayloadAndJoinData(t *testing.T) {
	key := make(ed25519.PublicKey, ed25519.PublicKeySize)
	key[0] = 7
	secret := [sessionSecretSize]byte{1, 2}
	long := strings.Repeat("é", maxNameRunes)
	b := pairPayload(key, secret, long)
	if len(b) > maxPairPayload {
		t.Fatalf("payload of %d bytes over %d", len(b), maxPairPayload)
	}
	k, s, name, err := parsePairPayload(b)
	if err != nil || !k.Equal(key) || s != secret || name != long {
		t.Fatalf("payload: %v", err)
	}
	for name, bad := range map[string][]byte{
		"empty":     nil,
		"other tag": pairPayload(key, secret, "x")[1:],
		"short":     b[:len(b)-1],
		"trailing":  append(b, 0),
	} {
		if _, _, _, err := parsePairPayload(bad); err == nil {
			t.Errorf("payload %s accepted", name)
		}
	}
	jd := joinData(long)
	if len(jd) > maxJoinData {
		t.Fatalf("join data of %d bytes over %d", len(jd), maxJoinData)
	}
	if got, err := parseJoinData(jd); err != nil || got != long {
		t.Fatalf("join data: %q %v", got, err)
	}
	for name, bad := range map[string][]byte{"empty": nil, "trailing": append(jd, 0), "other tag": joinData("x")[2:]} {
		if _, err := parseJoinData(bad); !errors.Is(err, ErrPeerProtocol) {
			t.Errorf("join data %s: %v", name, err)
		}
	}
}

func FuzzParsePairPayload(f *testing.F) {
	f.Add(pairPayload(make(ed25519.PublicKey, 32), [sessionSecretSize]byte{}, "Ana"))
	f.Fuzz(func(t *testing.T, b []byte) {
		k, s, name, err := parsePairPayload(b)
		if err == nil && !bytes.Equal(pairPayload(k, s, name), b) {
			t.Fatalf("%x is not canonical", b)
		}
	})
}

func FuzzParseJoinData(f *testing.F) {
	f.Add(joinData("Ana"))
	f.Fuzz(func(t *testing.T, b []byte) {
		name, err := parseJoinData(b)
		if err == nil && !bytes.Equal(joinData(name), b) {
			t.Fatalf("%x is not canonical", b)
		}
	})
}

func TestPeerName(t *testing.T) {
	key := ed25519.PublicKey(bytes.Repeat([]byte{0xab}, ed25519.PublicKeySize))
	for in, want := range map[string]string{
		"Ana":                         "Ana",
		"  Ana \n\t Lima  ":           "Ana Lima",
		"evil\u202egpj.exe":           "evilgpj.exe",
		"zero\u200bwidth\x00\x07":     "zerowidth",
		"":                            "Kolo abababab",
		"\u202e\u200b":                "Kolo abababab",
		"bad\xffutf8":                 "badutf8",
		strings.Repeat("x", 100):      strings.Repeat("x", maxNameRunes),
		"line\u2028sep\u2029par":      "line sep par",
		"private\ue000use":            "privateuse",
		strings.Repeat("é", 60) + "z": strings.Repeat("é", maxNameRunes),
	} {
		if got := peerName(in, key); got != want {
			t.Errorf("peerName(%q) = %q, want %q", in, got, want)
		}
	}
	if got, err := cleanDisplayName(" Ana  Lima "); err != nil || got != "Ana Lima" {
		t.Fatalf("display name: %q %v", got, err)
	}
	if got, err := cleanDisplayName(""); err != nil || got != "" {
		t.Fatalf("empty display name: %q %v", got, err)
	}
	if _, err := cleanDisplayName(strings.Repeat("x", maxNameRunes+1)); !errors.Is(err, ErrBadName) {
		t.Fatalf("long display name: %v", err)
	}
	if shortKey(key) != "abababab" {
		t.Fatal(shortKey(key))
	}
}

func TestSessionKeys(t *testing.T) {
	a, b := [sessionSecretSize]byte{1}, [sessionSecretSize]byte{2}
	ta, ga := sessionKeys(a)
	tb, gb := sessionKeys(b)
	if ta == tb || ga == gb || bytes.Equal(ta[:sessionTagSize], ga[:]) || bytes.Contains(ta[:], a[:]) {
		t.Fatal("topic and tag must be distinct one-way values of the secret")
	}
	if t2, g2 := sessionKeys(a); t2 != ta || g2 != ga {
		t.Fatal("both apps must derive the same topic and tag")
	}
}

func TestPruneFinals(t *testing.T) {
	ss := &session{finals: map[uint64]uint64{}}
	for id := uint64(1); id <= 100; id++ {
		ss.finals[id] = stDeclined
		pruneFinals(ss)
	}
	if len(ss.finals) != maxOpenOffers {
		t.Fatalf("%d kept", len(ss.finals))
	}
	for id := uint64(100 - maxOpenOffers + 1); id <= 100; id++ {
		if _, ok := ss.finals[id]; !ok {
			t.Fatalf("the newest %d are not kept", maxOpenOffers)
		}
	}
}

func TestPairTTLSetting(t *testing.T) {
	c, err := LoadConfig([]string{"-pair-ttl", "10m"}, env(nil), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := c.resolve(); err != nil || s.PairTTL.Minutes() != 10 {
		t.Fatalf("flag: %v %v", s.PairTTL, err)
	}
	c, _ = LoadConfig(nil, env(map[string]string{"KOLO_SHARE_PAIR_TTL": "2h"}), io.Discard)
	if s, err := c.resolve(); err != nil || s.PairTTL.Hours() != 2 {
		t.Fatalf("env: %v %v", s.PairTTL, err)
	}
	if s, err := (Config{DataDir: t.TempDir()}).resolve(); err != nil || s.PairTTL != defaultPairTTL {
		t.Fatalf("default: %v %v", s.PairTTL, err)
	}
	for _, args := range [][]string{{"-pair-ttl", "0"}, {"-pair-ttl", "500ms"}, {"-pair-ttl", "-1h"}} {
		c, err := LoadConfig(args, env(nil), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.resolve(); !errors.Is(err, ErrConfig) {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestResumeForSetting(t *testing.T) {
	c, err := LoadConfig([]string{"-resume-for", "10m"}, env(nil), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if s, err := c.resolve(); err != nil || s.ResumeFor.Minutes() != 10 {
		t.Fatalf("flag: %v %v", s.ResumeFor, err)
	}
	c, _ = LoadConfig(nil, env(map[string]string{"KOLO_SHARE_RESUME_FOR": "2h"}), io.Discard)
	if s, err := c.resolve(); err != nil || s.ResumeFor.Hours() != 2 {
		t.Fatalf("env: %v %v", s.ResumeFor, err)
	}
	if s, err := (Config{DataDir: t.TempDir()}).resolve(); err != nil || s.ResumeFor != defaultResumeFor || defaultResumeFor != 30*time.Minute {
		t.Fatalf("default: %v %v", s.ResumeFor, err)
	}
	for _, args := range [][]string{{"-resume-for", "0"}, {"-resume-for", "500ms"}, {"-resume-for", "-1h"}, {"-resume-for", "25h"}} {
		c, err := LoadConfig(args, env(nil), io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.resolve(); !errors.Is(err, ErrConfig) {
			t.Fatalf("%v: %v", args, err)
		}
	}
}
