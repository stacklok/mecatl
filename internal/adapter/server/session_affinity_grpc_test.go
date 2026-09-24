package server_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/contracts/sessionaffinity"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func affinityContext(id string) context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs(sessionaffinity.HeaderName, id))
}

func duplicateAffinityContext(first, second string) context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		sessionaffinity.HeaderName, first,
		sessionaffinity.HeaderName, second,
	))
}

func TestADR_0294_CreateSessionDerivedAffinity(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	tests := []struct {
		name string
		ctx  context.Context
		req  *mecatlv1.CreateSessionRequest
		want codes.Code
	}{
		{name: "debug exact", ctx: affinityContext("target"), req: &mecatlv1.CreateSessionRequest{Profile: "no-fs", DebugTargetSessionId: "target"}, want: codes.NotFound},
		{name: "debug missing header compatibility", ctx: context.Background(), req: &mecatlv1.CreateSessionRequest{Profile: "no-fs", DebugTargetSessionId: "target"}, want: codes.NotFound},
		{name: "no derived reference rejects header", ctx: affinityContext("target"), req: &mecatlv1.CreateSessionRequest{}, want: codes.InvalidArgument},
		{name: "debug mismatch", ctx: affinityContext("other"), req: &mecatlv1.CreateSessionRequest{Profile: "no-fs", DebugTargetSessionId: "target"}, want: codes.InvalidArgument},
		{name: "debug duplicate", ctx: duplicateAffinityContext("target", "target"), req: &mecatlv1.CreateSessionRequest{Profile: "no-fs", DebugTargetSessionId: "target"}, want: codes.InvalidArgument},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := client.CreateSession(tc.ctx, tc.req)
			if got := status.Code(err); got != tc.want {
				t.Fatalf("code = %v, want %v: %v", got, tc.want, err)
			}
		})
	}
}

func TestADR_0294_NewSessionBoundRPCsRequireAffinityClassification(t *testing.T) {
	want := map[protoreflect.Name]bool{
		"CreateSession": true, "GetSession": true, "GetSessionTranscript": true,
		"SetMode": true, "CloseSession": true, "RenameSession": true,
		"DeleteSession": true, "CompactSession": true, "ForkSession": true,
		"ClearSession": true, "ListCommands": true, "ListWorktrees": true, "StreamSessionEvents": true,
		"StreamSessionLive": true, "WatchSessionEvents": true, "ReflectSession": true,
		"ApprovePlan": true, "CreateTeam": true,
		"ResolveRunAsk": true, "ResolvePlanAsk": true, "CancelRun": true, "SteerRun": true, "CancelRunSteer": true,
		"GetMcpAuthorizationPresentation": true, "RecheckMcpAuthorization": true, "CancelMcpAuthorization": true,
		"ListSessionMcpConnectors": true, "ListGuardrailCoverage": true, "GetGuardrailReviewDetail": true,
		"ConnectWorkspaceServices": true, "RetryWorkspaceEnrollment": true, "CancelWorkspaceEnrollment": true,
		"RefreshMcpSources": true,
	}
	service := mecatlv1.File_mecatl_v1_harness_proto.Services().ByName("HarnessService")
	for i := range service.Methods().Len() {
		method := service.Methods().Get(i)
		fields := method.Input().Fields()
		sessionBound := fields.ByName("session_id") != nil || fields.ByName("source_session_id") != nil || fields.ByName("debug_target_session_id") != nil
		if sessionBound != want[method.Name()] {
			t.Errorf("RPC %s direct session binding = %t, classified = %t; update affinity validation and matrix", method.Name(), sessionBound, want[method.Name()])
		}
		delete(want, method.Name())
	}
	for name := range want {
		t.Errorf("stale affinity RPC classification %s", name)
	}
}

