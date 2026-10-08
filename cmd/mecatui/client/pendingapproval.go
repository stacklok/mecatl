package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// PendingApproval is the exact ordinary ask correlation recovered from a
// complete durable replay. Cursor remains scoped to the unfiltered watch that
// issued it; continuation filtering is therefore performed locally by RunID.
type PendingApproval struct {
	SessionID string
	RunID     string
	AskID     string
	Tool      string
	Args      string
	Reason    string
	Cursor    string
}

// PendingApprovalFailure is a closed, proto-free refusal category.
type PendingApprovalFailure string

// Pending approval failure categories are closed and safe for presentation code.
const (
	PendingApprovalNone                PendingApprovalFailure = "none"
	PendingApprovalIncomplete          PendingApprovalFailure = "incomplete_replay"
	PendingApprovalMalformed           PendingApprovalFailure = "malformed_correlation"
	PendingApprovalUnsupportedAsk      PendingApprovalFailure = "unsupported_ask"
	PendingApprovalWatchUnsupported    PendingApprovalFailure = "watch_unsupported"
	PendingApprovalControlsUnsupported PendingApprovalFailure = "controls_unsupported"
	PendingApprovalNotRecoverable      PendingApprovalFailure = "not_recoverable"
	PendingApprovalCorrelation         PendingApprovalFailure = "correlation_mismatch"
	PendingApprovalUnavailable         PendingApprovalFailure = "unavailable"
)

type pendingApprovalError struct {
	kind PendingApprovalFailure
	text string
}

func (e *pendingApprovalError) Error() string { return e.text }

// IsPendingApprovalFailure reports a closed recovery refusal without exposing a
// server error or tool arguments to presentation code.
func IsPendingApprovalFailure(err error, kind PendingApprovalFailure) bool {
	var target *pendingApprovalError
	return errors.As(err, &target) && target.kind == kind
}

func pendingApprovalFailure(kind PendingApprovalFailure) error {
	text := "pending approval recovery is unavailable"
	switch kind {
	case PendingApprovalNone:
		text = "no recoverable pending ordinary approval was found"
	case PendingApprovalIncomplete:
		text = "pending approval recovery requires a complete gap-free event replay"
	case PendingApprovalMalformed:
		text = "pending approval recovery found invalid event correlation"
	case PendingApprovalUnsupportedAsk:
		text = "this pending approval type is not supported by recovery"
	case PendingApprovalWatchUnsupported:
		text = "pending approval recovery requires durable event watch support"
	case PendingApprovalControlsUnsupported:
		text = "pending approval recovery controls are not supported"
	case PendingApprovalNotRecoverable:
		text = "pending approval is no longer recoverable because its current state changed"
	case PendingApprovalCorrelation:
		text = "pending approval recovery correlation changed"
	}
	return &pendingApprovalError{kind: kind, text: text}
}

func pendingApprovalControlFailure(err error) error {
	switch status.Code(err) {
	case codes.Aborted:
		return pendingApprovalFailure(PendingApprovalCorrelation)
	case codes.FailedPrecondition:
		return pendingApprovalFailure(PendingApprovalNotRecoverable)
	case codes.Unimplemented:
		return pendingApprovalFailure(PendingApprovalControlsUnsupported)
	default:
		return pendingApprovalFailure(PendingApprovalUnavailable)
	}
}

func pendingApprovalWatchFailure(err error) error {
	if status.Code(err) == codes.Unimplemented {
		return pendingApprovalFailure(PendingApprovalWatchUnsupported)
	}
	return pendingApprovalFailure(PendingApprovalUnavailable)
}

type pendingApprovalWatchStream interface {
	Recv() (*mecatlv1.WatchSessionEventsResponse, error)
}

// PendingApprovalEventKind is the narrow recovery lifecycle vocabulary.
type PendingApprovalEventKind string

// Pending approval event kinds describe the recovery lifecycle projection.
const (
	PendingApprovalEventBoundary  PendingApprovalEventKind = "boundary"
	PendingApprovalEventAsk       PendingApprovalEventKind = "ask"
	PendingApprovalEventResolved  PendingApprovalEventKind = "resolved"
	PendingApprovalEventRetracted PendingApprovalEventKind = "retracted"
	PendingApprovalEventTerminal  PendingApprovalEventKind = "terminal"
	PendingApprovalEventOther     PendingApprovalEventKind = "other"
)

// PendingApprovalEvent is one safe projection from the exact-run continuation.
type PendingApprovalEvent struct {
	Kind     PendingApprovalEventKind
	Approval *PendingApproval
	Message  tea.Msg
}

