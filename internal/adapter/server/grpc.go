package server

//revive:disable:exported // RPC methods mirror the generated HarnessService interface

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// HarnessServer implements the generated mecatlv1.HarnessServiceServer over the
// shared Service. It is the primary (gRPC) surface; the HTTP/SSE adapter wraps
// the same Service.
type HarnessServer struct {
	mecatlv1.UnimplementedHarnessServiceServer
	svc *Service
}

// NewHarnessServer constructs a HarnessServer over svc.
func NewHarnessServer(svc *Service) *HarnessServer {
	return &HarnessServer{svc: svc}
}

// compile-time assertion that HarnessServer satisfies the generated interface.
var _ mecatlv1.HarnessServiceServer = (*HarnessServer)(nil)

// CreateSession allocates a new session and returns its id.
func (h *HarnessServer) CreateSession(ctx context.Context, req *mecatlv1.CreateSessionRequest) (*mecatlv1.CreateSessionResponse, error) {
	// Session profile (issue #55): "" = default (full filesystem), "no-fs" = the
	// no-filesystem profile; anything else is a loud InvalidArgument. The
	// workspace requirement is PROFILE-AWARE and enforced in the service
	// (createSession): default requires a workspace, no-fs requires an EMPTY one
	// — so there is deliberately NO unconditional empty-workspace guard here.
	profile, err := ParseSessionProfile(req.GetProfile())
	if err != nil {
		return nil, toStatus(err)
	}
	// Per-session provider/model selector (multi-provider Phase 0, S3): the two
	// fields map to the neutral ProviderSelector; the zero selector keeps the
	// shared-engine fast path. An unknown/unavailable provider, or model_id without
	// provider_id, surfaces as InvalidArgument via toStatus.
	sel := ProviderSelector{ProviderID: req.GetProviderId(), ModelID: req.GetModelId(), ReasoningEffort: req.GetReasoningEffort()}
	var opts []CreateSessionOption
	if src := req.GetSourceSessionId(); src != "" {
		opts = append(opts, WithSourceSession(session.SessionID(src)))
	}
	if target := req.GetDebugTargetSessionId(); target != "" {
		opts = append(opts, WithDebugTarget(session.SessionID(target)))
	}
	sess, err := h.svc.CreateSessionWithProfile(ctx, req.GetWorkspace(), modeFromProto(req.GetMode()), limitsFromProto(req.GetLimits()), sel, profile, opts...)
	if err != nil {
		return nil, toStatus(err)
	}
	// session_capabilities echoes the per-session resolved input capability (catalog
	// ∩ adapter for THIS session's provider+model), which may differ from the
	// server-wide capabilities when a non-default selector was supplied. Both read
	// the composition's single source, so they cannot disagree.
	scaps := h.svc.SessionCapabilities(sess.ID)
	// resolved_model echoes the EFFECTIVE provider+model THIS session resolved to,
	// read from the composition single source (Service.ResolvedModel) — NEVER from
	// req.GetModelId(), which is empty for a default session and ambiguous for a
	// passthrough id. Same single-source discipline as session_capabilities.
	return &mecatlv1.CreateSessionResponse{
		SessionId:    string(sess.ID),
		Capabilities: h.svc.capabilities(),
		SessionCapabilities: &mecatlv1.SessionCapabilities{
			Image: scaps.Image,
			Audio: scaps.Audio,
		},
		ResolvedModel: resolvedModelToProto(h.svc.ResolvedModel(sess.ID)),
	}, nil
}

// GetServerInfo returns safe build, composition, and the caller-selected provider endpoint projection.
func (h *HarnessServer) GetServerInfo(_ context.Context, req *mecatlv1.GetServerInfoRequest) (*mecatlv1.GetServerInfoResponse, error) {
	return h.svc.serverInfoResponse(req.GetProviderId()), nil
}

// GetSession returns a snapshot of the requested session.
func (h *HarnessServer) GetSession(ctx context.Context, req *mecatlv1.GetSessionRequest) (*mecatlv1.GetSessionResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	sess, err := h.svc.GetSession(ctx, session.SessionID(req.GetSessionId()))
	if err != nil {
		return nil, toStatus(err)
	}
	proto := toProtoSession(sess, h.svc.ResolvedModel(sess.ID), h.svc.capabilities())
	// Lazy display-time fallback: a session whose snapshot Title was never seeded
	// (or is empty) gets a derived label so GetSession shows one without a
	// write-on-read — sess.Title is NOT mutated.
	if sess.Title == "" {
		proto.Title = DeriveTitle(sess)
	}
	return &mecatlv1.GetSessionResponse{Session: proto}, nil
}

// GetSessionTranscript returns the owned session's snapshot-derived transcript.
func (h *HarnessServer) GetSessionTranscript(ctx context.Context, req *mecatlv1.GetSessionTranscriptRequest) (*mecatlv1.GetSessionTranscriptResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	transcript, err := h.svc.GetTranscript(ctx, session.SessionID(req.GetSessionId()))
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoTranscript(transcript), nil
}

// SetMode changes the permission posture of the requested session.
func (h *HarnessServer) SetMode(ctx context.Context, req *mecatlv1.SetModeRequest) (*mecatlv1.SetModeResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	sess, err := h.svc.SetMode(ctx, session.SessionID(req.GetSessionId()), modeFromProto(req.GetMode()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.SetModeResponse{Session: toProtoSession(sess, h.svc.ResolvedModel(sess.ID), h.svc.capabilities())}, nil
}

// CloseSession ends a session and releases its server-side resources. It returns
// NotFound only for a never-created id; an already-released session succeeds
// (idempotent). It calls Service.EndSession, NOT the void Service.CloseSession, so
// an unknown id surfaces as NotFound rather than a silent success.
func (h *HarnessServer) CloseSession(ctx context.Context, req *mecatlv1.CloseSessionRequest) (*mecatlv1.CloseSessionResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if err := h.svc.EndSession(ctx, session.SessionID(req.GetSessionId())); err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.CloseSessionResponse{}, nil
}

// RenameSession explicitly replaces an idle main session's persisted title.
func (h *HarnessServer) RenameSession(ctx context.Context, req *mecatlv1.RenameSessionRequest) (*mecatlv1.RenameSessionResponse, error) {
	if req.GetSessionId() == "" || strings.TrimSpace(req.GetTitle()) == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id and non-blank title are required")
	}
	sess, err := h.svc.RenameSession(ctx, session.SessionID(req.GetSessionId()), req.GetTitle())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.RenameSessionResponse{Session: toProtoSession(sess, h.svc.ResolvedModel(sess.ID), h.svc.capabilities())}, nil
}

// DeleteSession physically removes an idle main session and store-managed sidecars.
func (h *HarnessServer) DeleteSession(ctx context.Context, req *mecatlv1.DeleteSessionRequest) (*mecatlv1.DeleteSessionResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if err := h.svc.DeleteSession(ctx, session.SessionID(req.GetSessionId())); err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.DeleteSessionResponse{}, nil
}

// CompactSession applies one out-of-band compaction pass to an owned session.
func (h *HarnessServer) CompactSession(ctx context.Context, req *mecatlv1.CompactSessionRequest) (*mecatlv1.CompactSessionResponse, error) {
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	result, err := h.svc.CompactSession(ctx, session.SessionID(req.GetSessionId()), session.PrincipalFromContext(ctx))
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.CompactSessionResponse{Compacted: result.Changed}, nil
}

// ForkSession creates a peer session from an existing session's history snapshot
// (ADR 0065). The new session inherits the source's mode, workspace, limits, and
// provider/model/profile labels; same provider and model only, with the ONE
// optional selector delta being a reasoning-effort override (ADR 0068, empty
// inherits). The source must be at a turn boundary (idle/terminal); a
// running/awaiting source is rejected with FailedPrecondition. No streaming — the
// fork is synchronous.
func (h *HarnessServer) ForkSession(ctx context.Context, req *mecatlv1.ForkSessionRequest) (*mecatlv1.ForkSessionResponse, error) {
	if req.GetSourceSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "source_session_id is required")
	}
	id, err := h.svc.ForkSession(ctx, session.SessionID(req.GetSourceSessionId()), req.GetTitle(), req.GetReasoningEffort())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ForkSessionResponse{SessionId: string(id)}, nil
}

func adoptionBindingsFromProto(binding *mecatlv1.AdoptionBindings) (AdoptionBindings, error) {
	if binding == nil {
		return AdoptionBindings{}, fmt.Errorf("%w: bindings are required", ErrInvalidArgument)
	}
	profile, err := ParseSessionProfile(binding.GetProfile())
	if err != nil {
		return AdoptionBindings{}, err
	}
	return AdoptionBindings{
		Workspace:      binding.GetWorkspace(),
		EnvironmentRef: session.EnvironmentRef{Kind: session.EnvironmentKind(binding.GetEnvironmentKind()), ID: binding.GetEnvironmentId()},
		ProviderID:     binding.GetProviderId(), ModelID: binding.GetModelId(), Profile: profile,
	}, nil
}

func adoptionBindingsToProto(binding AdoptionBindings) *mecatlv1.AdoptionBindings {
	return &mecatlv1.AdoptionBindings{Workspace: binding.Workspace, EnvironmentKind: string(binding.EnvironmentRef.Kind), EnvironmentId: binding.EnvironmentRef.ID, ProviderId: binding.ProviderID, ModelId: binding.ModelID, Profile: string(binding.Profile)}
}

