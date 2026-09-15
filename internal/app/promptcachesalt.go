package app

import (
	"crypto/rand"
	"encoding/hex"
)

// newPromptCacheKeySalt mints the per-process prompt_cache_key salt (ADR 0343).
//
// The ADR 0100 derivation has no installation-specific input, so two unrelated
// installations sharing harness version, soul, agent def and tool inventory emit
// an IDENTICAL prompt_cache_key. Salting removes that cross-principal
// correlation while preserving byte-stability within a run.
//
// It is deliberately NOT persisted: no durable pseudonymous identifier on disk,
// at the cost of one sticky-routing lane change per restart — which the
// derivation already treats as fail-soft for an anchor change.
//
// FAIL-SOFT: a crypto/rand read failure returns "", which degrades to ADR 0100's
// unsalted derivation. That is a privacy weakening, never a broken run, and a
// run must not die because the entropy source hiccuped.
func newPromptCacheKeySalt() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
