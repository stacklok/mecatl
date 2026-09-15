package client

import (
	"context"
	"errors"
	"io"
	"sync"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// Recver is the minimal receive side of a Converse stream: exactly what the
// reader goroutine needs. The generated grpc.BidiStreamingClient satisfies it,
// and tests supply a scripted fake — so the whole event pipeline runs offline,
// with no gRPC and no network. (The send side is the Sender interface below.)
type Recver interface {
	Recv() (*mecatlv1.ConverseResponse, error)
}

// EventRecver is the minimal receive side of a server-streaming Event replay
// (StreamSessionEvents): it yields *mecatlv1.Event directly, with NO
// ConverseResponse envelope. The generated grpc.ServerStreamingClient[Event]
// satisfies it (its Recv returns *Event); tests supply a scripted fake. It is
// the Event-replay analogue of Recver (which wraps one extra ConverseResponse
// envelope for the bidi Converse stream).
type EventRecver interface {
	Recv() (*mecatlv1.Event, error)
}

// Sender is the send side of the Converse stream. Separated from Recver so the
// reader goroutine holds only what it reads and the ui-side send helpers hold
// only what they send. The generated bidi client satisfies both.
type Sender interface {
	Send(*mecatlv1.ConverseRequest) error
}

// Stream wraps one open Converse run: a receive side, a send side, and a mutex
// that serialises Sends. gRPC permits concurrent Send and Recv from different
// goroutines but NOT concurrent Sends; the reader goroutine only Recvs, while
// approve/cancel commands Send — so a single send-mutex is sufficient and keeps
// the control frames ordered.
type Stream struct {
	recv Recver
	send Sender

	mu sync.Mutex // serialises Send (Recv is single-goroutine in the reader)
	// resolvedApprovals is run-scoped transport correlation. It is intentionally
	// owned by the stream rather than the UI Model, so closing a dynamic approval
	// surface does not leave a Model approval-state tombstone merely to reject a
	// late duplicate event.
	resolvedApprovals map[string]struct{}
	bearerBacked      bool
}

// NewStream binds a receive and send side into a Stream. Pass the same
// grpc.BidiStreamingClient for both in production; pass a fake Recver (and a
// no-op or recording Sender) in tests.
func NewStream(recv Recver, send Sender) *Stream {
	return &Stream{recv: recv, send: send}
}

func newAuthenticatedStream(recv Recver, send Sender, bearerBacked bool) *Stream {
	return &Stream{recv: recv, send: send, bearerBacked: bearerBacked}
}

// MarkApprovalResolved records an ask id as resolved for this stream. It is
// called before the deferred send command runs, so a re-delivered ask cannot
// reopen a just-closed UI surface in that scheduling window.
func (s *Stream) MarkApprovalResolved(askID string) {
	if s == nil || askID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolvedApprovals == nil {
		s.resolvedApprovals = make(map[string]struct{})
	}
	s.resolvedApprovals[askID] = struct{}{}
}

// ApprovalResolved reports whether askID was already resolved on this stream.
func (s *Stream) ApprovalResolved(askID string) bool {
	if s == nil || askID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.resolvedApprovals[askID]
	return ok
}

// ReadLoop runs the receive loop on its OWN goroutine over the live Converse
// stream: it drains Recv and pushes translated tea.Msgs onto out, then closes
// out when the stream ends. It MUST run off the Bubble Tea update goroutine (it
// does no rendering and touches no model state) — glamour and the model are
// driven only from Update via the drained channel. A clean EOF yields
// StreamClosedMsg; any other error yields StreamErrMsg; both then close the
// channel so WaitForMsg stops re-arming. It delegates to readEventLoop (the
// shared translation path) after stripping the ConverseResponse envelope via
// GetEvent, so the live stream and the replay feed project identically.
//
// Every send selects on ctx.Done() as well as out, so the goroutine can never
// wedge if the ui drops the channel (e.g. endRun finalised the run and stopped
// draining). Pass the run's context — cancelling it unblocks and exits the
// reader. This makes the no-leak property structural, not just a reasoned
// invariant.
//
// Note ordering: the terminal "result" event arrives as a ResultMsg BEFORE the
// server closes the stream, so the ui finalises on ResultMsg and treats a later
// StreamClosedMsg as a no-op.
func (s *Stream) ReadLoop(ctx context.Context, out chan<- tea.Msg) {
	readEventLoop(ctx, func() (*mecatlv1.Event, error) {
		resp, err := s.recv.Recv()
		if err != nil {
			return nil, err
		}
		return resp.GetEvent(), nil
	}, out, s.bearerBacked, true)
}

// readEventLoop is the SINGLE translation path both the live Converse stream
// (via Stream.ReadLoop, which strips the ConverseResponse envelope) and the
// replay feed (via EventStream.ReadLoop) drain through: it pulls Events from
// recv, translates each via EventToMsg, pushes tea.Msgs onto out, then closes
// out when the stream ends. A clean EOF yields StreamClosedMsg; any other error
// yields StreamErrMsg; ctx cancellation unblocks a stuck send (no-leak) —
// projection equivalence: the SAME EventToMsg path, the SAME lifecycle msgs, so
// a replay and a live run project identically for the same event sequence.
func readEventLoop(ctx context.Context, recv func() (*mecatlv1.Event, error), out chan<- tea.Msg, bearerBacked, classifyAuth bool) {
	defer close(out)
	for {
		ev, err := recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				emit(ctx, out, StreamClosedMsg{})
				return
			}
			authReason := AuthReason("")
			classified := false
			if classifyAuth {
				authReason, classified = AuthFailure(err, bearerBacked)
			}
			emit(ctx, out, StreamErrMsg{Err: err, AuthReason: authReason, Transient: !classified && TransientStreamErr(err)})
			return
		}
		if m := EventToMsg(ev); m != nil {
			if !emit(ctx, out, m) {
				return // context cancelled — stop reading
			}
		}
	}
}