func (h *HarnessServer) PreflightSessionAdoption(ctx context.Context, req *mecatlv1.PreflightSessionAdoptionRequest) (*mecatlv1.PreflightSessionAdoptionResponse, error) {
	if req.GetSourceSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "source_session_id is required")
	}
	bindings, err := adoptionBindingsFromProto(req.GetBindings())
	if err != nil {
		return nil, toStatus(err)
	}
	result, err := h.svc.PreflightSessionAdoption(ctx, session.SessionID(req.GetSourceSessionId()), bindings)
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.PreflightSessionAdoptionResponse{Eligible: result.Eligible, ReasonCode: string(result.Reason), Bindings: adoptionBindingsToProto(result.Bindings)}, nil
}

func (h *HarnessServer) AdoptSession(ctx context.Context, req *mecatlv1.AdoptSessionRequest) (*mecatlv1.AdoptSessionResponse, error) {
	if req.GetSourceSessionId() == "" || req.GetIdempotencyKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "source_session_id and idempotency_key are required")
	}
	bindings, err := adoptionBindingsFromProto(req.GetBindings())
	if err != nil {
		return nil, toStatus(err)
	}
	sess, err := h.svc.AdoptSession(ctx, session.SessionID(req.GetSourceSessionId()), req.GetIdempotencyKey(), bindings)
	if err != nil {
		return nil, toStatus(err)
	}
	caps := h.svc.SessionCapabilities(sess.ID)
	return &mecatlv1.AdoptSessionResponse{SessionId: string(sess.ID), SourceSessionId: string(adoptionSourceID(sess)), Capabilities: h.svc.capabilities(), SessionCapabilities: &mecatlv1.SessionCapabilities{Image: caps.Image, Audio: caps.Audio}, ResolvedModel: resolvedModelToProto(h.svc.ResolvedModel(sess.ID))}, nil
}

// Converse drives one run over a bidi stream. The first frame MUST be a Prompt
// or RetryStart;
// the server then relays the run's Events while concurrently reading
// ResumeApproval / Cancel control frames, until the events channel closes (the
// terminal result was delivered) or the stream context is cancelled.
func (h *HarnessServer) Converse(stream mecatlv1.HarnessService_ConverseServer) error {
	ctx := stream.Context()

	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return status.Error(codes.InvalidArgument, "converse: stream closed before a start frame")
		}
		return err
	}
	var (
		id       session.SessionID
		run      *agent.Run
		retrying bool
	)
	switch {
	case first.GetPrompt() != nil:
		prompt := first.GetPrompt()
		if prompt.GetSessionId() == "" {
			return status.Error(codes.InvalidArgument, "converse: prompt session_id is required")
		}
		if prompt.GetText() == "" && len(prompt.GetParts()) == 0 {
			return status.Error(codes.InvalidArgument, "converse: prompt text or parts is required")
		}
		parts, perr := contentFromProto(prompt.GetParts())
		if perr != nil {
			return status.Error(codes.InvalidArgument, perr.Error())
		}
		id = session.SessionID(prompt.GetSessionId())
		run, err = h.svc.StartRunContent(ctx, id, prompt.GetText(), parts)
	case first.GetRetry() != nil:
		retry := first.GetRetry()
		if retry.GetSessionId() == "" {
			return status.Error(codes.InvalidArgument, "converse: retry session_id is required")
		}
		id = session.SessionID(retry.GetSessionId())
		retrying = true
		run, err = h.svc.RetryFailedRun(ctx, id)
	default:
		return status.Error(codes.InvalidArgument, "converse: first frame must be a prompt or retry")
	}
	if err != nil {
		return toStatus(err)
	}
	defer h.svc.deregister(id, run)

	// The single-writer gate for EVERY Send on this bidi stream: a gRPC stream
	// is NOT goroutine-safe — Send called concurrently from the RecoverNotice
	// pre-send below and the run relay would race without it. streamSender
	// serializes every Send behind one mutex, so no two goroutines are ever
	// inside a Send on this stream at once. (The promoted-steer follow-up relay
	// is driven on the relay goroutine itself through this same sender — the
	// sequential handoff below; the gate is the structural guarantee, not a
	// per-goroutine coincidence.)
	snd := &streamSender{stream: stream}

	// The steer-ack lane: readControl enqueues the AUTHORITATIVE outcome of
	// each steer / steer_cancel frame here and the relay interleaves the acks
	// onto the event stream in send order (a client learns what actually
	// happened to its steer — the engine is authoritative on the slot / drain
	// race, never the client's guess). Buffered so a burst of steer frames
	// never blocks the control reader behind a slow client.
	steerAcks := make(chan *mecatlv1.SteerAck, 16)

	// The handoff mailbox: a too_late steer the Service PROMOTED to a fresh
	// follow-up run is POSTED here by readControl, and after the original run's
	// relay drains, Converse TAKES it and relays the promoted run SEQUENTIALLY
	// on this same stream (reusing snd + the same relay discipline) — never an
	// orphaned second relay goroutine left running when the RPC returns, and
	// runRelay.sendErr keeps a single owner.
	ho := newSteerHandoff()

	// The control frame target: ResumeApproval / Cancel / CancelChild apply to
	// the ACTIVE run — the original until a promoted steer hands off, then the
	// promoted run (the swap is atomic with the relay handoff, so a control
	// frame never hits the terminal original behind the client's back).
	ct := &controlTarget{run: run}

	rl := &runRelay{ctx: ctx, logCtx: context.WithoutCancel(ctx), id: id, acks: steerAcks, snd: snd}
	rl.recorder = NewRunEventRecorder(rl.logCtx, h.svc, id)

	// Read subsequent control frames concurrently so an approval/cancel/steer
	// can be delivered while events are still streaming. The reader exits on
	// stream EOF (client closed its send half) or context cancellation; a
	// parked bidi Recv does NOT observe ctx cancellation promptly (it is
	// buffered), so Converse — not the reader's exit — owns the handoff
	// mailbox's seal (see the relay loop below), and stopControl exists to
	// release the reader's non-Recv paths on the RPC's way out.
	controlCtx, stopControl := context.WithCancel(ctx)
	defer stopControl() // every early return releases the reader too
	go h.readControl(controlCtx, id, ct, rl, ho)

	// Inject a pre-flight EvRecoverNotice when the session just recovered from a
	// PERMANENT failure — surface the advisory BEFORE the main event loop burns a
	// provider call on the same unrecoverable error. Emitted ONCE per recovery
	// (RecoverNotice consumes the entry on the first call).
	if notice := h.svc.RecoverNotice(id); notice != "" {
		ev := session.Event{Type: session.EvRecoverNotice, Text: notice}
		// Durable log first (cancel-detached, survives client disconnect).
		rl.recorder.Observe(ev)
		// Forward to the client wire through the same serialized sender.
		if err := snd.Send(&mecatlv1.ConverseResponse{Event: toProto(ev)}); err != nil {
			// The relay goroutine owns Close on normal paths, but it has not been
			// installed yet. Flush the recorder on this sole pre-flight exit.
			rl.recorder.Close()
			return err
		}
	}

	// The sequential active-run handoff: relay the original run, then — while a
	// promoted steer is pending — relay EACH promoted run in turn on this same
	// stream BEFORE the RPC returns. The original's deregister fires before the
	// first take, the promoted run's FinishRun before the next take / the
	// return, so Converse never returns while a run it owns is still registered
	// (no orphaned relay). The relay runs on its own goroutine so the read below
	// can ALSO block on the next handoff post (the
	// promote-grace window — the promote legitimately completes only after the
	// original's relay drained, so readControl cannot have posted before the
	// take without this). relayRes accumulates the per-run results (a
	// mutex-guarded slice — a cap-N results channel could deadlock the producer
	// when a post arrives while an earlier result sits unread).
	var relayRes struct {
		mu   sync.Mutex
		errs []error
	}
	pushErr := func(err error) {
		relayRes.mu.Lock()
		relayRes.errs = append(relayRes.errs, err)
		relayRes.mu.Unlock()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer rl.recorder.Close()
		pushErr(h.relayRun(rl, run))
		if retrying {
			h.svc.Persist(context.WithoutCancel(ctx), id)
		}
		// The terminal original must leave the registry before a steer already
		// being routed can reopen the session. Finish it before sealing the
		// mailbox; a route that began before the seal is allowed to post its
		// promoted run, while a later frame is correctly too late.
		h.svc.FinishRun(id, run)
		// Seal new routes and wait only for a Steer handler that has already
		// received its frame. This closes the receive→post race without adding a
		// grace delay to ordinary completed runs.
		ho.closeAndWait()
		for {
			p, ok := ho.take()
			if !ok {
				return // sealed AND nothing queued: the RPC ends
			}
			// The control target swaps BEFORE the promoted relay starts, so a
			// control frame received from here on applies to the ACTIVE run —
			// never the terminal original.
			ct.swap(p.run)
			pushErr(h.relayRun(rl, p.run))
			h.svc.FinishRun(id, p.run)
			// The promoted run's terminal ack is SENT INLINE (never the ack
			// lane — relayRun already returned, so a lane-queued ack could lose
			// the select race to the closed events channel and ride after the
			// RPC returned): the promoted run's terminal EvResult drained
			// strictly before this point (causal order), and the RPC returns
			// only after it (no ack-after-close).
			if rl.sendErr == nil {
				if err := rl.snd.Send(&mecatlv1.ConverseResponse{Event: &mecatlv1.Event{
					Type: "steer.outcome",
					SteerOutcome: &mecatlv1.SteerAck{
						Outcome:   mecatlv1.SteerOutcome_STEER_OUTCOME_TOO_LATE,
						Text:      valid(p.text),
						Promoted:  true,
						MessageId: valid(p.messageID),
					},
				}}); err != nil {
					rl.sendErr = err
				}
			}
		}
	}()
	// Wait out the relay loop: done closes only after the original relay AND
	// every queued promotion's relay (plus its terminal ack) finished — the
	// seal guarantees nothing later can be queued.
	<-done
	relayRes.mu.Lock()
	defer relayRes.mu.Unlock()
	// The promoted-relay terminal ack may have set sendErr after the last
	// pushErr; the sticky relay error is the first non-nil of the recorded
	// results, else the relay's own final state.
	for _, err := range relayRes.errs {
		if err != nil {
			return err // the FIRST Send error, per the relay contract
		}
	}
	return rl.sendErr
}

