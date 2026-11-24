package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
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

var (
	capBoth = []string{
		server.ExecutionCapabilityFilesystem,
		server.ExecutionCapabilityForegroundCommand,
	}
	capFiles = []string{server.ExecutionCapabilityFilesystem}
)

type capabilityProvider struct {
	mu         sync.Mutex
	ref        session.EnvironmentRef
	ws         *memfs.Workspace
	enforce    bool
	admitted   []string
	successor  []string
	refusal    string
	binds      int
	reattaches int
	requested  [][]string
	successors [][]string
}

func newCapabilityProvider() *capabilityProvider {
	return &capabilityProvider{
		ref:      session.EnvironmentRef{Kind: "kubernetes", ID: "env", Revision: "rev"},
		ws:       memfs.NewWorkspace("/workspace"),
		enforce:  true,
		admitted: capBoth,
	}
}

func (p *capabilityProvider) binding(admitted []string) server.PlacementBinding {
	runner := remotePermissionRunner{memfs.NewCommandRunner(), p.ws.Root()}
	env := tool.MustEnvironment(p.ref, p.ws, memledger.New(), runner)
	return server.PlacementBinding{
		Environment:          env,
		Ref:                  p.ref,
		GovernanceRoot:       "/workspace",
		AdmittedCapabilities: slices.Clone(admitted),
	}
}

func (p *capabilityProvider) Bind(
	_ context.Context,
	req server.PlacementBindRequest,
) (server.PlacementBinding, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.binds++
	p.requested = append(p.requested, req.Capabilities)
	if p.refusal != "" {
		return server.PlacementBinding{}, &server.ExecutionCapabilityRefusal{Reason: p.refusal}
	}
	return p.binding(p.admitted), nil
}

func (p *capabilityProvider) Reattach(
	context.Context,
	server.PlacementReattachRequest,
) (server.PlacementBinding, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reattaches++
	return p.binding(p.admitted), nil
}

func (p *capabilityProvider) ReserveSuccessor(
	_ context.Context,
	req server.PlacementSuccessorRequest,
) (server.PlacementBinding, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.successors = append(p.successors, req.Capabilities)
	admitted := p.admitted
	if p.successor != nil {
		admitted = p.successor
	}
	return p.binding(admitted), nil
}

func (p *capabilityProvider) Applies(ref session.EnvironmentRef) bool { return ref == p.ref }

func (p *capabilityProvider) AcquireRun(
	context.Context,
	server.ExecutionRunRequest,
) (server.ExecutionRunHandle, error) {
	return capabilityRun{env: p.binding(nil).Environment}, nil
}

func (*capabilityProvider) ListExecutionTemplates(
	context.Context,
	*session.Principal,
) ([]server.ExecutionTemplateInfo, string, error) {
	return []server.ExecutionTemplateInfo{{
		ID:           "safe",
		Revision:     testRevision,
		DisplayToken: testRevision,
		Name:         "Safe",
		Capabilities: capBoth,
	}}, "revision", nil
}

func (p *capabilityProvider) EnforcesExecutionCapabilities(server.PlacementSelector) bool {
	return p.enforce
}

type remotePermissionRunner struct {
	*memfs.CommandRunner
	root string
}

func (r remotePermissionRunner) BoundWorkspaceRoot() string { return r.root }

type capabilityRun struct{ env tool.Environment }

func (r capabilityRun) Environment() tool.Environment { return r.env }
func (capabilityRun) Renew(context.Context) error     { return nil }
func (capabilityRun) Release(context.Context) error   { return nil }

type runnerProbe struct{ runner *atomic.Bool }

func (runnerProbe) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "Probe", Schema: json.RawMessage(`{"type":"object"}`)}
}
func (runnerProbe) ReadOnly() bool { return true }
func (p runnerProbe) Execute(
	_ context.Context,
	in session.ToolCall,
	env tool.Environment,
) (session.ToolResult, error) {
	p.runner.Store(env.CommandRunner() != nil)
	return session.ToolResult{CallID: in.ID, Content: "probed"}, nil
}

