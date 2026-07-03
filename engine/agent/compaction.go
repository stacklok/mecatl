package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
)

// ErrCompactionWouldOrphan is the sentinel a Compactor returns when the only
// history it could produce would be tool-pairing-invalid (an orphaned tool
// result or a dangling tool call). The loop treats it like any other compaction
// failure — keep the original history and continue without compaction — rather
// than emitting a history that draws a provider HTTP 400 and bricks the session.
// Compactors return the ORIGINAL messages alongside this error so a caller that
// ignores the sentinel still gets a safe slice.
var ErrCompactionWouldOrphan = errors.New("agent: compaction would orphan a tool result or dangle a tool call")

// Compactor compresses a Conversation that has grown past the context-window
// threshold into a shorter, semantically-equivalent history. It is a seam
// (ARCHITECTURE.md §8, gauntlet #12): the default implementation is a pure,
// offline heuristic, but an LLM-backed summariser can be slotted in behind the
// same interface.
//
// Compact returns the replacement message slice, a human-readable summary of
// what was dropped (surfaced on the compaction Event), and an error only on a
// genuine failure. Implementations MUST preserve file paths, decisions, and
// unresolved questions while dropping large tool-output bodies and stale file
// contents.
type Compactor interface {
	Compact(ctx context.Context, conv *session.Conversation) (compacted []session.Message, summary string, err error)
}

// maxToolBodyChars is the per-tool-result body budget the heuristic compactor
// keeps; longer bodies are truncated with an elision marker. File paths and the
// head of each body survive so the model retains orientation.
const maxToolBodyChars = 400

// compactionSummaryMarker prefixes the synthesised paths-summary message both
// compactors emit (a RoleUser message). tier4SummaryMarker prefixes the cascade's
// tier-4 LLM summary message (also a RoleUser message). BOTH are harness-authored
// context, not real user instructions, so the user-turn back-snap SKIPS them (via
// isSynthesisedSummary): a re-compaction must not treat a prior compaction's summary
// as a recent user turn, which would snap the whole post-summary history into the
// verbatim tail and defeat re-compaction. Keep these in sync with the literals
// buildSummary (compaction.go) and CascadeCompactor.summarize (cascade.go) emit.
const (
	compactionSummaryMarker = "[conversation compacted]"
	tier4SummaryMarker      = "[earlier turns summarised]"
)

// isSynthesisedSummary reports whether a message's text is a harness-synthesised
// compaction summary (the paths-summary OR the tier-4 LLM summary) rather than a
// genuine user instruction. The back-snap uses it to avoid anchoring the verbatim
// tail on a prior compaction's own output (the re-compaction footgun).
func isSynthesisedSummary(text string) bool {
	return strings.HasPrefix(text, compactionSummaryMarker) ||
		strings.HasPrefix(text, tier4SummaryMarker)
}

// keepLastTurns is the number of trailing conversation messages the heuristic
// compactor preserves verbatim (the recent working set the model is mid-task on).
const keepLastTurns = 6

// recentUserTurnsKept is how many of the most-recent user turns the verbatim-tail
// back-snap tries to pull into the kept window. Prior art (Codex, gemini-cli) keeps
// the recent user turns verbatim rather than summarising them, because the
// most-recent user instruction is the live task and must never be summarised away —
// the bug this fixes: during heavy tool use the last keep messages by COUNT are all
// assistant/tool messages, so the user's actual task fell into the dropped/summarised
// head ("I don't have the original task... please resend").
const recentUserTurnsKept = 3

// maxUserSnapLookback bounds how far back snapCutToRecentUserTurn may reach (the
// COUNT of messages between the snapped cut and the original count-cut). Without it
// a single ancient lone user turn could drag the WHOLE conversation into the
// verbatim tail, defeating compaction entirely. keepLastTurns*4 (=24 for keep=6) is
// generous enough to capture the recent user turns of a normal tool-heavy stretch
// while still hard-bounding the worst case; when the bound is hit the most-recent
// 1-2 user turns still survive (the bug fix), the older ones stay summarisable.
const maxUserSnapLookback = keepLastTurns * 4