// streamSender is the single-writer gate every Send on one Converse bidi stream
// goes through. A gRPC stream is NOT goroutine-safe: the RecoverNotice pre-send
// and the run relay reach the same stream, and only the mutex keeps any two of
// them out of a concurrent Send. (The sequential active-run handoff relays the
// promoted run on the SAME goroutine as the original, but the gate stays: it is
// the structural guarantee, not a per-goroutine coincidence.)
type streamSender struct {
	mu     sync.Mutex
	stream mecatlv1.HarnessService_ConverseServer
}

// Send serializes a single Send onto the stream.
func (s *streamSender) Send(m *mecatlv1.ConverseResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Send(m)
}

// relayRun drains run.Events() onto the Converse stream until the channel
// closes (the terminal result was delivered), interleaving any steer.outcome
// acks the control reader produces. It returns the FIRST Send error, if any —
// but only after the run fully drained (see the drain-to-discard contract
// below). Extracted from Converse so the steer too-late→promoted follow-up run
// (the sequential active-run handoff) reuses the EXACT same relay discipline
// rather than growing a second copy.
//
// On the FIRST Send error (the client is gone) the relay cancels the run but
// KEEPS RANGING, discarding events until the channel closes: a run that keeps
// emitting (a busy team / fan-out winding down) must never wedge in its own
// sends behind a dead relay. The error is sticky — no further Send happens
// after it — and is returned once the run has fully drained.
//
// The durable event-log Append (cloud-native Phase 3a) is DECOUPLED from the
// client send: it runs for EVERY observed event, BEFORE and independent of the
// drain-to-discard guard, so a disconnected client never stops the log (the
// whole point of a server-side durable log is to survive the client — it must
// record the post-disconnect tail, including the terminal EvResult). It uses a
// cancel-detached context so a cancelled stream ctx (client gone) cannot abort
// the durable write. This is DISTINCT from the EvPermissionAsk Persist below,
// which is snapshot semantics gated to the healthy path: the log is append-only
// history and must record what happened regardless of client liveness.
//
// The select has no priority: a buffered steer ack may be delivered ahead of an
// already-buffered run event (Go picks a ready case at random). That is the
// honest contract — an ack's precise interleave position is unobservable
// ordering across stream latency anyway; the drain echo (EvSteer) is the
// authoritative committed-text surface.
// runRelay carries the one Converse stream's relay state the SHARED relayRun
// consumes: the ctx + cancel-detached logCtx, the session id, the steer-ack
// lane it interleaves, and the first Send error (sticky — drain-to-discard).
// Driving it through the runRelay (never the raw stream) keeps EVERY relay
// send on the stream's single-writer streamSender.
//
// sendErr has a SINGLE OWNER: the relay goroutine is the only reader AND
// writer — the sequential active-run handoff drives the promoted run's relay
// on the SAME goroutine (one relayRun call at a time), so no cross-goroutine
// access exists by construction.
type runRelay struct {
	ctx      context.Context
	logCtx   context.Context
	id       session.SessionID
	acks     chan *mecatlv1.SteerAck
	snd      *streamSender
	recorder *RunEventRecorder
	sendErr  error
}

// sendEvent relays one run event through the streamSender, applying the
// drain-to-discard + first-error contract. It NEVER sends after the first
// error: a client-gone relay appends to the durable log only.
func (h *HarnessServer) sendEvent(rl *runRelay, ev session.Event) {
	if rl.sendErr != nil {
		// drain-to-discard: the client is gone. Still record the durable
		// projection (it must include the post-disconnect tail), but skip Persist /
		// auto-approve / the client send.
		rl.recorder.Observe(ev)
		return
	}
	if !h.svc.relayEvent(rl.ctx, rl.id, ev, true, rl.recorder) {
		return // log-only event: consumed by the durable log, not relayed to the client wire
	}
	proto := toProto(ev)
	if ev.Type == session.EvSteer && proto.GetSteer() != nil {
		// The EvSteer drain echo echoes the client-minted message_id of the
		// Steer frame that parked this text: the engine inbox carries text only,
		// so the id lives at the Service's wire-correlation FIFO — popped here
		// positionally (the TAIL). An unmatched echo (an id-less steer) rides
		// with "".
		id := h.svc.LookupSteerMessageID(rl.id)
		if id == "" {
			// The correlation FAILED: the echo carries "" and the client cannot
			// match it to the frame it sent (the queue can stall — the exact
			// symptom this WARN exists to make visible). No session.Event owns a
			// correlation miss, so it goes to diagnostics, text clamped to a prefix.
			h.svc.Diagnostics().Log(rl.logCtx, port.LevelWarn, "steer echo uncorrelated (no message_id for drained text)", "session", string(rl.id), "text_prefix", valid(firstRunes(ev.Steer.Text, 40)))
		} else {
			h.svc.Diagnostics().Log(rl.logCtx, port.LevelInfo, "steer drain echo correlated",
				"session", string(rl.id), "message_id", id, "text_len", len(ev.Steer.Text))
		}
		proto.GetSteer().MessageId = valid(id)
	}
	if err := rl.snd.Send(&mecatlv1.ConverseResponse{Event: proto}); err != nil {
		rl.sendErr = err
	}
}

// relayRun drains run.Events() onto the Converse stream until the channel
// closes (the terminal result was delivered), interleaving any steer.outcome
// acks the control reader produces. It returns the runRelay's FIRST Send error,
// if any — but only after the run fully drained (see the drain-to-discard
// contract above). Shared by the original run and a promoted-steer follow-up
// run (the sequential active-run handoff drives them ONE AT A TIME on the same
// goroutine), so every send crosses the stream's one single-writer streamSender
// and runRelay.sendErr keeps its single owner.
//
// On the FIRST Send error (the client is gone) the relay cancels the run but
// KEEPS RANGING, discarding events until the channel closes: a run that keeps
// emitting (a busy team / fan-out winding down) must never wedge in its own
// sends behind a dead relay. The error is sticky — no further Send happens after
// it (the sendEvent hard guard) — and is returned once the run has drained.
func (h *HarnessServer) relayRun(rl *runRelay, run *agent.Run) error {
	events := run.Events()
	acks := rl.acks
	for events != nil {
		select {
		case ev, ok := <-events:
			if !ok {
				events = nil // the run ended
				continue
			}
			h.sendEvent(rl, ev)
			if rl.sendErr != nil {
				run.Cancel() // first error: drain-to-discard from here
			}
		case ack, ok := <-acks:
			if !ok {
				acks = nil
				continue
			}
			if rl.sendErr != nil {
				continue // client gone: acks have no durable-log home; drop them
			}
			if err := rl.snd.Send(&mecatlv1.ConverseResponse{Event: &mecatlv1.Event{
				Type:         "steer.outcome",
				SteerOutcome: ack,
			}}); err != nil {
				rl.sendErr = err
				run.Cancel()
			}
		}
	}
	return rl.sendErr
}

// controlTarget is the ACTIVE run the stream's control frames apply to: the
// original run until a promoted steer hands off, then the promoted run. The
// relay handoff swaps it BEFORE the promoted relay starts, so a ResumeApproval
// / Cancel / CancelChild received after the handoff hits the run actually
// driving — never the terminal original.
type controlTarget struct {
	mu  sync.RWMutex
	run *agent.Run
}

func (c *controlTarget) swap(r *agent.Run) {
	c.mu.Lock()
	c.run = r
	c.mu.Unlock()
}

func (c *controlTarget) active() *agent.Run {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.run
}

// steerHandoff is the promoted-steer mailbox readControl posts into and
// Converse's sequential relay loop drains. It is a channel-based queue: at most
// one promotion is ever in flight per session (the promoted run is REGISTERED
// the moment Service.Steer returns it, so a second promote hits the funnel's
// liveness guard and is refused), so the buffered channel never blocks a
// poster, and take NEVER blocks — the relay loop consumes it only AFTER
// sealing, so a blocking take is never needed.
type steerHandoff struct {
	mu        sync.Mutex
	q         []steerPromotion
	closed    bool
	routing   int
	routeDone chan struct{}
}

// steerPromotion is one posted handoff: the promoted follow-up run plus the
// steer frame fields its terminal ack echoes.
type steerPromotion struct {
	run       *agent.Run
	text      string
	messageID string
}

func newSteerHandoff() *steerHandoff { return &steerHandoff{} }

// beginRoute reserves the handoff for a steer frame already received by the
// control reader. Once the original relay seals the mailbox, only such an
// in-flight route may still post a promotion.
func (h *steerHandoff) beginRoute() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return false
	}
	if h.routing == 0 {
		h.routeDone = make(chan struct{})
	}
	h.routing++
	return true
}

func (h *steerHandoff) endRoute() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.routing--
	if h.routing == 0 {
		close(h.routeDone)
	}
}

// post queues a promoted run for the relay loop. A route that started before
// close is allowed to finish its post; frames received after close are dropped.
func (h *steerHandoff) post(p steerPromotion) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed && h.routing == 0 {
		return
	}
	h.q = append(h.q, p)
}

// take pops the OLDEST queued promotion; the second return is false once the
// mailbox is sealed AND drained (the relay loop's exit condition).
func (h *steerHandoff) take() (steerPromotion, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.q) == 0 {
		return steerPromotion{}, false
	}
	p := h.q[0]
	h.q = h.q[1:]
	return p, true
}

