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
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type repairPlacementProvider struct {
	binding    PlacementBinding
	err        error
	binds      int
	reattaches int
}

func (p *repairPlacementProvider) Bind(context.Context, PlacementBindRequest) (PlacementBinding, error) {
	p.binds++
	return p.binding, p.err
}
func (p *repairPlacementProvider) Reattach(_ context.Context, req PlacementReattachRequest) (PlacementBinding, error) {
	p.reattaches++
	if p.err != nil {
		return PlacementBinding{}, p.err
	}
	binding := p.binding
	binding.Ref = req.Ref
	binding.Environment = tool.MustEnvironment(req.Ref, memfs.NewWorkspace("/fresh"), memledger.New(), nil)
	return binding, nil
}

func repairEngine() *agent.Engine {
	return agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: tool.NewCatalog()})
}

func TestInvariant_placement_binder_required_for_service_construction(t *testing.T) {
	_, err := NewService(Config{Engine: repairEngine(), Store: memstore.New()})
	if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), "PlacementProvider is required") {
		t.Fatalf("NewService without provider = %v, want required-provider config error", err)
	}
}

func TestInvariant_ordinary_placement_bindings_are_not_environment_overrides(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "placement", Revision: "v1"}
	provider := &repairPlacementProvider{binding: PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/bound"), memledger.New(), nil)}}
	svc, err := NewService(Config{Engine: repairEngine(), Store: memstore.New(), PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/bound", NewID: func() session.SessionID { return "created" }, Now: func() time.Time { return time.Unix(1, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{}); err != nil {
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
	binding.Environment = tool.MustEnvironment(req.Ref, memfs.NewWorkspace("/provisional"), memledger.New(), nil)
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
	provider := leaseLossPlacementProvider{binding: PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/source"), memledger.New(), nil)}, closed: closed}
	store := memstore.New()
	source := session.New("source", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	if err := store.Save(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(Config{Engine: repairEngine(), Store: store, PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/provisional", SessionLease: immediateLeaseLoss{}, LeaseOwner: "test", LeaseTTL: time.Hour, LeaseRenewInterval: time.Millisecond, NewID: func() session.SessionID { return "successor" }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = svc.ClearSessionSuccessor(ctx, source.ID, SuccessorPlacement{})
	if !errors.Is(err, ErrSessionLeasedElsewhere) || closed.Load() != 1 {
		t.Fatalf("lease-loss successor = %v, provisional closes = %d", err, closed.Load())
	}
	if _, loadErr := store.Load(context.Background(), "successor"); !errors.Is(loadErr, port.ErrSessionNotFound) {
		t.Fatalf("lease-loss successor persisted: %v", loadErr)
	}
}

type repairDiagnostics struct{ text string }

func (d *repairDiagnostics) Log(_ context.Context, _ port.Level, msg string, args ...any) {
	d.text += fmt.Sprint(append([]any{msg}, args...)...)
}
func (d *repairDiagnostics) With(...any) port.Diagnostics { return d }

type errorCommandLister struct{ err error }

func (l errorCommandLister) List(context.Context) ([]prompt.Command, error) {
	return nil, l.err
}
func (errorCommandLister) Expand(_ context.Context, input string) (string, bool, error) {
	return input, false, nil
}
func (l errorCommandLister) Borrow(context.Context, session.SessionID, *session.Principal, string) (CommandSourceBinding, func(), error) {
	return l, func() {}, nil
}
func (errorCommandLister) Activate(context.Context, session.SessionID, *session.Principal, string) error {
	return nil
}
func (errorCommandLister) Retire(session.SessionID) {}

func TestCommandDiscoveryDoesNotReattachExecution(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "placement", Revision: "v1"}
	closed := &atomic.Int32{}
	provider := &repairPlacementProvider{binding: PlacementBinding{
		Ref:         ref,
		Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/bound"), memledger.New(), nil),
		Close:       func() error { closed.Add(1); return nil },
	}}
	store := memstore.New()
	source := session.New("source", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	if err := store.Save(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(Config{
		Engine: repairEngine(), Store: store, PlacementProvider: provider,
		PlacementScope: "test", SharedEngineRoot: "/bound", Commands: errorCommandLister{},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Service construction validates and releases the default binding. Count only
	// the request-scoped exact reattachment below.
	closed.Store(0)
	if _, err := svc.ListCommandsForSession(t.Context(), source.ID); err != nil {
		t.Fatal(err)
	}
	if got := closed.Load(); got != 0 {
		t.Fatalf("command discovery reattached execution and closed %d bindings", got)
	}
}

func TestInvariant_command_discovery_errors_are_content_free(t *testing.T) {
	private := "/srv/private/tenant/commands.yaml"
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "placement", Revision: "v1"}
	provider := &repairPlacementProvider{binding: PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/bound"), memledger.New(), nil)}}
	store := memstore.New()
	source := session.New("source", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	if err := store.Save(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	diag := &repairDiagnostics{}
	svc, err := NewService(Config{Engine: repairEngine(), Store: store, PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: "/bound", Commands: errorCommandLister{err: errors.New("read " + private + ": denied")}, Diagnostics: diag})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ListCommandsForSession(context.Background(), source.ID)
	if !errors.Is(err, ErrInternal) || strings.Contains(err.Error(), private) {
		t.Fatalf("public command discovery error = %q", err)
	}
	if !strings.Contains(diag.text, "operationlist commands") ||
		!strings.Contains(diag.text, "causeread [redacted] denied") ||
		strings.Contains(diag.text, private) {
		t.Fatalf("diagnostic was not detailed and sanitized: %q", diag.text)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/commands?session_id="+string(source.ID), nil)
	rec := httptest.NewRecorder()
	NewHTTPHandler(svc).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("HTTP command discovery fault status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, private) || strings.Contains(body, "denied") {
		t.Fatalf("HTTP command discovery fault disclosed backend cause: %q", body)
	}
}

type failingPlacementStore struct {
	*memstore.Store
	err error
}

func (s failingPlacementStore) Create(context.Context, *session.Session) error { return s.err }

func TestInvariant_placement_storage_errors_are_content_free(t *testing.T) {
	private := "/srv/private/tenant/sessions.db"
	diag := &repairDiagnostics{}
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "placement", Revision: "v1"}
	svc, err := NewService(Config{
		Engine: repairEngine(), Store: failingPlacementStore{Store: memstore.New(), err: errors.New("write " + private + ": denied")},
		PlacementProvider: &repairPlacementProvider{binding: PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/bound"), memledger.New(), nil)}}, PlacementScope: "test", SharedEngineRoot: "/bound", Diagnostics: diag,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if !errors.Is(err, ErrInternal) || strings.Contains(err.Error(), private) {
		t.Fatalf("public placement storage error = %q", err)
	}
	if !strings.Contains(diag.text, "[redacted]") || strings.Contains(diag.text, private) {
		t.Fatalf("placement storage diagnostic was not detailed and sanitized: %q", diag.text)
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
