package client

import (
	"context"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/contracts/sessionaffinity"
)

type pendingApprovalWireServer struct {
	mecatlv1.UnimplementedHarnessServiceServer
	mu              sync.Mutex
	frames          []*mecatlv1.WatchSessionEventsResponse
	watchErr        error
	watchReqs       []*mecatlv1.WatchSessionEventsRequest
	affinities      [][]string
	resolveReq      *mecatlv1.ResolveRunAskRequest
	resolveResp     *mecatlv1.ResolveRunAskResponse
	resolveAffinity []string
	cancelReq       *mecatlv1.CancelRunRequest
	cancelResp      *mecatlv1.CancelRunResponse
	cancelAffinity  []string
	watchDone       chan struct{}
}

func (s *pendingApprovalWireServer) WatchSessionEvents(req *mecatlv1.WatchSessionEventsRequest, stream grpc.ServerStreamingServer[mecatlv1.WatchSessionEventsResponse]) error {
	md, _ := metadata.FromIncomingContext(stream.Context())
	s.mu.Lock()
	s.watchReqs = append(s.watchReqs, req)
	s.affinities = append(s.affinities, append([]string(nil), md.Get(sessionaffinity.HeaderName)...))
	frames, watchErr, done := s.frames, s.watchErr, s.watchDone
	s.mu.Unlock()
	if watchErr != nil {
		return watchErr
	}
	for _, frame := range frames {
		if err := stream.Send(frame); err != nil {
			return err
		}
	}
	if done != nil {
		<-stream.Context().Done()
		close(done)
	}
	return nil
}

func (s *pendingApprovalWireServer) ResolveRunAsk(ctx context.Context, req *mecatlv1.ResolveRunAskRequest) (*mecatlv1.ResolveRunAskResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resolveReq = req
	s.resolveAffinity = append([]string(nil), md.Get(sessionaffinity.HeaderName)...)
	if s.resolveResp != nil {
		return s.resolveResp, nil
	}
	return &mecatlv1.ResolveRunAskResponse{RunId: req.GetExpectedRunId(), AskId: req.GetAskId()}, nil
}

func (s *pendingApprovalWireServer) CancelRun(ctx context.Context, req *mecatlv1.CancelRunRequest) (*mecatlv1.CancelRunResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelReq = req
	s.cancelAffinity = append([]string(nil), md.Get(sessionaffinity.HeaderName)...)
	if s.cancelResp != nil {
		return s.cancelResp, nil
	}
	return &mecatlv1.CancelRunResponse{RunId: req.GetExpectedRunId()}, nil
}