// HeuristicCompactor is the default, network-free Compactor. It keeps the system
// prompt and the user goal, synthesises a single summary message that lists every
// file path touched so far, truncates large tool-result bodies, and preserves the
// last keepLastTurns messages verbatim — back-snapped to recent USER turns
// (snapCutToRecentUserTurn) so the most-recent user instruction survives verbatim
// rather than being summarised away (prior art: Codex, gemini-cli keep the recent
// user turns verbatim; docs/harnesses/07-context-and-mcp.md §4 "what to preserve",
// 03-claude-code-architecture.md "first user message is summarized away",
// 08-design-considerations.md §12). It performs no LLM call, so it is fully
// deterministic and testable offline (gauntlet #12-lite).
type HeuristicCompactor struct {
	// MaxToolBodyChars overrides maxToolBodyChars when > 0.
	MaxToolBodyChars int
	// KeepLastTurns overrides keepLastTurns when > 0.
	KeepLastTurns int
}

// Compact implements Compactor with the heuristic described on HeuristicCompactor.
func (h HeuristicCompactor) Compact(_ context.Context, conv *session.Conversation) ([]session.Message, string, error) {
	bodyBudget := maxToolBodyChars
	if h.MaxToolBodyChars > 0 {
		bodyBudget = h.MaxToolBodyChars
	}
	keep := keepLastTurns
	if h.KeepLastTurns > 0 {
		keep = h.KeepLastTurns
	}

	msgs := conv.Messages
	paths := touchedPaths(msgs)

	// Split the head (to be summarised) from the preserved tail in three steps:
	//  1. the count-based cut (keep the last `keep` messages);
	//  2. back-snap so the tail begins at a recent USER turn (floor = one past the
	//     first-user index keeps the first-user pin out of the tail, so it is neither
	//     double-emitted nor — when it is the only user turn — able to drag the whole
	//     history into the tail) — the role-blind count-tail bug fix, so the
	//     most-recent user instruction stays verbatim instead of being summarised;
	//  3. forward-snap past any leading tool-result messages so the tail never STARTS
	//     on a tool result whose matching assistant call landed in the dropped head —
	//     that orphan draws a provider HTTP 400 on replay and bricks the session.
	// The forward snap stays LAST so the orphan guarantee always holds.
	cut := len(msgs) - keep
	cut = snapCutToRecentUserTurn(msgs, cut, userSnapFloor(msgs))
	cut = snapCutToTurnBoundary(msgs, cut)
	head := msgs[:cut]
	tail := msgs[cut:]

	out := make([]session.Message, 0, len(tail)+3)

	// Preserve the leading system prompt(s) verbatim, if any.
	for _, m := range head {
		if m.Role == session.RoleSystem {
			out = append(out, m)
		}
	}

	// Preserve the first user message (the goal) verbatim.
	if goal, ok := firstUser(head); ok {
		out = append(out, goal)
	}

	// Synthesise the summary message listing touched paths.
	summary := buildSummary(paths)
	out = append(out, session.NewUserMessage(summary))

	// Preserve the tail verbatim, but truncate any oversized tool-result bodies
	// so old file dumps do not dominate the kept window.
	for _, m := range tail {
		out = append(out, truncateToolBody(m, bodyBudget))
	}

	// Self-validate: never emit a history that would orphan a tool result or
	// dangle a tool call (boundary-snapping handles the common case, but defend
	// the contract directly). On failure, abort to the ORIGINAL history.
	if err := session.ValidateToolPairing(out); err != nil {
		return msgs, "", fmt.Errorf("%w: %v", ErrCompactionWouldOrphan, err)
	}

	return out, summary, nil
}

// snapCutToTurnBoundary clamps cut to [0,len(msgs)] and then advances it past any
// leading tool-result messages, so the slice msgs[cut:] never STARTS on a
// RoleTool message whose matching assistant tool call sits below the cut. Both
// compactors share it. A tail that is entirely tool results snaps cut all the way
// to len(msgs) (empty tail), which is fine — the compacted output still carries
// the system prompt, the goal, and the synthesised summary.
func snapCutToTurnBoundary(msgs []session.Message, cut int) int {
	if cut < 0 {
		cut = 0
	}
	if cut > len(msgs) {
		cut = len(msgs)
	}
	for cut < len(msgs) && msgs[cut].Role == session.RoleTool {
		cut++
	}
	return cut
}