// capabilityFactory registers Shell only when the context admits it, as the
// composition factory does.
type capabilityFactory struct {
	mu     sync.Mutex
	llm    port.LLMProvider
	seen   [][]string
	runner atomic.Bool
}

func (f *capabilityFactory) engine(names []string, ok bool) *agent.Engine {
	cat := tool.NewCatalog()
	cat.MustRegister(fstools.ReadTool{})
	cat.MustRegister(runnerProbe{runner: &f.runner})
	if !ok || slices.Contains(names, server.ExecutionCapabilityForegroundCommand) {
		cat.MustRegister(agent.NewShellTool())
	}
	return agent.NewEngine(agent.Deps{
		LLM:     f.llm,
		Catalog: cat,
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test",
	})
}

func (f *capabilityFactory) build(
	ctx context.Context,
	_ server.ProviderSelector,
	_ []mcp.ServerConfig,
	_ server.SessionProfile,
	_ string,
	mode session.PermissionMode,
) (server.SessionEngineResult, error) {
	names, ok := server.ExecutionCapabilitiesFromContext(ctx)
	f.mu.Lock()
	f.seen = append(f.seen, names)
	f.mu.Unlock()
	return server.SessionEngineResult{Engine: f.engine(names, ok), BuiltForMode: mode}, nil
}

func (f *capabilityFactory) last() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[len(f.seen)-1]
}

func capabilityService(
	t *testing.T,
	p *capabilityProvider,
	f *capabilityFactory,
	store port.SessionStore,
	edit func(*server.Config),
) *server.Service {
	t.Helper()
	if f.llm == nil {
		f.llm = mockllm.New(mockllm.TextTurn("ok"))
	}
	cfg := server.Config{
		Engine:                   f.engine(nil, false),
		Store:                    store,
		PlacementProvider:        p,
		PlacementScope:           "test",
		ExecutionAccess:          p,
		OwnershipEnforced:        true,
		ExecutionTemplateAllowed: func(*session.Principal, string, string) bool { return true },
		SessionEngine:            f.build,
		SharedEngineRoot:         "/shared",
	}
	if edit != nil {
		edit(&cfg)
	}
	svc, err := server.NewService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return svc
}

func ceilingAgent(eng *agent.Engine) server.AgentDefSessionEngineFactory {
	return func(
		_ context.Context,
		_ server.ProviderSelector,
		_ server.SessionProfile,
		mode session.PermissionMode,
		_ session.Limits,
		name string,
		_ []tool.Tool,
	) (server.SessionEngineResult, error) {
		read := governance.CapabilitySet{FileSystem: true, Tools: []string{"Read"}}
		return server.SessionEngineResult{
			AgentDefinitionName: name,
			Engine:              eng,
			BuiltForMode:        mode,
			Authority: session.Authority{
				Provenance:    "agent-def:test",
				CapabilitySet: read,
				Ceiling:       &read,
			},
		}, nil
	}
}

type capabilityRequest struct {
	capabilities []string
	debugTarget  string
	agent        string
}

func (r capabilityRequest) proto() *mecatlv1.CreateSessionRequest {
	selection := templateProto("safe", testRevision)
	selection.Template.Capabilities = r.capabilities
	return &mecatlv1.CreateSessionRequest{
		Execution:            selection,
		DebugTargetSessionId: r.debugTarget,
		AgentDefinitionName:  r.agent,
	}
}

func (r capabilityRequest) json() string {
	body := map[string]any{"execution": map[string]any{"template": map[string]any{
		"id": "safe", "revision": testRevision, "capabilities": r.capabilities,
	}}}
	if r.debugTarget != "" {
		body["debug_target_session_id"] = r.debugTarget
	}
	if r.agent != "" {
		body["agent_definition_name"] = r.agent
	}
	raw, _ := json.Marshal(body)
	return string(raw)
}

type capabilityOutcome struct {
	grpc      codes.Code
	http      int
	problem   string
	effective []string
}

