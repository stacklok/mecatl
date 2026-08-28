package server

import (
	"crypto/rand"
	"encoding/base32"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
)

// runIDBytes is the entropy behind a minted run id.
//
// 16 bytes = 128 bits. The uniqueness obligation a run id inherits from ADR 0044
// is not merely "distinct within this process" — it backs the CWE-863 askID
// replay guard, so a collision between two attempts (in ANY process, at ANY
// time, since ids are persisted and compared across restarts) would let a stale
// verdict resolve a re-minted ask. A process-local counter cannot promise that;
// 128 random bits can.
const runIDBytes = 16

// runIDEncoding is unpadded lowercase base32.
//
// The requirement that decides this is COLON-FREE: a run id feeds the ask
// discriminator, and the askID grammar is
// "<sessionID>:<n>:<callID>:<discriminator>", so a colon would make it
// ambiguous. Base32 also avoids base64's "+" and "/", keeps the id
// case-insensitively stable (so it survives being lowercased by a log pipeline
// or a URL path), and stays alphanumeric — which matters because this value is
// echoed to clients and may end up in a URL or a filename.
var runIDEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newRunID mints an opaque run identity (ADR 0249).
//
// OPAQUE means opaque: it encodes nothing — no session id, no timestamp, no
// sequence — so no client can parse structure out of it and come to depend on
// that structure. It exists to be compared for equality and nothing else.
//
// crypto/rand.Read is documented never to return an error on any supported
// platform (it panics internally rather than failing), so there is no error to
// return here and no degraded path to design.
func newRunID() string {
	var b [runIDBytes]byte
	_, _ = rand.Read(b[:])
	return "run_" + strings.ToLower(runIDEncoding.EncodeToString(b[:]))
}

// runlessEventTypes is the CLOSED set of event types permitted to carry an empty
// Event.RunID (ADR 0249).
//
// An empty RunID means "session-scoped, not run-scoped" — a real meaning, not a
// missing value. These three are emitted by the scheduler from composition,
// outside any agent loop, so there is genuinely no run to attribute them to.
// Manufacturing a synthetic run id for them would be a lie that later reads as
// truth.
//
// This map is the AUDIT RECORD, in the same idiom as the harnessTokenFields
// exemption list guarding the UTF-8 mapper: adding a line is a deliberate,
// reviewable act, and a new run-less emitter fails CI rather than quietly
// widening the meaning of "empty". The consequence a reviewer should weigh
// before adding one: an ergonomic attachment filters to a single run and so will
// NEVER deliver the new event type, while a session activity stream will.
var runlessEventTypes = map[session.EventType]struct{}{
	session.EvScheduleFired:   {},
	session.EvScheduleSkipped: {},
	session.EvScheduleFailed:  {},
}

// allowsEmptyRunID reports whether t may legitimately be appended with no run id.
func allowsEmptyRunID(t session.EventType) bool {
	_, ok := runlessEventTypes[t]
	return ok
}
