package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"

	kolocodec "github.com/ciranda-dev/kolo-codec"
	kolofiles "github.com/ciranda-dev/kolo-files"
	kololog "github.com/ciranda-dev/kolo-log"
)

// The control stream of a session: each side opens one unidirectional stream on the session's connection and
// writes frames on it, a 4-byte little-endian length and a kolo-codec body.

// Control message types.
const (
	ctlHello    = 1 // first: protocol tag, session tag, display name
	ctlOffer    = 2 // an item the sender offers
	ctlWithdraw = 3 // the sender ended an offer
	ctlStatus   = 4 // the receiver's answer and progress for an offer
	ctlSynced   = 5 // the sender resent every open offer after HELLO
	ctlBye      = 6 // the side that closes the session
	// The next three go only to a peer that said it understands them (see featPrepare): an older app ends the
	// session on a type it does not know.
	ctlPrepare = 7 // the sender is preparing an item: what it is and how far it got
	ctlReady   = 8 // a prepared item is offered: the OFFER with this ID follows
	ctlPrepEnd = 9 // a prepared item ended before it was offered
	ctlLast    = ctlPrepEnd
)

// Reasons of a PREPEND.
const (
	endCancelled = 1
	endFailed    = 2
	endLast      = endFailed
)

// Status values of a STATUS message.
const (
	stAccepted  = 1
	stReceiving = 2
	stVerifying = 3
	stDone      = 4
	stDeclined  = 5
	stCancelled = 6
	stFailed    = 7
	stLast      = stFailed
)

const (
	controlTag     = "kolo-share/control/v0"
	featuresTag    = "kolo-share/features/v0"
	sessionTagSize = 16
	// featPrepare is the feature bit of a peer that reads PREPARE, READY and PREPEND.
	featPrepare = 1
	// maxControlFrame is the largest body, an OFFER with the longest name kolo-files stores and every integer at
	// its widest; the others are smaller.
	maxOfferBody    = 7*binary.MaxVarintLen64 + kolofiles.DefaultMaxNameLength + kololog.LogIDSize
	maxHelloBody    = 3*binary.MaxVarintLen64 + len(controlTag) + sessionTagSize + maxNameBytes
	maxPrepareBody  = 5*binary.MaxVarintLen64 + kolofiles.DefaultMaxNameLength
	maxControlFrame = max(maxOfferBody, maxHelloBody, maxPrepareBody)
)

// offer is what an OFFER says about one item: its top name in the volume, its files and folders, its bytes and
// the volume's metadata log. A receiver checks the verified tree against it before anything is saved.
type offer struct {
	id    uint64
	kind  string // kindFile or kindFolder
	name  string
	nodes int
	files int
	bytes int64
	meta  kololog.LogID
}

// prepare is what a PREPARE says about an item the sender is still storing: the sender's transfer ID, which stays
// the same until the item is offered or ends, what it is, and how many bytes it has done of its total (0: not
// known yet).
type prepare struct {
	id    uint64
	kind  string // kindFile or kindFolder
	name  string
	done  int64
	total int64
}

// ctlMsg is one control message; which fields are set depends on typ.
type ctlMsg struct {
	typ   uint64
	tag   [sessionTagSize]byte // hello
	name  string               // hello
	offer offer                // offer; offer.id also for withdraw, status and ready
	prep  prepare              // prepare; prep.id also for ready and prepend
	state uint64               // status; the reason of a prepend
	done  int64                // status: bytes done, shown and never trusted
}

func kindCode(kind string) uint64 {
	if kind == kindFolder {
		return 2
	}
	return 1
}

