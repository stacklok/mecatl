// Package eventsource provides a REFERENCE implementation of the event-sourced
// rehydration path for a host whose system of record is an append-only EVENT LOG
// (rather than mecatl's own snapshot store): Fold reconstructs a *session.Session
// by folding a port.EventLog stream back into the aggregate.
//
// WHY THIS EXISTS. mecatl persists a session via SNAPSHOTS (engine/adapter/sessnap,
// the JSON DTO the store adapters round-trip), and its OWN resume always reloads
// from that snapshot. But the durable EventLog (ADR 0027 Phase 3a) records the FULL
// chronological timeline — the same events a relay emits — and a host that already
// keeps an append-only event log as its system of record (a downstream consumer) would rather
// implement port.SessionStore.Load by folding its own event stream into a Session
// than maintain a parallel snapshot. ADR 0027 shipped the durable RECORDING of that
// stream (List-2 row 11) but left the RECONSTRUCTION direction as snapshot-only; this
// package is the documented, reference-implemented shape that closes that deferral
// (ADR 0038).
//
// WHAT FOLD RECONSTRUCTS — AND WHAT IT CANNOT. A pure event fold rebuilds the
// STRUCTURAL conversation faithfully: the assistant/tool message sequence, every
// assistant text + tool-call, every tool result, paired so the history is
// provider-replayable (ValidateToolPairing passes). It derives cumulative Usage,
// the lifecycle State/stop, the Counters, and a trailing pending ask.
//
// It is byte-identical-REPLAY-faithful, however, ONLY for providers that do not use
// the opaque assistant-message replay fields — because those fields NEVER cross the
// event stream:
//
//   - Message.Reasoning      (the provider reasoning REPLAY blob)
//   - Message.ProviderPhase  (the OpenAI Responses phase marker)
//   - ToolCall.ItemID        (the provider item id)
//
// reach the conversation ONLY via Session.RecordAssistant in the loop, never via an
// emitted Event. (EvReasoningDelta carries a human-readable reasoning SUMMARY, which
// the loop deliberately never places on Message.Reasoning — so a fold MUST NOT either.)
// A reconstructed Session is therefore a faithful structural conversation, and a
// byte-identical replay only for providers that leave those three fields empty (e.g.
// mockllm, a plain chat model). For OpenAI/Anthropic reasoning models the snapshot
// (which carries them) is the byte-identical path, which is why mecatl's own resume
// uses the snapshot; the fold is for event-log-SoR hosts that accept (or themselves
// carry, in a richer event schema) this contract boundary. This is a DOCUMENTED
// CONTRACT LIMITATION (engine/COMPATIBILITY.md, ADR 0038), not a bug.
//
// CREATION METADATA is supplied via SessionMeta: the id, mode, limits, workspace,
// profile, provider/model selector, and createdAt are creation facts that NO event
// carries, so the caller (who created the session and thus knows them) provides them
// alongside the stream. There is deliberately no EvSessionCreated event (ADR 0038
// records that as a possible future).
//
// USER MESSAGES: the loop emits a log-only EvUserPrompt at every site it records a
// user-role message — the genuine client prompt AND the harness-authored synthetic
// continuations (no-progress nudge, background-pending nudge, background-completion
// notice) — so the fold reconstructs user turns in stream order, and the
// pre-compaction span recovered from EvCompactionArchive carries its user messages
// verbatim. The reconstructed conversation is therefore COMPLETE except the
// provider-private replay fields above. (Project-instruction messages discovered at
// turn 0 — AGENTS.md/CLAUDE.md — are NOT event-carried; they are derivable from the
// workspace and are out of the conversation the fold rebuilds.)
//
// This is an EXCLUDED reference adapter (engine/COMPATIBILITY.md): it carries no
// public-API stability promise and is not part of the guarded core surface.
package eventsource

