package server_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func affinityContext(id string) context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs(port.SessionIDHeaderName, id))
}

func duplicateAffinityContext(first, second string) context.Context {
	return metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		port.SessionIDHeaderName, first,
		port.SessionIDHeaderName, second,
	))
}

func TestADR_0290_CreateSessionDerivedAffinity(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	tests := []struct {
		name string
		ctx  context.Context
		req  *mecatlv1.CreateSessionRequest
		want codes.Code
	}{
		{name: "source exact", ctx: affinityContext("source"), req: &mecatlv1.CreateSessionRequest{Workspace: "/ws", SourceSessionId: "source"}, want: codes.NotFound},
		{name: "debug exact", ctx: affinityContext("target"), req: &mecatlv1.CreateSessionRequest{Profile: "no-fs", DebugTargetSessionId: "target"}, want: codes.NotFound},
		{name: "source missing header compatibility", ctx: context.Background(), req: &mecatlv1.CreateSessionRequest{Workspace: "/ws", SourceSessionId: "source"}, want: codes.NotFound},
		{name: "no derived reference rejects header", ctx: affinityContext("source"), req: &mecatlv1.CreateSessionRequest{Workspace: "/ws"}, want: codes.InvalidArgument},
		{name: "source mismatch", ctx: affinityContext("other"), req: &mecatlv1.CreateSessionRequest{Workspace: "/ws", SourceSessionId: "source"}, want: codes.InvalidArgument},
		{name: "source duplicate", ctx: duplicateAffinityContext("source", "source"), req: &mecatlv1.CreateSessionRequest{Workspace: "/ws", SourceSessionId: "source"}, want: codes.InvalidArgument},
		{name: "ambiguous dual reference without header", ctx: context.Background(), req: &mecatlv1.CreateSessionRequest{Workspace: "/ws", SourceSessionId: "source", DebugTargetSessionId: "target"}, want: codes.InvalidArgument},
		{name: "ambiguous dual reference with header", ctx: affinityContext("source"), req: &mecatlv1.CreateSessionRequest{Workspace: "/ws", SourceSessionId: "source", DebugTargetSessionId: "target"}, want: codes.InvalidArgument},
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

func TestSessionAffinityAndHandoff_Scenario2_GRPCUnaryAndServerStreamMatrix(t *testing.T) {
	svc := newService(t, mockllm.New(), allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	const requestID = "request-session"
	ctx := affinityContext("other-session")
	bindings := &mecatlv1.AdoptionBindings{Workspace: "/ws", EnvironmentKind: "local", EnvironmentId: "/ws", ProviderId: "provider", ModelId: "model"}
	tests := []struct {
		name string
		call func() error
	}{
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
		{"ForkSession", func() error {
			_, err := client.ForkSession(ctx, &mecatlv1.ForkSessionRequest{SourceSessionId: requestID})
			return err
		}},
		{"PreflightSessionAdoption", func() error {
			_, err := client.PreflightSessionAdoption(ctx, &mecatlv1.PreflightSessionAdoptionRequest{SourceSessionId: requestID, Bindings: bindings})
			return err
		}},
		{"AdoptSession", func() error {
			_, err := client.AdoptSession(ctx, &mecatlv1.AdoptSessionRequest{SourceSessionId: requestID, IdempotencyKey: "key", Bindings: bindings})
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
		{"StreamSessionLive", func() error {
			stream, err := client.StreamSessionLive(ctx, &mecatlv1.StreamSessionLiveRequest{SessionId: requestID})
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
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, affinity := range []struct {
				name string
				ctx  context.Context
				want codes.Code
			}{
				{name: "missing", ctx: context.Background()},
				{name: "exact", ctx: affinityContext(requestID)},
				{name: "mismatch", ctx: affinityContext("other-session"), want: codes.InvalidArgument},
				{name: "duplicate", ctx: duplicateAffinityContext(requestID, requestID), want: codes.InvalidArgument},
			} {
				t.Run(affinity.name, func(t *testing.T) {
					callCtx, cancel := context.WithTimeout(affinity.ctx, 250*time.Millisecond)
					defer cancel()
					ctx = callCtx
					got := status.Code(tc.call())
					if affinity.want == codes.InvalidArgument {
						if got != affinity.want {
							t.Fatalf("code = %v, want %v", got, affinity.want)
						}
					} else if got == codes.InvalidArgument {
						t.Fatalf("compatible affinity was rejected: %v", got)
					}
				})
			}
		})
	}
}

func TestADR_0290_GRPCHeaderFailureIsNonDisclosing(t *testing.T) {
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
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(port.SessionIDHeaderName, illegal))
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
	created, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{Workspace: "/ws"})
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

func TestADR_0290_ConverseControlsStaySessionBound(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	svc := newService(t, llm, allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	first, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{Workspace: "/one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{Workspace: "/two"})
	if err != nil {
		t.Fatal(err)
	}

	for _, msg := range []protoreflect.Message{
		(&mecatlv1.ResumeApproval{}).ProtoReflect(),
		(&mecatlv1.Cancel{}).ProtoReflect(),
		(&mecatlv1.CancelChild{}).ProtoReflect(),
		(&mecatlv1.Steer{}).ProtoReflect(),
		(&mecatlv1.SteerCancel{}).ProtoReflect(),
	} {
		if msg.Descriptor().Fields().ByName("session_id") != nil {
			t.Fatalf("control %s unexpectedly carries session_id", msg.Descriptor().FullName())
		}
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
	controls := []*mecatlv1.ConverseRequest{
		{Kind: &mecatlv1.ConverseRequest_ResumeApproval{ResumeApproval: &mecatlv1.ResumeApproval{AskId: "unknown"}}},
		{Kind: &mecatlv1.ConverseRequest_CancelChild{CancelChild: &mecatlv1.CancelChild{ChildId: "unknown"}}},
		{Kind: &mecatlv1.ConverseRequest_Steer{Steer: &mecatlv1.Steer{Text: "continue"}}},
		{Kind: &mecatlv1.ConverseRequest_SteerCancel{SteerCancel: &mecatlv1.SteerCancel{MessageId: "unknown"}}},
		{Kind: &mecatlv1.ConverseRequest_Cancel{Cancel: &mecatlv1.Cancel{}}},
	}
	for _, control := range controls {
		if err := stream.Send(control); err != nil {
			t.Fatalf("send bound control %T: %v", control.GetKind(), err)
		}
	}
	// A later prompt cannot replace the identity established by the first frame.
	_ = stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: second.GetSessionId(), Text: "wrong session"}}})
	_ = stream.CloseSend()
	for {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}
	if llm.Calls() != 1 {
		t.Fatalf("provider calls = %d, want 1", llm.Calls())
	}
	got, err := client.GetSession(context.Background(), &mecatlv1.GetSessionRequest{SessionId: second.GetSessionId()})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetSession().GetState() != "idle" {
		t.Fatalf("second session state = %q, want idle", got.GetSession().GetState())
	}
}

func TestADR_0290_GRPCMissingHeaderCompatibility(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	svc := newService(t, llm, allowRules())
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	created, err := client.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{Workspace: "/ws"})
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