// emit pushes one msg onto out unless ctx is cancelled first; it reports whether
// the send succeeded. A cancelled context means the ui has torn the run down, so
// the reader stops rather than blocking on a no-longer-drained channel.
func emit(ctx context.Context, out chan<- tea.Msg, m tea.Msg) bool {
	select {
	case out <- m:
		return true
	case <-ctx.Done():
		return false
	}
}

// WaitForMsg is the canonical Bubble Tea fan-in command: it blocks on one msg
// from ch and returns it, so Update can re-arm it (return WaitForMsg(ch) again)
// to pull the next one. When ch is closed it returns StreamClosedMsg so the ui
// can tear down cleanly without a nil-msg storm.
func WaitForMsg(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		m, ok := <-ch
		if !ok {
			return StreamClosedMsg{}
		}
		return m
	}
}

// SendPrompt sends the mandatory first frame. It MUST be the first Send on a
// fresh stream (the server rejects a non-prompt first frame). parts carries the
// non-text media (image/audio) built by ExpandMentions; nil for a text-only
// prompt. The server enforces the cross-field "text or parts non-empty" rule and
// re-validates every part (session.ValidateMediaParts), so a media-only prompt
// (empty text, non-nil parts) is legal here.
func (s *Stream) SendPrompt(sessionID, text string, parts []*mecatlv1.Content) error {
	return s.sendFrame(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Prompt{
			Prompt: &mecatlv1.Prompt{SessionId: sessionID, Text: text, Parts: parts},
		},
	})
}

// SendRetryStart sends the mandatory first frame for a failed-step retry. It
// contains no prompt text: the server reuses persisted conversation/tool state while
// resolving live instruction and system-prompt sources for the new model attempt.
func (s *Stream) SendRetryStart(sessionID string) error {
	return s.sendFrame(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Retry{
			Retry: &mecatlv1.RetryStart{SessionId: sessionID},
		},
	})
}

// Verdict is the client-local three-way resolution of a permission.ask. It keeps
// the proto ApprovalVerdict enum out of the ui package (which never imports
// contracts/gen): the ui chooses a Verdict, SendApproval translates it. The zero
// value is VerdictAllowOnce (the safe, transient allow).
type Verdict int

const (
	// VerdictAllowOnce permits this single call only (no rule learned).
	VerdictAllowOnce Verdict = iota
	// VerdictAllowAlways permits this call AND learns a session-scoped rule so the
	// same exact command is not re-asked for the rest of the session.
	VerdictAllowAlways
	// VerdictDeny denies this call.
	VerdictDeny
)

