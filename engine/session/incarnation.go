package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
)

const incarnationBytes = 16

var incarnationEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// IncarnationID is an opaque identity for one lifetime of a session key. New
// values contain 128 bits from crypto/rand and encode no metadata.
type IncarnationID string

// NewIncarnationID mints an opaque session-incarnation identity.
func NewIncarnationID() IncarnationID {
	var random [incarnationBytes]byte
	_, _ = rand.Read(random[:])
	var encoded [26]byte
	incarnationEncoding.Encode(encoded[:], random[:])
	for i, c := range encoded {
		if c >= 'A' && c <= 'Z' {
			encoded[i] = c + ('a' - 'A')
		}
	}
	var id [4 + len(encoded)]byte
	copy(id[:], "inc_")
	copy(id[4:], encoded[:])
	return IncarnationID(string(id[:]))
}

// Valid reports whether i has the closed syntax of a minted or deterministic
// legacy incarnation. Prefix separation makes legacy values disjoint from all
// newly minted values.
func (i IncarnationID) Valid() bool {
	s := string(i)
	if strings.HasPrefix(s, "inc_") {
		if len(s) != 4+26 {
			return false
		}
		for _, c := range s[4:] {
			if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
				return false
			}
		}
		switch s[len(s)-1] {
		case 'a', 'e', 'i', 'm', 'q', 'u', 'y', '4':
			return true
		default:
			return false
		}
	}
	if strings.HasPrefix(s, "legacy_") {
		if len(s) != 7+64 {
			return false
		}
		_, err := hex.DecodeString(s[7:])
		return err == nil
	}
	return false
}

// LegacyIncarnationID deterministically identifies a pre-incarnation snapshot.
// Its reserved prefix cannot collide with a newly minted incarnation. It is
// honest only at the legacy snapshot boundary; new sessions never use it.
func LegacyIncarnationID(id SessionID, createdAtUnixNano int64, owner *Principal) IncarnationID {
	h := sha256.New()
	_, _ = h.Write([]byte("mecatl.session-incarnation/legacy-v1\x00"))
	var framed [8]byte
	binary.BigEndian.PutUint64(framed[:], uint64(len(id)))
	_, _ = h.Write(framed[:])
	_, _ = h.Write([]byte(id))
	var timestamp [binary.MaxVarintLen64]byte
	n := binary.PutVarint(timestamp[:], createdAtUnixNano)
	_, _ = h.Write(timestamp[:n])
	scope := PrincipalScopeHash(owner)
	_, _ = h.Write(scope[:])
	return IncarnationID("legacy_" + hex.EncodeToString(h.Sum(nil)))
}

// PersistedIncarnationID returns inc when valid, or the deterministic legacy
// identity for a snapshot written before incarnations were persisted.
func PersistedIncarnationID(inc IncarnationID, id SessionID, createdAtUnixNano int64, owner *Principal) IncarnationID {
	if inc.Valid() {
		return inc
	}
	return LegacyIncarnationID(id, createdAtUnixNano, owner)
}

var errInvalidIncarnation = errors.New("session: invalid incarnation")

// Incarnation returns this aggregate's immutable incarnation identity.
func (s *Session) Incarnation() IncarnationID {
	if s == nil {
		return ""
	}
	return s.incarnation
}

// RestoreIncarnation replaces New's provisional random incarnation while an
// aggregate is still idle. Empty persisted values become a deterministic,
// prefix-disjoint legacy identity.
func (s *Session) RestoreIncarnation(inc IncarnationID) error {
	if s == nil || s.State != StateIdle {
		return errInvalidIncarnation
	}
	if inc == "" {
		inc = LegacyIncarnationID(s.ID, s.CreatedAt.UnixNano(), s.Owner)
	}
	if !inc.Valid() {
		return errInvalidIncarnation
	}
	s.incarnation = inc
	return nil
}

// IncarnationFingerprint returns a domain-separated internal binding for an
// incarnation in its session-key and owner scope. It contains no timestamp.
func IncarnationFingerprint(inc IncarnationID, id SessionID, owner *Principal) string {
	if !inc.Valid() {
		return ""
	}
	h := sha256.New()
	_, _ = h.Write([]byte("mecatl.session-incarnation-fingerprint/v2\x00"))
	_, _ = h.Write([]byte(inc))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(id))
	scope := PrincipalScopeHash(owner)
	_, _ = h.Write(scope[:])
	return hex.EncodeToString(h.Sum(nil))
}

// DebugTargetFingerprint returns the immutable identity of a target incarnation.
func DebugTargetFingerprint(s *Session) string {
	if s == nil {
		return ""
	}
	return IncarnationFingerprint(s.Incarnation(), s.ID, s.Owner)
}