// close seals the handoff against new routes. It is idempotent: Converse seals
// after the original relay drains and readControl seals again on exit.
func (h *steerHandoff) close() {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
}

// closeAndWait seals the handoff and waits for a steer route that was already
// received to finish posting. No new route may begin after the seal, so the
// captured channel is sufficient and an ordinary terminal run returns at once.
func (h *steerHandoff) closeAndWait() {
	h.mu.Lock()
	h.closed = true
	done := h.routeDone
	h.mu.Unlock()
	if done != nil {
		<-done
	}
}

// readControl reads ResumeApproval / Cancel / CancelChild / Steer / SteerCancel
// frames until the client closes its send half or the context is cancelled.
// Approval / cancel / cancel-child go to the ACTIVE run (ct — the original
// until a promoted steer hands off, then the promoted run, swapped atomically);
// a steer / steer_cancel frame routes through the Service (the single routing
// owner) and produces exactly ONE authoritative ack on the runRelay's ack lane
// (the relay interleaves them onto the stream). A too_late steer the Service
// PROMOTED is posted to the handoff mailbox: Converse relays it SEQUENTIALLY
// after the active run drains, and its terminal outcome is reported back as
// the steer ack. On exit readControl closes the mailbox so the relay loop
// learns no more promotions can arrive.
func (h *HarnessServer) readControl(ctx context.Context, id session.SessionID, ct *controlTarget, rl *runRelay, ho *steerHandoff) {
	defer ho.close()
	stream := rl.snd.stream
	for {
		if ctx.Err() != nil {
			return
		}
		frame, err := stream.Recv()
		if err != nil {
			// EOF means "no more inputs"; any other error means the stream is
			// gone. Either way stop reading; the relay loop owns termination.
			return
		}
		switch k := frame.GetKind().(type) {
		case *mecatlv1.ConverseRequest_ResumeApproval:
			if k.ResumeApproval != nil {
				ra := k.ResumeApproval
				if h.staleStreamControl(ctx, id, "resume_approval", ra.GetExpectedRunId(), ct.active()) {
					break
				}
				ct.active().Approve(ra.GetAskId(), verdictFromResumeApproval(ra.GetVerdict(), ra.GetAllow()))
			}
		case *mecatlv1.ConverseRequest_Cancel:
			if k.Cancel != nil && h.staleStreamControl(ctx, id, "cancel", k.Cancel.GetExpectedRunId(), ct.active()) {
				break
			}
			ct.active().Cancel()
		case *mecatlv1.ConverseRequest_CancelChild:
			if k.CancelChild != nil {
				// Per-child cancel, addressed by the child session id. A false return
				// (unknown / already-done child) is ignored by design on the stream: the
				// finished-as-you-pressed race is benign and the observable outcome is the
				// child's terminal event (clients disable the key for done lanes).
				_ = ct.active().CancelChild(k.CancelChild.GetChildId())
			}
		case *mecatlv1.ConverseRequest_Steer:
			if k.Steer != nil && ho.beginRoute() {
				h.handleSteerFrame(ctx, id, k.Steer, rl, ho)
				ho.endRoute()
			}
		case *mecatlv1.ConverseRequest_SteerCancel:
			if k.SteerCancel != nil {
				h.handleSteerCancelFrame(ctx, id, k.SteerCancel.GetMessageId(), rl)
			}
		default:
			// A second Prompt or an unknown frame is ignored: the run is
			// already driving and a new prompt cannot start a second run here.
		}
	}
}

// staleStreamControl reports whether a Converse control frame names a run that
// is no longer the active one, refusing it if so (ADR 0249).
//
// The refusal is SILENT to the client, and that asymmetry is deliberate rather
// than an oversight. Converse's approve and cancel frames are fire-and-forget:
// the stream carries no per-control ack to put a typed error on, so the choices
// are refuse-and-log or tear down the whole stream over one stale frame. Tearing
// down would punish a client for a race it cannot avoid. A caller that needs the
// typed ErrStaleRunControl uses the HTTP control endpoints, which return it; the
// steer frame is the exception on this stream because it already HAS an ack
// channel, so it reports too_late.
//
// The operator-visible half is the diagnostic below: nothing in the event
// taxonomy reports a refused control, so without it a stale approve would vanish
// without trace.
func (h *HarnessServer) staleStreamControl(ctx context.Context, id session.SessionID, frame, expected string, run *agent.Run) bool {
	if expected == "" || run == nil {
		return false
	}
	if err := checkExpectedRun(expected, run.RunID()); err != nil {
		h.svc.Diagnostics().Log(ctx, port.LevelWarn, "stale control frame refused",
			"session", string(id), "frame", frame, "expected_run", valid(expected), "active_run", valid(run.RunID()))
		return true
	}
	return false
}

// handleSteerFrame routes one steer frame through the Service (the single
// routing owner — the handler is a dumb frame→Service mapper, mirroring how
// the Approve/Cancel frames route). The Service decides live-enqueue vs
// terminal-race promote: an accepted/appended outcome acks immediately, and a
// too_late frame the Service PROMOTED to a fresh follow-up run is POSTED to
// the handoff mailbox — Converse's sequential relay loop drives it on the SAME
// stream after the active run drains (no orphaned relay goroutine), then
// reports its terminal outcome back as the steer ack (promoted=true: never a
// drop). Every ack echoes the frame's client-minted message_id.
func (h *HarnessServer) handleSteerFrame(ctx context.Context, id session.SessionID, frame *mecatlv1.Steer, rl *runRelay, ho *steerHandoff) {
	text, msgID := frame.GetText(), frame.GetMessageId()
	expectedRunID := frame.GetExpectedRunId()
	parts, perr := contentFromProto(frame.GetParts())
	if perr != nil || (text == "" && len(parts) == 0) {
		reason := "empty"
		if perr != nil {
			reason = "invalid_content"
		}
		h.svc.Diagnostics().Log(ctx, port.LevelWarn, "invalid steer frame", "session", string(id), "reason", reason, "part_count", len(frame.GetParts()))
		enqueueSteerAck(ctx, rl.acks, &mecatlv1.SteerAck{Outcome: mecatlv1.SteerOutcome_STEER_OUTCOME_TOO_LATE, Text: valid(text), MessageId: valid(msgID)})
		return
	}
	outcome, promoted, promotedRun, err := h.svc.Steer(ctx, id, text, parts, msgID, expectedRunID)
	switch {
	case err != nil:
		h.svc.Diagnostics().Log(ctx, port.LevelWarn, "steer route failed", "session", string(id), "error", err)
		enqueueSteerAck(ctx, rl.acks, &mecatlv1.SteerAck{Outcome: mecatlv1.SteerOutcome_STEER_OUTCOME_TOO_LATE, Text: valid(text), MessageId: valid(msgID)})
		return
	case !promoted || promotedRun == nil:
		// The Service's live path answered (accepted / appended — the run
		// parked/merged it). Ack the engine's authoritative outcome and log
		// the correlation state (id + outcome only — never the text, which is
		// producer-influenced and carries no diagnostic value).
		h.svc.Diagnostics().Log(ctx, port.LevelInfo, "steer frame enqueued",
			"session", string(id), "message_id", firstRunes(msgID, msgIDLogMax),
			"text_len", len(text), "outcome", string(outcome))
		enqueueSteerAck(ctx, rl.acks, &mecatlv1.SteerAck{Outcome: steerOutcomeToProto(outcome), Text: valid(text), MessageId: valid(msgID)})
		return
	}
	// too_late + promoted: post the follow-up run to the handoff mailbox. The
	// sequential relay loop relays its full event stream on this stream after
	// the active run drains, FinishRun-deregisters it BEFORE the RPC returns,
	// and reports its terminal outcome as the steer ack (promoted=true: never a
	// drop, never an ack-after-close).
	h.svc.Diagnostics().Log(ctx, port.LevelInfo, "steer frame promoted (too_late follow-up)",
		"session", string(id), "message_id", firstRunes(msgID, msgIDLogMax), "text_len", len(text))
	ho.post(steerPromotion{run: promotedRun, text: text, messageID: msgID})
}

// handleSteerCancelFrame routes a steer_cancel frame through the Service and
// reports its authoritative outcome, echoing the frame's client-minted
// message_id. A session with no live run (the run went terminal behind the
// client's belief) reports none_pending — there is no inbox to retract from;
// the pending steer is already lost with its run.
func (h *HarnessServer) handleSteerCancelFrame(ctx context.Context, id session.SessionID, msgID string, rl *runRelay) {
	outcome, err := h.svc.CancelSteer(ctx, id)
	if err != nil {
		outcome = agent.SteerNonePending // never wedge the reader on a cancel fault
	}
	// Log only on a RETRACTED outcome (a none_pending cancel is the
	// information-free common case — the ack carries it; the none_pending
	// line would be unbounded attacker-driven log volume, CWE-770). The
	// client-minted message_id is clamped to a bounded prefix.
	if outcome == agent.SteerRetracted {
		h.svc.Diagnostics().Log(ctx, port.LevelInfo, "steer_cancel frame retracted",
			"session", string(id), "message_id", firstRunes(msgID, msgIDLogMax), "outcome", string(outcome))
	}
	enqueueSteerAck(ctx, rl.acks, &mecatlv1.SteerAck{Outcome: steerOutcomeToProto(outcome), MessageId: valid(msgID)})
}

// enqueueSteerAck delivers an ack on the lane, bounded by ctx: a buffered lane
// accepts it immediately; a FULL lane (a burst outrunning a stalled client)
// drops it on ctx cancel rather than wedging the control reader (the run's own
// guard sends give up on the same signal).
func enqueueSteerAck(ctx context.Context, acks chan<- *mecatlv1.SteerAck, ack *mecatlv1.SteerAck) {
	select {
	case acks <- ack:
	case <-ctx.Done():
	}
}

