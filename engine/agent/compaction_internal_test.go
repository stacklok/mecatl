package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// internalSoulSrc / internalIndexSrc are minimal turn-0 sources for the internal
// (package agent) tests, used to render REAL fragments via the live prompt
// assemblers — so the genuine-user predicate is exercised against the same bytes
// the loop injects (never a hand-copied header literal). A header reword that broke
// the predicate would turn these helpers' callers red.
type internalSoulSrc struct{ body string }

func (s internalSoulSrc) Load(context.Context) (string, error) { return s.body, nil }

type internalIndexSrc struct{ entries []tool.MemoryEntry }

func (s internalIndexSrc) Index(context.Context) ([]tool.MemoryEntry, error) {
	return s.entries, nil
}

func injectedSoulFragment(t *testing.T) session.Message {
	t.Helper()
	msgs, err := (prompt.SoulAssembler{Src: internalSoulSrc{body: "terse engineer"}}).Assemble(context.Background(), nil)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("render soul fragment: msgs=%d err=%v", len(msgs), err)
	}
	return msgs[0]
}

func injectedMemoryFragment(t *testing.T) session.Message {
	t.Helper()
	entries := []tool.MemoryEntry{{Key: "pref/runner", Description: "preferred test runner"}}
	msgs, err := (prompt.MemoryIndexAssembler{Src: internalIndexSrc{entries: entries}}).Assemble(context.Background(), nil)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("render memory fragment: msgs=%d err=%v", len(msgs), err)
	}
	return msgs[0]
}

// TestIsGenuineUserTurnSkipsInjectedFragments pins that the genuine-user predicate
// (the shared anchor for the compaction pin, the back-snap, and the resume gate)
// recognises a real user instruction but skips harness-injected turn-0 fragments
// (rendered via the live assemblers) AND synthesised compaction summaries.
func TestIsGenuineUserTurnSkipsInjectedFragments(t *testing.T) {
	genuine := session.NewUserMessage("rename Foo to Bar")
	if !isGenuineUserTurn(genuine) {
		t.Fatalf("a real user instruction must be a genuine user turn")
	}
	for _, m := range []session.Message{
		injectedSoulFragment(t),
		injectedMemoryFragment(t),
		session.NewUserMessage(compactionSummaryMarker + " earlier turns…"),
		session.NewUserMessage(tier4SummaryMarker + "\n## Goal"),
		session.NewAssistantMessage("not a user turn", "", nil),
	} {
		if isGenuineUserTurn(m) {
			t.Fatalf("isGenuineUserTurn must skip non-genuine message: %q", m.Text)
		}
	}
}

// TestFirstUserAndFloorAnchorPastInjectedFragments is the unit-level pin-fix
// guard: with leading system + injected soul/memory fragments BEFORE the genuine
// goal, firstUser returns the GENUINE goal (not the injected fragment) and
// userSnapFloor lands one PAST the genuine goal's index — so the back-snap can
// never pull the goal into the tail nor anchor on a fragment.
func TestFirstUserAndFloorAnchorPastInjectedFragments(t *testing.T) {
	msgs := []session.Message{
		session.NewSystemMessage("system rules"),   // 0
		injectedSoulFragment(t),                    // 1 (RoleUser, injected)
		injectedMemoryFragment(t),                  // 2 (RoleUser, injected)
		session.NewUserMessage("THE REAL GOAL"),    // 3 (genuine)
		session.NewAssistantMessage("ok", "", nil), // 4
	}
	got, ok := firstUser(msgs)
	if !ok || got.Text != "THE REAL GOAL" {
		t.Fatalf("firstUser anchored on the wrong message: ok=%v text=%q", ok, got.Text)
	}
	if floor := userSnapFloor(msgs); floor != 4 {
		t.Fatalf("userSnapFloor = %d, want 4 (one past the genuine goal at index 3)", floor)
	}
}

