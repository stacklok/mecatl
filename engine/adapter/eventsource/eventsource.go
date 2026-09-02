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
//   - Message.ReasoningItemID (the provider reasoning-item id)
//   - ToolCall.ItemID        (the provider item id)
//
// reach the conversation ONLY via Session.RecordAssistant in the loop, never via an
// emitted Event. (EvReasoningDelta carries a human-readable reasoning SUMMARY, which
// the loop deliberately never places on Message.Reasoning — so a fold MUST NOT either.)
// A reconstructed Session is therefore a faithful structural conversation, and a
// byte-identical replay only for providers that leave those four fields empty (e.g.
// mockllm, a plain chat model). For OpenAI/Anthropic reasoning models the snapshot
// (which carries them) is the byte-identical path, which is why mecatl's own resume
// uses the snapshot; the fold is for event-log-SoR hosts that accept (or themselves
// carry, in a richer event schema) this contract boundary. This is a DOCUMENTED
// CONTRACT LIMITATION (engine/COMPATIBILITY.md, ADR 0038), not a bug.
//
// CREATION METADATA is supplied via SessionMeta: id, mode, limits, exact
// EnvironmentRef, display-only placement metadata, profile, provider/model selector,
// reasoning effort, authoritative title/provenance, kind/relationship, and createdAt
// are facts that NO event carries, so the caller
// (who created or discovered the session and thus knows them) provides them alongside
// the stream. A legacy empty title falls back to the first genuine EvUserPrompt.
// There is deliberately no EvSessionCreated event (ADR 0038 records that as a
// possible future).
//
// USER MESSAGES: the loop emits a log-only EvUserPrompt at every site it records a
// user-role message — the genuine client prompt AND the harness-authored synthetic
// continuations (no-progress nudge, background-pending nudge, background-completion
// notice) — so the fold reconstructs user turns in stream order, and the
// pre-compaction span recovered from EvCompactionArchive carries its user messages
// verbatim. The reconstructed conversation is therefore COMPLETE except the
// provider-private replay fields above. (Project-instruction messages discovered at
// turn 0 — AGENTS.md/CLAUDE.md — are NOT event-carried; they are reassembled from
// the exactly reattached environment and are out of the conversation the fold rebuilds.)
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
	// EnvironmentRef is the exact durable execution-environment identity.
	EnvironmentRef session.EnvironmentRef
	// Placement is safe display-only metadata; it is never used for reattachment.
	Placement session.PlacementMetadata
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
	// DebugMCPServers and DebugMCPTools are the durable selected global MCP names
	// and exact direct-tool ceiling for a debug session.
	DebugMCPServers        []string
	DebugMCPTools          []string
	DebugTargetFingerprint string
	// Title and TitleProvenance are authoritative creation/discovery metadata when
	// supplied. A legacy empty title is derived from the first genuine user event.
	Title           string
	TitleProvenance session.TitleProvenance
	// Kind and Relationship are the trusted producer taxonomy supplied alongside
	// the event stream. An empty kind is legacy and folds to unknown.
	Kind         session.SessionKind
	Relationship session.SessionRelationship
	// Incarnation and Owner are immutable creation identity. Empty Incarnation is
	// legacy metadata and folds to the deterministic prefix-disjoint legacy token.
	Incarnation session.IncarnationID
	Owner       *session.Principal
	// Authority is the plain derived-capability payload supplied with creation
	// metadata. Nil is a documented pre-feature legacy record; a present payload
	// is validated and bound before reconstruction proceeds.
	Authority *session.Authority
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
// ProviderPhase / ReasoningItemID / ItemID are not event-carried).
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
	f.finalizeOpenTurn()

	if !meta.EnvironmentRef.Valid() {
		return nil, fmt.Errorf("%w: missing or invalid environment ref", ErrReconstruct)
	}
	s := session.New(meta.ID, meta.Mode, meta.EnvironmentRef, meta.Limits, meta.CreatedAt)
	if err := s.RestoreSessionMetadata(meta.Kind, meta.Relationship); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrReconstruct, err)
	}
	if err := restoreAuthority(s, meta.Authority); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrReconstruct, err)
	}
	if err := s.RestoreLabels(meta.Owner, session.Authority{}); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrReconstruct, err)
	}
	if err := s.RestoreIncarnation(meta.Incarnation); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrReconstruct, err)
	}
	// Inert creation labels — opaque to the domain, restored by direct assignment
	// exactly as sessnap.Restore does (these are authoritative exported values, not
	// state transitions).
	s.Placement = meta.Placement
	s.Profile = meta.Profile
	s.ProviderID = meta.ProviderID
	s.ModelID = meta.ModelID
	s.ReasoningEffort = meta.ReasoningEffort
	s.DebugMCPServers = append([]string(nil), meta.DebugMCPServers...)
	s.DebugMCPTools = append([]string(nil), meta.DebugMCPTools...)
	s.DebugTargetFingerprint = meta.DebugTargetFingerprint
	s.Title = meta.Title
	s.TitleProvenance = meta.TitleProvenance

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
	if err := sessnap.RestoreState(s, f.restoreState(), f.stop, nil, f.finalCounters(), f.usage, f.permanent, f.lastError); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrReconstruct, err)
	}
	if s.State == session.StateFailed {
		if err := s.RecordFailureMetadata(f.disposition, f.progress); err != nil {
			return nil, fmt.Errorf("%w: restore failure metadata: %w", ErrReconstruct, err)
		}
	}
	if err := f.restoreRetryPending(s); err != nil {
		return nil, err
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

func (f *folder) finalizeOpenTurn() {
	// Awaiting keeps a dangling tool-call turn unflushed. A crashed retry after
	// turn.start discards partial deltas because no terminal made them history.
	crashedRetry := f.retryPending && f.retryTurnStarted && !f.ended
	if f.pending == nil && !crashedRetry {
		f.flushTurn()
		return
	}
	if crashedRetry {
		f.curOpen = false
		f.curText = ""
		f.curCalls = nil
	}
}

func (f *folder) restoreRetryPending(s *session.Session) error {
	if !f.retryPending {
		return nil
	}
	if err := s.RestoreFailedStepRetryPending(f.retryDisposition, f.retryProgress); err != nil {
		return fmt.Errorf("%w: restore failed-step retry intent: %w", ErrReconstruct, err)
	}
	return nil
}

func restoreAuthority(s *session.Session, authority *session.Authority) error {
	if authority == nil {
		return nil
	}
	if err := sessnap.ValidatePersistedAuthority(authority); err != nil {
		return fmt.Errorf("restore authority: %w", err)
	}
	if err := s.BindAuthority(*authority); err != nil {
		return fmt.Errorf("restore authority: %w", err)
	}
	return nil
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
	usage       session.Usage // cumulative = SUM of every EvResult.Usage
	stop        session.StopReason
	pending     *session.PendingAsk
	ended       bool // a terminal EvResult was seen
	permanent   bool // compatibility projection of disposition==permanent
	disposition session.RetryDisposition
	progress    session.StreamProgress
	lastError   string // last EvResult.Error (meaningful only when stop==StopError) — issue #332

	// failed-step retry segment state. model.retry starts a prompt-free segment and
	// carries the prior failure facts; turn.start proves an authoritative model attempt.
	retryPending     bool
	retryTurnStarted bool
	retryDisposition session.RetryDisposition
	retryProgress    session.StreamProgress

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
	case session.EvCompactionArchive, session.EvUserPrompt:
		f.consumeHistoryEvent(ev)
	case session.EvModelRetry:
		// A retry starts a new prompt-free run segment. The prior failed terminal is
		// consumed into durable pending intent; advisory Text is never parsed.
		f.flushTurn()
		f.ended = false
		f.stop = session.StopNone
		f.pending = nil
		f.curTurns, f.curToolCalls, f.curConsecFail = 0, 0, 0
		// PrepareFailedStepRetry resets per-run Counters. The previous terminal's
		// snapshot must not win over this new prompt-free segment when a crash lands
		// before turn.start (all zero) or while its first turn is running.
		f.finalCnt = session.Counters{}
		f.finalCntSet = false
		f.retryPending = ev.ModelRetry != nil
		f.retryTurnStarted = false
		if ev.ModelRetry != nil {
			f.retryDisposition = ev.ModelRetry.Disposition
			f.retryProgress = ev.ModelRetry.Progress
		}
	case session.EvTurnStart:
		// A new turn begins: flush the previous assistant turn (if any) and start a
		// fresh one. Each turn-start is a model call (mirrors BeginTurn's Turns++).
		f.flushTurn()
		f.curOpen = true
		f.curTurns++
		if f.retryPending {
			f.retryTurnStarted = true
		}
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

func (f *folder) consumeHistoryEvent(ev session.Event) {
	switch ev.Type {
	case session.EvCompactionArchive:
		if ev.CompactionArchive != nil {
			f.messages = session.CloneMessages(ev.CompactionArchive.Replaced)
		}
	case session.EvUserPrompt:
		f.flushTurn()
		if ev.UserPrompt == nil {
			return
		}
		msg := session.NewUserMessageWithParts(ev.UserPrompt.Text, ev.UserPrompt.Parts)
		f.messages = append(f.messages, msg)
		if f.firstGenuineText == "" && session.IsGenuineUserPrompt(msg) && strings.TrimSpace(msg.Text) != "" {
			f.firstGenuineText = msg.Text
		}
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
	deferredRetry := f.retryPending && !f.retryTurnStarted && ev.Result != nil && ev.Result.Stop != session.StopCancelled
	failedStream := ev.Result != nil && ev.Result.Stop == session.StopError && ev.Result.Progress != session.StreamProgressComplete
	if failedStream {
		// Deltas from a failed model stream remain in the event log for audit, but the
		// incomplete assistant must never enter replay history.
		f.curOpen = false
		f.curText = ""
		f.curCalls = nil
	} else {
		// A ChunkDone carrying StopError is still a clean terminal when progress is
		// complete. The live loop records the assistant before terminateComplete, so
		// event reconstruction must retain it too.
		f.flushTurn()
	}
	if ev.Result != nil {
		f.usage = f.usage.Add(ev.Result.Usage)
		f.stop = ev.Result.Stop
		f.disposition = session.RetryDispositionUnknown
		f.progress = ev.Result.Progress
		f.permanent = false
		f.lastError = ""
		if failedStream {
			f.disposition = ev.Result.Disposition
			if f.disposition == session.RetryDispositionUnknown && ev.Result.Permanent {
				f.disposition = session.RetryDispositionPermanent
			}
			f.progress = ev.Result.Progress
			f.permanent = f.disposition == session.RetryDispositionPermanent
			f.lastError = ev.Result.Error
		}
	}
	f.ended = !deferredRetry
	if !deferredRetry {
		f.retryPending = false
		f.retryTurnStarted = false
	}
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
		if f.retryPending && f.retryTurnStarted {
			return session.StateRunning
		}
		// No terminal result and no pending ask: an ordinary incomplete stream lands
		// idle; a retry that has not reached turn.start stays idle-but-pending.
		return session.StateIdle
	}
	return stateForStop(f.stop, f.progress)
}

// stateForStop maps a recorded terminal result to its terminal State. StopError
// with complete progress came from a clean ChunkDone and mirrors terminateComplete;
// other StopError results came from iterator/provider errors and mirror terminate.
func stateForStop(stop session.StopReason, progress session.StreamProgress) session.State {
	switch stop {
	case session.StopCancelled:
		return session.StateCancelled
	case session.StopError:
		if progress == session.StreamProgressComplete {
			return session.StateCompleted
		}
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
