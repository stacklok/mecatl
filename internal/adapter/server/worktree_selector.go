package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"

	"github.com/stacklok/mecatl/engine/session"
)

const worktreeSelectorKeySize = 32

// WorktreeSelectorIssuer issues opaque process-local selectors without keeping
// a registry. Matching recomputes every candidate against current inventory.
type WorktreeSelectorIssuer struct {
	key [worktreeSelectorKeySize]byte
}

// NewWorktreeSelectorIssuer constructs an issuer from one random process key.
func NewWorktreeSelectorIssuer(key []byte) (*WorktreeSelectorIssuer, error) {
	if len(key) != worktreeSelectorKeySize {
		return nil, ErrInvalidPlacementSelection
	}
	issuer := &WorktreeSelectorIssuer{}
	copy(issuer.key[:], key)
	return issuer, nil
}

// Issue derives one opaque caller/source/current-choice selector.
func (i *WorktreeSelectorIssuer) Issue(principal *session.Principal, source session.SessionID, choice Worktree) string {
	if i == nil {
		return ""
	}
	digest := i.digest(principal, source, choice)
	return base64.RawURLEncoding.EncodeToString(digest)
}

// Match checks every current candidate in constant time. It deliberately does
// not decode the selector or retain issued values.
func (i *WorktreeSelectorIssuer) Match(selector string, principal *session.Principal, source session.SessionID, current []Worktree) (Worktree, error) {
	if i == nil || selector == "" {
		return Worktree{}, ErrPlacementNotFound
	}
	provided, err := base64.RawURLEncoding.DecodeString(selector)
	if err != nil || len(provided) != sha256.Size {
		return Worktree{}, ErrPlacementNotFound
	}
	matched := -1
	matches := 0
	for index, candidate := range current {
		expected := i.digest(principal, source, candidate)
		equal := subtle.ConstantTimeCompare(provided, expected)
		matches += equal
		matched = subtle.ConstantTimeSelect(equal, index, matched)
	}
	if matches != 1 {
		return Worktree{}, ErrPlacementNotFound
	}
	return current[matched], nil
}

func (i *WorktreeSelectorIssuer) digest(principal *session.Principal, source session.SessionID, choice Worktree) []byte {
	mac := hmac.New(sha256.New, i.key[:])
	writeSelectorField(mac, string(source))
	if principal != nil {
		writeSelectorField(mac, principal.Issuer)
		writeSelectorField(mac, principal.Subject)
	} else {
		writeSelectorField(mac, "")
		writeSelectorField(mac, "")
	}
	writeSelectorField(mac, choice.Path)
	writeSelectorField(mac, choice.Branch)
	writeSelectorField(mac, choice.Head)
	if choice.Bare {
		writeSelectorField(mac, "1")
	} else {
		writeSelectorField(mac, "0")
	}
	return mac.Sum(nil)
}

func writeSelectorField(mac interface{ Write([]byte) (int, error) }, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = mac.Write(size[:])
	_, _ = mac.Write([]byte(value))
}