// TestSnapCutToRecentUserTurn is the direct unit test of the back-snap's pure index
// arithmetic, exercising each of its three exits (recentUserTurnsKept reached /
// maxUserSnapLookback hit / floor reached), the clamps, and the
// synthesised-summary-skip. It builds a role string per case (u=user, a=assistant,
// t=tool, s=summary-prefixed user) so the message positions are obvious.
func TestSnapCutToRecentUserTurn(t *testing.T) {
	// mk turns a role string into a message slice. 'u' is a plain user turn (the
	// Text is its index so equality is unambiguous); 's' is a user turn prefixed with
	// compactionSummaryMarker (a synthesised summary the back-snap must skip).
	mk := func(roles string) []session.Message {
		out := make([]session.Message, len(roles))
		for i, r := range roles {
			switch r {
			case 'u':
				out[i] = session.NewUserMessage("u")
			case 's':
				out[i] = session.NewUserMessage(compactionSummaryMarker + " synthesised")
			case 'a':
				out[i] = session.NewAssistantMessage("a", "", nil)
			case 't':
				out[i] = session.NewToolMessage(session.NewToolResult("c", "body"))
			default:
				t.Fatalf("bad role %q", r)
			}
		}
		return out
	}

	cases := []struct {
		name  string
		roles string
		cut   int
		floor int
		want  int
	}{
		{
			// recentUserTurnsKept(3) reached: snap to the 3rd-most-recent user turn.
			// users at 1,2,3,4; cut=8 walks back 7,6,5(no users until)... here users
			// are contiguous early, so walking back from 7 finds u@4,u@3,u@2 → stop@2.
			name:  "recentUserTurnsKept reached",
			roles: "auuuuaaa",
			cut:   8,
			floor: 0,
			want:  2,
		},
		{
			// maxUserSnapLookback hit: a single recent user turn within the bound is
			// captured (best moves to it), then the bound stops the walk before any
			// older turn. Build a run longer than the bound with one user near the cut.
			name:  "maxUserSnapLookback hit, recent user captured",
			roles: "u" + strings.Repeat("a", maxUserSnapLookback) + "u" + strings.Repeat("a", 3),
			cut:   1 + maxUserSnapLookback + 1 + 3, // = len
			floor: 0,
			// The recent user sits at index 1+maxUserSnapLookback; the walk from cut-1
			// reaches it (3 'a's back) and snaps there; the bound then halts before the
			// index-0 user.
			want: 1 + maxUserSnapLookback,
		},
		{
			// maxUserSnapLookback hit, NO user within the bound: cut unchanged.
			name:  "maxUserSnapLookback hit, no user in reach",
			roles: "u" + strings.Repeat("a", maxUserSnapLookback+5),
			cut:   1 + maxUserSnapLookback + 5,
			floor: 0,
			want:  1 + maxUserSnapLookback + 5, // unchanged: the lone user is beyond the bound
		},
		{
			// floor reached before any user: cut unchanged (no snap into the head).
			name:  "floor reached, no user above floor",
			roles: "uuaaaa", // users only at 0,1 — below the floor
			cut:   6,
			floor: 2,
			want:  6,
		},
		{
			// floor reached WITH a user exactly at floor: it counts, snap to it.
			name:  "user at floor",
			roles: "aauaaa",
			cut:   6,
			floor: 2,
			want:  2,
		},
		{
			// synthesised-summary skip: the only "user" turns are summary-prefixed, so
			// the back-snap finds no genuine user turn and leaves the cut unchanged.
			name:  "synthesised summary skipped",
			roles: "asaasa",
			cut:   6,
			floor: 0,
			want:  6,
		},
		{
			// a genuine user turn behind a synthesised summary: the summary is skipped,
			// the genuine user (index 1) is the anchor.
			name:  "genuine user behind a summary",
			roles: "uasaaa", // u@1? no — u@0, s@2; floor 1 to exclude pin-like idx0
			cut:   6,
			floor: 0,
			want:  0,
		},
		{
			// clamp: cut > len is clamped to len; here no user, so unchanged at len.
			name:  "cut clamped to len",
			roles: "aaa",
			cut:   99,
			floor: 0,
			want:  3,
		},
		{
			// clamp: negative cut clamped to 0; floor>cut returns cut(0).
			name:  "negative cut clamped to zero",
			roles: "uaa",
			cut:   -5,
			floor: 0,
			want:  0,
		},
		{
			// floor > cut: returns cut unchanged (disjoint window).
			name:  "floor above cut",
			roles: "uuaa",
			cut:   1,
			floor: 3,
			want:  1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs := mk(tc.roles)
			got := snapCutToRecentUserTurn(msgs, tc.cut, tc.floor)
			if got != tc.want {
				t.Fatalf("snapCutToRecentUserTurn(%q, cut=%d, floor=%d) = %d, want %d",
					tc.roles, tc.cut, tc.floor, got, tc.want)
			}
			// Contract: never larger than the clamped input cut, never below floor.
			clamped := tc.cut
			if clamped < 0 {
				clamped = 0
			}
			if clamped > len(msgs) {
				clamped = len(msgs)
			}
			if got > clamped {
				t.Fatalf("back-snap moved FORWARD: got %d > clamped cut %d", got, clamped)
			}
			if got < tc.floor && tc.floor <= clamped {
				t.Fatalf("back-snap went below floor: got %d < floor %d", got, tc.floor)
			}
		})
	}
}

