package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type repairPlacementProvider struct {
	binding PlacementBinding
	err     error
	binds   int
}

func (p *repairPlacementProvider) Bind(context.Context, PlacementBindRequest) (PlacementBinding, error) {
	p.binds++
	return p.binding, p.err
}
func (p *repairPlacementProvider) Reattach(_ context.Context, req PlacementReattachRequest) (PlacementBinding, error) {
	if p.err != nil {
		return PlacementBinding{}, p.err
	}
	binding := p.binding
	binding.Ref = req.Ref
	binding.Environment = tool.MustEnvironment(req.Ref, memfs.NewWorkspace("/fresh"), nil)
	return binding, nil
}

func repairEngine() *agent.Engine {
	return agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog()})
}

func TestInvariant_placement_binder_required_for_service_construction(t *testing.T) {
	_, err := NewService(Config{Engine: repairEngine(), Store: memstore.New(), Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) }})
	if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), "PlacementProvider is required") {
		t.Fatalf("NewService without provider = %v, want required-provider config error", err)
	}
}

func TestInvariant_ordinary_placement_bindings_are_not_environment_overrides(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "placement", Revision: "v1"}
	provider := &repairPlacementProvider{binding: PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/bound"), nil)}}
	svc, err := NewService(Config{Engine: repairEngine(), Store: memstore.New(), PlacementProvider: provider, PlacementScope: "test", NewID: func() session.SessionID { return "created" }, Now: func() time.Time { return time.Unix(1, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSession(context.Background(), "", session.ModeDefault, session.Limits{}); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	got := len(svc.sessionEnvironments)
	svc.mu.Unlock()
	if got != 0 {
		t.Fatalf("ordinary environment overrides = %d, want 0", got)
	}
}

func TestInvariant_placement_errors_and_metadata_are_content_free(t *testing.T) {
	private := "/srv/private/tenant/repository"
	provider := &repairPlacementProvider{err: errors.New("dial " + private + ": unavailable")}
	binder, err := NewPlacementBinder(provider)
	if err != nil {
		t.Fatal(err)
	}
	_, err = binder.Bind(context.Background(), PlacementBindRequest{Selector: DefaultPlacement(), Scope: "test", Operation: PlacementOperationCreate})
	if !errors.Is(err, ErrPlacementUnavailable) || strings.Contains(err.Error(), private) {
		t.Fatalf("public placement error = %q, want stable content-free unavailable", err)
	}

	provider.err = fmt.Errorf("%w: denied /srv/private/hidden", ErrPlacementNotFound)
	_, err = binder.Bind(context.Background(), PlacementBindRequest{
		Selector: SelectWorktree("source", session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "private", Revision: "v1"}, "opaque-token"),
		Scope:    "test", Operation: PlacementOperationSuccessor,
	})
	if !errors.Is(err, ErrPlacementNotFound) || strings.Contains(err.Error(), "/srv/private") {
		t.Fatalf("provider-denied valid selector = %q, want content-free not-found", err)
	}

	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: private, Revision: "secret-revision"}
	meta := canonicalPlacementMetadata(PlacementBinding{Ref: ref, Metadata: PlacementMetadata{Label: private + "\n" + strings.Repeat("x", 300), Branch: "feature/safe", Revision: strings.Repeat("r", 400)}})
	if strings.Contains(meta.Label, private) || len([]rune(meta.Branch)) > maxPlacementNameRunes || len([]rune(meta.Revision)) > maxPlacementIdentityRunes {
		t.Fatalf("unsafe placement metadata escaped canonicalization: %+v", meta)
	}
}

type leaseLossPlacementProvider struct {
	binding PlacementBinding
	closed  *atomic.Int32
}

func (p leaseLossPlacementProvider) Bind(context.Context, PlacementBindRequest) (PlacementBinding, error) {
	return p.binding, nil
}
func (p leaseLossPlacementProvider) Reattach(ctx context.Context, req PlacementReattachRequest) (PlacementBinding, error) {
	<-ctx.Done()
	binding := p.binding
	binding.Ref = req.Ref
	binding.Environment = tool.MustEnvironment(req.Ref, memfs.NewWorkspace("/provisional"), nil)
	binding.Close = func() error { p.closed.Add(1); return nil }
	return binding, nil
}

type immediateLeaseLoss struct{}

func (immediateLeaseLoss) Acquire(context.Context, session.SessionID, string) (port.Lease, error) {
	return port.Lease{SessionID: "source", Owner: "test", Token: 1, Expiry: time.Now().Add(time.Hour)}, nil
}
func (immediateLeaseLoss) Renew(context.Context, port.Lease) (port.Lease, error) {
	return port.Lease{}, port.ErrLeaseHeld
}
func (immediateLeaseLoss) Release(context.Context, port.Lease) error { return nil }

func TestInvariant_successor_lease_loss_cleans_provisional_binding(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "placement", Revision: "v1"}
	closed := &atomic.Int32{}
	provider := leaseLossPlacementProvider{binding: PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/source"), nil)}, closed: closed}
	store := memstore.New()
	source := session.New("source", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	if err := store.Save(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(Config{Engine: repairEngine(), Store: store, PlacementProvider: provider, PlacementScope: "test", SessionLease: immediateLeaseLoss{}, LeaseOwner: "test", LeaseTTL: time.Hour, LeaseRenewInterval: time.Millisecond, NewID: func() session.SessionID { return "successor" }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ClearSessionSuccessor(context.Background(), source.ID, SuccessorPlacement{})
	if !errors.Is(err, ErrSessionLeasedElsewhere) || closed.Load() != 1 {
		t.Fatalf("lease-loss successor = %v, provisional closes = %d", err, closed.Load())
	}
	if _, loadErr := store.Load(context.Background(), "successor"); !errors.Is(loadErr, port.ErrSessionNotFound) {
		t.Fatalf("lease-loss successor persisted: %v", loadErr)
	}
}

func TestInvariant_successor_selector_presence_and_json_are_strict(t *testing.T) {
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{} {}`))
	var body successorBody
	if err := decodeOptionalStrictJSON(req, &body); err == nil {
		t.Fatal("second JSON document was accepted")
	}

	svc := &Service{}
	_, err := svc.createPlacedSuccessor(context.Background(), ForkSuccessorRequest{Source: "source", Placement: SuccessorPlacement{SelectorPresent: true}}, false)
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("present empty selector = %v, want InvalidArgument", err)
	}

	empty := ""
	harness := NewHarnessServer(svc)
	_, err = harness.ClearSession(context.Background(), &mecatlv1.ClearSessionRequest{SourceSessionId: "source", WorktreeSelector: &empty})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("gRPC present-empty selector = %v, want InvalidArgument", err)
	}

	httpReq := httptest.NewRequest(http.MethodPost, "/v1/sessions/source/clear", strings.NewReader(`{"worktree_selector":""}`))
	httpReq.Header.Set("Content-Type", "application/json")
	httpRec := httptest.NewRecorder()
	NewHTTPHandler(svc).ServeHTTP(httpRec, httpReq)
	if httpRec.Code != http.StatusBadRequest {
		t.Fatalf("HTTP present-empty selector status = %d, want 400", httpRec.Code)
	}
}