func encodeControl(m ctlMsg) []byte {
	var e kolocodec.Encoder
	e.PutUvarint(m.typ)
	switch m.typ {
	case ctlHello:
		e.PutString(controlTag)
		e.PutFixed(m.tag[:])
		e.PutString(m.name)
	case ctlOffer:
		o := m.offer
		e.PutUvarint(o.id)
		e.PutUvarint(kindCode(o.kind))
		e.PutString(o.name)
		e.PutUvarint(uint64(o.nodes))
		e.PutUvarint(uint64(o.files))
		e.PutUvarint(uint64(o.bytes))
		e.PutFixed(o.meta[:])
	case ctlWithdraw:
		e.PutUvarint(m.offer.id)
	case ctlStatus:
		e.PutUvarint(m.offer.id)
		e.PutUvarint(m.state)
		e.PutUvarint(uint64(max(m.done, 0)))
	case ctlPrepare:
		p := m.prep
		e.PutUvarint(p.id)
		e.PutUvarint(kindCode(p.kind))
		e.PutString(p.name)
		e.PutUvarint(uint64(max(p.done, 0)))
		e.PutUvarint(uint64(max(p.total, 0)))
	case ctlReady:
		e.PutUvarint(m.prep.id)
		e.PutUvarint(m.offer.id)
	case ctlPrepEnd:
		e.PutUvarint(m.prep.id)
		e.PutUvarint(m.state)
	}
	return e.Bytes()
}

// encodeFeatures is the one frame of a features stream.
func encodeFeatures(bits uint64) []byte {
	var e kolocodec.Encoder
	e.PutString(featuresTag)
	e.PutUvarint(bits)
	return e.Bytes()
}

// decodeFeatures reads the frame of a features stream. Bits it does not know are kept out of the result.
func decodeFeatures(b []byte) (uint64, error) {
	d := kolocodec.NewDecoder(b)
	tag, err := d.String(len(featuresTag))
	if err != nil || tag != featuresTag {
		return 0, protoErr("features tag", err)
	}
	bits, err := d.Uvarint()
	if err != nil {
		return 0, protoErr("features", err)
	}
	if err := d.Done(); err != nil {
		return 0, protoErr("trailing bytes", err)
	}
	return bits & featPrepare, nil
}

// protoErr wraps what is wrong with a frame in ErrPeerProtocol.
func protoErr(what string, err error) error {
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrPeerProtocol, what, err)
	}
	return fmt.Errorf("%w: %s", ErrPeerProtocol, what)
}

// decodeControl reads one body. Anything malformed, out of range or left over is ErrPeerProtocol; it never
// panics on what a peer sends.
func decodeControl(b []byte) (ctlMsg, error) {
	d := kolocodec.NewDecoder(b)
	var m ctlMsg
	var err error
	if m.typ, err = d.Uvarint(); err != nil || m.typ < ctlHello || m.typ > ctlLast {
		return ctlMsg{}, protoErr("message type", err)
	}
	switch m.typ {
	case ctlHello:
		tag, err := d.String(len(controlTag))
		if err != nil || tag != controlTag {
			return ctlMsg{}, protoErr("protocol tag", err)
		}
		t, err := d.Fixed(sessionTagSize)
		if err != nil {
			return ctlMsg{}, protoErr("session tag", err)
		}
		copy(m.tag[:], t)
		if m.name, err = d.String(maxNameBytes); err != nil {
			return ctlMsg{}, protoErr("name", err)
		}
	case ctlOffer:
		if m.offer, err = decodeOffer(d); err != nil {
			return ctlMsg{}, err
		}
	case ctlWithdraw:
		if m.offer.id, err = offerID(d); err != nil {
			return ctlMsg{}, err
		}
	case ctlStatus:
		if m.offer.id, err = offerID(d); err != nil {
			return ctlMsg{}, err
		}
		if m.state, err = d.Uvarint(); err != nil || m.state < stAccepted || m.state > stLast {
			return ctlMsg{}, protoErr("status", err)
		}
		done, err := d.UvarintMax(math.MaxInt64)
		if err != nil {
			return ctlMsg{}, protoErr("bytes done", err)
		}
		m.done = int64(done)
	case ctlPrepare:
		if m.prep, err = decodePrepare(d); err != nil {
			return ctlMsg{}, err
		}
	case ctlReady:
		if m.prep.id, err = transferID(d); err != nil {
			return ctlMsg{}, err
		}
		if m.offer.id, err = offerID(d); err != nil {
			return ctlMsg{}, err
		}
	case ctlPrepEnd:
		if m.prep.id, err = transferID(d); err != nil {
			return ctlMsg{}, err
		}
		if m.state, err = d.Uvarint(); err != nil || m.state < endCancelled || m.state > endLast {
			return ctlMsg{}, protoErr("reason", err)
		}
	}
	if err := d.Done(); err != nil {
		return ctlMsg{}, protoErr("trailing bytes", err)
	}
	return m, nil
}