var (
	capInvalid = capabilityOutcome{
		grpc:    codes.InvalidArgument,
		http:    400,
		problem: "invalid_argument",
	}
	capRefused = capabilityOutcome{
		grpc:    codes.FailedPrecondition,
		http:    412,
		problem: "failed_precondition",
	}
	capHonored = capabilityOutcome{http: 201, effective: capBoth}
)

func createOverGRPC(
	ctx context.Context,
	svc *server.Service,
	req capabilityRequest,
) (capabilityOutcome, string) {
	created, err := server.NewHarnessServer(svc).CreateSession(ctx, req.proto())
	st := status.Convert(err)
	effective := created.GetSessionCapabilities().GetExecution()
	return capabilityOutcome{grpc: st.Code(), effective: effective}, st.Message()
}

func createOverHTTP(
	ctx context.Context,
	svc *server.Service,
	req capabilityRequest,
) (capabilityOutcome, string) {
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/sessions", strings.NewReader(req.json()))
	rec := httptest.NewRecorder()
	server.NewHTTPHandler(svc).ServeHTTP(rec, httpReq.WithContext(ctx))
	var body struct {
		Code                string `json:"code"`
		SessionCapabilities struct {
			Execution []string `json:"execution"`
		} `json:"session_capabilities"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	out := capabilityOutcome{
		http:      rec.Code,
		problem:   body.Code,
		effective: body.SessionCapabilities.Execution,
	}
	return out, rec.Body.String()
}

func TestExecutionCapabilityRequestsAcrossTransports(t *testing.T) {
	both := capabilityRequest{capabilities: capBoth}
	cases := []struct {
		name     string
		request  capabilityRequest
		provider func(*capabilityProvider)
		config   func(*server.Config)
		want     capabilityOutcome
		message  string
		binds    int
	}{
		{
			name:    "unknown",
			request: capabilityRequest{capabilities: []string{"gpu"}},
			want:    capInvalid,
		},
		{
			name:    "duplicate",
			request: capabilityRequest{capabilities: []string{"filesystem", "filesystem"}},
			want:    capInvalid,
		},
		{
			name:    "file-only",
			request: capabilityRequest{capabilities: capFiles},
			want:    capRefused,
			message: "file-only",
		},
		{
			name:    "no-shell",
			request: both,
			config:  func(cfg *server.Config) { cfg.ExecutionShellDisabled = true },
			want:    capRefused,
			message: "disabled on this deployment",
		},
		{
			name:    "debug target",
			request: capabilityRequest{capabilities: capBoth, debugTarget: "target"},
			want:    capRefused,
			message: "debug sessions",
		},
		{
			name:     "placement cannot enforce",
			request:  both,
			provider: func(p *capabilityProvider) { p.enforce = false },
			want:     capRefused,
			message:  "cannot enforce",
		},
		{
			name:     "undeclared",
			request:  both,
			provider: func(p *capabilityProvider) { p.refusal = "template does not declare it" },
			want:     capRefused,
			message:  "template does not declare it",
			binds:    1,
		},
		{
			name:     "narrowed by provider",
			request:  both,
			provider: func(p *capabilityProvider) { p.admitted = capFiles },
			want:     capRefused,
			message:  "did not admit",
			binds:    1,
		},
		{
			name:    "agent definition ceiling",
			request: capabilityRequest{capabilities: capBoth, agent: "reviewer"},
			config: func(cfg *server.Config) {
				cfg.AgentDefSessionEngine = ceilingAgent(cfg.Engine)
			},
			want:    capRefused,
			message: "does not allow",
			binds:   1,
		},
		{name: "honored", request: both, want: capHonored, binds: 1},
	}
	owner := templateOwner("alice")
	type outcome = capabilityOutcome
	type create func(context.Context, *server.Service, capabilityRequest) (outcome, string)
	transports := map[string]create{"grpc": createOverGRPC, "http": createOverHTTP}
	for _, tc := range cases {
		for name, create := range transports {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				p := newCapabilityProvider()
				if tc.provider != nil {
					tc.provider(p)
				}
				svc := capabilityService(t, p, &capabilityFactory{}, memstore.New(), tc.config)
				got, message := create(owner, svc, tc.request)
				want := tc.want
				if name == "grpc" {
					want.http, want.problem = 0, ""
				} else {
					want.grpc = codes.OK
				}
				same := got.grpc == want.grpc && got.http == want.http &&
					got.problem == want.problem && slices.Equal(got.effective, want.effective)
				if !same || !strings.Contains(message, tc.message) || p.binds != tc.binds {
					t.Fatalf("got %+v %q after %d binds; want %+v", got, message, p.binds, want)
				}
				if want.effective != nil && !slices.Equal(p.requested[0], capBoth) {
					t.Fatalf("requested at bind = %v", p.requested)
				}
			})
		}
	}
}

func TestResumeUsesRecordedCapabilitiesWithoutReadmission(t *testing.T) {
	p := newCapabilityProvider()
	p.admitted = capFiles
	store := memstore.New()
	owner := templateOwner("alice")
	creator := &capabilityFactory{}
	creatorSvc := capabilityService(t, p, creator, store, nil)
	created, err := creatorSvc.CreateSession(owner, "", session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(creator.last(), capFiles) {
		t.Fatalf("create factory capabilities = %v", creator.last())
	}

	p.admitted = capBoth
	resumed := &capabilityFactory{llm: mockllm.New(
		mockllm.ToolCallTurn(session.ToolCall{
			ID:   "probe",
			Name: "Probe",
			Args: json.RawMessage(`{}`),
		}),
		mockllm.TextTurn("done"),
	)}
	resumed.runner.Store(true)
	svc := capabilityService(t, p, resumed, store, nil)
	run, err := svc.StartRun(owner, created.ID, "probe")
	if err != nil {
		t.Fatal(err)
	}
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
			run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
		}
	}
	svc.FinishRun(created.ID, run)
	if p.binds != 1 || p.reattaches == 0 {
		t.Fatalf("binds = %d, reattaches = %d; want reattach only", p.binds, p.reattaches)
	}
	if !slices.Equal(resumed.last(), capFiles) {
		t.Fatalf("resume factory capabilities = %v, want the recorded set", resumed.last())
	}
	if resumed.runner.Load() {
		t.Fatal("run environment kept a command runner without foreground-command")
	}
}

func TestSuccessorsRequestSourceCapabilities(t *testing.T) {
	p := newCapabilityProvider()
	f := &capabilityFactory{}
	owner := templateOwner("alice")
	svc := capabilityService(t, p, f, memstore.New(), nil)
	source, err := svc.CreateSession(owner, "", session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	fork, err := svc.ForkSessionSuccessor(owner, server.ForkSuccessorRequest{Source: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.successors[0], capBoth) || !slices.Equal(f.last(), capBoth) {
		t.Fatalf("fork requested %v, factory saw %v", p.successors, f.last())
	}
	forked, err := svc.GetSession(owner, fork)
	full := session.ExecutionCapabilities{Files: true, Shell: true}
	if err != nil || *forked.ExecutionCapabilities != full {
		t.Fatalf("fork = %+v, %v", forked, err)
	}

	p.successor = capFiles
	_, err = svc.ForkSessionSuccessor(owner, server.ForkSuccessorRequest{Source: source.ID})
	if !errors.Is(err, server.ErrExecutionCapabilityRefused) {
		t.Fatalf("fork with narrower admission = %v", err)
	}
	_, err = svc.ClearSessionSuccessor(owner, source.ID, server.SuccessorPlacement{})
	refused := errors.Is(err, server.ErrExecutionCapabilityRefused)
	if !refused || !slices.Equal(p.successors[2], capBoth) {
		t.Fatalf("clear with narrower admission = %v, requested %v", err, p.successors)
	}
}

func TestSuccessorsOfShellSourceRefusedAfterNoShell(t *testing.T) {
	p := newCapabilityProvider()
	f := &capabilityFactory{}
	owner := templateOwner("alice")
	store := memstore.New()
	source, err := capabilityService(t, p, f, store, nil).CreateSession(
		owner,
		"",
		session.Limits{},
	)
	if err != nil {
		t.Fatal(err)
	}
	noShell := capabilityService(t, p, f, store, func(cfg *server.Config) {
		cfg.ExecutionShellDisabled = true
	})
	_, forkErr := noShell.ForkSessionSuccessor(
		owner,
		server.ForkSuccessorRequest{Source: source.ID},
	)
	_, clearErr := noShell.ClearSessionSuccessor(owner, source.ID, server.SuccessorPlacement{})
	if !errors.Is(forkErr, server.ErrExecutionCapabilityRefused) ||
		!errors.Is(clearErr, server.ErrExecutionCapabilityRefused) {
		t.Fatalf("fork = %v, clear = %v", forkErr, clearErr)
	}
	kept, err := noShell.GetSession(owner, source.ID)
	if err != nil || len(p.successors) != 0 || kept.State != source.State {
		t.Fatalf("source = %+v, %v; reservations %v", kept, err, p.successors)
	}
}

func TestSuccessorsOfAgentBoundSourceIgnoreAuthority(t *testing.T) {
	p := newCapabilityProvider()
	owner := templateOwner("alice")
	svc := capabilityService(t, p, &capabilityFactory{}, memstore.New(), func(cfg *server.Config) {
		cfg.AgentDefSessionEngine = ceilingAgent(cfg.Engine)
	})
	source, err := svc.CreateSessionWithProfile(
		owner,
		"",
		session.Limits{},
		server.ProviderSelector{},
		server.ProfileDefault,
		server.WithAgentDefinitionName("reviewer"),
	)
	if err != nil {
		t.Fatal(err)
	}
	fork, forkErr := svc.ForkSessionSuccessor(
		owner,
		server.ForkSuccessorRequest{Source: source.ID},
	)
	cleared, clearErr := svc.ClearSessionSuccessor(owner, source.ID, server.SuccessorPlacement{})
	if forkErr != nil || clearErr != nil {
		t.Fatalf("fork = %v, clear = %v", forkErr, clearErr)
	}
	for _, id := range []session.SessionID{fork, cleared} {
		got, err := svc.GetSession(owner, id)
		if err != nil || *got.ExecutionCapabilities != *source.ExecutionCapabilities {
			t.Fatalf("successor %s = %+v, %v", id, got, err)
		}
	}
}

func TestScheduleFiresRequestSourceCapabilities(t *testing.T) {
	p := newCapabilityProvider()
	f := &capabilityFactory{}
	owner := templateOwner("alice")
	store := memstore.New()
	source, err := capabilityService(t, p, f, store, nil).CreateSession(
		owner,
		"",
		session.Limits{},
	)
	if err != nil {
		t.Fatal(err)
	}
	spec := port.ScheduleSpec{
		EnvironmentRef:  source.EnvironmentRef,
		PlacementScope:  "test",
		OriginSessionID: source.ID,
		Owner:           session.PrincipalFromContext(owner),
	}
	noShell := capabilityService(t, p, f, store, func(cfg *server.Config) {
		cfg.ExecutionShellDisabled = true
	})
	_, err = noShell.ScheduleFirePlacement(owner, spec, "sched--no-shell")
	if !errors.Is(err, server.ErrExecutionCapabilityRefused) || len(p.successors) != 0 {
		t.Fatalf("fire under --no-shell = %v, reservations %v", err, p.successors)
	}

	source.ExecutionCapabilities.Shell = false
	if err := store.Save(owner, source); err != nil {
		t.Fatal(err)
	}
	fresh := capabilityService(t, p, f, store, nil)
	binding, err := fresh.ScheduleFirePlacement(owner, spec, "sched--files")
	if err != nil || !slices.Equal(binding.AdmittedCapabilities, capFiles) ||
		!slices.Equal(p.successors[0], capFiles) {
		t.Fatalf("fire = %+v, %v; reservations %v", binding, err, p.successors)
	}
}
