package client

import (
	"context"
	"iter"
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
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// affinityCaptureProvider decorates the reference provider so the fixture can
// inspect the authoritative run context at the provider seam.
type affinityCaptureProvider struct {
	inner *mockllm.Provider
	mu    sync.Mutex
	ids   []session.SessionID
}

func (p *affinityCaptureProvider) Capabilities() port.ProviderCapabilities {
	return p.inner.Capabilities()
}

func (p *affinityCaptureProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	id, _ := port.SessionIDFromContext(ctx)
	p.mu.Lock()
	p.ids = append(p.ids, id)
	p.mu.Unlock()
	return p.inner.Stream(ctx, req)
}

func (p *affinityCaptureProvider) captured() []session.SessionID {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]session.SessionID(nil), p.ids...)
}

type affinityIngressRecorder struct {
	mu     sync.Mutex
	values [][]string
}

func (r *affinityIngressRecorder) intercept(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if info.FullMethod == mecatlv1.HarnessService_Converse_FullMethodName {
		md, _ := metadata.FromIncomingContext(stream.Context())
		r.mu.Lock()
		r.values = append(r.values, append([]string(nil), md.Get(sessionaffinity.HeaderName)...))
		r.mu.Unlock()
	}
	return handler(srv, stream)
}

func (r *affinityIngressRecorder) captured() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, len(r.values))
	for i := range r.values {
		out[i] = append([]string(nil), r.values[i]...)
	}
	return out
}

type modeledPlacementProvider struct{}

func (modeledPlacementProvider) Bind(context.Context, server.PlacementBindRequest) (server.PlacementBinding, error) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/modeled", Revision: "in-tree-v1"}
	return server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/modeled"), memledger.New(), nil)}, nil
}

func (modeledPlacementProvider) Reattach(_ context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	return server.PlacementBinding{Ref: req.Ref, Environment: tool.MustEnvironment(req.Ref, memfs.NewWorkspace("/modeled"), memledger.New(), nil)}, nil
}

type modeledAffinityFixture struct {
	official *Client
	raw      mecatlv1.HarnessServiceClient
	ingress  *affinityIngressRecorder
	provider *affinityCaptureProvider
	cleanup  func()
}

func newModeledAffinityFixture(t *testing.T) *modeledAffinityFixture {
	t.Helper()
	provider := &affinityCaptureProvider{inner: mockllm.New(
		mockllm.TextTurn("legal complete"),
		mockllm.TextTurn("headerless complete"),
	)}
	eng := agent.NewEngine(agent.Deps{
		LLM:     provider,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "modeled-provider",
	})
	store := memstore.New()
	svc, err := server.NewService(server.Config{
		Engine:            eng,
		Store:             store,
		PlacementProvider: modeledPlacementProvider{},
		PlacementScope:    "test",
		SharedEngineRoot:  "/modeled",
		Now:               func() time.Time { return time.Unix(0, 0) },
	})
	if err != nil {
		t.Fatalf("new modeled mecak8s service: %v", err)
	}

	listener := bufconn.Listen(1 << 20)
	ingress := &affinityIngressRecorder{}
	grpcServer := grpc.NewServer(grpc.StreamInterceptor(ingress.intercept))
	mecatlv1.RegisterHarnessServiceServer(grpcServer, server.NewHarnessServer(svc))
	go func() { _ = grpcServer.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///modeled-mecak8s",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial modeled transport: %v", err)
	}
	return &modeledAffinityFixture{
		official: &Client{conn: conn, svc: mecatlv1.NewHarnessServiceClient(conn)},
		raw:      mecatlv1.NewHarnessServiceClient(conn),
		ingress:  ingress,
		provider: provider,
		cleanup: func() {
			_ = conn.Close()
			grpcServer.Stop()
			_ = listener.Close()
		},
	}
}