func TestSessionAffinityAndHandoff_Scenario2_GRPCUnaryAndServerStreamMatrix(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	const requestID = "request-session"
	ctx := affinityContext("other-session")
	tests := []struct {
		name string
		call func() error
	}{
		{"ListSessionMcpConnectors", func() error {
			_, err := client.ListSessionMcpConnectors(ctx, &mecatlv1.ListSessionMcpConnectorsRequest{SessionId: requestID})
			return err
		}},
		{"GetSession", func() error {
			_, err := client.GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: requestID})
			return err
		}},
		{"GetSessionTranscript", func() error {
			_, err := client.GetSessionTranscript(ctx, &mecatlv1.GetSessionTranscriptRequest{SessionId: requestID})
			return err
		}},
		{"SetMode", func() error {
			_, err := client.SetMode(ctx, &mecatlv1.SetModeRequest{SessionId: requestID})
			return err
		}},
		{"CloseSession", func() error {
			_, err := client.CloseSession(ctx, &mecatlv1.CloseSessionRequest{SessionId: requestID})
			return err
		}},
		{"RenameSession", func() error {
			_, err := client.RenameSession(ctx, &mecatlv1.RenameSessionRequest{SessionId: requestID, Title: "title"})
			return err
		}},
		{"DeleteSession", func() error {
			_, err := client.DeleteSession(ctx, &mecatlv1.DeleteSessionRequest{SessionId: requestID})
			return err
		}},
		{"CompactSession", func() error {
			_, err := client.CompactSession(ctx, &mecatlv1.CompactSessionRequest{SessionId: requestID})
			return err
		}},
		{"CreateTeam", func() error {
			_, err := client.CreateTeam(ctx, &mecatlv1.CreateTeamRequest{SessionId: requestID})
			return err
		}},
		{"ForkSession", func() error {
			_, err := client.ForkSession(ctx, &mecatlv1.ForkSessionRequest{SourceSessionId: requestID})
			return err
		}},
		{"ClearSession", func() error {
			_, err := client.ClearSession(ctx, &mecatlv1.ClearSessionRequest{SourceSessionId: requestID})
			return err
		}},
		{"ListCommands", func() error {
			_, err := client.ListCommands(ctx, &mecatlv1.ListCommandsRequest{SessionId: requestID})
			return err
		}},
		{"ListWorktrees", func() error {
			_, err := client.ListWorktrees(ctx, &mecatlv1.ListWorktreesRequest{SessionId: requestID})
			return err
		}},
		{"ReflectSession", func() error {
			_, err := client.ReflectSession(ctx, &mecatlv1.ReflectSessionRequest{SessionId: requestID})
			return err
		}},
		{"StreamSessionEvents", func() error {
			stream, err := client.StreamSessionEvents(ctx, &mecatlv1.StreamSessionEventsRequest{SessionId: requestID})
			if err != nil {
				return err
			}
			_, err = stream.Recv()
			return err
		}},
		{"WatchSessionEvents", func() error {
			stream, err := client.WatchSessionEvents(ctx, &mecatlv1.WatchSessionEventsRequest{SessionId: requestID})
			if err != nil {
				return err
			}
			_, err = stream.Recv()
			return err
		}},
		{"ApprovePlan", func() error {
			stream, err := client.ApprovePlan(ctx, &mecatlv1.ApprovePlanRequest{SessionId: requestID})
			if err != nil {
				return err
			}
			_, err = stream.Recv()
			return err
		}},
		{"ResolveRunAsk", func() error {
			_, err := client.ResolveRunAsk(ctx, &mecatlv1.ResolveRunAskRequest{
				SessionId: requestID, ExpectedRunId: "run", AskId: "ask",
				Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY,
			})
			return err
		}},
		{"ResolvePlanAsk", func() error {
			_, err := client.ResolvePlanAsk(ctx, &mecatlv1.ResolvePlanAskRequest{
				SessionId: requestID, ExpectedRunId: "run", AskId: "ask",
				Verdict: mecatlv1.ApprovalVerdict_APPROVAL_VERDICT_DENY,
			})
			return err
		}},
		{"CancelRun", func() error {
			_, err := client.CancelRun(ctx, &mecatlv1.CancelRunRequest{SessionId: requestID, ExpectedRunId: "run"})
			return err
		}},
		{"SteerRun", func() error {
			_, err := client.SteerRun(ctx, &mecatlv1.SteerRunRequest{SessionId: requestID, ExpectedRunId: "run", Text: "steer"})
			return err
		}},
		{"CancelRunSteer", func() error {
			_, err := client.CancelRunSteer(ctx, &mecatlv1.CancelRunSteerRequest{SessionId: requestID, ExpectedRunId: "run"})
			return err
		}},
		{"ConnectWorkspaceServices", func() error {
			_, err := client.ConnectWorkspaceServices(ctx, &mecatlv1.WorkspaceEnrollmentConnectRequest{SessionId: requestID})
			return err
		}},
		{"RetryWorkspaceEnrollment", func() error {
			_, err := client.RetryWorkspaceEnrollment(ctx, &mecatlv1.WorkspaceEnrollmentControlRequest{SessionId: requestID})
			return err
		}},
		{"CancelWorkspaceEnrollment", func() error {
			_, err := client.CancelWorkspaceEnrollment(ctx, &mecatlv1.WorkspaceEnrollmentControlRequest{SessionId: requestID})
			return err
		}},
		{"GetMcpAuthorizationPresentation", func() error {
			_, err := client.GetMcpAuthorizationPresentation(ctx, &mecatlv1.GetMcpAuthorizationPresentationRequest{SessionId: requestID})
			return err
		}},
		{"RecheckMcpAuthorization", func() error {
			// Bidi: the affinity header rides the stream's metadata and is
			// checked (twice — see grpc.go's two-phase pattern) independent of
			// Send/Recv timing, but the RPC status itself surfaces only on Recv.
			stream, err := client.RecheckMcpAuthorization(ctx)
			if err != nil {
				return err
			}
			_ = stream.Send(&mecatlv1.RecheckMcpAuthorizationRequest{SessionId: requestID, AuthorizationId: "authorization:1"})
			_, err = stream.Recv()
			return err
		}},
		{"CancelMcpAuthorization", func() error {
			stream, err := client.CancelMcpAuthorization(ctx)
			if err != nil {
				return err
			}
			_ = stream.Send(&mecatlv1.CancelMcpAuthorizationRequest{SessionId: requestID, AuthorizationId: "authorization:1"})
			_, err = stream.Recv()
			return err
		}},
	}
	var commonFailure string
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			call := func(base context.Context) (codes.Code, string) {
				callCtx, cancel := context.WithTimeout(base, 250*time.Millisecond)
				defer cancel()
				ctx = callCtx
				err := tc.call()
				return status.Code(err), status.Convert(err).Message()
			}

			baselineCode, _ := call(context.Background())
			exactCode, exactMessage := call(affinityContext(requestID))
			if exactCode != baselineCode {
				t.Fatalf("exact affinity outcome = (%v, %q), want headerless status %v", exactCode, exactMessage, baselineCode)
			}

			for name, invalidCtx := range map[string]context.Context{
				"mismatch":  affinityContext("other-session"),
				"duplicate": duplicateAffinityContext(requestID, requestID),
			} {
				t.Run(name, func(t *testing.T) {
					got, message := call(invalidCtx)
					if got != codes.InvalidArgument {
						t.Fatalf("code = %v, want InvalidArgument", got)
					}
					if commonFailure == "" {
						commonFailure = message
					} else if message != commonFailure {
						t.Fatalf("message = %q, want common pre-dispatch message %q", message, commonFailure)
					}
				})
			}
		})
	}
}