import (
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

// SessionMeta carries the creation facts that NO event in the stream records, so a
// fold can construct the aggregate. The caller — the event-log-SoR host, which
// created the session and therefore knows these — supplies them alongside the
// stream. They mirror the inert creation labels sessnap restores by direct
// assignment (Profile / ProviderID / ModelID are opaque to the domain).
type SessionMeta struct {
	// ID is the session id (session.New's first argument).
	ID session.SessionID
	// Mode is the permission posture.
	Mode session.PermissionMode
	// Limits are the configured stop conditions.
	Limits session.Limits
	// Workspace is the root directory tools operate against.
	Workspace string
	// Profile is the opaque tool-surface profile label ("" = default).
	Profile string
	// ProviderID and ModelID are the opaque neutral provider+model selector pair
	// ("" / "" = server default).
	ProviderID string
	ModelID    string
	// ReasoningEffort is the opaque neutral reasoning-effort token (ADR 0055), ""
	// when unset. Opaque to the domain; carried so the rehydrated session re-mints
	// the same-effort per-session engine via the factory.
	ReasoningEffort string
	// CreatedAt is the creation timestamp.
	CreatedAt time.Time
}

// Errors returned by Fold.
var (
	// ErrStream is returned when the event iterator yields an error; it wraps the
	// underlying per-item error.
	ErrStream = errors.New("eventsource: event stream error")
	// ErrReconstruct is returned when the folded events cannot be reconstructed into
	// a valid Session (an unpairable conversation, an inconsistent terminal state).
	ErrReconstruct = errors.New("eventsource: cannot reconstruct session")
)

// Fold reconstructs a *session.Session by folding the durable event stream back
// into the aggregate, supplemented by the creation metadata in meta. It is the
// reference implementation of an event-sourced port.SessionStore.Load.
//
// The returned Session has its Conversation, Counters, cumulative Usage, lifecycle
// State, recorded stop reason, and trailing pending ask reconstructed from the
// stream. See the package doc for the replay-fidelity limitation (Reasoning /
// ProviderPhase / ItemID are not event-carried).
//
// It returns ErrStream (wrapping the per-item error) if the iterator yields an
// error, and ErrReconstruct if the reconstructed history is not provider-replayable
// or the derived state is inconsistent. It never panics.
func Fold(meta SessionMeta, events iter.Seq2[session.Event, error]) (*session.Session, error) {
	f := &folder{}
	for ev, err := range events {
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrStream, err)
		}
		f.consume(ev)
	}
	// When awaiting, the trailing assistant turn (with the unanswered tool call) stays
	// UNFLUSHED so reconstructAwaiting can drive it through the running aggregate (its
	// dangling tool_use would fail SeedHistory's pairing guard). Otherwise flush it.
	if f.pending == nil {
		f.flushTurn()
	}

	s := session.New(meta.ID, meta.Mode, meta.Workspace, meta.Limits, meta.CreatedAt)
	// Inert creation labels — opaque to the domain, restored by direct assignment
	// exactly as sessnap.Restore does (these are authoritative exported values, not
	// state transitions).
	s.Profile = meta.Profile
	s.ProviderID = meta.ProviderID
	s.ModelID = meta.ModelID
	s.ReasoningEffort = meta.ReasoningEffort

	if f.pending != nil {
		// AWAITING: the live session at pause time holds the assistant message WITH its
		// not-yet-answered tool call (RecordAssistant runs before dispatch; the ask
		// pauses dispatch). That history legitimately ends on a dangling tool_use, which
		// SeedHistory's pairing guard would reject — so we reconstruct it the way the
		// loop did: seed the fully-paired prefix, then drive the trailing assistant turn
		// through the running aggregate (BeginTurn → RecordAssistant) and PauseForApproval.
		// RestoreState is NOT used here because its awaiting branch seeds no trailing turn.
		return f.reconstructAwaiting(s, meta)
	}

	if err := s.SeedHistory(f.messages); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrReconstruct, err)
	}
	// Drive the lifecycle (idle / terminal) and seed the cumulative usage + counters
	// through the SAME state-driving logic sessnap.Restore uses (sessnap.RestoreState),
	// so the terminal-transition vocabulary lives in exactly one place.
	if err := sessnap.RestoreState(s, f.restoreState(), f.stop, nil, f.finalCounters(), f.usage, f.permanent); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrReconstruct, err)
	}
	// Seed the session Title from the first genuine user prompt captured during the
	// fold (set-once + clamped via SetTitle). This reuses the domain predicate + the
	// same seam the loop uses (recordPrompt → SetTitle), so a Fold-reconstructed
	// session carries the same label a snapshot round-trip would. For a compacted
	// session the opener's EvUserPrompt was emitted before the compaction, so the
	// title survives compaction here (better than the snapshot lazy fallback).
	s.SetTitle(f.firstGenuineText)
	return s, nil
}