// msgIDLogMax bounds the client-minted message_id before it reaches any
// diagnostics log (CWE-770 — the log echo is bounded; the wire ack continues
// to carry the verbatim id for correlation). Shared by the three frame-handler
// log sites.
const msgIDLogMax = 64

// firstRunes returns the first n runes of s without allocating a []rune (the
// range-over-string form; used to clamp a diagnostics text preview).
func firstRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	for i := range s {
		if n == 0 {
			return s[:i]
		}
		n--
	}
	return s
}

// steerOutcomeToProto maps the engine's closed-enum agent.SteerOutcome onto the
// wire enum. The zero/unknown value maps to UNSPECIFIED (never sent in
// practice — every engine outcome has an arm).
func steerOutcomeToProto(o agent.SteerOutcome) mecatlv1.SteerOutcome {
	switch o {
	case agent.SteerAccepted:
		return mecatlv1.SteerOutcome_STEER_OUTCOME_ACCEPTED
	case agent.SteerAppended:
		return mecatlv1.SteerOutcome_STEER_OUTCOME_APPENDED
	case agent.SteerRetracted:
		return mecatlv1.SteerOutcome_STEER_OUTCOME_RETRACTED
	case agent.SteerNonePending:
		return mecatlv1.SteerOutcome_STEER_OUTCOME_NONE_PENDING
	case agent.SteerTooLate:
		return mecatlv1.SteerOutcome_STEER_OUTCOME_TOO_LATE
	default:
		return mecatlv1.SteerOutcome_STEER_OUTCOME_UNSPECIFIED
	}
}

// --- MCP inspection RPCs -----------------------------------------------------

// ListMcpResources returns the resource snapshots for the requested server
// (empty server = all). Nil provider yields an empty list.
func (h *HarnessServer) ListMcpResources(ctx context.Context, req *mecatlv1.ListMcpResourcesRequest) (*mecatlv1.ListMcpResourcesResponse, error) {
	res, err := h.svc.ListMcpResources(ctx, req.GetServer())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ListMcpResourcesResponse{Resources: toProtoMcpResources(res)}, nil
}

// ReadMcpResource reads a single resource by URI from the named server.
func (h *HarnessServer) ReadMcpResource(ctx context.Context, req *mecatlv1.ReadMcpResourceRequest) (*mecatlv1.ReadMcpResourceResponse, error) {
	if req.GetServer() == "" || req.GetUri() == "" {
		return nil, status.Error(codes.InvalidArgument, "server and uri are required")
	}
	c, err := h.svc.ReadMcpResource(ctx, req.GetServer(), req.GetUri())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ReadMcpResourceResponse{
		Contents: []*mecatlv1.McpResourceContents{toProtoMcpResourceContents(c)},
	}, nil
}

// ListMcpPrompts returns the prompt snapshots for the requested server
// (empty server = all). Nil provider yields an empty list.
func (h *HarnessServer) ListMcpPrompts(ctx context.Context, req *mecatlv1.ListMcpPromptsRequest) (*mecatlv1.ListMcpPromptsResponse, error) {
	ps, err := h.svc.ListMcpPrompts(ctx, req.GetServer())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ListMcpPromptsResponse{Prompts: toProtoMcpPrompts(ps)}, nil
}

// GetMcpPrompt expands a named prompt with arguments on the named server.
func (h *HarnessServer) GetMcpPrompt(ctx context.Context, req *mecatlv1.GetMcpPromptRequest) (*mecatlv1.GetMcpPromptResponse, error) {
	if req.GetServer() == "" || req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "server and name are required")
	}
	res, err := h.svc.GetMcpPrompt(ctx, req.GetServer(), req.GetName(), req.GetArguments())
	if err != nil {
		return nil, toStatus(err)
	}
	msgs := make([]*mecatlv1.McpPromptMessage, 0, len(res.Messages))
	for _, m := range res.Messages {
		msgs = append(msgs, toProtoMcpPromptMessage(m))
	}
	return &mecatlv1.GetMcpPromptResponse{Description: res.Description, Messages: msgs}, nil
}

// GetCompatibilityInfo returns the deployment's compatibility descriptor
// (ADR 0248).
//
// Distinct from GetServerInfo above, which answers "which BUILD is this?" under
// ADR 0245's privacy boundary. This answers "what may I do with this server?"
// and carries exactly the capabilities/configuration that boundary keeps out of
// the identity response.
//
// It is authenticated like every other RPC, which keeps UNAUTHENTICATED and
// UNIMPLEMENTED distinguishable at the client: the SDK treats UNIMPLEMENTED as
// "below the compatibility floor" and fails loudly, so an auth failure must not
// be able to masquerade as one.
func (h *HarnessServer) GetCompatibilityInfo(ctx context.Context, _ *mecatlv1.GetCompatibilityInfoRequest) (*mecatlv1.GetCompatibilityInfoResponse, error) {
	return h.svc.CompatibilityInfo(ctx), nil
}

// ListMcpSources returns the resolved MCP source inventory snapshot.
func (h *HarnessServer) ListMcpSources(ctx context.Context, _ *mecatlv1.ListMcpSourcesRequest) (*mecatlv1.ListMcpSourcesResponse, error) {
	infos := h.svc.ListMcpSources(ctx)
	out := make([]*mecatlv1.McpSource, 0, len(infos))
	for _, s := range infos {
		out = append(out, toProtoMcpSource(s))
	}
	return &mecatlv1.ListMcpSourcesResponse{Sources: out}, nil
}

// ListToolHiveGroups returns the distinct, non-empty ToolHive groups derived
// from the inventory snapshot.
func (h *HarnessServer) ListToolHiveGroups(ctx context.Context, _ *mecatlv1.ListToolHiveGroupsRequest) (*mecatlv1.ListToolHiveGroupsResponse, error) {
	return &mecatlv1.ListToolHiveGroupsResponse{Groups: h.svc.ListToolHiveGroups(ctx)}, nil
}

// ListAgents returns the resolved agent-definition inventory snapshot.
func (h *HarnessServer) ListAgents(ctx context.Context, _ *mecatlv1.ListAgentsRequest) (*mecatlv1.ListAgentsResponse, error) {
	return &mecatlv1.ListAgentsResponse{Agents: h.svc.ListAgents(ctx)}, nil
}

// ListSkills returns the resolved skills-inventory snapshot.
func (h *HarnessServer) ListSkills(ctx context.Context, _ *mecatlv1.ListSkillsRequest) (*mecatlv1.ListSkillsResponse, error) {
	return &mecatlv1.ListSkillsResponse{Skills: h.svc.ListSkills(ctx)}, nil
}

// ListModels returns the resolved selectable-model inventory snapshot plus
// (issue #262) the per-provider live-listing status. ListModels itself
// triggers the on-demand refresh (when installed), so ProviderStatuses is read
// AFTER it to reflect the just-completed refresh.
func (h *HarnessServer) ListModels(ctx context.Context, _ *mecatlv1.ListModelsRequest) (*mecatlv1.ListModelsResponse, error) {
	models := h.svc.ListModels(ctx)
	return &mecatlv1.ListModelsResponse{Models: models, ProviderStatus: h.svc.ProviderStatuses()}, nil
}

// GetSoul returns the resolved soul (persona) snapshot.
func (h *HarnessServer) GetSoul(ctx context.Context, _ *mecatlv1.GetSoulRequest) (*mecatlv1.GetSoulResponse, error) {
	return &mecatlv1.GetSoulResponse{Soul: h.svc.GetSoul(ctx)}, nil
}

