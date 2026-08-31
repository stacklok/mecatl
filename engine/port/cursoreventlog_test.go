package port

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// encodedEnvelope mirrors the cursor's on-the-wire shape so these tests can build
// a cursor that is structurally valid but semantically wrong — the determined
// client ADR 0250 says the opaque encoding must survive.
type encodedEnvelope struct {
	V string `json:"v"`
	S string `json:"s"`
	G string `json:"g"`
	P string `json:"p"`
}

func encodeEnvelope(t *testing.T, e encodedEnvelope) Cursor {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return Cursor(base64.RawURLEncoding.EncodeToString(raw))
}

// TestADR_0250_StaleGenerationCursorExpires is AC6.3: a cursor from a prior log
// generation yields ErrCursorExpired, never silent degradation or wrong data.
//
// It exercises DecodeCursor directly rather than through a backend. The shared
// conformance suite proves each backend PROPAGATES the outcome; this proves the
// single decision itself, which ADR 0250 deliberately sited in port so that four
// backends could not drift into four generation policies.
//
// The failure this forbids is not an error. A positional cursor into a rebuilt
// log resolves happily to a real record that is simply not the one the client
// last saw, so the only observable symptom is wrong data much later.
func TestADR_0250_StaleGenerationCursorExpires(t *testing.T) {
	const sid = session.SessionID("cursor-unit-session")
	issued := EncodeCursor(sid, "gen-A", "42")

	t.Run("a superseded generation expires", func(t *testing.T) {
		pos, err := DecodeCursor(issued, sid, "gen-B")
		if !errors.Is(err, ErrCursorExpired) {
			t.Fatalf("DecodeCursor(genA cursor, current=gen-B) error = %v, want ErrCursorExpired", err)
		}
		if pos != "" {
			t.Errorf("an expired cursor surfaced position %q; a rejected cursor must never hand back a usable position", pos)
		}
	})

	t.Run("the matching generation resolves", func(t *testing.T) {
		pos, err := DecodeCursor(issued, sid, "gen-A")
		if err != nil {
			t.Fatalf("DecodeCursor(genA cursor, current=gen-A): %v", err)
		}
		if pos != "42" {
			t.Errorf("position = %q, want %q", pos, "42")
		}
	})

	t.Run("the zero cursor means the beginning, not the latest", func(t *testing.T) {
		// Load-bearing: a first-time attach has no cursor, and "replay
		// everything then follow" is the common case. Treating the zero value
		// as "latest" would silently skip a session's entire history.
		pos, err := DecodeCursor("", sid, "gen-A")
		if err != nil {
			t.Fatalf("DecodeCursor(zero, gen-A): %v", err)
		}
		if pos != "" {
			t.Errorf("zero cursor resolved to position %q, want the empty (beginning) position", pos)
		}
	})

	t.Run("a legacy log with no generation basis still resolves", func(t *testing.T) {
		// A log written before generations existed reports the empty generation,
		// and its cursors carry the empty generation too, so the comparison
		// holds with no special case at any call site. Losing this makes every
		// pre-cursor log unreadable.
		legacy := EncodeCursor(sid, "", "7")
		pos, err := DecodeCursor(legacy, sid, "")
		if err != nil {
			t.Fatalf("DecodeCursor(legacy cursor, current=\"\"): %v", err)
		}
		if pos != "7" {
			t.Errorf("position = %q, want %q", pos, "7")
		}
	})

	t.Run("a generation-bearing cursor does not pass as legacy", func(t *testing.T) {
		// The inverse of the case above, and the reason it is safe: an empty
		// currentGeneration must not become a wildcard that accepts anything.
		if _, err := DecodeCursor(issued, sid, ""); !errors.Is(err, ErrCursorExpired) {
			t.Errorf("DecodeCursor(genA cursor, current=\"\") error = %v, want ErrCursorExpired", err)
		}
	})
}

