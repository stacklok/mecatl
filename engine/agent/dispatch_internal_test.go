package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// TestNewAskIDSessionPrefixContract pins the askID namespace contract at the
// PRODUCER: every askID starts with "<sessionID>:". cmd/mecatui's isChildAsk
// consumes this prefix to classify a surfaced ask as main-agent vs subagent (the
// child session id IS the namespace), so changing newAskID's format silently
// breaks that client-side classification — this test makes the break loud here,
// where the format is minted.
func TestNewAskIDSessionPrefixContract(t *testing.T) {
	cases := []struct {
		name   string
		sessID session.SessionID
		n      int
		callID session.ToolCallID
	}{
		{"main session id", "sess-abc123", 1, "call-9"},
		{"child session id", "subagent-call-9", 3, "k1"},
		{"zero counter", "sess-x", 0, "c0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := newAskID(tc.sessID, tc.n, tc.callID, "r7")
			if !strings.HasPrefix(got, string(tc.sessID)+":") {
				t.Fatalf("newAskID(%q, %d, %q) = %q, must start with %q (the prefix cmd/mecatui isChildAsk consumes)",
					tc.sessID, tc.n, tc.callID, got, string(tc.sessID)+":")
			}
		})
	}
}

// TestNewAskIDRunSerialDisjoint pins the per-RUN suffix: identical (session, n,
// call) inputs under DIFFERENT runs must mint DIFFERENT askIDs — the CWE-863
// brake (a retracted run's replayed verdict must never resolve a later run's
// re-minted ask). The suffix must not disturb the consumed prefix.
func TestNewAskIDRunSerialDisjoint(t *testing.T) {
	a := newAskID("subagent-p1", 0, "k1", "r1")
	b := newAskID("subagent-p1", 0, "k1", "r2")
	if a == b {
		t.Fatalf("askIDs of two runs over the same (session, n, call) must differ; both = %q", a)
	}
	if !strings.HasPrefix(a, "subagent-p1:") || !strings.HasPrefix(b, "subagent-p1:") {
		t.Fatalf("the run-serial suffix must not disturb the consumed session-id prefix: %q / %q", a, b)
	}
}

// TestNewAskIDDiscriminatorReconstructable pins ADR-0044's headline property: the
// trailing component is now a plain string, so the SAME (session, n, callID,
// discriminator) inputs mint an IDENTICAL askID across two independent calls — a
// durable host that persists its discriminator can reconstruct the askID in a
// later process. Distinct discriminators still mint disjoint askIDs (the CWE-863
// brake under the host's unique-per-attempt contract).
func TestNewAskIDDiscriminatorReconstructable(t *testing.T) {
	a := newAskID("sess-abc", 2, "call-7", "run-42")
	b := newAskID("sess-abc", 2, "call-7", "run-42")
	if a != b {
		t.Fatalf("the same (session, n, callID, discriminator) must reconstruct an identical askID: %q != %q", a, b)
	}
	c := newAskID("sess-abc", 2, "call-7", "run-43")
	if a == c {
		t.Fatalf("distinct discriminators must mint disjoint askIDs (CWE-863 brake); both = %q", a)
	}
}

// TestNewAskIDNoDiscriminatorMatchesSerial pins that the "r<serial>" fallback
// reproduces today's EXACT askID format byte-for-byte: when startRun passes the
// process-global serial as "r<n>", the minted id equals the legacy layout, so an
// in-memory host that supplies no discriminator is unaffected (ADR-0044 "no
// change when unset").
func TestNewAskIDNoDiscriminatorMatchesSerial(t *testing.T) {
	const (
		id     session.SessionID  = "sess-x"
		n                         = 3
		callID session.ToolCallID = "c0"
	)
	got := newAskID(id, n, callID, "r5")
	want := fmt.Sprintf("%s:%d:%s:r5", id, n, callID)
	if got != want {
		t.Fatalf("the r<serial> fallback must reproduce the legacy format: got %q, want %q", got, want)
	}
}

// TestNewAskIDDiscriminatorPreservesPrefix pins that a host-supplied discriminator
// does NOT disturb the consumed "<sessionID>:" prefix that cmd/mecatui's isChildAsk
// classifies main-vs-subagent on (ADR-0044 constraint 1). It mirrors the prefix
// assertion of TestNewAskIDSessionPrefixContract for the discriminator path.
func TestNewAskIDDiscriminatorPreservesPrefix(t *testing.T) {
	const sessionID session.SessionID = "sess-main-1"
	askID := newAskID(sessionID, 1, "call-9", "run-42")
	if !strings.HasPrefix(askID, string(sessionID)+":") {
		t.Fatalf("a host discriminator must preserve the %q prefix isChildAsk consumes; got %q", string(sessionID)+":", askID)
	}
}

// TestADR_0245_RunIDIsTheAskDiscriminator is AC4.2: a host that sets ONLY
// RunRequest.RunID gets reconstructable askIDs, because the run id SUPPLIES the
// ask discriminator.
//
// This is the arrangement ADR 0044 wrote in terms of a run id that did not yet
// exist ("A durable host passes its own RunID"). Before ADR 0249 the seam was
// real but unused: nothing in the repo set AskIDDiscriminator, so the property
// was theoretical.
func TestADR_0245_RunIDIsTheAskDiscriminator(t *testing.T) {
	cases := []struct {
		name         string
		req          RunRequest
		want         string
		wantRejected bool
		why          string
	}{
		{
			name: "run id supplies the discriminator",
			req:  RunRequest{RunID: "run_abc"},
			want: "run_abc",
			why:  "a durable host sets ONE field and gets both the event stamp and reconstructable askIDs",
		},
		{
			name: "explicit discriminator wins over run id",
			req:  RunRequest{RunID: "run_abc", AskIDDiscriminator: "explicit"},
			want: "explicit",
			why:  "the derivation is a DEFAULT, not a constraint — a caller needing a non-run-id discriminator can still set one",
		},
		{
			name: "neither set falls back to the serial",
			req:  RunRequest{},
			want: "r7",
			why:  "an in-memory host that supplies nothing keeps the exact legacy behaviour",
		},
		{
			name:         "colon-bearing run id is rejected, not sanitised",
			req:          RunRequest{RunID: "run:abc"},
			want:         "r7",
			wantRejected: true,
			why:          "stripping a colon could collapse two distinct host ids onto one askID, re-opening the CWE-863 replay collision",
		},
		{
			name:         "colon-bearing explicit discriminator is rejected too",
			req:          RunRequest{AskIDDiscriminator: "a:b"},
			want:         "r7",
			wantRejected: true,
			why:          "the colon rule is about the askID grammar, so it binds whichever field supplied the value",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rejected := askDiscriminatorFor(tc.req, 7)
			if got != tc.want {
				t.Errorf("discriminator = %q, want %q: %s", got, tc.want, tc.why)
			}
			if rejected != tc.wantRejected {
				t.Errorf("colonRejected = %v, want %v: %s", rejected, tc.wantRejected, tc.why)
			}
		})
	}

	// And the resolved value really does land in the minted askID.
	d, _ := askDiscriminatorFor(RunRequest{RunID: "run_xyz"}, 1)
	if got := newAskID("sess-1", 0, "call-1", d); got != "sess-1:0:call-1:run_xyz" {
		t.Errorf("askID = %q, want the run id as its trailing component", got)
	}
}