func TestSessionAffinityAndHandoff_Scenario2_StreamSessionLiveHeaderlessAndExactAffinity(t *testing.T) {
	svc := newLiveSubscriptionService(t)
	origin, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	client, cleanup := dialLiveSubscriptionGRPC(t, svc)
	defer cleanup()
	warmupLiveConn(t, client, origin.ID)

	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{name: "headerless", ctx: context.Background()},
		{name: "exact affinity", ctx: affinityContext(string(origin.ID))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(tc.ctx)
			stream, err := client.StreamSessionLive(ctx, &mecatlv1.StreamSessionLiveRequest{SessionId: string(origin.ID)})
			if err != nil {
				cancel()
				t.Fatalf("StreamSessionLive: %v", err)
			}

			var (
				mu  sync.Mutex
				evs []*mecatlv1.Event
				wg  sync.WaitGroup
			)
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					ev, recvErr := stream.Recv()
					if recvErr != nil {
						return
					}
					mu.Lock()
					evs = append(evs, ev)
					mu.Unlock()
				}
			}()
			t.Cleanup(func() {
				cancel()
				wg.Wait()
			})

			if !probeLiveSubscription(svc, origin.ID, &mu, &evs, 3*time.Second) {
				count, types := liveEventSummary(&mu, &evs)
				t.Fatalf("StreamSessionLive did not relay the public probe event within 3s; got %d events: %v", count, types)
			}
		})
	}
}