// GetUserModel returns the current user-model index snapshot.
func (h *HarnessServer) GetUserModel(ctx context.Context, req *mecatlv1.GetUserModelRequest) (*mecatlv1.GetUserModelResponse, error) {
	resp, err := h.svc.GetUserModel(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	detail, err := h.svc.GetUserModelDetail(ctx, req.GetKey())
	if err != nil {
		return nil, toStatus(err)
	}
	resp.Detail = detail
	return resp, nil
}

// ReflectSession submits one caller-owned completed session to the bounded coordinator.
func (h *HarnessServer) ReflectSession(ctx context.Context, req *mecatlv1.ReflectSessionRequest) (*mecatlv1.ReflectSessionResponse, error) {
	receipt, err := h.svc.ReflectSession(ctx, session.SessionID(req.GetSessionId()))
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ReflectSessionResponse{Receipt: receipt}, nil
}

// GenerateDreamPlan creates a retained manual consolidation review.
func (h *HarnessServer) GenerateDreamPlan(ctx context.Context, req *mecatlv1.GenerateDreamPlanRequest) (*mecatlv1.GenerateDreamPlanResponse, error) {
	review, err := h.svc.GenerateDream(ctx, DreamTarget(req.GetTarget()))
	if err != nil {
		return nil, toStatus(normalizeDreamError(err))
	}
	return &mecatlv1.GenerateDreamPlanResponse{Plan: toProtoDreamReview(review)}, nil
}

// DecideDreamPlan applies or dismisses the exact retained review plan.
func (h *HarnessServer) DecideDreamPlan(ctx context.Context, req *mecatlv1.DecideDreamPlanRequest) (*mecatlv1.DecideDreamPlanResponse, error) {
	receipt, err := h.svc.DecideDream(ctx, req.GetPlanId(), DreamDecision(req.GetDecision()))
	if err != nil && (!errors.Is(err, ErrDreamApplyFailed) || receipt.ID == "") {
		return nil, toStatus(normalizeDreamError(err))
	}
	return &mecatlv1.DecideDreamPlanResponse{Receipt: toProtoDreamReceipt(receipt)}, nil
}

// ListLearningProposals returns one bounded proposal page for the caller partition.
func (h *HarnessServer) ListLearningProposals(ctx context.Context, req *mecatlv1.ListLearningProposalsRequest) (*mecatlv1.ListLearningProposalsResponse, error) {
	resp, err := h.svc.ListLearningProposals(ctx, req.GetStatus(), req.GetCursor(), int(req.GetLimit()), req.GetProject())
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}

// GetLearningProposal returns bounded detail for one caller-owned proposal.
func (h *HarnessServer) GetLearningProposal(ctx context.Context, req *mecatlv1.GetLearningProposalRequest) (*mecatlv1.GetLearningProposalResponse, error) {
	proposal, err := h.svc.GetLearningProposal(ctx, req.GetId(), req.GetProject())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.GetLearningProposalResponse{Proposal: proposal}, nil
}

// DecideLearningProposal applies a version-checked approve or reject decision.
func (h *HarnessServer) DecideLearningProposal(ctx context.Context, req *mecatlv1.DecideLearningProposalRequest) (*mecatlv1.DecideLearningProposalResponse, error) {
	proposal, err := h.svc.DecideLearningProposal(ctx, req.GetId(), req.GetExpectedVersion(), req.GetDecision(), req.GetReason(), req.GetProject())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.DecideLearningProposalResponse{Proposal: proposal}, nil
}

// UndoLearningPromotion applies a version-checked compensating promotion undo.
func (h *HarnessServer) UndoLearningPromotion(ctx context.Context, req *mecatlv1.UndoLearningPromotionRequest) (*mecatlv1.UndoLearningPromotionResponse, error) {
	proposal, err := h.svc.UndoLearningPromotion(ctx, req.GetId(), req.GetExpectedVersion(), req.GetProject())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.UndoLearningPromotionResponse{Proposal: proposal}, nil
}

func (h *HarnessServer) ListLearnedSkills(ctx context.Context, req *mecatlv1.ListLearnedSkillsRequest) (*mecatlv1.ListLearnedSkillsResponse, error) {
	resp, err := h.svc.ListLearnedSkills(ctx, req)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}
func (h *HarnessServer) GetLearnedSkill(ctx context.Context, req *mecatlv1.GetLearnedSkillRequest) (*mecatlv1.GetLearnedSkillResponse, error) {
	resp, err := h.svc.GetLearnedSkill(ctx, req)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}
func (h *HarnessServer) DiffLearnedSkillVersions(ctx context.Context, req *mecatlv1.DiffLearnedSkillVersionsRequest) (*mecatlv1.DiffLearnedSkillVersionsResponse, error) {
	resp, err := h.svc.DiffLearnedSkillVersions(ctx, req)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}
func (h *HarnessServer) ActivateLearnedSkill(ctx context.Context, req *mecatlv1.MutateLearnedSkillRequest) (*mecatlv1.MutateLearnedSkillResponse, error) {
	resp, err := h.svc.ActivateLearnedSkill(ctx, req)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}
func (h *HarnessServer) RejectLearnedSkill(ctx context.Context, req *mecatlv1.MutateLearnedSkillRequest) (*mecatlv1.MutateLearnedSkillResponse, error) {
	resp, err := h.svc.RejectLearnedSkill(ctx, req)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}
func (h *HarnessServer) ArchiveLearnedSkill(ctx context.Context, req *mecatlv1.MutateLearnedSkillRequest) (*mecatlv1.MutateLearnedSkillResponse, error) {
	resp, err := h.svc.ArchiveLearnedSkill(ctx, req)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}
func (h *HarnessServer) RollbackLearnedSkill(ctx context.Context, req *mecatlv1.RollbackLearnedSkillRequest) (*mecatlv1.MutateLearnedSkillResponse, error) {
	resp, err := h.svc.RollbackLearnedSkill(ctx, req)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}
func (h *HarnessServer) ListSkillChanges(ctx context.Context, req *mecatlv1.ListSkillChangesRequest) (*mecatlv1.ListSkillChangesResponse, error) {
	resp, err := h.svc.ListSkillChanges(ctx, req)
	if err != nil {
		return nil, toStatus(err)
	}
	return resp, nil
}

// ListCommands returns the available slash commands for the requested workspace.
func (h *HarnessServer) ListCommands(ctx context.Context, req *mecatlv1.ListCommandsRequest) (*mecatlv1.ListCommandsResponse, error) {
	cmds, err := h.svc.ListCommands(ctx, req.GetWorkspace())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ListCommandsResponse{Commands: toProtoCommands(cmds)}, nil
}

// ListWorktrees returns the git worktrees of the repo rooted at the requested
// workspace (issue #102).
func (h *HarnessServer) ListWorktrees(ctx context.Context, req *mecatlv1.ListWorktreesRequest) (*mecatlv1.ListWorktreesResponse, error) {
	wts, err := h.svc.ListWorktrees(ctx, req.GetWorkspace())
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ListWorktreesResponse{Worktrees: toProtoWorktrees(wts)}, nil
}

// StreamSessionEvents replays a session's durable event log as a server stream
// of Event envelopes (issue #245 Phase 1; cloud-native Phase 3a read-back).
func (h *HarnessServer) StreamSessionEvents(req *mecatlv1.StreamSessionEventsRequest, stream grpc.ServerStreamingServer[mecatlv1.Event]) error {
	if req.GetSessionId() == "" {
		return status.Error(codes.InvalidArgument, ErrInvalidArgument.Error())
	}
	events, err := h.svc.StreamSessionEvents(stream.Context(), session.SessionID(req.GetSessionId()))
	if err != nil {
		return toStatus(err)
	}
	// CRITICAL (replay-vs-live): the LIVE Converse relay SKIPS the three log-only
	// event kinds (EvApproval/EvCompactionArchive/EvUserPrompt) on the client wire
	// because they are persistence-only. StreamSessionEvents is the READ-BACK of the
	// durable log itself — a client opening a PAST session WANTS the verdicts and
	// user prompts (they ARE the transcript). So relay ALL events through toProto,
	// including the three log-only kinds. They are already metadata-only/redacted by
	// construction (gauntlet #7). Do NOT copy the live-relay filter here.
	for ev, iterErr := range events {
		if iterErr != nil {
			return status.Error(codes.Internal, iterErr.Error())
		}
		if err := stream.Send(toProto(ev)); err != nil {
			return err
		}
	}
	return nil
}

// StreamSessionLive is the LIVE per-session event stream (ADR 0075
// fire-result-delivery Scenario 6 / Wave 3): a thin transport over the in-process
// per-session subscription registry (Service.Subscribe / PublishSessionEvent). It
// is the UNIFIED bridge serving BOTH the embedded mecatui (which dials its
// in-process server over a real gRPC UNIX socket) AND a remote mecated — ONE
// proto, ONE TUI consumption path.
//
// Relay discipline: the live wire applies the SAME log-only skip as the live
// Converse relay, with ONE narrow exception — a fire-result DELIVERY note (an
// EvUserPrompt whose text starts with the renderFireDelivery provenance header
// "[scheduled task ") is RELAYED so a connected client renders the delivery card
// as it happens (AC6.2). The other two log-only kinds (EvApproval,
// EvCompactionArchive) stay SKIPPED, and a non-delivery EvUserPrompt stays
// skipped too (the client already holds its own prompt). The delivery note is
// metadata-only/redacted by construction (gauntlet #7).
//
// Drain-to-discard: a Send error (dead/disconnected client) cancels the
// subscription and the handler returns — the in-process registry's
// PublishSessionEvent is already non-blocking (a full subscriber channel drops
// the event, AC6.3), so a dead client never wedges the delivery run. The
// durable log records the tail regardless (it is appended by the relay/loop,
// independent of this stream).
func (h *HarnessServer) StreamSessionLive(req *mecatlv1.StreamSessionLiveRequest, stream grpc.ServerStreamingServer[mecatlv1.Event]) error {
	if req.GetSessionId() == "" {
		return status.Error(codes.InvalidArgument, ErrInvalidArgument.Error())
	}
	id := session.SessionID(req.GetSessionId())
	ch, unsub, err := h.svc.Subscribe(stream.Context(), id)
	if err != nil {
		return toStatus(err)
	}
	defer unsub()
	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			// The client cancelled or disconnected — exit cleanly. The deferred
			// unsub closes the subscription channel; PublishSessionEvent drops
			// further events for this (now-gone) subscriber.
			return nil
		case ev, ok := <-ch:
			if !ok {
				// The subscription channel was closed (unsub by the registry, e.g.
				// at Service.Close). Exit cleanly.
				return nil
			}
			// Apply the live-wire relay discipline: relay the delivery EvUserPrompt
			// (the ONE narrow exception) and every non-log-only event; skip the two
			// other log-only kinds and a non-delivery EvUserPrompt.
			if !relayLiveEvent(ev) {
				continue
			}
			if err := stream.Send(toProto(ev)); err != nil {
				// The client is gone — exit cleanly. The delivery run is NOT wedged
				// (PublishSessionEvent is non-blocking; a dead subscriber's channel
				// is closed by unsub, so further publishes drop).
				return nil
			}
		}
	}
}

