package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const testRevision = "v1-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type publicTemplateProvider struct {
	testPlacementProvider
	binds      atomic.Int32
	last       server.PlacementSelector
	catalogErr error
	items      []server.ExecutionTemplateInfo
}

func (p *publicTemplateProvider) Bind(ctx context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	p.binds.Add(1)
	p.last = req.Selector
	return p.testPlacementProvider.Bind(ctx, req)
}
func (p *publicTemplateProvider) ListExecutionTemplates(context.Context, *session.Principal) ([]server.ExecutionTemplateInfo, string, error) {
	if p.catalogErr != nil {
		return nil, "", p.catalogErr
	}
	if p.items != nil {
		return p.items, "revision", nil
	}
	return []server.ExecutionTemplateInfo{{ID: "safe", Revision: testRevision, DisplayToken: testRevision, Name: "Safe", Extensions: map[string]string{"team/key": "value"}}}, "revision", nil
}
func (*publicTemplateProvider) Reattach(ctx context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	return testPlacementProvider{}.Reattach(ctx, req)
}
func templateTestService(t *testing.T, provider server.PlacementProvider, allow func(*session.Principal, string, string) bool) *server.Service {
	t.Helper()
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
	svc, err := server.NewService(server.Config{Engine: engine, Store: memstore.New(), PlacementProvider: provider, PlacementScope: "test", OwnershipEnforced: true, ExecutionTemplateAllowed: allow, SessionEngine: profileRecordingFactory("ok", &atomic.Value{}, &atomic.Int32{})})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}
func templateOwner(subject string) context.Context {
	return session.WithPrincipal(context.Background(), &session.Principal{Issuer: "test", Subject: subject, GrantType: session.GrantTypeUser})
}
func templateProto(id, revision string) *mecatlv1.ExecutionSelection {
	return &mecatlv1.ExecutionSelection{Template: &mecatlv1.ExecutionTemplate{Id: id, Revision: revision}}
}

func TestExecutionSelectionRealGRPCRejectsConflicts(t *testing.T) {
	p := &publicTemplateProvider{}
	svc := templateTestService(t, p, func(*session.Principal, string, string) bool { return true })
	client, closeGRPC := dialGRPC(t, svc)
	defer closeGRPC()
	for _, selection := range []*mecatlv1.ExecutionSelection{{}, {None: &mecatlv1.ExecutionNone{}, Template: &mecatlv1.ExecutionTemplate{Id: "safe", Revision: testRevision}}, templateProto("safe", "")} {
		_, err := client.CreateSession(t.Context(), &mecatlv1.CreateSessionRequest{Execution: selection})
		if status.Code(err) != codes.InvalidArgument {
			t.Fatalf("selection=%v status=%v", selection, err)
		}
	}
	old := &mecatlv1.CreateSessionRequest{}
	old.ProtoReflect().SetUnknown(protowire.AppendString(protowire.AppendTag(nil, 6, protowire.BytesType), "no-fs"))
	if _, err := client.CreateSession(t.Context(), old); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("old wire profile=%v", err)
	}
	if p.binds.Load() != 0 {
		t.Fatal("invalid wire allocated placement")
	}
}