// PendingApprovalWatch owns the context of one durable subscription. Call Close
// on every path; Recv does not start a background goroutine.
type PendingApprovalWatch struct {
	stream    pendingApprovalWatchStream
	cancel    context.CancelFunc
	closeOnce sync.Once
	sessionID string
	runID     string
}

// Close cancels the in-flight Recv and releases the server-side follower.
func (w *PendingApprovalWatch) Close() {
	if w == nil {
		return
	}
	w.closeOnce.Do(w.cancel)
}

func (c *Client) openPendingApprovalWatch(ctx context.Context, sessionID, cursor string) (*PendingApprovalWatch, error) {
	if sessionID == "" {
		return nil, pendingApprovalFailure(PendingApprovalMalformed)
	}
	watchCtx, cancel := context.WithCancel(ctx)
	stream, err := c.svc.WatchSessionEvents(withSessionAffinity(watchCtx, sessionID), &mecatlv1.WatchSessionEventsRequest{
		SessionId: sessionID,
		Cursor:    cursor,
		// cursor was issued by an unfiltered discovery watch. Changing its wire
		// run_id scope would silently skip records; exact-run filtering is local.
	})
	if err != nil {
		cancel()
		return nil, pendingApprovalWatchFailure(err)
	}
	return &PendingApprovalWatch{stream: stream, cancel: cancel, sessionID: sessionID}, nil
}

// WatchPendingApprovalRun resumes from the discovery cursor before a verdict is
// sent and projects only the recovered run. The caller owns Close.
func (c *Client) WatchPendingApprovalRun(ctx context.Context, approval PendingApproval) (*PendingApprovalWatch, error) {
	if approval.SessionID == "" || approval.RunID == "" || approval.AskID == "" || approval.Cursor == "" {
		return nil, pendingApprovalFailure(PendingApprovalMalformed)
	}
	watch, err := c.openPendingApprovalWatch(ctx, approval.SessionID, approval.Cursor)
	if err != nil {
		return nil, err
	}
	watch.runID = approval.RunID
	return watch, nil
}

// Recv returns the next event for the exact recovered run. Events from other
// runs are consumed but never adopted. Gap and unknown watch phases fail closed.
func (w *PendingApprovalWatch) Recv() (PendingApprovalEvent, error) {
	for {
		frame, err := w.stream.Recv()
		if err != nil {
			w.Close()
			if errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled {
				return PendingApprovalEvent{}, context.Canceled
			}
			if errors.Is(err, io.EOF) {
				return PendingApprovalEvent{}, pendingApprovalFailure(PendingApprovalIncomplete)
			}
			return PendingApprovalEvent{}, pendingApprovalWatchFailure(err)
		}
		if frame.GetPhase() == "gap" || (frame.GetPhase() != "replay" && frame.GetPhase() != "live") {
			w.Close()
			return PendingApprovalEvent{}, pendingApprovalFailure(PendingApprovalIncomplete)
		}
		ev := frame.GetEvent()
		if ev == nil {
			if frame.GetPhase() != "live" || frame.GetCursor() == "" {
				w.Close()
				return PendingApprovalEvent{}, pendingApprovalFailure(PendingApprovalMalformed)
			}
			return PendingApprovalEvent{Kind: PendingApprovalEventBoundary}, nil
		}
		if frame.GetCursor() == "" {
			w.Close()
			return PendingApprovalEvent{}, pendingApprovalFailure(PendingApprovalMalformed)
		}
		if ev.GetRunId() == "" {
			if ev.GetType() == "session.title" && ev.GetTitle() != nil {
				continue
			}
			w.Close()
			return PendingApprovalEvent{}, pendingApprovalFailure(PendingApprovalMalformed)
		}
		if ev.GetRunId() != w.runID {
			continue
		}
		projected, err := projectPendingApprovalEvent(w.sessionID, frame.GetCursor(), ev)
		if err != nil {
			w.Close()
			return PendingApprovalEvent{}, err
		}
		return projected, nil
	}
}

