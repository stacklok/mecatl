// Package id provides typed, fixed-length Mecatl resource identifiers.
// IDs contain a three-character kind prefix and 22 independent, uniformly random
// characters from the Bitcoin base58 alphabet. They are case-sensitive and have
// no timestamp or checksum. Commit content addresses are a separate contract.
package id

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strings"
)

const (
	alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	bodyLen  = 22
	kindLen  = 3
)

// Kind identifies a registered resource type. Unknown kinds are rejected.
type Kind string

// History identifies an append-only history.
const History Kind = "HST"

// ID is a validated resource identifier. Its zero value is unset, not valid.
// IDs are comparable and may be used as map keys.
type ID struct {
	value string
}

// New generates an ID of the given kind using secure, unbiased randomness.
// It panics for an unknown kind or if secure randomness is unavailable.
// The caller must enforce uniqueness with create-only storage operations;
// generation alone does not reserve an identity.
func New(kind Kind) ID {
	v, err := newID(kind, rand.Reader)
	if err != nil {
		panic(err)
	}
	return v
}

// newID keeps the entropy source private and injectable for deterministic tests.
// rand.Int performs unbiased rejection sampling; failures discard the partial ID.
func newID(kind Kind, source io.Reader) (ID, error) {
	if kind != History {
		return ID{}, errors.New("unknown ID kind")
	}
	var body [bodyLen]byte
	limit := big.NewInt(int64(len(alphabet)))
	for i := range body {
		n, err := rand.Int(source, limit)
		if err != nil {
			return ID{}, fmt.Errorf("generate ID: %w", err)
		}
		body[i] = alphabet[n.Int64()]
	}
	return ID{value: string(kind) + string(body[:])}, nil
}

// Parse validates a full ID without trimming, case folding, or abbreviation.
func Parse(s string) (ID, error) {
	if len(s) != kindLen+bodyLen {
		return ID{}, errors.New("invalid ID length")
	}
	if Kind(s[:kindLen]) != History {
		return ID{}, errors.New("unknown ID kind")
	}
	for i := kindLen; i < len(s); i++ {
		if !strings.ContainsRune(alphabet, rune(s[i])) {
			return ID{}, errors.New("invalid ID character")
		}
	}
	return ID{value: s}, nil
}

// Kind returns the resource kind, or the empty kind for an unset ID.
func (v ID) Kind() Kind {
	if v.value == "" {
		return ""
	}
	return Kind(v.value[:kindLen])
}

// String returns the full ID, or an empty string for an unset ID.
func (v ID) String() string { return v.value }
