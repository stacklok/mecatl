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
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
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
	return []server.ExecutionTemplateInfo{{ID: "safe", Revision: testRevision, DisplayToken: testRevision, Name: "Safe", DeclaredExecutionFiles: true, DeclaredBuiltInShell: false, Extensions: map[string]string{"team/key": "value"}}}, "revision", nil
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
	if !catalog.Items[0].GetDeclaredExecutionFiles() || catalog.Items[0].GetDeclaredBuiltInShell() {
		t.Fatalf("gRPC catalog lost declared affordances: %v", catalog.Items[0])
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
	if created.GetSessionCapabilities().GetExecutionFiles() || created.GetSessionCapabilities().GetBuiltInShell() {
		t.Fatal("declared catalog affordances widened the tool-less session's effective capabilities")
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
	if !bytes.Contains(inventory.Items[0], []byte(`"declared_execution_files":true`)) || !bytes.Contains(inventory.Items[0], []byte(`"declared_built_in_shell":false`)) {
		t.Fatalf("catalog omitted declared affordances: %s", inventory.Items[0])
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

type offlineReadPlacementProvider struct {
	testPlacementProvider
	reattachCalls atomic.Int32
	offline       atomic.Bool
}

func (p *offlineReadPlacementProvider) Reattach(ctx context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	p.reattachCalls.Add(1)
	if p.offline.Load() {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return p.testPlacementProvider.Reattach(ctx, req)
}

func TestNoneRunDoesNotBorrowSharedFileTools(t *testing.T) {
	sharedCatalog := tool.NewCatalog()
	sharedCatalog.MustRegister(fstools.ReadTool{})
	sharedCatalog.MustRegister(fstools.NewShellTool())
	shared := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("shared")), Catalog: sharedCatalog, Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
	var requests []port.LLMRequest
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { requests = append(requests, req) })},
		mockllm.ToolCallTurn(session.NewToolCall("r", "Read", []byte(`{"path":"secret"}`)), session.NewToolCall("s", tool.ShellToolName, []byte(`{"command":"echo forbidden"}`))), mockllm.TextTurn("done"))
	svc, err := newPlacementTestService(server.Config{Engine: shared, Store: memstore.New(),
		SessionEngine: func(_ context.Context, _ server.ProviderSelector, _ []mcp.ServerConfig, profile server.SessionProfile, _ string, _ session.PermissionMode) (server.SessionEngineResult, error) {
			if profile != server.ProfileNoFS {
				t.Fatalf("profile = %q", profile)
			}
			return server.SessionEngineResult{Engine: agent.NewEngine(agent.Deps{LLM: llm, Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	created, err := server.NewHarnessServer(svc).CreateSession(t.Context(), &mecatlv1.CreateSessionRequest{Execution: &mecatlv1.ExecutionSelection{None: &mecatlv1.ExecutionNone{}}})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetSessionCapabilities().GetExecutionFiles() || created.GetSessionCapabilities().GetBuiltInShell() {
		t.Fatalf("none caps = %+v", created.GetSessionCapabilities())
	}
	run, err := svc.StartRun(t.Context(), session.SessionID(created.GetSessionId()), "try both")
	if err != nil {
		t.Fatal(err)
	}
	var final string
	var rejected int
	for ev := range run.Events() {
		if ev.Type == session.EvResult && ev.Result != nil {
			final = ev.Result.Text
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "unknown tool") {
			rejected++
		}
	}
	svc.FinishRun(session.SessionID(created.GetSessionId()), run)
	if final != "done" {
		t.Fatalf("reply = %q", final)
	}
	if len(requests) == 0 {
		t.Fatal("no model request")
	}
	for _, req := range requests {
		for _, spec := range req.Tools {
			if spec.Name == "Read" || spec.Name == tool.ShellToolName {
				t.Fatalf("none advertised %q", spec.Name)
			}
		}
	}
	if rejected != 2 {
		t.Fatalf("unknown-tool rejections = %d, want 2", rejected)
	}
}

func TestBoundExecutionCapabilitiesRespectPersistedAuthority(t *testing.T) {
	catalog := tool.NewCatalog()
	catalog.MustRegister(fstools.ReadTool{})
	catalog.MustRegister(fstools.NewShellTool())
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: catalog, Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
	store := memstore.New()
	provider := &offlineReadPlacementProvider{testPlacementProvider: testPlacementProvider{root: "/ws"}}
	root := session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read", tool.ShellToolName}}, Provenance: "test"}
	build := func() *server.Service {
		t.Helper()
		svc, err := newPlacementTestService(server.Config{Engine: engine, Store: store, PlacementProvider: provider, PlacementScope: "test", RootAuthority: func(session.SessionKind) session.Authority { return root }})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	svc := build()
	created, err := server.NewHarnessServer(svc).CreateSession(t.Context(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetSessionCapabilities().GetExecutionFiles() || created.GetSessionCapabilities().GetBuiltInShell() {
		t.Fatalf("ungranted caps = %+v", created.GetSessionCapabilities())
	}
	svc.Close()
	provider.offline.Store(true)
	before := provider.reattachCalls.Load()
	fresh := build()
	defer fresh.Close()
	got, err := server.NewHarnessServer(fresh).GetSession(t.Context(), &mecatlv1.GetSessionRequest{SessionId: created.GetSessionId()})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetSession().GetSessionCapabilities().GetExecutionFiles() || got.GetSession().GetSessionCapabilities().GetBuiltInShell() {
		t.Fatalf("offline bounded caps = %+v", got)
	}
	if provider.reattachCalls.Load() != before {
		t.Fatal("offline GetSession reattached provider")
	}
}

func TestBoundExecutionCapabilitiesAcrossTransportsAndRestart(t *testing.T) {
	catalog := tool.NewCatalog()
	catalog.MustRegister(fstools.ReadTool{})
	catalog.MustRegister(fstools.NewShellTool())
	var modelRequest port.LLMRequest
	llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) { modelRequest = req })}, mockllm.TextTurn("ok"))
	engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: catalog, Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
	store := memstore.New()
	provider := &offlineReadPlacementProvider{testPlacementProvider: testPlacementProvider{root: "/ws"}}
	makeService := func() *server.Service {
		t.Helper()
		svc, err := newPlacementTestService(server.Config{Engine: engine, Store: store, PlacementProvider: provider, PlacementScope: "test", SessionEngine: profileRecordingFactory("ok", &atomic.Value{}, &atomic.Int32{})})
		if err != nil {
			t.Fatal(err)
		}
		return svc
	}
	check := func(label string, caps *mecatlv1.SessionCapabilities, files bool) {
		t.Helper()
		if caps.GetExecutionFiles() != files || caps.GetBuiltInShell() {
			t.Fatalf("%s: capabilities=%+v, want files=%v shell=false", label, caps, files)
		}
	}
	svc := makeService()
	h := server.NewHarnessServer(svc)
	created, err := h.CreateSession(t.Context(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetSessionId()
	check("create", created.GetSessionCapabilities(), true)
	get, err := h.GetSession(t.Context(), &mecatlv1.GetSessionRequest{SessionId: id})
	if err != nil {
		t.Fatal(err)
	}
	check("get", get.GetSession().GetSessionCapabilities(), true)
	mode, err := h.SetMode(t.Context(), &mecatlv1.SetModeRequest{SessionId: id, Mode: mecatlv1.PermissionMode_PERMISSION_MODE_PLAN})
	if err != nil {
		t.Fatal(err)
	}
	check("mode", mode.GetSession().GetSessionCapabilities(), true)
	rename, err := h.RenameSession(t.Context(), &mecatlv1.RenameSessionRequest{SessionId: id, Title: "named"})
	if err != nil {
		t.Fatal(err)
	}
	check("rename", rename.GetSession().GetSessionCapabilities(), true)
	checkHTTP := func(label, method, path, body string, files bool) {
		t.Helper()
		rec := httptest.NewRecorder()
		server.NewHTTPHandler(svc).ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		var detail struct {
			SessionCapabilities struct {
				ExecutionFiles bool `json:"execution_files"`
				BuiltInShell   bool `json:"built_in_shell"`
			} `json:"session_capabilities"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &detail) != nil || detail.SessionCapabilities.ExecutionFiles != files || detail.SessionCapabilities.BuiltInShell {
			t.Fatalf("%s: HTTP status=%d body=%s", label, rec.Code, rec.Body.String())
		}
	}
	checkHTTP("HTTP get", http.MethodGet, "/v1/sessions/"+id, "", true)
	checkHTTP("HTTP mode", http.MethodPost, "/v1/sessions/"+id+"/mode", `{"mode":"default"}`, true)
	checkHTTP("HTTP rename", http.MethodPost, "/v1/sessions/"+id+"/rename", `{"title":"HTTP named"}`, true)
	run, err := svc.StartRun(t.Context(), session.SessionID(id), "read a file")
	if err != nil {
		t.Fatal(err)
	}
	if got := drainServerRun(run); got != "ok" {
		t.Fatalf("run result=%q", got)
	}
	if !strings.Contains(modelRequest.System.VolatileSuffix, "NO shell") {
		t.Fatalf("shell-less instruction missing from model request")
	}
	readAvailable := false
	for _, spec := range modelRequest.Tools {
		if spec.Name == tool.ShellToolName {
			t.Fatal("nil runner advertised Shell to model")
		}
		readAvailable = readAvailable || spec.Name == "Read"
	}
	if !readAvailable {
		t.Fatal("filesystem binding did not advertise Read to model")
	}
	none, err := h.CreateSession(t.Context(), &mecatlv1.CreateSessionRequest{Execution: &mecatlv1.ExecutionSelection{None: &mecatlv1.ExecutionNone{}}})
	if err != nil {
		t.Fatal(err)
	}
	check("none create", none.GetSessionCapabilities(), false)
	createHTTP := httptest.NewRecorder()
	server.NewHTTPHandler(svc).ServeHTTP(createHTTP, httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(`{"execution":{"none":{}}}`)))
	var createdHTTP struct {
		SessionCapabilities struct {
			ExecutionFiles bool `json:"execution_files"`
			BuiltInShell   bool `json:"built_in_shell"`
		} `json:"session_capabilities"`
	}
	if createHTTP.Code != http.StatusCreated || json.Unmarshal(createHTTP.Body.Bytes(), &createdHTTP) != nil || createdHTTP.SessionCapabilities.ExecutionFiles || createdHTTP.SessionCapabilities.BuiltInShell {
		t.Fatalf("HTTP none create=%d %s", createHTTP.Code, createHTTP.Body.String())
	}
	svc.Close()
	provider.offline.Store(true)
	beforeAttach := provider.reattachCalls.Load()
	fresh := makeService()
	defer fresh.Close()
	svc = fresh
	checkHTTP("HTTP restart get", http.MethodGet, "/v1/sessions/"+id, "", true)
	checkHTTP("HTTP restart mode", http.MethodPost, "/v1/sessions/"+id+"/mode", `{"mode":"plan"}`, true)
	checkHTTP("HTTP restart rename", http.MethodPost, "/v1/sessions/"+id+"/rename", `{"title":"offline"}`, true)
	for _, tc := range []struct {
		id    string
		files bool
	}{{id, true}, {none.GetSessionId(), false}} {
		get, err := server.NewHarnessServer(fresh).GetSession(t.Context(), &mecatlv1.GetSessionRequest{SessionId: tc.id})
		if err != nil {
			t.Fatal(err)
		}
		check("restart get", get.GetSession().GetSessionCapabilities(), tc.files)
		if tc.files {
			mode, err := server.NewHarnessServer(fresh).SetMode(t.Context(), &mecatlv1.SetModeRequest{SessionId: tc.id, Mode: mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT})
			if err != nil {
				t.Fatal(err)
			}
			check("restart mode", mode.GetSession().GetSessionCapabilities(), true)
			rename, err := server.NewHarnessServer(fresh).RenameSession(t.Context(), &mecatlv1.RenameSessionRequest{SessionId: tc.id, Title: "offline grpc"})
			if err != nil {
				t.Fatal(err)
			}
			check("restart rename", rename.GetSession().GetSessionCapabilities(), true)
		}
	}
	if got := provider.reattachCalls.Load(); got != beforeAttach {
		t.Fatalf("offline reads and mutations called Reattach %d times", got-beforeAttach)
	}
	if run, err := fresh.StartRun(t.Context(), session.SessionID(id), "must not execute"); err == nil {
		if run != nil {
			fresh.FinishRun(session.SessionID(id), run)
		}
		t.Fatal("offline placement admitted a run")
	}
	if got := provider.reattachCalls.Load(); got == beforeAttach {
		t.Fatal("run did not check the live placement")
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