func projectPendingApprovalEvent(sessionID, cursor string, ev *mecatlv1.Event) (PendingApprovalEvent, error) {
	out := PendingApprovalEvent{Kind: PendingApprovalEventOther}
	switch ev.GetType() {
	case "permission.ask":
		approval, supported, err := approvalFromEvent(sessionID, cursor, ev)
		if err != nil {
			return PendingApprovalEvent{}, err
		}
		if !supported {
			return PendingApprovalEvent{}, pendingApprovalFailure(PendingApprovalUnsupportedAsk)
		}
		out.Kind, out.Approval = PendingApprovalEventAsk, &approval
	case "approval":
		if !validApprovalEvent(ev) {
			return PendingApprovalEvent{}, pendingApprovalFailure(PendingApprovalMalformed)
		}
		out.Kind = PendingApprovalEventResolved
	case "permission.retract":
		if ev.GetRunId() == "" || ev.GetAsk() == nil || ev.GetAsk().GetAskId() == "" {
			return PendingApprovalEvent{}, pendingApprovalFailure(PendingApprovalMalformed)
		}
		out.Kind = PendingApprovalEventRetracted
	case "result":
		if ev.GetRunId() == "" || ev.GetResult() == nil || !validResultStop(ev.GetResult().GetStop()) {
			return PendingApprovalEvent{}, pendingApprovalFailure(PendingApprovalMalformed)
		}
		out.Kind = PendingApprovalEventTerminal
	}
	out.Message = EventToMsg(ev)
	return out, nil
}

func approvalFromEvent(sessionID, cursor string, ev *mecatlv1.Event) (PendingApproval, bool, error) {
	ask := ev.GetAsk()
	if ev.GetRunId() == "" || ask == nil || ask.GetAskId() == "" || ask.GetTool() == "" || !validToolArgs(ask.GetArgs()) {
		return PendingApproval{}, false, pendingApprovalFailure(PendingApprovalMalformed)
	}
	approval := PendingApproval{
		SessionID: sessionID,
		RunID:     ev.GetRunId(),
		AskID:     ask.GetAskId(),
		Tool:      ask.GetTool(),
		Args:      ask.GetArgs(),
		Reason:    ask.GetReason(),
		Cursor:    cursor,
	}
	return approval, ask.GetGuardrail() == nil && ask.GetTool() != "PresentPlan", nil
}

func validToolArgs(args string) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal([]byte(args), &object) == nil && object != nil
}

func validResultStop(stop string) bool {
	switch stop {
	case "end_turn", "max_turns", "max_tool_calls", "max_consecutive_failures", "cancelled", "error", "no_progress", "budget", "timeout", "structured_output", "plan_approved", "plan_iterate":
		return true
	default:
		return false
	}
}

func validApprovalEvent(ev *mecatlv1.Event) bool {
	if ev.GetRunId() == "" || ev.GetApproval() == nil || ev.GetApproval().GetAskId() == "" {
		return false
	}
	switch ev.GetApproval().GetVerdict() {
	case mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY,
		mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE,
		mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ALWAYS:
	default:
		return false
	}
	switch ev.GetApproval().GetOrigin() {
	case "permission", "hook_guardrail", "plan":
		return true
	default:
		return false
	}
}

// DiscoverPendingApproval drains one watch from the beginning through its exact
// replay/live boundary, closes it, and returns the sole unresolved ordinary ask.
func (c *Client) DiscoverPendingApproval(ctx context.Context, sessionID string) (PendingApproval, error) {
	watch, err := c.openPendingApprovalWatch(ctx, sessionID, "")
	if err != nil {
		return PendingApproval{}, err
	}
	defer watch.Close()

	pending := make(map[string]PendingApproval)
	unsupported := make(map[string]bool)
	for {
		frame, recvErr := watch.stream.Recv()
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				return PendingApproval{}, pendingApprovalFailure(PendingApprovalIncomplete)
			}
			return PendingApproval{}, pendingApprovalWatchFailure(recvErr)
		}
		phase := frame.GetPhase()
		if phase == "gap" || (phase != "replay" && phase != "live") {
			return PendingApproval{}, pendingApprovalFailure(PendingApprovalIncomplete)
		}
		ev := frame.GetEvent()
		if ev == nil {
			if phase != "live" {
				return PendingApproval{}, pendingApprovalFailure(PendingApprovalMalformed)
			}
			if frame.GetCursor() == "" {
				return PendingApproval{}, pendingApprovalFailure(PendingApprovalMalformed)
			}
			if len(pending) != 1 {
				return PendingApproval{}, pendingApprovalFailure(PendingApprovalNone)
			}
			for key, approval := range pending {
				if unsupported[key] {
					return PendingApproval{}, pendingApprovalFailure(PendingApprovalUnsupportedAsk)
				}
				approval.Cursor = frame.GetCursor()
				return approval, nil
			}
		}
		if phase != "replay" || frame.GetCursor() == "" {
			return PendingApproval{}, pendingApprovalFailure(PendingApprovalIncomplete)
		}
		if err := foldPendingApprovalEvent(sessionID, frame.GetCursor(), ev, pending, unsupported); err != nil {
			return PendingApproval{}, err
		}
	}
}