// TestADR_0250_TamperedCursorRejected is AC6.4: a tampered or malformed cursor is
// rejected, never coerced to a position.
//
// Every case here could plausibly be "helpfully" treated as the beginning of the
// log. That help is precisely what loses data: a client whose cursor was
// corrupted in transit would receive a full replay it believes is an increment,
// and the duplicate delivery would look like a harness bug rather than a
// corrupted token.
func TestADR_0250_TamperedCursorRejected(t *testing.T) {
	const gen = "gen-A"
	const sid = session.SessionID("cursor-unit-session")
	issued := EncodeCursor(sid, gen, "42")

	malformed := map[string]Cursor{
		"not base64url":            "!!!not-base64!!!",
		"base64 of non-JSON":       Cursor(base64.RawURLEncoding.EncodeToString([]byte("hello"))),
		"base64 of a bare string":  Cursor(base64.RawURLEncoding.EncodeToString([]byte(`"just-a-string"`))),
		"unknown envelope version": encodeEnvelope(t, encodedEnvelope{V: "cur/999", S: string(sid), G: gen, P: "42"}),
		"empty envelope version":   encodeEnvelope(t, encodedEnvelope{V: "", S: string(sid), G: gen, P: "42"}),
		"unknown field smuggled in": Cursor(base64.RawURLEncoding.EncodeToString(
			[]byte(`{"v":"cur/1","s":"cursor-unit-session","g":"gen-A","p":"42","extra":true}`))),
		"truncated base64": issued[:len(issued)/2],
	}
	for name, cur := range malformed {
		t.Run("malformed/"+name, func(t *testing.T) {
			pos, err := DecodeCursor(cur, sid, gen)
			if !errors.Is(err, ErrCursorMalformed) {
				t.Fatalf("DecodeCursor(%q) error = %v, want ErrCursorMalformed", cur, err)
			}
			if pos != "" {
				t.Errorf("a malformed cursor surfaced position %q; it must never be coerced to a position", pos)
			}
		})
	}

	t.Run("a hand-edited generation is expired, not accepted", func(t *testing.T) {
		// The opacity promise in practice: the encoding is stateless and
		// therefore inspectable, so the guarantee cannot be "you cannot read
		// it" — it is "editing it does not get you anywhere".
		forged := encodeEnvelope(t, encodedEnvelope{V: cursorEnvelopeVersion, S: string(sid), G: "gen-FORGED", P: "42"})
		if _, err := DecodeCursor(forged, sid, gen); !errors.Is(err, ErrCursorExpired) {
			t.Errorf("DecodeCursor(forged generation) error = %v, want ErrCursorExpired", err)
		}
	})

	t.Run("malformed and expired are distinguishable", func(t *testing.T) {
		// A consumer acts differently on each: an expired cursor is recoverable
		// by restarting from the beginning, while a malformed one indicates a
		// bug or tampering that restarting would hide. Collapsing them into one
		// sentinel removes the consumer's ability to tell those apart.
		if errors.Is(ErrCursorMalformed, ErrCursorExpired) || errors.Is(ErrCursorExpired, ErrCursorMalformed) {
			t.Fatal("ErrCursorMalformed and ErrCursorExpired must be distinct sentinels")
		}
	})

	t.Run("a round-trip preserves the position exactly", func(t *testing.T) {
		// Guards the other direction: rejection is only meaningful if a genuine
		// cursor survives intact, including positions with bytes that a naive
		// encoding would mangle.
		for _, pos := range []string{"", "0", "42", "1755012345678-0", "offset:987654321", "a/b+c=d", "é世"} {
			got, err := DecodeCursor(EncodeCursor(sid, gen, pos), sid, gen)
			if err != nil {
				t.Fatalf("round-trip %q: %v", pos, err)
			}
			if got != pos {
				t.Errorf("round-trip %q = %q", pos, got)
			}
		}
	})
}

// TestADR_0250_CursorEventLogIsAdditive pins the shape ADR 0250 decision 1 rests
// on: CursorEventLog EXTENDS EventLog rather than replacing it, so every cursor
// backend is usable anywhere an EventLog is expected and no existing consumer is
// touched.
//
// The structural assertion is the point. "Additive" is easy to claim in a doc
// comment and easy to break with one method signature change, and the breakage
// surfaces at a distant call site rather than here.
func TestADR_0250_CursorEventLogIsAdditive(t *testing.T) {
	cursorLog := reflect.TypeOf((*CursorEventLog)(nil)).Elem()
	eventLog := reflect.TypeOf((*EventLog)(nil)).Elem()

	if !cursorLog.Implements(eventLog) {
		t.Fatal("CursorEventLog does not satisfy EventLog; the port is no longer additive")
	}
	for i := range eventLog.NumMethod() {
		m := eventLog.Method(i)
		got, ok := cursorLog.MethodByName(m.Name)
		if !ok {
			t.Errorf("CursorEventLog is missing inherited method %q", m.Name)
			continue
		}
		if got.Type != m.Type {
			t.Errorf("CursorEventLog.%s signature = %v, want the inherited %v", m.Name, got.Type, m.Type)
		}
	}
}