// TestIsSynthesisedSummary pins that BOTH synthesised-summary markers are recognised
// (the paths-summary AND the tier-4 LLM summary) — the re-compaction footgun fix
// covers both. A genuine user message is not a summary.
func TestIsSynthesisedSummary(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{compactionSummaryMarker + " earlier turns…", true},
		{tier4SummaryMarker + "\n## Goal", true},
		{"rename Foo to Bar", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isSynthesisedSummary(tc.text); got != tc.want {
			t.Fatalf("isSynthesisedSummary(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// TestTruncateToolBodyPreservesParts pins the PR #226 fix: truncateToolBody keys
// truncation on the TEXT body length only, and must not collaterally drop the
// result's typed Parts (image/resource_link/embedded/structured blocks). The text
// is still trimmed with the elision marker (the reason the function exists), but a
// non-empty Parts slice must survive verbatim, and IsError must be preserved.
func TestTruncateToolBodyPreservesParts(t *testing.T) {
	longBody := strings.Repeat("X", 1000)
	link := session.NewResourceLinkBlock("file:///report.pdf", "report.pdf", "Report", "", "application/pdf", 2048, nil)

	t.Run("success result", func(t *testing.T) {
		call := session.ToolCallID("c1")
		orig := session.NewToolMessage(session.NewToolResultWithParts(call, longBody, []session.Content{link}))

		got := truncateToolBody(orig, 400)

		if got.ToolResult == nil {
			t.Fatalf("truncateToolBody dropped the tool result")
		}
		if len(got.ToolResult.Content) >= len(longBody) {
			t.Fatalf("text body was not truncated: got %d chars, want < %d", len(got.ToolResult.Content), len(longBody))
		}
		if !strings.Contains(got.ToolResult.Content, "elided by compaction") {
			t.Fatalf("truncated body missing elision marker: %q", got.ToolResult.Content)
		}
		if len(got.ToolResult.Parts) != 1 {
			t.Fatalf("Parts dropped by truncation: got %d blocks, want 1", len(got.ToolResult.Parts))
		}
		if !reflect.DeepEqual(got.ToolResult.Parts[0], link) {
			t.Fatalf("surviving block was mutated: got %+v, want %+v", got.ToolResult.Parts[0], link)
		}
		if got.ToolResult.IsError {
			t.Fatalf("IsError flipped true on a success result")
		}
	})

	t.Run("error result", func(t *testing.T) {
		call := session.ToolCallID("c2")
		orig := session.NewToolResultWithParts(call, longBody, []session.Content{link})
		orig.IsError = true
		origMsg := session.NewToolMessage(orig)

		got := truncateToolBody(origMsg, 400)

		if !got.ToolResult.IsError {
			t.Fatalf("IsError not preserved through truncation of an error result")
		}
		if len(got.ToolResult.Parts) != 1 {
			t.Fatalf("Parts dropped by truncation of an error result: got %d blocks, want 1", len(got.ToolResult.Parts))
		}
	})
}