// snapCutToRecentUserTurn moves cut BACKWARD (toward 0) so the verbatim tail
// msgs[cut:] begins at a recent user turn, keeping the most-recent user
// instruction(s) verbatim instead of letting them fall into the summarised head.
// This is the fix for the role-blind count-tail bug: during heavy tool use the last
// keep messages are all assistant/tool messages, so the user's ACTUAL task drops out
// of the kept window and is lost after compaction. Prior art (Codex, gemini-cli)
// preserves the recent user turns verbatim for exactly this reason
// (docs/harnesses/07-context-and-mcp.md §4, 08-design-considerations.md §8+§12).
//
// It walks backward from cut-1 toward floor, counting RoleUser messages, and stops
// at the FIRST of:
//   - recentUserTurnsKept user messages passed: snap to that Kth-most-recent user
//     turn so it lands IN the tail;
//   - maxUserSnapLookback messages walked: snap only as far as the bound permits, so
//     an ancient lone user turn can't drag the whole conversation into the tail (the
//     most-recent 1-2 user turns still survive — the bug fix);
//   - floor reached: never snap into the preserved head (keeps the first-user pin and
//     the tail disjoint, so neither is double-emitted).
//
// The returned cut is NEVER larger than the input (back-snap only moves toward 0)
// and NEVER below floor. cut is clamped to [0,len] first.
func snapCutToRecentUserTurn(msgs []session.Message, cut int, floor int) int {
	if cut < 0 {
		cut = 0
	}
	if cut > len(msgs) {
		cut = len(msgs)
	}
	if floor < 0 {
		floor = 0
	}
	if floor > cut {
		return cut
	}

	users := 0
	walked := 0
	best := cut
	for i := cut - 1; i >= floor; i-- {
		walked++
		if isRecentUserTurn(msgs[i]) {
			users++
			best = i
			if users >= recentUserTurnsKept {
				return best
			}
		}
		if walked >= maxUserSnapLookback {
			// Bound hit: snap to the most-recent user turn we found within the
			// lookback (best), or leave cut unchanged if none was seen.
			return best
		}
	}
	return best
}

// isGenuineUserTurn reports whether m is a GENUINE user instruction — the thing
// the compaction pin and the verbatim-tail back-snap must anchor on — as opposed
// to the two classes of harness-authored RoleUser message that can ride in the same
// history:
//
//   - synthesised compaction summaries (the paths-summary and the tier-4 LLM
//     summary), recognised by isSynthesisedSummary — this arm is LOAD-BEARING: a
//     re-compaction must not anchor the pin/back-snap on a PRIOR summary;
//   - turn-0 context fragments (project instructions / soul / memory index / user
//     model), recognised by prompt.IsInjectedTurn0Fragment. As of ADR 0043 these
//     fragments are EPHEMERAL — prepended to the request per-run, never persisted
//     into Conversation.Messages — so they normally do not appear here at all. This
//     arm is therefore DEFENSE-IN-DEPTH: it keeps the predicate correct for any
//     legacy/persisted history that still carries injected fragments (e.g. a
//     session snapshotted before the ephemeral cutover) so the pin never anchors on
//     a stray fragment instead of the user's real goal.
//
// Without the synthesised-summary skip the pin anchored on the FIRST RoleUser
// message — which on a re-compaction could be a prior summary, and historically
// (with a persisted soul/memory deployment) an injected fragment — so the genuine
// first instruction fell into the summarised middle and was dropped (the "I don't
// have the original task" bug). The count of leading injected fragments was
// config-variable (0–4+), so a positional "first N" cannot work; the anchor must be
// content-identified.
func isGenuineUserTurn(m session.Message) bool {
	return m.Role == session.RoleUser &&
		!prompt.IsInjectedTurn0Fragment(m.Text) &&
		!isSynthesisedSummary(m.Text)
}

// isRecentUserTurn reports whether m is a genuine user instruction the back-snap
// should anchor the verbatim tail on. It is isGenuineUserTurn (kept as a named
// alias because the back-snap's doc-comments speak of "recent user turns").
func isRecentUserTurn(m session.Message) bool {
	return isGenuineUserTurn(m)
}