func TestSessionAffinityAndHandoff_Scenario2_StreamSessionLiveRejectsInvalidAffinity(t *testing.T) {
	svc := newLiveSubscriptionService(t)
	origin, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	client, cleanup := dialLiveSubscriptionGRPC(t, svc)
	defer cleanup()
	warmupLiveConn(t, client, origin.ID)

	for name, baseCtx := range map[string]context.Context{
		"mismatch":  affinityContext("other-session"),
		"duplicate": duplicateAffinityContext(string(origin.ID), string(origin.ID)),
	} {
		t.Run(name, func(t *testing.T) {
			// The deadline is a test-hang guard only: affinity validation must return
			// InvalidArgument before StreamSessionLive subscribes and can become idle.
			ctx, cancel := context.WithTimeout(baseCtx, 3*time.Second)
			defer cancel()
			stream, err := client.StreamSessionLive(ctx, &mecatlv1.StreamSessionLiveRequest{SessionId: string(origin.ID)})
			if err != nil {
				assertAffinityFailureIsNonDisclosing(t, err, string(origin.ID), "other-session")
				return
			}
			_, err = stream.Recv()
			assertAffinityFailureIsNonDisclosing(t, err, string(origin.ID), "other-session")
		})
	}
}

func TestADR_0294_GRPCHeaderFailureIsNonDisclosing(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	requestID := "request-secret"
	for name, ctx := range map[string]context.Context{
		"duplicate": duplicateAffinityContext(requestID, "metadata-secret"),
		"mismatch":  affinityContext("metadata-secret"),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := client.GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: requestID})
			assertAffinityFailureIsNonDisclosing(t, err, requestID, "metadata-secret")
		})
	}

	t.Run("illegal", func(t *testing.T) {
		const illegal = "metadata-secret\x7f"
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(sessionaffinity.HeaderName, illegal))
		_, err := server.NewHarnessServer(svc).GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: requestID})
		assertAffinityFailureIsNonDisclosing(t, err, requestID, illegal)
	})
}

func assertAffinityFailureIsNonDisclosing(t *testing.T, err error, values ...string) {
	t.Helper()
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
	}
	message := status.Convert(err).Message()
	for _, value := range values {
		if strings.Contains(message, value) {
			t.Fatalf("status disclosed an affinity value: %q", message)
		}
	}
}

func TestSessionAffinityAndHandoff_Scenario2_ConversePreStreamAndFirstFrame(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("must not run"))
	svc := newService(t, llm, allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	created, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("ambiguous metadata rejected before first frame", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(duplicateAffinityContext(created.GetSessionId(), "other-session"), 5*time.Second)
		defer cancel()
		stream, err := client.Converse(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = stream.Recv()
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
		}
	})

	for name, frame := range map[string]*mecatlv1.ConverseRequest{
		"prompt": {Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: created.GetSessionId(), Text: "go"}}},
		"retry":  {Kind: &mecatlv1.ConverseRequest_Retry{Retry: &mecatlv1.RetryStart{SessionId: created.GetSessionId()}}},
	} {
		t.Run(name+" mismatch", func(t *testing.T) {
			stream, err := client.Converse(affinityContext("different-session"))
			if err != nil {
				t.Fatal(err)
			}
			if err := stream.Send(frame); err != nil {
				t.Fatal(err)
			}
			_, err = stream.Recv()
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("code = %v, want InvalidArgument", status.Code(err))
			}
		})
	}
	if llm.Calls() != 0 {
		t.Fatalf("provider calls = %d, want 0", llm.Calls())
	}
	got, err := client.GetSession(context.Background(), &mecatlv1.GetSessionRequest{SessionId: created.GetSessionId()})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetSession().GetState() != "idle" {
		t.Fatalf("session state = %q, want idle", got.GetSession().GetState())
	}
}