func offerID(d *kolocodec.Decoder) (uint64, error) {
	id, err := d.Uvarint()
	if err != nil || id == 0 {
		return 0, protoErr("offer ID", err)
	}
	return id, nil
}

func transferID(d *kolocodec.Decoder) (uint64, error) {
	id, err := d.Uvarint()
	if err != nil || id == 0 {
		return 0, protoErr("transfer ID", err)
	}
	return id, nil
}

// decodePrepare reads the fields of a PREPARE: a known kind, a name of at most the length kolo-files stores, and
// no more bytes done than the total when the total is known.
func decodePrepare(d *kolocodec.Decoder) (prepare, error) {
	var p prepare
	var err error
	if p.id, err = transferID(d); err != nil {
		return prepare{}, err
	}
	kind, err := d.Uvarint()
	switch {
	case err == nil && kind == 1:
		p.kind = kindFile
	case err == nil && kind == 2:
		p.kind = kindFolder
	default:
		return prepare{}, protoErr("kind", err)
	}
	if p.name, err = d.String(kolofiles.DefaultMaxNameLength); err != nil {
		return prepare{}, protoErr("name", err)
	}
	done, err := d.UvarintMax(math.MaxInt64)
	if err != nil {
		return prepare{}, protoErr("bytes done", err)
	}
	total, err := d.UvarintMax(math.MaxInt64)
	if err != nil {
		return prepare{}, protoErr("bytes", err)
	}
	p.done, p.total = int64(done), int64(total)
	if p.total > 0 && p.done > p.total {
		return prepare{}, protoErr("more bytes done than the total", nil)
	}
	return p, nil
}

// decodeOffer reads the fields of an OFFER and checks how they fit together: one top entry, a file is one node,
// a folder holds itself and its files. Names and sizes are checked against the receiver's limits later.
func decodeOffer(d *kolocodec.Decoder) (offer, error) {
	var o offer
	var err error
	if o.id, err = offerID(d); err != nil {
		return offer{}, err
	}
	kind, err := d.Uvarint()
	switch {
	case err == nil && kind == 1:
		o.kind = kindFile
	case err == nil && kind == 2:
		o.kind = kindFolder
	default:
		return offer{}, protoErr("kind", err)
	}
	if o.name, err = d.String(kolofiles.DefaultMaxNameLength); err != nil {
		return offer{}, protoErr("name", err)
	}
	nodes, err := d.UvarintMax(math.MaxInt32)
	if err != nil {
		return offer{}, protoErr("nodes", err)
	}
	files, err := d.UvarintMax(math.MaxInt32)
	if err != nil {
		return offer{}, protoErr("files", err)
	}
	bytes, err := d.UvarintMax(math.MaxInt64)
	if err != nil {
		return offer{}, protoErr("bytes", err)
	}
	o.nodes, o.files, o.bytes = int(nodes), int(files), int64(bytes)
	if o.kind == kindFile && (o.nodes != 1 || o.files != 1) || o.kind == kindFolder && (o.nodes < 1 || o.files >= o.nodes) {
		return offer{}, protoErr("files and folders", nil)
	}
	meta, err := d.Fixed(kololog.LogIDSize)
	if err != nil {
		return offer{}, protoErr("log ID", err)
	}
	copy(o.meta[:], meta)
	return o, nil
}

// readFrame reads one frame; a length over maxControlFrame is refused before its body is read.
func readFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(hdr[:])
	if n == 0 || n > uint32(maxControlFrame) {
		return nil, protoErr(fmt.Sprintf("frame of %d bytes", n), nil)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

// writeFrame writes m with its length in front, in one Write.
func writeFrame(w io.Writer, m ctlMsg) error { return writeBody(w, encodeControl(m)) }

func writeBody(w io.Writer, body []byte) error {
	b := binary.LittleEndian.AppendUint32(make([]byte, 0, 4+len(body)), uint32(len(body)))
	_, err := w.Write(append(b, body...))
	return err
}