// reconstructAwaiting rebuilds a session parked on a permission ask. The fold left
// the trailing assistant turn (carrying the unanswered tool call) UNFLUSHED in f, so
// f.messages is the paired prefix; we seed that, then drive the trailing turn through
// the running aggregate and pause on the ask — mirroring how the loop reached the
// awaiting state.
func (f *folder) reconstructAwaiting(s *session.Session, _ SessionMeta) (*session.Session, error) {
	if err := s.SeedHistory(f.messages); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrReconstruct, err)
	}
	if err := s.BeginTurn(); err != nil {
		return nil, fmt.Errorf("%w: awaiting begin: %w", ErrReconstruct, err)
	}
	s.Counters = f.finalCounters()
	// RecordAssistant tolerates a dangling tool call (no pairing validation) — exactly
	// the live history at pause time.
	if err := s.RecordAssistant(session.NewAssistantMessage(f.curText, "", f.curCalls)); err != nil {
		return nil, fmt.Errorf("%w: awaiting assistant: %w", ErrReconstruct, err)
	}
	s.Usage = f.usage
	if err := s.PauseForApproval(*f.pending); err != nil {
		return nil, fmt.Errorf("%w: awaiting pause: %w", ErrReconstruct, err)
	}
	// Seed the session Title from the first genuine user prompt captured during the
	// fold (same set-once seam as the non-awaiting path above).
	s.SetTitle(f.firstGenuineText)
	return s, nil
}

// folder accumulates the per-turn reconstruction state as it walks the stream.
//
// The loop emits, per turn: EvTurnStart → EvMessageDelta* (assistant text) →
// EvTurnEnd → (EvToolCall + EvToolResult)*; then RecordAssistant + RecordToolResults
// land the assistant message and the tool-result messages. We mirror that: text and
// tool calls accumulate into a pending assistant turn that is flushed (appended) when
// a tool result arrives, when the next turn starts, or at end of stream; tool results
// append a tool-role message immediately after the assistant turn they answer. A user
// prompt is not individually evented (see the package doc), so the head of the
// conversation is whatever an EvCompactionArchive carries plus the per-turn
// assistant/tool reconstruction.
type folder struct {
	messages []session.Message

	// pending assistant turn being assembled.
	curText  string
	curCalls []session.ToolCall
	curOpen  bool // an assistant turn is being assembled

	// firstGenuineText captures the text of the FIRST genuine user prompt seen
	// in the stream (an EvUserPrompt whose message passes
	// session.IsGenuineUserPrompt), so Fold can seed the session Title from it
	// after reconstruction (via SetTitle, which clamps + is set-once). A
	// synthesised-summary EvUserPrompt or a synthetic continuation does not
	// capture. For a compacted session whose opener was dropped into the
	// summarised head, the opener's EvUserPrompt was emitted BEFORE the
	// compaction in the stream, so Fold captured it — the title survives
	// compaction (better than the snapshot lazy fallback, which only sees the
	// post-compaction history).
	firstGenuineText string

	// derived lifecycle.
	usage     session.Usage // cumulative = SUM of every EvResult.Usage
	stop      session.StopReason
	pending   *session.PendingAsk
	ended     bool // a terminal EvResult was seen
	permanent bool // last EvResult.Permanent (meaningful only when stop==StopError)

	// counters of the CURRENT run segment (reset on each terminal, so the final
	// values reflect the latest run — mirroring resetToIdle on Reopen).
	curTurns      int
	curToolCalls  int
	curConsecFail int
	// finalCnt snapshots the counters at the most recent terminal EvResult; the live
	// segment (curTurns/…) is used when no terminal has been seen (idle / awaiting).
	finalCnt    session.Counters
	finalCntSet bool
}