// TestADR_0250_CursorIsSessionScoped pins the session half of the
// never-silently-wrong contract, raised in review on #868.
//
// A position means nothing on its own — it is an offset, an index, or a stream id
// inside ONE log. Before this check existed, a cursor issued for session A and
// handed to session B decoded cleanly whenever the two shared a generation value
// and resolved to a real, wrong record. The generation check cannot catch it: a
// LEGACY log predating generations reports the EMPTY generation, so every legacy
// log in a deployment shares one basis value. That was reproduced against both
// shipped backends (jsonlstore returned three of session B's records; the Redis
// legacy LIST path returned two) before the fix.
//
// The check lives in DecodeCursor so it is structural rather than per-backend: a
// backend cannot forget it, and a new backend inherits it.
func TestADR_0250_CursorIsSessionScoped(t *testing.T) {
	const (
		a   = session.SessionID("session-a")
		b   = session.SessionID("session-b")
		gen = "shared-generation"
	)

	t.Run("a cursor from another session is malformed", func(t *testing.T) {
		issued := EncodeCursor(a, gen, "42")
		pos, err := DecodeCursor(issued, b, gen)
		if !errors.Is(err, ErrCursorMalformed) {
			t.Fatalf("DecodeCursor(cursor for A, id=B) error = %v, want ErrCursorMalformed", err)
		}
		if pos != "" {
			t.Errorf("a cross-session cursor surfaced position %q; it must never be coerced", pos)
		}
	})

	t.Run("the empty generation does not disable the session check", func(t *testing.T) {
		// The legacy-log shape, and the whole reason this is not merely a
		// stricter generation comparison: with no generation to compare, the
		// session id is the ONLY thing separating two logs.
		issued := EncodeCursor(a, "", "42")
		if _, err := DecodeCursor(issued, b, ""); !errors.Is(err, ErrCursorMalformed) {
			t.Errorf("legacy cross-session cursor error = %v, want ErrCursorMalformed", err)
		}
	})

	t.Run("the same session still resolves", func(t *testing.T) {
		// Rejection is only meaningful if the ordinary path is untouched.
		issued := EncodeCursor(a, gen, "42")
		got, err := DecodeCursor(issued, a, gen)
		if err != nil {
			t.Fatalf("DecodeCursor(cursor for A, id=A): %v", err)
		}
		if got != "42" {
			t.Errorf("position = %q, want %q", got, "42")
		}
	})

	t.Run("the zero cursor stays session-agnostic", func(t *testing.T) {
		// The zero cursor carries no envelope, so there is nothing to scope. It
		// must keep meaning "the beginning of THIS log" for any id.
		for _, id := range []session.SessionID{a, b, ""} {
			pos, err := DecodeCursor("", id, gen)
			if err != nil || pos != "" {
				t.Errorf("DecodeCursor(zero, id=%q) = (%q, %v), want (\"\", nil)", id, pos, err)
			}
		}
	})
}

// TestADR_0250_EncodeFailureIsFailClosed pins that a failed encode never yields
// the zero Cursor, raised in review on #868.
//
// EncodeCursor cannot return an error (four strings always marshal), so the
// branch is unreachable today — but the VALUE it falls back to is the whole
// point. The zero Cursor means "the beginning of the log", so returning it from a
// broken encode would turn an impossible bug into a silent full replay the
// consumer believes is an increment. The fallback must instead be a value every
// DecodeCursor rejects.
func TestADR_0250_EncodeFailureIsFailClosed(t *testing.T) {
	if cursorEncodeFailed == "" {
		t.Fatal("the encode-failure fallback is the zero Cursor, which means 'replay everything'")
	}
	if _, err := DecodeCursor(cursorEncodeFailed, "any-session", "any-generation"); !errors.Is(err, ErrCursorMalformed) {
		t.Errorf("DecodeCursor(encode-failure fallback) error = %v, want ErrCursorMalformed — the fallback must fail closed", err)
	}
}
