package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// maxNameRunes bounds a display name, as in the meet example: it fits
	// a row of the rail and a notch line.
	maxNameRunes = 48
	maxNameBytes = maxNameRunes * utf8.UTFMax
)

// cleanDisplayName checks the name a person typed for themselves: blanks collapsed, no control or format
// characters, at most maxNameRunes characters. An empty name is allowed: the peer then sees the short key.
func cleanDisplayName(s string) (string, error) {
	name := tidyName(s)
	if utf8.RuneCountInString(name) > maxNameRunes {
		return "", shareErr("name-too-long", ErrBadName, strconv.Itoa(maxNameRunes))
	}
	return name, nil
}

// peerName is the name to show for what another computer sent: the same cleaning, cut at maxNameRunes, and
// "Kolo" with the short key when nothing is left.
func peerName(s string, key ed25519.PublicKey) string {
	name := tidyName(s)
	if utf8.RuneCountInString(name) > maxNameRunes {
		name = string([]rune(name)[:maxNameRunes])
	}
	if name == "" {
		return "Kolo " + shortKey(key)
	}
	return name
}

// shortKey is the first 8 hex characters of a key: a label to tell sessions apart, too short to compare.
func shortKey(key ed25519.PublicKey) string {
	return hex.EncodeToString(key[:min(len(key), 4)])
}

// fingerprintSize is 128 bits, kolo-invite's fingerprint of a key: posing as a key needs a second preimage of
// that value, about 2^128 work (kolo-invite README, "Security").
const fingerprintSize = 16

// fingerprint is what people compare over a channel they trust: the first 16 bytes of a labelled SHA-256 of the
// key, as 8 groups of 4 hex characters.
func fingerprint(key ed25519.PublicKey) string {
	sum := sha256.Sum256(append([]byte("kolo-share/fingerprint/v0"), key...))
	h := hex.EncodeToString(sum[:fingerprintSize])
	groups := make([]string, 0, len(h)/4)
	for i := 0; i < len(h); i += 4 {
		groups = append(groups, h[i:i+4])
	}
	return strings.Join(groups, " ")
}

// tidyName drops what is not printable text and collapses blanks.
func tidyName(s string) string {
	s = strings.ToValidUTF8(s, "")
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = true
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), unicode.Is(unicode.Co, r), r == utf8.RuneError:
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	return b.String()
}