// consume folds one event into the accumulator.
func (f *folder) consume(ev session.Event) {
	switch ev.Type {
	case session.EvCompactionArchive:
		// The pre-compaction head the snapshot would otherwise have lost. It REPLACES
		// the history reconstructed so far (it IS the conversation up to the compaction
		// point); the in-progress assistant turn (if any) continues accumulating ON TOP
		// of it, so we do NOT close curOpen. Dropping this case would silently lose every
		// turn before the last compaction.
		if ev.CompactionArchive != nil {
			f.messages = session.CloneMessages(ev.CompactionArchive.Replaced)
		}
	case session.EvUserPrompt:
		// A user-role message was recorded (the genuine prompt OR a harness-authored
		// continuation/notice). It is its OWN message, appended in stream order: flush any
		// in-progress assistant turn first (the user message follows it), then append the
		// user message verbatim (Text + Parts). This is what closes the "log can't show
		// what the user asked" gap — without this case the fold would reconstruct only
		// assistant/tool turns. EvUserPrompt does NOT begin a model turn (the following
		// EvTurnStart does), so it touches no counter.
		f.flushTurn()
		if ev.UserPrompt != nil {
			msg := session.NewUserMessageWithParts(ev.UserPrompt.Text, ev.UserPrompt.Parts)
			f.messages = append(f.messages, msg)
			// Capture the first GENUINE user prompt for the session Title (Fold seeds
			// it via SetTitle after reconstruction). A synthesised compaction summary
			// does not capture (IsSynthesisedSummary skips it). Text-only; a
			// multimodal-only prompt (Text=="") leaves firstGenuineText=="" — the lazy
			// fallback applies.
			if f.firstGenuineText == "" && session.IsGenuineUserPrompt(msg) && strings.TrimSpace(msg.Text) != "" {
				f.firstGenuineText = msg.Text
			}
		}
	case session.EvTurnStart:
		// A new turn begins: flush the previous assistant turn (if any) and start a
		// fresh one. Each turn-start is a model call (mirrors BeginTurn's Turns++).
		f.flushTurn()
		f.curOpen = true
		f.curTurns++
	case session.EvMessageDelta:
		f.curOpen = true
		f.curText += ev.Text
	case session.EvToolCall:
		if ev.ToolCall != nil {
			f.curOpen = true
			f.curCalls = append(f.curCalls, *ev.ToolCall)
		}
	case session.EvToolResult:
		// A tool result answers a call on the just-assembled assistant turn. Flush the
		// assistant message first (so the assistant message precedes its tool results),
		// then append the tool-role message. Mirrors RecordToolResults' counter updates.
		f.flushTurn()
		if ev.ToolResult != nil {
			f.messages = append(f.messages, session.NewToolMessage(*ev.ToolResult))
			f.curToolCalls++
			if ev.ToolResult.IsError {
				f.curConsecFail++
			} else {
				f.curConsecFail = 0
			}
		}
	case session.EvPermissionAsk:
		// Record the latest ask; a following EvApproval/retract or a terminal EvResult
		// clears it. A trailing unanswered ask => the session is awaiting on it.
		if ev.Ask != nil {
			a := *ev.Ask
			f.pending = &a
		}
	case session.EvApproval, session.EvPermissionRetract:
		// The ask was resolved (verdict) or withdrawn — no longer pending.
		f.pending = nil
	case session.EvResult:
		f.applyResult(ev)
	default:
		// Advisory / observability events (turn.end, reasoning.delta, compaction notice,
		// no_progress, hook, the delegation families) carry no conversation or lifecycle
		// state a fold needs — ignore them.
	}
}