// resumeApproval builds the shared wire payload used by Converse and both
// authorization-control request envelopes.
func resumeApproval(askID string, v Verdict) *mecatlv1.ResumeApproval {
	allow := v == VerdictAllowOnce || v == VerdictAllowAlways
	var verdict mecatlv1.ApprovalVerdict
	switch v {
	case VerdictAllowOnce:
		verdict = mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ONCE
	case VerdictAllowAlways:
		verdict = mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_ALLOW_ALWAYS
	case VerdictDeny:
		verdict = mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY
	}
	return &mecatlv1.ResumeApproval{AskId: askID, Allow: allow, Verdict: verdict}
}

func resumeApprovalForScope(askID string, v Verdict, scope *GuardrailApprovalScope) *mecatlv1.ResumeApproval {
	ra := resumeApproval(askID, v)
	if scope == nil {
		return ra
	}
	ra.ReviewId = scope.ReviewID
	switch scope.Kind {
	case "action":
		ra.GuardrailKind = mecatlv1.GuardrailApprovalKind_GUARDRAIL_APPROVAL_KIND_ACTION
	case "result_release":
		ra.GuardrailKind = mecatlv1.GuardrailApprovalKind_GUARDRAIL_APPROVAL_KIND_RESULT_RELEASE
	}
	return ra
}

// SendGuardrailApproval resolves a contextual ask while acknowledging its displayed purpose.
func (s *Stream) SendGuardrailApproval(askID string, v Verdict, scope *GuardrailApprovalScope) error {
	return s.sendFrame(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_ResumeApproval{ResumeApproval: resumeApprovalForScope(askID, v, scope)}})
}

// SendApproval resolves a paused permission.ask. The server prefers the
// three-way verdict and retains the legacy allow bool as fallback.
func (s *Stream) SendApproval(askID string, v Verdict) error {
	return s.sendFrame(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_ResumeApproval{
			ResumeApproval: resumeApproval(askID, v),
		},
	})
}

// SendCancel aborts the in-flight run; the loop ends with a result whose stop is
// "cancelled". The ui keeps the stream open until that terminal result arrives.
func (s *Stream) SendCancel() error {
	return s.sendFrame(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Cancel{Cancel: &mecatlv1.Cancel{}},
	})
}

// SendCancelChild cancels ONE child (a subagent) of the in-flight run, addressed
// by its child session id — the SubagentMsg ChildID, verbatim. The run itself
// keeps streaming; the child ends with a "cancelled by user" terminal and stays
// resumable. The server ignores an unknown/already-finished id (the
// finished-as-you-pressed race is benign).
func (s *Stream) SendCancelChild(childID string) error {
	return s.sendFrame(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_CancelChild{CancelChild: &mecatlv1.CancelChild{ChildId: childID}},
	})
}

// SendSteer sends a mid-run operator steer frame on the bidi Converse stream
// (steer-while-running, issue #512). The server routes its text and media to
// the live run's single-slot inbox; the AUTHORITATIVE outcome (accepted /
// appended / too_late+promoted) arrives on the SAME stream as a
// SteerOutcomeMsg — never assumed client-side, since the client cannot observe
// the exact drain moment across stream latency. The ui only calls this when
// Capabilities.Steer is true; a runtime-disabled server falls back to the
// client-side merge queue. messageID is the client-minted correlation key the
// server echoes verbatim on the ack and the drain echo; empty degrades to
// text-order matching.
func (s *Stream) SendSteer(text string, media MediaResult, messageID string) error {
	return s.sendFrame(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_Steer{Steer: &mecatlv1.Steer{Text: text, MessageId: messageID, Parts: media.Parts}},
	})
}

// SendSteerCancel retracts the run's PENDING (un-drained) steer, if any. The
// authoritative outcome (retracted / none_pending) arrives as a SteerOutcomeMsg.
// messageID echoes the id of the Steer frame it cancels (the ui only ever has
// ONE bundle outstanding, so the id is a scoping hint, not a selector).
func (s *Stream) SendSteerCancel(messageID string) error {
	return s.sendFrame(&mecatlv1.ConverseRequest{
		Kind: &mecatlv1.ConverseRequest_SteerCancel{SteerCancel: &mecatlv1.SteerCancel{MessageId: messageID}},
	})
}

// sendFrame serialises one Send under the mutex.
func (s *Stream) sendFrame(req *mecatlv1.ConverseRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.send.Send(req)
}