func pendingApprovalKey(runID, askID string) string {
	return runID + "\x00" + askID
}

func removePendingApproval(key string, pending map[string]PendingApproval, unsupported map[string]bool) error {
	if _, exists := pending[key]; !exists {
		return pendingApprovalFailure(PendingApprovalMalformed)
	}
	delete(pending, key)
	delete(unsupported, key)
	return nil
}

func clearPendingApprovalRun(runID string, pending map[string]PendingApproval, unsupported map[string]bool) {
	prefix := runID + "\x00"
	for key := range pending {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			delete(pending, key)
			delete(unsupported, key)
		}
	}
}

func hasPendingApprovalRun(runID string, pending map[string]PendingApproval) bool {
	prefix := runID + "\x00"
	for key := range pending {
		if runID != "" && len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func foldPendingApprovalEvent(sessionID, cursor string, ev *mecatlv1.Event, pending map[string]PendingApproval, unsupported map[string]bool) error {
	switch ev.GetType() {
	case "permission.ask":
		approval, supported, err := approvalFromEvent(sessionID, cursor, ev)
		if err != nil {
			return err
		}
		key := pendingApprovalKey(approval.RunID, approval.AskID)
		if _, exists := pending[key]; exists {
			return pendingApprovalFailure(PendingApprovalMalformed)
		}
		pending[key], unsupported[key] = approval, !supported
	case "approval":
		if !validApprovalEvent(ev) {
			return pendingApprovalFailure(PendingApprovalMalformed)
		}
		return removePendingApproval(pendingApprovalKey(ev.GetRunId(), ev.GetApproval().GetAskId()), pending, unsupported)
	case "permission.retract":
		if ev.GetRunId() == "" || ev.GetAsk() == nil || ev.GetAsk().GetAskId() == "" {
			return pendingApprovalFailure(PendingApprovalMalformed)
		}
		return removePendingApproval(pendingApprovalKey(ev.GetRunId(), ev.GetAsk().GetAskId()), pending, unsupported)
	case "result":
		if ev.GetRunId() == "" || ev.GetResult() == nil || !validResultStop(ev.GetResult().GetStop()) {
			return pendingApprovalFailure(PendingApprovalMalformed)
		}
		clearPendingApprovalRun(ev.GetRunId(), pending, unsupported)
	default:
		if EventToMsg(ev) == nil && hasPendingApprovalRun(ev.GetRunId(), pending) {
			return pendingApprovalFailure(PendingApprovalIncomplete)
		}
	}
	return nil
}

// ResolvePendingApproval submits only Allow Once or Deny and requires the wire
// acknowledgement to echo the exact run and ask.
func (c *Client) ResolvePendingApproval(ctx context.Context, approval PendingApproval, verdict Verdict) error {
	if approval.SessionID == "" || approval.RunID == "" || approval.AskID == "" || (verdict != VerdictAllowOnce && verdict != VerdictDeny) {
		return pendingApprovalFailure(PendingApprovalMalformed)
	}
	wireVerdict := mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE
	if verdict == VerdictDeny {
		wireVerdict = mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY
	}
	ack, err := c.svc.ResolveRunAsk(withSessionAffinity(ctx, approval.SessionID), &mecatlv1.ResolveRunAskRequest{
		SessionId: approval.SessionID, ExpectedRunId: approval.RunID, AskId: approval.AskID, Verdict: wireVerdict,
	})
	if err != nil {
		return pendingApprovalControlFailure(err)
	}
	if ack.GetRunId() != approval.RunID || ack.GetAskId() != approval.AskID {
		return pendingApprovalFailure(PendingApprovalCorrelation)
	}
	return nil
}

// CancelPendingRun explicitly cancels only the exact recovered run and checks
// the correlated acknowledgement.
func (c *Client) CancelPendingRun(ctx context.Context, approval PendingApproval) error {
	if approval.SessionID == "" || approval.RunID == "" {
		return pendingApprovalFailure(PendingApprovalMalformed)
	}
	ack, err := c.svc.CancelRun(withSessionAffinity(ctx, approval.SessionID), &mecatlv1.CancelRunRequest{SessionId: approval.SessionID, ExpectedRunId: approval.RunID})
	if err != nil {
		return pendingApprovalControlFailure(err)
	}
	if ack.GetRunId() != approval.RunID {
		return pendingApprovalFailure(PendingApprovalCorrelation)
	}
	return nil
}