func TestExecutionSelectionHTTPAndGRPC(t *testing.T) {
	p := &publicTemplateProvider{}
	svc := templateTestService(t, p, func(owner *session.Principal, id, revision string) bool {
		return owner.Subject == "alice" && id == "safe" && revision == testRevision
	})
	h := server.NewHarnessServer(svc)
	owner := templateOwner("alice")
	catalog, err := h.ListExecutionTemplates(owner, &mecatlv1.ListExecutionTemplatesRequest{})
	if err != nil || len(catalog.GetItems()) != 1 || catalog.GetItems()[0].GetTemplate().GetId() != "safe" {
		t.Fatalf("catalog=%+v err=%v", catalog, err)
	}
	if rows, _, err := svc.ListExecutionTemplates(templateOwner("bob")); err != nil || len(rows) != 0 {
		t.Fatalf("denied catalog=%v err=%v", rows, err)
	}
	if _, err := h.ListExecutionTemplates(context.Background(), &mecatlv1.ListExecutionTemplatesRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous list=%v", err)
	}
	if _, err := h.CreateSession(templateOwner("bob"), &mecatlv1.CreateSessionRequest{Execution: templateProto("safe", testRevision)}); status.Code(err) != codes.NotFound || p.binds.Load() != 0 {
		t.Fatalf("unauthorized template bind=%v, count=%d", err, p.binds.Load())
	}
	if _, err := h.CreateSession(owner, &mecatlv1.CreateSessionRequest{Execution: templateProto("missing", testRevision)}); status.Code(err) != codes.NotFound || p.binds.Load() != 0 {
		t.Fatalf("unknown template bind=%v, count=%d", err, p.binds.Load())
	}
	created, err := h.CreateSession(owner, &mecatlv1.CreateSessionRequest{Execution: templateProto("safe", testRevision)})
	if err != nil || !p.last.IsTemplate() || p.last.ID != "safe" || created.GetSessionId() == "" {
		t.Fatalf("bind=%+v response=%+v err=%v", p.last, created, err)
	}
	persisted, err := svc.GetSession(owner, session.SessionID(created.GetSessionId()))
	if err != nil || persisted.ExecutionTemplateRevision != testRevision {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	beforeFork := p.binds.Load()
	forkID, err := svc.ForkSessionSuccessor(owner, server.ForkSuccessorRequest{Source: persisted.ID})
	if err != nil {
		t.Fatal(err)
	}
	fork, err := svc.GetSession(owner, forkID)
	if err != nil || fork.EnvironmentRef != persisted.EnvironmentRef || fork.ExecutionTemplateID != persisted.ExecutionTemplateID || fork.ExecutionTemplateRevision != testRevision || p.binds.Load() != beforeFork {
		t.Fatalf("fork=%+v err=%v new-binds=%d", fork, err, p.binds.Load()-beforeFork)
	}
	for _, body := range []string{`{"profile":"no-fs"}`, `{"execution":null}`, `{"execution":{},"execution":{"none":{}}}`, `{"execution":{}}`, `{"execution":{"none":{},"template":{"id":"safe","revision":"` + testRevision + `"}}}`, `{"execution":{"none":null}}`, `{"execution":{"template":{"id":"safe"}}}`, `{"execution":{"template":{"ID":"safe","revision":"` + testRevision + `"}}}`, `{"execution":{"template":{"id":"safe","revision":"` + testRevision + `","image":"evil"}}}`, `{"execution":{"template":{"id":"safe","revision":"` + testRevision + `"},"template":{"id":"safe","revision":"` + testRevision + `"}}}`} {
		req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewBufferString(body)).WithContext(owner)
		rec := httptest.NewRecorder()
		server.NewHTTPHandler(svc).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid body %s got %d: %s", body, rec.Code, rec.Body.String())
		}
	}
	for _, selector := range []*mecatlv1.ExecutionSelection{{}, {None: &mecatlv1.ExecutionNone{}, Template: &mecatlv1.ExecutionTemplate{Id: "safe", Revision: testRevision}}, templateProto("", testRevision), templateProto("safe", ""), templateProto("safe", "v1-"+strings.Repeat("Z", 64))} {
		if _, err := h.CreateSession(owner, &mecatlv1.CreateSessionRequest{Execution: selector}); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("invalid selector %+v: %v", selector, err)
		}
	}
	unknown := &mecatlv1.CreateSessionRequest{}
	unknown.ProtoReflect().SetUnknown(protowire.AppendString(protowire.AppendTag(nil, 6, protowire.BytesType), "no-fs"))
	if _, err := h.CreateSession(owner, unknown); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("old profile wire=%v", err)
	}
	none := &mecatlv1.CreateSessionRequest{Execution: &mecatlv1.ExecutionSelection{None: &mecatlv1.ExecutionNone{}}}
	before := p.binds.Load()
	noFS, err := h.CreateSession(owner, none)
	if err != nil || !p.last.IsNoFS() || p.binds.Load() != before+1 || noFS.GetSessionCapabilities().GetExecutionFiles() || noFS.GetSessionCapabilities().GetBuiltInShell() {
		t.Fatalf("no-fs=%+v selector=%+v err=%v", noFS, p.last, err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/execution-templates", nil).WithContext(owner)
	rec := httptest.NewRecorder()
	server.NewHTTPHandler(svc).ServeHTTP(rec, req)
	var inventory struct {
		Items []json.RawMessage `json:"items"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &inventory) != nil || len(inventory.Items) != 1 {
		t.Fatalf("http list=%d: %s", rec.Code, rec.Body.String())
	}
}

func TestExecutionCatalogHiddenMalformedMetadataDoesNotBlockAllowedRow(t *testing.T) {
	key := strings.Repeat("a", 238) + "/schema_version"
	p := &publicTemplateProvider{items: []server.ExecutionTemplateInfo{
		{ID: "hidden", Revision: testRevision, DisplayToken: testRevision, Extensions: map[string]string{key + "x": "invalid"}},
		{ID: "safe", Revision: testRevision, DisplayToken: testRevision, Extensions: map[string]string{key: "1"}},
	}}
	svc := templateTestService(t, p, func(_ *session.Principal, id, _ string) bool { return id == "safe" })
	rows, _, err := svc.ListExecutionTemplates(templateOwner("alice"))
	if err != nil || len(rows) != 1 || rows[0].Extensions[key] != "1" {
		t.Fatalf("filtered catalog = %+v, %v", rows, err)
	}
}

func TestExecutionSessionFilesWithoutShell(t *testing.T) {
	catalog := tool.NewCatalog()
	catalog.MustRegister(fstools.ReadTool{})
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: catalog, Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
	svc, err := newPlacementTestService(server.Config{Engine: eng, Store: memstore.New()})
	if err != nil {
		t.Fatal(err)
	}
	created, err := server.NewHarnessServer(svc).CreateSession(t.Context(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	caps := created.GetSessionCapabilities()
	if !caps.GetExecutionFiles() || caps.GetBuiltInShell() {
		t.Fatalf("filesystem without shell=%+v", caps)
	}
	got, err := server.NewHarnessServer(svc).GetSession(t.Context(), &mecatlv1.GetSessionRequest{SessionId: created.GetSessionId()})
	if err != nil || !got.GetSession().GetSessionCapabilities().GetExecutionFiles() || got.GetSession().GetSessionCapabilities().GetBuiltInShell() {
		t.Fatalf("get caps=%+v err=%v", got, err)
	}
}

func TestExecutionTemplateCatalogUnavailable(t *testing.T) {
	p := &publicTemplateProvider{catalogErr: server.ErrPlacementUnavailable}
	svc := templateTestService(t, p, func(*session.Principal, string, string) bool { return true })
	info := svc.CompatibilityInfo(templateOwner("alice"))
	if !info.GetCapabilities().GetExecutionTemplates() {
		t.Fatal("configured catalog not advertised")
	}
	if _, err := server.NewHarnessServer(svc).ListExecutionTemplates(templateOwner("alice"), &mecatlv1.ListExecutionTemplatesRequest{}); status.Code(err) != codes.Unavailable {
		t.Fatalf("unavailable catalog=%v", err)
	}
}

func TestExecutionTemplateCatalogDisabled(t *testing.T) {
	svc := templateTestService(t, testPlacementProvider{}, nil)
	info := svc.CompatibilityInfo(templateOwner("alice"))
	if info.GetCapabilities().GetExecutionTemplates() {
		t.Fatal("nil catalog advertised")
	}
	if !slices.Contains(info.GetFeatures(), server.FeatureExecutionTemplates) {
		t.Fatal("build feature missing on disabled deployment")
	}
	if _, err := server.NewHarnessServer(svc).ListExecutionTemplates(templateOwner("alice"), &mecatlv1.ListExecutionTemplatesRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("disabled list=%v", err)
	}
}
