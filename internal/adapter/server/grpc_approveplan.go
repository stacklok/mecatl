package server

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
)

// grpc_approveplan.go implements the ApprovePlan streaming RPC (issue #206,
// Wave 4) over the shared Service. It mirrors the Converse/RunTeam event-relay
// discipline: range the Service-returned event channel, observe each event through
// the durable recorder, skip the three log-only kinds on the client wire, Persist
// on EvPermissionAsk, and Send toProto(ev); on the first Send error cancel the ctx so the Service's
// internal ctx-watcher cancels the LIVE run (the run is registered in s.runs and
// the Service deregisters it after drain). The Service owns run lifecycle
// (resume + continuation); this handler owns the wire.

// ApprovePlan atomically resolves a parked plan-approval ask and — on an allow
// verdict — streams the resumed run's AND the continuation run's events on one
// response stream. See Service.ApprovePlan for the contract.
func (h *HarnessServer) ApprovePlan(req *mecatlv1.ApprovePlanRequest, stream mecatlv1.HarnessService_ApprovePlanServer) error {
	if err := validateGRPCSessionAffinity(stream.Context(), req.GetSessionId()); err != nil {
		return err
	}
	if req.GetSessionId() == "" {
		return status.Error(codes.InvalidArgument, "session_id is required")
	}
	// The Service's ctx-watcher cancels the live run when this ctx is cancelled,
	// so derive a cancellable child we can also trigger on a Send error (a dead
	// client): cancelling here propagates to the Service, which cancels the run,
	// ending the stream promptly instead of running on to completion unseen.
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()

	events, err := h.svc.ApprovePlan(ctx, session.SessionID(req.GetSessionId()), modeFromProto(req.GetTargetMode()), req.GetNote())
	if err != nil {
		return toStatus(err)
	}
	// appendEvent uses a cancel-detached ctx so a dead client never stops the
	// durable log (it must record the post-disconnect tail, including the terminal
	// EvResult) — the same discipline as the Converse relay.
	logCtx := context.WithoutCancel(ctx)
	recorder := NewRunEventRecorder(logCtx, h.svc, session.SessionID(req.GetSessionId()))
	defer recorder.Close()
	var sendErr error
	for ev := range events {
		if sendErr != nil {
			// drain-to-discard: the client is gone. Still append to the durable
			// log, but skip Persist / the client send.
			recorder.Observe(ev)
			continue
		}
		// autoApprove=false: this path IS the plan-approval resolution — running
		// the auto-approve observer inside it would recurse.
		if !h.svc.relayEvent(ctx, session.SessionID(req.GetSessionId()), ev, false, recorder) {
			continue // log-only event: consumed by the durable log, not relayed to the client wire
		}
		if e := stream.Send(toProto(ev)); e != nil {
			sendErr = e
			cancel() // cancel the live run so the stream drains and closes promptly
		}
	}
	return sendErr
}