func newPendingApprovalWireClient(t *testing.T, service *pendingApprovalWireServer) *Client {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	t.Cleanup(func() { _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///pending-approval", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &Client{conn: conn, svc: mecatlv1.NewHarnessServiceClient(conn)}
}

func watchFrame(cursor, phase string, event *mecatlv1.Event) *mecatlv1.WatchSessionEventsResponse {
	return &mecatlv1.WatchSessionEventsResponse{Cursor: cursor, Phase: phase, Event: event}
}

func ordinaryAsk(runID, askID string) *mecatlv1.Event {
	return &mecatlv1.Event{Type: "permission.ask", RunId: runID, Ask: &mecatlv1.PermissionAsk{AskId: askID, Tool: "Shell", Args: `{"command":"go test ./..."}`, Reason: "protected"}}
}

func TestDiscoverPendingApprovalAcceptsOnlyTrailingOrdinaryAsk(t *testing.T) {
	service := &pendingApprovalWireServer{frames: []*mecatlv1.WatchSessionEventsResponse{
		watchFrame("c1", "replay", ordinaryAsk("old-run", "old-ask")),
		watchFrame("c2", "replay", &mecatlv1.Event{Type: "result", RunId: "old-run", Result: &mecatlv1.Result{Stop: "end_turn"}}),
		watchFrame("c3", "replay", ordinaryAsk("current-run", "current-ask")),
		watchFrame("c3", "live", nil),
	}}
	candidate, err := newPendingApprovalWireClient(t, service).DiscoverPendingApproval(t.Context(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	want := PendingApproval{SessionID: "session-1", RunID: "current-run", AskID: "current-ask", Tool: "Shell", Args: `{"command":"go test ./..."}`, Reason: "protected", Cursor: "c3"}
	if !reflect.DeepEqual(candidate, want) {
		t.Fatalf("candidate = %#v, want %#v", candidate, want)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if got := service.affinities; !reflect.DeepEqual(got, [][]string{{"session-1"}}) {
		t.Fatalf("watch affinity = %#v", got)
	}
}

func TestDiscoverPendingApprovalRefusesUnsafeReplay(t *testing.T) {
	tests := []struct {
		name   string
		frames []*mecatlv1.WatchSessionEventsResponse
		kind   PendingApprovalFailure
	}{
		{"gap", []*mecatlv1.WatchSessionEventsResponse{watchFrame("c1", "gap", nil)}, PendingApprovalIncomplete},
		{"unknown phase", []*mecatlv1.WatchSessionEventsResponse{watchFrame("c1", "future", ordinaryAsk("run", "ask"))}, PendingApprovalIncomplete},
		{"missing run id", []*mecatlv1.WatchSessionEventsResponse{watchFrame("c1", "replay", ordinaryAsk("", "ask"))}, PendingApprovalMalformed},
		{"missing ask id", []*mecatlv1.WatchSessionEventsResponse{watchFrame("c1", "replay", ordinaryAsk("run", ""))}, PendingApprovalMalformed},
		{"malformed args", []*mecatlv1.WatchSessionEventsResponse{watchFrame("c1", "replay", &mecatlv1.Event{Type: "permission.ask", RunId: "run", Ask: &mecatlv1.PermissionAsk{AskId: "ask", Tool: "Shell", Args: "{"}})}, PendingApprovalMalformed},
		{"unknown event after ask", []*mecatlv1.WatchSessionEventsResponse{watchFrame("c1", "replay", ordinaryAsk("run", "ask")), watchFrame("c2", "replay", &mecatlv1.Event{Type: "future.terminal", RunId: "run"})}, PendingApprovalIncomplete},
		{"plan ask", []*mecatlv1.WatchSessionEventsResponse{watchFrame("c1", "replay", &mecatlv1.Event{Type: "permission.ask", RunId: "run", Ask: &mecatlv1.PermissionAsk{AskId: "ask", Tool: "PresentPlan", Args: `{}`}}), watchFrame("c1", "live", nil)}, PendingApprovalUnsupportedAsk},
		{"scoped ask", []*mecatlv1.WatchSessionEventsResponse{watchFrame("c1", "replay", &mecatlv1.Event{Type: "permission.ask", RunId: "run", Ask: &mecatlv1.PermissionAsk{AskId: "ask", Tool: "Shell", Args: `{}`, Guardrail: &mecatlv1.GuardrailApprovalScope{}}}), watchFrame("c1", "live", nil)}, PendingApprovalUnsupportedAsk},
		{"resolved ask", []*mecatlv1.WatchSessionEventsResponse{watchFrame("c1", "replay", ordinaryAsk("run", "ask")), watchFrame("c2", "replay", &mecatlv1.Event{Type: "approval", RunId: "run", Approval: &mecatlv1.Approval{AskId: "ask", Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY, Origin: "permission"}}), watchFrame("c2", "live", nil)}, PendingApprovalNone},
		{"retracted ask", []*mecatlv1.WatchSessionEventsResponse{watchFrame("c1", "replay", ordinaryAsk("run", "ask")), watchFrame("c2", "replay", &mecatlv1.Event{Type: "permission.retract", RunId: "run", Ask: &mecatlv1.PermissionAsk{AskId: "ask"}}), watchFrame("c2", "live", nil)}, PendingApprovalNone},
		{"terminal run", []*mecatlv1.WatchSessionEventsResponse{watchFrame("c1", "replay", ordinaryAsk("run", "ask")), watchFrame("c2", "replay", &mecatlv1.Event{Type: "result", RunId: "run", Result: &mecatlv1.Result{Stop: "cancelled"}}), watchFrame("c2", "live", nil)}, PendingApprovalNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newPendingApprovalWireClient(t, &pendingApprovalWireServer{frames: tt.frames}).DiscoverPendingApproval(t.Context(), "session-1")
			if !IsPendingApprovalFailure(err, tt.kind) {
				t.Fatalf("error = %v, want kind %q", err, tt.kind)
			}
		})
	}
}

func TestDiscoverPendingApprovalRefusesUnsupportedWatchWithoutFallback(t *testing.T) {
	service := &pendingApprovalWireServer{watchErr: status.Error(codes.Unimplemented, "sensitive backend detail")}
	_, err := newPendingApprovalWireClient(t, service).DiscoverPendingApproval(t.Context(), "session-1")
	if !IsPendingApprovalFailure(err, PendingApprovalWatchUnsupported) || err.Error() != "pending approval recovery requires durable event watch support" {
		t.Fatalf("error = %q", err)
	}
}

func TestPendingApprovalContinuationUsesRetainedCursorAndCloses(t *testing.T) {
	done := make(chan struct{})
	service := &pendingApprovalWireServer{frames: []*mecatlv1.WatchSessionEventsResponse{watchFrame("c4", "live", ordinaryAsk("run-1", "ask-2"))}, watchDone: done}
	client := newPendingApprovalWireClient(t, service)
	watch, err := client.WatchPendingApprovalRun(t.Context(), PendingApproval{SessionID: "session-1", RunID: "run-1", Cursor: "c3"})
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close()
	event, err := watch.Recv()
	if err != nil || event.Kind != PendingApprovalEventAsk || event.Approval == nil || event.Approval.AskID != "ask-2" {
		t.Fatalf("event = %#v, err = %v", event, err)
	}
	watch.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closing the watch did not release its server read loop")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.watchReqs) != 1 || service.watchReqs[0].GetCursor() != "c3" || service.watchReqs[0].GetRunId() != "" {
		t.Fatalf("watch request = %#v; retained unfiltered cursor must keep its wire scope", service.watchReqs)
	}
}

func TestResolvePendingApprovalChecksAckAndPayload(t *testing.T) {
	service := &pendingApprovalWireServer{}
	client := newPendingApprovalWireClient(t, service)
	candidate := PendingApproval{SessionID: "session-1", RunID: "run-1", AskID: "ask-1"}
	if err := client.ResolvePendingApproval(t.Context(), candidate, VerdictDeny); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	req := service.resolveReq
	affinity := append([]string(nil), service.resolveAffinity...)
	service.resolveResp = &mecatlv1.ResolveRunAskResponse{RunId: "other-run", AskId: "ask-1"}
	service.mu.Unlock()
	if req.GetSessionId() != "session-1" || req.GetExpectedRunId() != "run-1" || req.GetAskId() != "ask-1" || req.GetVerdict() != mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY {
		t.Fatalf("resolve request = %#v", req)
	}
	if !reflect.DeepEqual(affinity, []string{"session-1"}) {
		t.Fatalf("resolve affinity = %#v", affinity)
	}
	if err := client.ResolvePendingApproval(t.Context(), candidate, VerdictAllowOnce); !IsPendingApprovalFailure(err, PendingApprovalCorrelation) {
		t.Fatalf("ack mismatch error = %v", err)
	}
}

func TestCancelPendingRunChecksAckPayloadAndAffinity(t *testing.T) {
	service := &pendingApprovalWireServer{}
	client := newPendingApprovalWireClient(t, service)
	candidate := PendingApproval{SessionID: "session-1", RunID: "run-1"}
	if err := client.CancelPendingRun(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	req := service.cancelReq
	affinity := append([]string(nil), service.cancelAffinity...)
	service.cancelResp = &mecatlv1.CancelRunResponse{RunId: "other-run"}
	service.mu.Unlock()
	if req.GetSessionId() != "session-1" || req.GetExpectedRunId() != "run-1" {
		t.Fatalf("cancel request = %#v", req)
	}
	if !reflect.DeepEqual(affinity, []string{"session-1"}) {
		t.Fatalf("cancel affinity = %#v", affinity)
	}
	if err := client.CancelPendingRun(t.Context(), candidate); !IsPendingApprovalFailure(err, PendingApprovalCorrelation) {
		t.Fatalf("ack mismatch error = %v", err)
	}
}