// applyResult folds a terminal EvResult. EvResult.Usage is the PER-RUN figure; the
// cumulative session usage is the SUM across every run's EvResult (a multi-run log
// carries several), so using a single EvResult.Usage (not the sum) would undercount
// a reopened session's spend. Permanence is recorded ONLY when this run ended in a
// StopError (transient failures and clean terminals carry Permanent==false); the
// fold's last terminal result wins, mirroring how a snapshot captures the final
// state. The terminal counters are snapshotted, then the live segment is reset: a
// subsequent EvTurnStart (a Reopen) begins a fresh run whose Counters are per-run
// (resetToIdle zeroes them on Reopen); the FINAL counters reflect the latest run.
func (f *folder) applyResult(ev session.Event) {
	f.flushTurn()
	if ev.Result != nil {
		f.usage = f.usage.Add(ev.Result.Usage)
		f.stop = ev.Result.Stop
		f.permanent = ev.Result.Stop == session.StopError && ev.Result.Permanent
	}
	f.ended = true
	f.pending = nil
	f.finalCnt = f.curCounters()
	f.finalCntSet = true
	f.curTurns = 0
	f.curToolCalls = 0
	f.curConsecFail = 0
}

// flushTurn appends the in-progress assistant message (if any) to the history and
// clears the pending turn. An assistant turn with neither text nor tool calls still
// produces an (empty) assistant message — the loop records one too (the no-progress
// path records an empty assistant turn before nudging), so the reconstruction
// matches.
func (f *folder) flushTurn() {
	if !f.curOpen {
		return
	}
	// Reasoning is deliberately empty: the reasoning REPLAY blob is not event-carried
	// (see the package doc), and the EvReasoningDelta summary must never be placed on
	// Message.Reasoning (loop.go forbids it).
	f.messages = append(f.messages, session.NewAssistantMessage(f.curText, "", f.curCalls))
	f.curOpen = false
	f.curText = ""
	f.curCalls = nil
}

// restoreState derives the lifecycle State a non-awaiting reconstructed session lands
// in (the awaiting case is handled by reconstructAwaiting before this is reached).
func (f *folder) restoreState() session.State {
	if !f.ended {
		// No terminal result and no pending ask: the stream stopped mid-run (e.g. a
		// crash between turns). The faithful, resumable landing is IDLE — the
		// conversation is intact and the next prompt can resume it.
		return session.StateIdle
	}
	return stateForStop(f.stop)
}

// stateForStop maps a recorded terminal stop reason to its terminal State, mirroring
// the loop's terminate paths: StopCancelled→cancelled, StopError→failed, everything
// else (the clean terminals: end_turn/budget/no_progress/limits/structured_output)→
// completed.
func stateForStop(stop session.StopReason) session.State {
	switch stop {
	case session.StopCancelled:
		return session.StateCancelled
	case session.StopError:
		return session.StateFailed
	default:
		return session.StateCompleted
	}
}

// curCounters returns the current (live) run-segment counters.
func (f *folder) curCounters() session.Counters {
	return session.Counters{
		Turns:               f.curTurns,
		ToolCalls:           f.curToolCalls,
		ConsecutiveFailures: f.curConsecFail,
	}
}

// finalCounters returns the counters of the latest run: the snapshot taken at the
// most recent terminal EvResult, or the live segment when no terminal was seen (an
// idle/awaiting reconstruction).
func (f *folder) finalCounters() session.Counters {
	if f.finalCntSet {
		return f.finalCnt
	}
	return f.curCounters()
}