// WatchSessionEvents is the DURABLE replay-then-follow stream (issue #821, ADR
// 0250): a thin transport over Service.WatchSessionEvents.
//
// The SSE route GET /v1/sessions/{id}/watch consumes the SAME service method, so
// the two transports deliver identical envelope sequences by construction rather
// than by parallel maintenance (AC7.3). Everything below is framing.
//
// Relay discipline mirrors StreamSessionEvents, NOT the live wire: this is the
// READ-BACK of the durable log, so ALL events are relayed including the three
// log-only kinds (EvApproval/EvCompactionArchive/EvUserPrompt) — a client
// replaying a session wants the verdicts and prompts, as they ARE the transcript.
// Do NOT copy relayLiveEvent's filter here.
//
// A Send error means the client is gone: break out of the iterator, which
// releases the watch and its backend read per port.CursorEventLog's contract.
// There is no drain-to-discard to do — unlike a live relay, this stream pulls
// from durable storage and has no run whose emits could wedge behind it.
func (h *HarnessServer) WatchSessionEvents(req *mecatlv1.WatchSessionEventsRequest, stream grpc.ServerStreamingServer[mecatlv1.WatchSessionEventsResponse]) error {
	if req.GetSessionId() == "" {
		return status.Error(codes.InvalidArgument, ErrInvalidArgument.Error())
	}
	envelopes, err := h.svc.WatchSessionEvents(stream.Context(),
		session.SessionID(req.GetSessionId()), port.Cursor(req.GetCursor()), req.GetRunId())
	if err != nil {
		return toStatus(err)
	}
	for env, iterErr := range envelopes {
		if iterErr != nil {
			// toStatus classifies through the shared registry, so a lagging
			// termination, a delivery gap, and a cursor fault each reach the client
			// as the same code the HTTP surface would report.
			return toStatus(iterErr)
		}
		if err := stream.Send(toProtoWatchEnvelope(env)); err != nil {
			return err
		}
	}
	return nil
}

// toProtoWatchEnvelope projects one delivery envelope onto the wire.
//
// A nil Event stays nil — the phase-only frames (the replay/live boundary and
// every gap) carry no event by design, and synthesising an empty one would make
// a client's "did anything happen?" check answer yes.
func toProtoWatchEnvelope(env WatchEnvelope) *mecatlv1.WatchSessionEventsResponse {
	out := &mecatlv1.WatchSessionEventsResponse{
		// Phase is a harness constant from a closed set, so it needs no repair.
		//
		// Cursor DOES get the producer-influenced-string repair, because it is not
		// as harness-authored as it looks: the four in-tree backends mint ASCII
		// (base64url, or a Redis XADD id), but the cursor is BACKEND-OWNED and a
		// third-party port.CursorEventLog may mint anything. Without the repair,
		// one invalid byte in a cursor kills the whole stream at proto marshal —
		// the issue-#402 failure mode the mapper's mechanical backstop exists to
		// prevent. Repairing it does corrupt that token, and that is the better
		// failure: a corrupt cursor is rejected LOUDLY as ErrCursorMalformed at the
		// next resume (never silently resolved to a wrong position), whereas a dead
		// stream takes the session's whole live view with it. It also keeps the two
		// transports byte-identical — SSE encodes this same struct through
		// encoding/json, which would substitute U+FFFD on its own and diverge.
		Cursor: valid(string(env.Cursor)),
		Phase:  env.Phase,
	}
	if env.Event != nil {
		out.Event = toProto(*env.Event)
	}
	return out
}

// relayLiveEvent reports whether a live-subscription event should be relayed on
// the client wire. It mirrors the live Converse relay's log-only skip with ONE
// narrow exception: a fire-result DELIVERY note (an EvUserPrompt whose text
// starts with the renderFireDelivery provenance header) is relayed so the
// connected client renders the delivery card as it happens. The other two
// log-only kinds (EvApproval, EvCompactionArchive) and a non-delivery
// EvUserPrompt stay skipped (they are persistence-only; the client holds its
// own verdict/compaction/prompt view).
func relayLiveEvent(ev session.Event) bool {
	switch ev.Type {
	case session.EvApproval, session.EvCompactionArchive:
		return false
	case session.EvUserPrompt:
		// Relay the delivery note only — the renderFireDelivery provenance header
		// is the single detection pattern (mirrors the TUI client's
		// deliveryNotePrefix). A non-delivery EvUserPrompt stays skipped.
		//
		// renderFireDelivery wraps the note in governance.FenceUntrusted, so the
		// recorded text starts with the untrusted-fence opener
		// ("<<<UNTRUSTED\n") FOLLOWED by the "[scheduled task " provenance
		// header on the next line. The detection matches that FENCED form so a
		// delivery note (which is always fenced) is relayed while a non-delivery
		// user prompt (which is never fenced) stays skipped. A bare
		// HasPrefix("[scheduled task ") would NEVER match the real note (the
		// fence opener precedes the header) and would FALSE-match a user who
		// literally typed "[scheduled task …"; the fenced discriminator closes
		// both.
		return ev.UserPrompt != nil && isDeliveryNoteText(ev.UserPrompt.Text)
	default:
		return true
	}
}

// deliveryNoteHeaderPrefix is the literal provenance header renderFireDelivery
// emits INSIDE the fenced-untrusted block (the first line after the
// "<<<UNTRUSTED\n" opener). It is the single detection pattern the live-wire
// relay + the TUI client key off; it must match the SAME literal
// renderFireDelivery produces (internal/app/scheduler_delivery.go) and the TUI
// client's deliveryNotePrefix (cmd/mecatui/client/msgs.go). It is the shared
// contract between the server relay and the client projection.
const deliveryNoteHeaderPrefix = "[scheduled task "

// deliveryNoteFenceOpener is the leading fence marker renderFireDelivery wraps
// EVERY delivery note in (governance.FenceUntrusted writes "<<<UNTRUSTED\n" then the
// body). Detection keys off the fence opener FOLLOWED by the header prefix so a
// non-delivery user prompt (never fenced) cannot match.
const deliveryNoteFenceOpener = "<<<UNTRUSTED\n"

// isDeliveryNoteText reports whether text is a fenced fire-result delivery
// note: it starts with the untrusted-fence opener and its first content line
// begins with the delivery provenance header. This is the precise shape
// renderFireDelivery produces; a plain user prompt (un-fenced) never matches.
func isDeliveryNoteText(text string) bool {
	if !strings.HasPrefix(text, deliveryNoteFenceOpener) {
		return false
	}
	return strings.HasPrefix(text[len(deliveryNoteFenceOpener):], deliveryNoteHeaderPrefix)
}

