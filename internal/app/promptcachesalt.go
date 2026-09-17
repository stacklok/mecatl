package app

import "crypto/rand"

// newPromptCacheKeySalt mints the per-process prompt_cache_key salt (ADR 0346).
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
// crypto/rand.Text is the stdlib's own "I need a secret string" constructor: it
// returns at least 128 bits of base32-encoded randomness and, since Go 1.24,
// crypto/rand CANNOT fail — a read error is now an unrecoverable program crash,
// not a returned error. The previous hand-rolled read + hex encode carried a
// fail-soft branch for that error, which was already unreachable; documenting a
// graceful degradation the code could not perform was worse than having none.
// The value is never sent as itself, never logged, and never persisted.
func newPromptCacheKeySalt() string {
	return rand.Text()
}
