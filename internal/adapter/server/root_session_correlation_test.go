package server_test

import (
	"context"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/metadata"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type providerContextCapture struct {
	provider port.LLMProvider
	mu       sync.Mutex
	pairs    [][2]session.SessionID
}

func (p *providerContextCapture) Capabilities() port.ProviderCapabilities {
	return p.provider.Capabilities()
}

func (p *providerContextCapture) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	active, _ := port.SessionIDFromContext(ctx)
	root, _ := port.RootSessionIDFromContext(ctx)
	p.mu.Lock()
	p.pairs = append(p.pairs, [2]session.SessionID{active, root})
	p.mu.Unlock()
	return p.provider.Stream(ctx, req)
}

func (p *providerContextCapture) assertLast(t *testing.T, want session.SessionID) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.pairs) == 0 || p.pairs[len(p.pairs)-1] != [2]session.SessionID{want, want} {
		t.Fatalf("provider correlation = %v, want final authoritative pair [%s %s]", p.pairs, want, want)
	}
}

type teamRootCaptureProvider struct {
	mu    sync.Mutex
	pairs [][2]session.SessionID
}

func (*teamRootCaptureProvider) Capabilities() port.ProviderCapabilities {
	return port.ProviderCapabilities{}
}

func (p *teamRootCaptureProvider) Stream(ctx context.Context, _ port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	active, _ := port.SessionIDFromContext(ctx)
	root, _ := port.RootSessionIDFromContext(ctx)
	p.mu.Lock()
	p.pairs = append(p.pairs, [2]session.SessionID{active, root})
	p.mu.Unlock()
	return func(yield func(port.Chunk, error) bool) {
		yield(port.Chunk{Kind: port.ChunkText, Text: "done"}, nil)
		yield(port.Chunk{Kind: port.ChunkDone, Stop: session.StopEndTurn}, nil)
	}, nil
}

func TestRootSessionProviderCorrelation_Scenario1_IngressCannotChooseRoot(t *testing.T) {
	svc, provider, sess := rootCorrelationService(t)
	run, err := svc.StartRun(port.WithRootSessionID(context.Background(), "forged-caller-root"), sess.ID, "go")
	if err != nil {
		t.Fatal(err)
	}
	_ = drainServerRun(run)
	svc.FinishRun(sess.ID, run)

	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.pairs) != 1 || provider.pairs[0] != [2]session.SessionID{sess.ID, sess.ID} {
		t.Fatalf("service correlation = %v, want authoritative pair [%s %s]", provider.pairs, sess.ID, sess.ID)
	}
}

func TestRootSessionProviderCorrelation_Scenario1_IngressCannotChooseRootGRPC(t *testing.T) {
	svc, provider, sess := rootCorrelationService(t)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs(
		"x-mecatl-session-id", string(sess.ID),
		"x-mecatl-root-session-id", "forged-grpc-root",
	))
	stream, err := client.Converse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(sess.ID), Text: "go"}}}); err != nil {
		t.Fatal(err)
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := stream.Recv(); err != nil {
			if err != io.EOF {
				t.Fatal(err)
			}
			break
		}
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.pairs) != 1 || provider.pairs[0] != [2]session.SessionID{sess.ID, sess.ID} {
		t.Fatalf("gRPC ingress root affected execution identity: %v", provider.pairs)
	}
}

func TestADR_0360_RootHeaderIsCorrelationOnly(t *testing.T) {
	svc, provider, sess := rootCorrelationService(t)
	httpServer := httptest.NewServer(server.NewHTTPHandler(svc))
	defer httpServer.Close()
	req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/v1/sessions/"+string(sess.ID)+"/prompt", strings.NewReader(`{"text":"go"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Mecatl-Session-ID", string(sess.ID))
	req.Header.Set("X-Mecatl-Root-Session-ID", "forged-ingress-root")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	if copyErr != nil || closeErr != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP prompt status=%d copy=%v close=%v", resp.StatusCode, copyErr, closeErr)
	}

	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.pairs) != 1 || provider.pairs[0] != [2]session.SessionID{sess.ID, sess.ID} {
		t.Fatalf("ingress root affected execution identity: %v", provider.pairs)
	}
	loaded, err := svc.GetSession(context.Background(), sess.ID)
	if err != nil || loaded.ID != sess.ID {
		t.Fatalf("authoritative session changed: loaded=%v err=%v", loaded, err)
	}
}

func rootCorrelationService(t *testing.T) (*server.Service, *teamRootCaptureProvider, *session.Session) {
	t.Helper()
	ctx := context.Background()
	store := memstore.New()
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	env := tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), memledger.New(), nil)
	provider := &teamRootCaptureProvider{}
	engine := agent.NewEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Model: "test"})
	svc, err := server.NewService(server.Config{
		Engine: engine,
		Store:  store, PlacementProvider: delegationPlacementProvider{binding: server.PlacementBinding{Environment: env, Ref: ref}}, PlacementScope: "test-scope",
		SessionEngine: func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
			return server.SessionEngineResult{Engine: engine}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return svc, provider, sess
}

func TestRootSessionProviderCorrelation_Scenario1_DirectTeamRooting(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
	env := tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), memledger.New(), nil)
	placement := delegationPlacementProvider{binding: server.PlacementBinding{Environment: env, Ref: ref}}
	provider := &teamRootCaptureProvider{}
	memberEngine := func(*team.Team, agent.MemberSpec, string) agent.MemberBuild {
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Model: "test"})}
	}
	svc, err := server.NewService(server.Config{
		Engine: agent.NewEngine(agent.Deps{Catalog: tool.NewCatalog()}), Store: store,
		PlacementProvider: placement, PlacementScope: "test-scope", MemberEngine: memberEngine,
	})
	if err != nil {
		t.Fatal(err)
	}
	source := session.New("source-root", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	if err := store.Create(ctx, source); err != nil {
		t.Fatal(err)
	}
	members := []agent.MemberSpec{{Name: "lead", Lead: true}}
	derivedID, _, err := svc.CreateTeamForSession(ctx, source.ID, "derived", "goal", 0, members)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunTeam(port.WithRootSessionID(ctx, "forged"), derivedID, nil); err != nil {
		t.Fatal(err)
	}
	defaultID, _, err := svc.CreateTeamOnDefaultPlacement(ctx, "default", "goal", 0, members)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunTeam(port.WithRootSessionID(ctx, "forged"), defaultID, nil); err != nil {
		t.Fatal(err)
	}

	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.pairs) < 2 {
		t.Fatalf("provider calls = %v", provider.pairs)
	}
	wantRoots := map[session.SessionID]session.SessionID{
		agent.MemberSessionID(derivedID, "lead"): source.ID,
	}
	for active := range map[session.SessionID]struct{}{
		agent.MemberSessionID(defaultID, "lead"): {},
	} {
		wantRoots[active] = active
	}
	seen := make(map[session.SessionID]bool, len(wantRoots))
	for _, pair := range provider.pairs {
		wantRoot, tracked := wantRoots[pair[0]]
		if !tracked {
			continue
		}
		if pair[1] != wantRoot {
			t.Fatalf("member %s root = %s, want %s; all pairs %v", pair[0], pair[1], wantRoot, provider.pairs)
		}
		seen[pair[0]] = true
	}
	for member := range wantRoots {
		if !seen[member] {
			t.Errorf("member %s made no correlated provider call; all pairs %v", member, provider.pairs)
		}
	}
}