// ListSessions returns the stored-session inventory — the picker metadata a
// client renders to let an operator open an EXISTING session by id (issue #245
// Phase 1).
func (h *HarnessServer) ListSessions(ctx context.Context, req *mecatlv1.ListSessionsRequest) (*mecatlv1.ListSessionsResponse, error) {
	page, err := h.svc.ListSessionPage(ctx, ListSessionsPageRequest{
		PageSize: int(req.GetPageSize()), Cursor: req.GetCursor(),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return &mecatlv1.ListSessionsResponse{
		Sessions: toProtoSessionSummaries(page.Sessions), NextCursor: page.NextCursor,
		TotalCount: ClampInt32(page.TotalCount),
	}, nil
}

// GetStorageHealth returns authenticated aggregate storage status.
func (h *HarnessServer) GetStorageHealth(ctx context.Context, _ *mecatlv1.GetStorageHealthRequest) (*mecatlv1.GetStorageHealthResponse, error) {
	health, err := h.svc.StorageHealth(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoStorageHealth(health), nil
}

func toProtoStorageHealth(h StorageHealth) *mecatlv1.GetStorageHealthResponse {
	ownerlessSessionIDs := make([]string, len(h.Ownerless.SessionIDs))
	for i, id := range h.Ownerless.SessionIDs {
		ownerlessSessionIDs[i] = valid(id)
	}
	ownerlessScheduleNames := make([]string, len(h.Ownerless.ScheduleNames))
	for i, name := range h.Ownerless.ScheduleNames {
		ownerlessScheduleNames[i] = valid(name)
	}
	resp := &mecatlv1.GetStorageHealthResponse{
		Available: h.Available, UnavailableReason: h.UnavailableReason,
		CurrentBytes: h.CurrentBytes, CurrentBytesAvailable: h.CurrentBytesAvailable,
		ReclaimableBytes: h.ReclaimableBytes, ReclaimableBytesAvailable: h.ReclaimableBytesAvailable,
		SessionCount: h.SessionCount, FileCount: h.FileCount, V1Count: h.V1Count, V2Count: h.V2Count,
		MainCount: h.MainCount, ChildCount: h.ChildCount, ScheduledCount: h.ScheduledCount,
		UnknownCount: h.UnknownCount, CorruptCount: h.CorruptCount,
		Policy: &mecatlv1.RetentionPolicy{
			MainMaxAgeSeconds: int64(h.Policy.MainMaxAge.Seconds()), MainMaxCount: ClampInt32(h.Policy.MainMaxCount),
			ChildMaxAgeSeconds: int64(h.Policy.ChildMaxAge.Seconds()), ChildMaxCount: ClampInt32(h.Policy.ChildMaxCount),
			ScheduledMaxAgeSeconds: int64(h.Policy.ScheduledMaxAge.Seconds()), ScheduledMaxCount: ClampInt32(h.Policy.ScheduledMaxCount),
			SweepCadenceSeconds: int64(h.Policy.SweepCadence.Seconds()),
		},
		LastSweepAvailable: h.LastSweepAvailable,
		NextSweepAvailable: h.NextSweepAvailable,
		ActiveJob:          h.ActiveJob, LastFailure: h.LastFailure,
		OwnerlessSessionsAvailable:          h.Ownerless.SessionsAvailable,
		OwnerlessSessionsUnavailableReason:  valid(h.Ownerless.SessionsUnavailableReason),
		OwnerlessSessionCount:               int64(h.Ownerless.SessionCount),
		OwnerlessSessionIds:                 ownerlessSessionIDs,
		OwnerlessSessionIdsTruncated:        h.Ownerless.SessionIDsTruncated,
		OwnerlessSchedulesAvailable:         h.Ownerless.SchedulesAvailable,
		OwnerlessSchedulesUnavailableReason: valid(h.Ownerless.SchedulesUnavailableReason),
		OwnerlessScheduleCount:              int64(h.Ownerless.ScheduleCount),
		OwnerlessScheduleNames:              ownerlessScheduleNames,
		OwnerlessScheduleNamesTruncated:     h.Ownerless.ScheduleNamesTruncated,
	}
	if h.LastSweepAvailable {
		resp.LastSweepUnix = h.LastSweep.Unix()
	}
	if h.NextSweepAvailable {
		resp.NextSweepUnix = h.NextSweep.Unix()
	}
	return resp
}

func toProtoMigrationPlan(plan MigrationPlan) *mecatlv1.SessionMigrationPlan {
	return &mecatlv1.SessionMigrationPlan{
		PlanId: plan.ID, Available: plan.Available, UnavailableReason: plan.UnavailableReason,
		V1Families: plan.V1Families, V2Families: plan.V2Families, InvalidFamilies: plan.InvalidFamilies,
		SkippedFamilies: plan.SkippedFamilies, CurrentBytes: plan.CurrentBytes,
		ReclaimableBytes: plan.ReclaimableBytes, TemporaryBytes: plan.TemporaryBytes,
	}
}

func toProtoMigrationJob(job MigrationJob) *mecatlv1.SessionMigrationJob {
	out := &mecatlv1.SessionMigrationJob{
		JobId: job.ID, State: job.State, V1Families: job.V1Families, V2Families: job.V2Families,
		InvalidFamilies: job.InvalidFamilies, SkippedFamilies: job.SkippedFamilies,
		CurrentBytes: job.CurrentBytes, ReclaimableBytes: job.ReclaimableBytes, TemporaryBytes: job.TemporaryBytes,
		Processed: job.Processed, Migrated: job.Migrated, Failed: job.Failed,
		Errors: make([]*mecatlv1.SessionMigrationItemError, 0, len(job.Errors)),
	}
	for _, item := range job.Errors {
		out.Errors = append(out.Errors, &mecatlv1.SessionMigrationItemError{ItemHandle: item.ItemHandle, ReasonCode: item.ReasonCode, Message: item.Message})
	}
	return out
}

func (h *HarnessServer) PlanSessionMigration(ctx context.Context, _ *mecatlv1.PlanSessionMigrationRequest) (*mecatlv1.SessionMigrationPlan, error) {
	plan, err := h.svc.PlanSessionMigration(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoMigrationPlan(plan), nil
}

func (h *HarnessServer) ApplySessionMigration(ctx context.Context, req *mecatlv1.ApplySessionMigrationRequest) (*mecatlv1.SessionMigrationJob, error) {
	job, err := h.svc.ApplySessionMigration(ctx, req.GetPlanId(), int(req.GetBatchSize()))
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoMigrationJob(job), nil
}

func (h *HarnessServer) ResumeSessionMigration(ctx context.Context, req *mecatlv1.ResumeSessionMigrationRequest) (*mecatlv1.SessionMigrationJob, error) {
	job, err := h.svc.ResumeSessionMigration(ctx, req.GetJobId(), int(req.GetBatchSize()))
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoMigrationJob(job), nil
}

func (h *HarnessServer) CancelSessionMigration(ctx context.Context, req *mecatlv1.CancelSessionMigrationRequest) (*mecatlv1.SessionMigrationJob, error) {
	job, err := h.svc.CancelSessionMigration(ctx, req.GetJobId())
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoMigrationJob(job), nil
}

func (h *HarnessServer) GetSessionMigrationJob(ctx context.Context, req *mecatlv1.GetSessionMigrationJobRequest) (*mecatlv1.SessionMigrationJob, error) {
	job, err := h.svc.SessionMigrationJob(ctx, req.GetJobId())
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoMigrationJob(job), nil
}

// PlanSessionCleanup returns a caller-bound read-only retention plan.
func (h *HarnessServer) PlanSessionCleanup(ctx context.Context, req *mecatlv1.PlanSessionCleanupRequest) (*mecatlv1.PlanSessionCleanupResponse, error) {
	scope := CleanupScope{}
	for _, kind := range req.GetKinds() {
		scope.Kinds = append(scope.Kinds, session.SessionKind(kind))
	}
	plan, err := h.svc.PlanSessionCleanup(ctx, scope)
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoCleanupPlan(plan), nil
}

func (h *HarnessServer) ApplySessionCleanup(ctx context.Context, req *mecatlv1.ApplySessionCleanupRequest) (*mecatlv1.CleanupJob, error) {
	job, err := h.svc.ApplySessionCleanup(ctx, req.GetConfirmationToken())
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoCleanupJob(job), nil
}

func (h *HarnessServer) CancelSessionCleanup(ctx context.Context, req *mecatlv1.CancelSessionCleanupRequest) (*mecatlv1.CleanupJob, error) {
	job, err := h.svc.CancelSessionCleanup(ctx, req.GetJobId())
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoCleanupJob(job), nil
}

func (h *HarnessServer) GetSessionCleanupJob(ctx context.Context, req *mecatlv1.GetSessionCleanupJobRequest) (*mecatlv1.CleanupJob, error) {
	job, err := h.svc.SessionCleanupJob(ctx, req.GetJobId())
	if err != nil {
		return nil, toStatus(err)
	}
	return toProtoCleanupJob(job), nil
}

func toProtoCleanupPlan(plan CleanupPlan) *mecatlv1.PlanSessionCleanupResponse {
	resp := &mecatlv1.PlanSessionCleanupResponse{
		ConfirmationToken: plan.Token, Available: plan.Available, UnavailableReason: plan.UnavailableReason,
		Generation: plan.Generation, PolicyVersion: plan.PolicyVersion, EstimatedBytes: plan.EstimatedBytes,
		PlannedJobId:   plan.JobID,
		EligibleCounts: &mecatlv1.CleanupCounts{Total: ClampInt32(plan.EligibleCounts.Total), ByKind: mapStringInt32(plan.EligibleCounts.ByKind), ByState: mapStringInt32(plan.EligibleCounts.ByState), ByReason: mapStringInt32(plan.EligibleCounts.ByReason)},
		Protected:      &mecatlv1.CleanupCounts{Total: ClampInt32(plan.Protected.Total), ByKind: mapStringInt32(plan.Protected.ByKind), ByState: mapStringInt32(plan.Protected.ByState), ByReason: mapStringInt32(plan.Protected.ByReason)},
	}
	for _, item := range plan.Eligible {
		resp.Eligible = append(resp.Eligible, &mecatlv1.CleanupCandidate{SessionId: string(item.ID), Kind: string(item.Kind), State: string(item.State), Reason: item.Reason, ModifiedAtUnix: item.ModifiedAt.Unix(), EstimatedBytes: item.EstimatedBytes})
	}
	return resp
}

func mapStringInt32(values map[string]int) map[string]int32 {
	out := make(map[string]int32, len(values))
	for key, value := range values {
		out[key] = ClampInt32(value)
	}
	return out
}

func toProtoCleanupJob(job CleanupJob) *mecatlv1.CleanupJob {
	out := &mecatlv1.CleanupJob{JobId: job.ID, State: job.State, Processed: ClampInt32(job.Processed), Deleted: ClampInt32(job.Deleted), Skipped: ClampInt32(job.Skipped), Stale: ClampInt32(job.Stale), Failed: ClampInt32(job.Failed)}
	for _, item := range job.Errors {
		out.Errors = append(out.Errors, &mecatlv1.CleanupItemError{ItemHandle: item.ItemHandle, ReasonCode: item.ReasonCode, Message: item.Message})
	}
	return out
}

// toStatus maps service sentinel errors to gRPC status codes.
func toStatus(err error) error {
	return statusForEntry(classifyError(err), err)
}

// statusForEntry builds the gRPC status for a classified error, attaching the
// stable mecatl code as a google.rpc.ErrorInfo detail.
//
// ErrorInfo is the standard carrier for exactly this (a machine-readable
// `Reason` plus a `Domain` that scopes it), so a client reads the same
// identifier the HTTP surface puts in the problem body's `code`. gRPC status
// codes are far coarser than the domain — a dozen distinct conditions collapse
// onto FailedPrecondition — so without the detail a gRPC caller simply cannot
// tell them apart, and the SDK's "same normalized errors on both transports"
// promise would be false on the gRPC side.
//
// The message stays err.Error(), unchanged from before this registry landed, and
// is repaired to valid UTF-8: it can carry a downstream's error text, and
// invalid UTF-8 in a status message is the marshal-time fault AGENTS.md
// documents. If attaching the detail fails (it can only fail on a marshal
// error), the bare status is returned — a missing detail degrades a client to
// the old coarse behaviour, whereas dropping the status entirely would lose the
// error.
func statusForEntry(entry errorCodeEntry, err error) error {
	st := status.New(entry.GRPC, session.ToValidUTF8(err.Error()))
	withDetail, derr := st.WithDetails(&errdetails.ErrorInfo{
		// Reason carries entry.Code VERBATIM, in lower_snake_case. AIP-193
		// conventionally spells Reason in UPPER_SNAKE_CASE; that convention is
		// deliberately NOT followed, and this is not an oversight to correct.
		// AC2.2 requires the IDENTICAL string on both transports, and the HTTP
		// problem body's `code`/`type` are lowercase to match RFC 9457 style.
		// Upper-casing here would give one error identity two spellings, and
		// every SDK a case conversion to know about. ADR 0248 decision 7 records
		// the trade; TestSDKServerEnablers_Scenario2_ErrorCodeTransportParity
		// fails if the two ever diverge.
		Reason: entry.Code,
		Domain: errorDomain,
	})
	if derr != nil {
		return st.Err()
	}
	return withDetail.Err()
}

// errorDomain scopes the ErrorInfo Reason above, per the google.rpc.ErrorInfo
// contract that a Reason is unique only within its Domain.
const errorDomain = "mecatl.stacklok.com"