// firstUser returns the first GENUINE user-role message in msgs (the goal),
// skipping harness-injected turn-0 fragments and synthesised summaries via
// isGenuineUserTurn. Anchoring on the genuine goal — not the first injected
// fragment — is what makes buildSummary's "original goal … preserved verbatim"
// claim true again.
func firstUser(msgs []session.Message) (session.Message, bool) {
	for _, m := range msgs {
		if isGenuineUserTurn(m) {
			return m, true
		}
	}
	return session.Message{}, false
}

// userSnapFloor returns the back-snap floor for the heuristic compactor: one PAST
// the first GENUINE user message's index, so snapCutToRecentUserTurn never pulls
// the first-user pin (the goal, preserved separately) into the verbatim tail —
// which would both double-emit it and, when it is the ONLY user turn, drag the
// whole history into the tail and defeat compaction. It skips harness-injected
// turn-0 fragments (isGenuineUserTurn) so the floor lands past the genuine goal,
// not past an injected soul/memory fragment. When there is no genuine user message
// the floor is 0 (the back-snap is a no-op anyway: nothing to snap to).
func userSnapFloor(msgs []session.Message) int {
	for i, m := range msgs {
		if isGenuineUserTurn(m) {
			return i + 1
		}
	}
	return 0
}

// touchedPaths collects the distinct file paths referenced by tool calls across
// the conversation, sorted for determinism. It looks for a "path" or "file_path"
// field in each tool call's JSON args.
func touchedPaths(msgs []session.Message) []string {
	seen := make(map[string]struct{})
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			if p := extractPath(c.Args); p != "" {
				seen[p] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// extractPath pulls a file path out of a tool call's JSON args, checking the
// conventional "path" and "file_path" fields the core tools use. It returns ""
// when args carry no recognisable path (or are not an object).
func extractPath(args json.RawMessage) string {
	if len(args) == 0 {
		return ""
	}
	var obj struct {
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(args, &obj); err != nil {
		return ""
	}
	if obj.Path != "" {
		return obj.Path
	}
	return obj.FilePath
}

// buildSummary renders the compaction summary message body. It always names the
// preserved-context contract so the model knows history was elided, then lists
// the touched file paths (the load-bearing facts gauntlet #12 requires to
// survive). The wording is honest about the real contract: the FIRST instruction
// and the MOST-RECENT user instructions survive verbatim (the user-turn back-snap),
// while earlier/superseded context is summarised here or dropped — not every user
// message is preserved (doc-08 §12; doc-07 §4 "what to preserve / what to drop").
func buildSummary(paths []string) string {
	var b strings.Builder
	b.WriteString(compactionSummaryMarker + " Earlier turns were summarised to fit the context window. ")
	b.WriteString("The original goal and the most-recent user instructions are preserved verbatim, ")
	b.WriteString("along with the files touched so far; earlier or superseded context, ")
	b.WriteString("large tool outputs, and stale file contents were summarised or dropped.")
	if len(paths) > 0 {
		b.WriteString("\n\nFiles touched so far:")
		for _, p := range paths {
			b.WriteString("\n- ")
			b.WriteString(p)
		}
	}
	return b.String()
}

// truncateToolBody returns m unchanged unless it carries an oversized tool result
// body, in which case it returns a copy with the body truncated to budget chars
// plus an elision marker. Non-tool messages pass through untouched.
//
// Truncation is keyed on the TEXT body length only; it must not collaterally
// destroy the result's typed Parts (image/resource_link/embedded/structured
// blocks, PR #226). The original Parts and the IsError flag are carried over
// verbatim onto the rebuilt result — only Content is trimmed.
func truncateToolBody(m session.Message, budget int) session.Message {
	if m.Role != session.RoleTool || m.ToolResult == nil {
		return m
	}
	body := m.ToolResult.Content
	if len(body) <= budget {
		return m
	}
	truncated := fmt.Sprintf("%s\n... [%d bytes elided by compaction]",
		body[:budget], len(body)-budget)
	tr := session.NewToolResultWithParts(m.ToolResult.CallID, truncated, m.ToolResult.Parts)
	tr.IsError = m.ToolResult.IsError
	return session.NewToolMessage(tr)
}