func TestADR_0294_ConverseControlsStaySessionBound(t *testing.T) {
	// Reuse the full live wire fixtures so this acceptance pin proves each
	// control changes runtime state, rather than merely inspecting protobuf shape.
	for name, fixture := range map[string]func(*testing.T){
		"approval":     TestGRPCConverseApproveSurfacedChildAsk,
		"cancel":       TestGRPCConverseCancel,
		"child cancel": TestGRPCConverseCancelChild,
		"steer":        TestSteer_ConverseFrameRoundTrip,
		"steer cancel": TestSteer_ConverseCancelRetracts,
	} {
		t.Run(name, fixture)
	}

	llm := mockllm.New(mockllm.ChunksTurn(blockingChunks()...))
	svc := newService(t, llm, allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	first, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	secondBaseline, err := client.GetSession(context.Background(), &mecatlv1.GetSessionRequest{SessionId: second.GetSessionId()})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(affinityContext(first.GetSessionId()), 5*time.Second)
	defer cancel()
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: first.GetSessionId(), Text: "go"}}}); err != nil {
		t.Fatal(err)
	}
	for {
		resp, recvErr := stream.Recv()
		if recvErr != nil {
			t.Fatalf("receive established run: %v", recvErr)
		}
		if resp.GetEvent().GetType() == "message.delta" {
			break
		}
	}
	controls := []*mecatlv1.ConverseRequest{
		{}, // unset/future oneof frames remain ignorable for forward compatibility
		{Kind: &mecatlv1.ConverseRequest_ResumeApproval{ResumeApproval: &mecatlv1.ResumeApproval{AskId: "unknown"}}},
		{Kind: &mecatlv1.ConverseRequest_CancelChild{CancelChild: &mecatlv1.CancelChild{ChildId: "unknown"}}},
		{Kind: &mecatlv1.ConverseRequest_Steer{Steer: &mecatlv1.Steer{Text: "continue"}}},
		{Kind: &mecatlv1.ConverseRequest_SteerCancel{SteerCancel: &mecatlv1.SteerCancel{MessageId: "unknown"}}},
	}
	for _, control := range controls {
		if err := stream.Send(control); err != nil {
			t.Fatalf("send bound control %T: %v", control.GetKind(), err)
		}
	}
	// A later prompt cannot replace the identity established by the first frame.
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: second.GetSessionId(), Text: "wrong session"}}}); err != nil {
		t.Fatalf("send second-session prompt: %v", err)
	}
	_ = stream.CloseSend()
	var recvErr error
	for {
		if _, err := stream.Recv(); err != nil {
			recvErr = err
			break
		}
	}
	// The relay may complete before its control reader receives this frame. Either
	// normal completion or rejection is valid; neither may replace the established
	// session or start another run.
	if !errors.Is(recvErr, io.EOF) && status.Code(recvErr) != codes.InvalidArgument {
		t.Fatalf("second-session prompt result = %v, want EOF or InvalidArgument", recvErr)
	}
	if llm.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", llm.Calls())
	}
	got, err := client.GetSession(context.Background(), &mecatlv1.GetSessionRequest{SessionId: second.GetSessionId()})
	if err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(got, secondBaseline) {
		t.Fatalf("second session changed: got %+v, want baseline %+v", got, secondBaseline)
	}
}

func TestADR_0294_ConverseIgnoresUnsetFramesAndDoesNotRestart(t *testing.T) {
	llm := mockllm.New(mockllm.ChunksTurn(blockingChunks()...))
	svc := newService(t, llm, allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	created, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Converse(affinityContext(created.GetSessionId()))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: created.GetSessionId(), Text: "go"}}}); err != nil {
		t.Fatal(err)
	}
	for {
		resp, recvErr := stream.Recv()
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		if resp.GetEvent().GetType() == "message.delta" {
			break
		}
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{}); err != nil {
		t.Fatalf("send unset compatibility frame: %v", err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Retry{Retry: &mecatlv1.RetryStart{SessionId: created.GetSessionId()}}}); err != nil {
		t.Fatalf("send second retry: %v", err)
	}
	var recvErr error
	for {
		if _, recvErr = stream.Recv(); recvErr != nil {
			break
		}
	}
	// Converse may have completed before its control reader received the second
	// start frame. Either normal completion or rejection is valid; neither may
	// start another run.
	if !errors.Is(recvErr, io.EOF) && status.Code(recvErr) != codes.InvalidArgument {
		t.Fatalf("second retry result = %v, want EOF or InvalidArgument", recvErr)
	}
	if llm.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", llm.Calls())
	}
}

func TestADR_0294_GRPCMissingHeaderCompatibility(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	svc := newService(t, llm, allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	created, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetSession(context.Background(), &mecatlv1.GetSessionRequest{SessionId: created.GetSessionId()}); err != nil {
		t.Fatalf("headerless unary: %v", err)
	}
	stream, err := client.Converse(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: created.GetSessionId(), Text: "go"}}}); err != nil {
		t.Fatal(err)
	}
	_ = stream.CloseSend()
	for {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}
	if llm.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", llm.Calls())
	}
}