func createModeledSession(t *testing.T, c *Client) string {
	t.Helper()
	id, _, _, err := c.CreateSession(context.Background(), mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, ModelSelection{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return id
}

func runOfficialPrompt(t *testing.T, c *Client, sessionID string, bound bool) {
	t.Helper()
	var (
		stream *Stream
		err    error
	)
	if bound {
		stream, err = c.OpenConverseForSession(context.Background(), sessionID)
	} else {
		stream, err = c.OpenConverse(context.Background())
	}
	if err != nil {
		t.Fatalf("open official converse: %v", err)
	}
	if err := stream.SendPrompt(sessionID, "modeled request", nil); err != nil {
		t.Fatalf("send official prompt: %v", err)
	}
	for {
		response, err := stream.recv.Recv()
		if err != nil {
			t.Fatalf("receive official response: %v", err)
		}
		if response.GetEvent().GetType() == string(session.EvResult) {
			return
		}
	}
}

func rejectedRawPrompt(ctx context.Context, t *testing.T, raw mecatlv1.HarnessServiceClient, sessionID string) error {
	t.Helper()
	stream, err := raw.Converse(ctx)
	if err != nil {
		return err
	}
	sendErr := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{
		Prompt: &mecatlv1.Prompt{SessionId: sessionID, Text: "must not run"},
	}})
	// Converse can reject affinity metadata before it reads the prompt. In that
	// case Send may observe only the closed stream; Recv carries the terminal
	// server status.
	_, recvErr := stream.Recv()
	if recvErr != nil {
		return recvErr
	}
	return sendErr
}

// TestSessionAffinityAndHandoff_Scenario7_ClientTransportProviderBytes is a
// modeled, offline transport proof: bufconn stands in for the gateway-to-mecak8s
// hop. It does not model EndpointSlice/Envoy behavior, and authentication remains
// an independent concern (this fixture intentionally installs no authenticator).
func TestSessionAffinityAndHandoff_Scenario7_ClientTransportProviderBytes(t *testing.T) {
	fixture := newModeledAffinityFixture(t)
	defer fixture.cleanup()

	legalID := createModeledSession(t, fixture.official)
	runOfficialPrompt(t, fixture.official, legalID, true)
	headerlessID := createModeledSession(t, fixture.official)
	runOfficialPrompt(t, fixture.official, headerlessID, false)

	wantIngress := [][]string{{legalID}, nil}
	if got := fixture.ingress.captured(); !reflect.DeepEqual(got, wantIngress) {
		t.Fatalf("mecak8s ingress session bytes = %#v, want exact legal then omitted %#v", got, wantIngress)
	}
	wantProviderIDs := []session.SessionID{session.SessionID(legalID), session.SessionID(headerlessID)}
	if got := fixture.provider.captured(); !reflect.DeepEqual(got, wantProviderIDs) {
		t.Fatalf("provider session bytes = %#v, want exact legal and headerless-compatible IDs %#v", got, wantProviderIDs)
	}

	badCases := []struct {
		name string
		ctx  func(string) context.Context
	}{
		{
			name: "duplicate",
			ctx: func(id string) context.Context {
				return metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
					sessionaffinity.HeaderName, id,
					sessionaffinity.HeaderName, id,
				))
			},
		},
		{
			name: "illegal",
			ctx: func(id string) context.Context {
				return metadata.NewOutgoingContext(context.Background(), metadata.Pairs(sessionaffinity.HeaderName, " "+id))
			},
		},
		{
			name: "mismatch",
			ctx: func(string) context.Context {
				return metadata.NewOutgoingContext(context.Background(), metadata.Pairs(sessionaffinity.HeaderName, "different-session"))
			},
		},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			id := createModeledSession(t, fixture.official)
			before := len(fixture.provider.captured())
			err := rejectedRawPrompt(tc.ctx(id), t, fixture.raw, id)
			if err == nil {
				t.Fatal("illegal affinity was accepted")
			}
			// gRPC may reject leading whitespace while encoding metadata, before the
			// server can return its ordinary InvalidArgument response. The other
			// cases prove server-side validation; this one only requires rejection.
			if tc.name != "illegal" && status.Code(err) != codes.InvalidArgument {
				t.Fatalf("status = %v (%v), want InvalidArgument", status.Code(err), err)
			}
			if after := len(fixture.provider.captured()); after != before {
				t.Fatalf("provider calls changed from %d to %d for rejected affinity", before, after)
			}
		})
	}
}
