package mcpbrokergrpc_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	b "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type sessionClientFixture struct {
	p.UnimplementedSessionServiceServer
	invoke       func(context.Context, *p.InvokeToolRequest) (*p.InvocationOutcome, error)
	check        *p.CheckAuthorizationResponse
	checkErr     error
	snapshot     *p.SessionSnapshot
	cancel       *p.CancelOutcome
	cancelledRef string
}

func (f *sessionClientFixture) InvokeTool(ctx context.Context, r *p.InvokeToolRequest) (*p.InvocationOutcome, error) {
	return f.invoke(ctx, r)
}
func (f *sessionClientFixture) ResumeTool(ctx context.Context, _ *p.ResumeToolRequest) (*p.InvocationOutcome, error) {
	return f.invoke(ctx, nil)
}
func (f *sessionClientFixture) CheckAuthorization(context.Context, *p.CheckAuthorizationRequest) (*p.CheckAuthorizationResponse, error) {
	return f.check, f.checkErr
}
func (f *sessionClientFixture) OpenSession(context.Context, *p.OpenSessionRequest) (*p.SessionSnapshot, error) {
	return f.snapshot, nil
}
func (f *sessionClientFixture) CancelAuthorization(_ context.Context, r *p.CancelAuthorizationRequest) (*p.CancelOutcome, error) {
	f.cancelledRef = r.AuthorizationRef
	return f.cancel, nil
}
func testSessionRef(n byte) string {
	raw := make([]byte, 32)
	raw[0] = n
	return base64.RawURLEncoding.EncodeToString(raw)
}
func fixtureSessionClient(t *testing.T, f *sessionClientFixture, deadline time.Duration) *mcpbrokergrpc.SessionClient {
	t.Helper()
	server := grpc.NewServer()
	p.RegisterSessionServiceServer(server, f)
	listener := bufconn.Listen(1 << 20)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	client, err := mcpbrokergrpc.NewSessionClient("passthrough:///session-client", deadline, deadline, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithDefaultServiceConfig(`{"methodConfig":[{"name":[{"service":"mecatl.broker.v1.SessionService"}],"retryPolicy":{"MaxAttempts":4,"InitialBackoff":"0.001s","MaxBackoff":"0.001s","BackoffMultiplier":1,"RetryableStatusCodes":["UNAVAILABLE"]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}
func TestSessionRemoteToolAuthorizationFollowupNeverLosesZeroDispatch(t *testing.T) {
	for _, mode := range []string{"ready", "refused", "malformed", "unavailable", "pending"} {
		t.Run(mode, func(t *testing.T) {
			ref, cat, connection, auth := testSessionRef(1), testSessionRef(2), testSessionRef(3), testSessionRef(4)
			var invokes atomic.Int32
			f := &sessionClientFixture{
				snapshot: &p.SessionSnapshot{Ref: ref, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour)), Catalogue: &p.Catalogue{Ref: cat, ConnectionRef: &connection, Tools: []*p.ToolDescriptor{{Name: "echo", Schema: []byte(`{}`)}}}},
				invoke: func(context.Context, *p.InvokeToolRequest) (*p.InvocationOutcome, error) {
					invokes.Add(1)
					return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_AuthorizationRequired{AuthorizationRequired: auth}}, nil
				},
			}
			switch mode {
			case "ready":
				f.check = &p.CheckAuthorizationResponse{Outcome: &p.CheckAuthorizationResponse_Ready{Ready: &emptypb.Empty{}}}
			case "refused":
				f.check = &p.CheckAuthorizationResponse{Outcome: &p.CheckAuthorizationResponse_NotDispatched{NotDispatched: &p.NonDispatch{Reason: p.FailureReason_FAILURE_REASON_INTERRUPTED}}}
			case "malformed":
				f.check = &p.CheckAuthorizationResponse{}
			case "unavailable":
				f.checkErr = status.Error(codes.Unavailable, "followup unavailable")
			case "pending":
				f.check = &p.CheckAuthorizationResponse{Outcome: &p.CheckAuthorizationResponse_AuthorizationRequired{AuthorizationRequired: &p.FlowRef{Ref: auth, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}}}
			}
			client := fixtureSessionClient(t, f, time.Second)
			snapshot, err := client.OpenSession(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			remote := snapshot.Catalogue.Tools()[0]
			_, err = remote.Execute(tool.WithBrokerInvocation(t.Context(), session.NewBrokerAttempt()), session.NewToolCall("same", "echo", []byte(`{}`)), tool.Environment{})
			var required *tool.BrokerAuthorizationRequired
			if mode == "pending" {
				if !errors.As(err, &required) || required.Authorization.ID != auth {
					t.Fatalf("lost pending authorization: %v", err)
				}
			} else if disposition := remote.(interface {
				BrokerInvocationDisposition(error) session.BrokerAttemptDisposition
			}).BrokerInvocationDisposition(err); disposition != session.BrokerAttemptNotDispatched || errors.Is(err, mcpbrokergrpc.ErrSessionOutcomeUnknown) {
				t.Fatalf("zero dispatch became %s: %v", disposition, err)
			}
			if invokes.Load() != 1 {
				t.Fatalf("implicit resend: %d", invokes.Load())
			}
		})
	}
}

func TestSessionClientConnectionReferenceValidation(t *testing.T) {
	valid := testSessionRef(3)
	empty, malformed := "", "bad"
	for _, tc := range []struct {
		name       string
		connection *string
		tools      bool
		valid      bool
	}{
		{"never-connected", nil, false, true},
		{"retained-disconnected", &valid, false, true},
		{"published", &valid, true, true},
		{"missing-published", nil, true, false},
		{"empty-present", &empty, false, false},
		{"malformed", &malformed, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cat := &p.Catalogue{Ref: testSessionRef(2), ConnectionRef: tc.connection}
			if tc.tools {
				cat.Tools = []*p.ToolDescriptor{{Name: "echo", Schema: []byte(`{}`)}}
			}
			fixture := &sessionClientFixture{snapshot: &p.SessionSnapshot{Ref: testSessionRef(1), ExpiresAt: timestamppb.New(time.Now().Add(time.Hour)), Catalogue: cat}}
			client := fixtureSessionClient(t, fixture, time.Second)
			snapshot, err := client.OpenSession(t.Context(), nil)
			if (err == nil) != tc.valid {
				t.Fatalf("validation: %+v %v", snapshot, err)
			}
			if err == nil && snapshot.Catalogue.Connection() != c.ConnectionRef(cat.GetConnectionRef()) {
				t.Fatal("connection projection lost")
			}
		})
	}
}

func TestSessionClientMalformedInvocationFailsClosed(t *testing.T) {
	unknown := &p.InvocationOutcome{Outcome: &p.InvocationOutcome_OutcomeUnknown{OutcomeUnknown: &emptypb.Empty{}}}
	unknown.ProtoReflect().SetUnknown([]byte{0x98, 0x06, 0x01})
	cases := map[string]*p.InvocationOutcome{
		"missing arm":         {},
		"bad auth ref":        {Outcome: &p.InvocationOutcome_AuthorizationRequired{AuthorizationRequired: "bad"}},
		"unknown reason":      {Outcome: &p.InvocationOutcome_NotDispatched{NotDispatched: &p.NonDispatch{Reason: 257}}},
		"wrong call":          {Outcome: &p.InvocationOutcome_Completed{Completed: &b.ToolResult{CallId: "other", Content: "not ours"}}},
		"invalid result kind": {Outcome: &p.InvocationOutcome_Completed{Completed: &b.ToolResult{CallId: "one", Parts: []*b.ResultPart{{BlockKind: "made_up", Text: "bad"}}}}},
		"oversized result":    {Outcome: &p.InvocationOutcome_Completed{Completed: &b.ToolResult{CallId: "one", Content: string(make([]byte, 256*1024))}}},
		"unknown wire fields": unknown,
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			f := &sessionClientFixture{invoke: func(context.Context, *p.InvokeToolRequest) (*p.InvocationOutcome, error) {
				calls.Add(1)
				return response, nil
			}}
			client := fixtureSessionClient(t, f, time.Second)
			out, err := client.InvokeTool(t.Context(), c.SessionRef(testSessionRef(1)), c.CatalogueRef(testSessionRef(2)), c.Call{ID: "one", Name: "echo", Arguments: []byte(`{}`)}, session.NewBrokerAttempt())
			if err == nil || out.Kind != c.InvocationOutcomeUnknown || calls.Load() != 1 {
				t.Fatalf("out=%#v err=%v calls=%d", out, err, calls.Load())
			}
		})
	}
}
func TestSessionClientNoRetry(t *testing.T) {
	for _, method := range []string{"invoke", "resume"} {
		for _, timeout := range []bool{false, true} {
			t.Run(method+"/"+map[bool]string{false: "unavailable", true: "timeout"}[timeout], func(t *testing.T) {
				var calls atomic.Int32
				f := &sessionClientFixture{invoke: func(ctx context.Context, _ *p.InvokeToolRequest) (*p.InvocationOutcome, error) {
					calls.Add(1)
					if timeout {
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return nil, status.Error(codes.Unavailable, "lost return")
				}}
				client := fixtureSessionClient(t, f, 100*time.Millisecond)
				var out c.InvocationOutcome
				var err error
				if method == "resume" {
					out, err = client.ResumeTool(t.Context(), c.SessionRef(testSessionRef(1)), c.AuthorizationRef(testSessionRef(3)), c.CatalogueRef(testSessionRef(2)), session.NewBrokerAttempt())
				} else {
					out, err = client.InvokeTool(t.Context(), c.SessionRef(testSessionRef(1)), c.CatalogueRef(testSessionRef(2)), c.Call{ID: "one", Name: "echo", Arguments: []byte(`{}`)}, session.NewBrokerAttempt())
				}
				if err == nil || out.Kind != c.InvocationOutcomeUnknown || calls.Load() != 1 {
					t.Fatalf("out=%#v err=%v calls=%d", out, err, calls.Load())
				}
			})
		}
	}
}
func TestSessionClientMalformedControlsAndReferences(t *testing.T) {
	ref, cat, auth := testSessionRef(1), testSessionRef(2), testSessionRef(3)
	f := &sessionClientFixture{snapshot: &p.SessionSnapshot{Ref: ref, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour)), Catalogue: &p.Catalogue{Ref: cat}}, cancel: &p.CancelOutcome{Outcome: p.CancelOutcome_CANCELLED}}
	client := fixtureSessionClient(t, f, time.Second)
	for _, response := range []*p.CheckAuthorizationResponse{
		{},
		{Outcome: &p.CheckAuthorizationResponse_AuthorizationRequired{AuthorizationRequired: &p.FlowRef{Ref: "bad", ExpiresAt: timestamppb.Now()}}},
		{Outcome: &p.CheckAuthorizationResponse_AuthorizationRequired{AuthorizationRequired: &p.FlowRef{Ref: ref, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}}},
		{Outcome: &p.CheckAuthorizationResponse_NotDispatched{NotDispatched: &p.NonDispatch{Reason: 257}}},
	} {
		f.check = response
		check, err := client.CheckAuthorization(t.Context(), c.SessionRef(ref), c.CatalogueRef(cat), nil, c.AuthorizationRef(auth), session.NewBrokerAttempt())
		if err == nil || check.Valid() {
			t.Fatalf("malformed check accepted: %#v %v", check, err)
		}
	}
	saved := c.SessionRef(auth)
	if _, err := client.OpenSession(t.Context(), &saved); err == nil {
		t.Fatal("reopen substituted reference")
	}
	f.snapshot.Ref = "bad"
	if _, err := client.OpenSession(t.Context(), nil); err == nil {
		t.Fatal("malformed ref accepted")
	}
	f.snapshot.Ref = ref
	f.snapshot.Catalogue.Tools = []*b.ToolDescriptor{{Name: "echo", Schema: []byte(`{}`)}, {Name: "echo", Schema: []byte(`{}`)}}
	if _, err := client.OpenSession(t.Context(), nil); err == nil {
		t.Fatal("duplicate descriptors accepted")
	}
	for _, value := range []p.CancelOutcome_Value{p.CancelOutcome_UNSPECIFIED, 257} {
		f.cancel = &p.CancelOutcome{Outcome: value}
		result, err := client.CancelAuthorization(t.Context(), c.SessionRef(ref), c.AuthorizationRef(auth), session.NewBrokerAttempt())
		if err == nil || result.Valid() {
			t.Fatalf("fabricated cancel: %v %v", result, err)
		}
	}
	f.cancel = &p.CancelOutcome{Outcome: p.CancelOutcome_ALREADY_RESOLVED}
	result, err := client.CancelAuthorization(t.Context(), c.SessionRef(ref), c.AuthorizationRef(auth), session.NewBrokerAttempt())
	if err != nil || result != c.AlreadyResolved || f.cancelledRef != auth {
		t.Fatalf("inexact cancel: %v %v %s", result, err, f.cancelledRef)
	}
}
func TestSessionClientValidToolErrorIsNotFabricatedSuccess(t *testing.T) {
	f := &sessionClientFixture{invoke: func(_ context.Context, r *p.InvokeToolRequest) (*p.InvocationOutcome, error) {
		return &p.InvocationOutcome{Outcome: &p.InvocationOutcome_Completed{Completed: &b.ToolResult{CallId: r.Call.Id, IsError: true, Content: "upstream error"}}}, nil
	}}
	client := fixtureSessionClient(t, f, time.Second)
	out, err := client.InvokeTool(t.Context(), c.SessionRef(testSessionRef(1)), c.CatalogueRef(testSessionRef(2)), c.Call{ID: session.ToolCallID("one"), Name: "echo", Arguments: []byte(`{}`)}, session.NewBrokerAttempt())
	if err != nil || out.Kind != c.InvocationCompleted || !out.Result.IsError {
		t.Fatalf("error result lost: %#v %v", out, err)
	}
}
