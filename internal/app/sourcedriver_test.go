package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/sourceconformance"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/grpcdriver"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// startSourceDriver serves the given driver services on a loopback listener
// (offline: 127.0.0.1 only) and returns the dial target. The server stops at
// test cleanup so the goleak gate stays clean.
func startSourceDriver(t *testing.T, register func(gs *grpc.Server)) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	gs := grpc.NewServer()
	register(gs)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	return lis.Addr().String()
}

// soulDriverBody is a fixed-body prompt.SoulSource for the driver fixtures.
type soulDriverBody string

func (b soulDriverBody) Load(context.Context) (string, error) { return string(b), nil }

// driverConnsForTest returns a build-scoped conn cache whose every dialled
// conn closes at cleanup, so direct selectSoulSource/seam calls (which in
// production share Build's cache and its folded closes) stay goleak-clean.
func driverConnsForTest(t *testing.T) *driverConns {
	t.Helper()
	dc := newDriverConns()
	t.Cleanup(func() {
		dc.mu.Lock()
		defer dc.mu.Unlock()
		for _, c := range dc.conns {
			c.close()
		}
	})
	return dc
}

// TestValidateDriverConfigSourceExclusivity pins the new Phase-C1 rows: a
// local source and a driver URL for the same content seam are mutually
// exclusive; a lone URL passes.
func TestValidateDriverConfigSourceExclusivity(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{name: "skill driver alone", cfg: Config{SkillSourceURL: "127.0.0.1:7443"}},
		{name: "soul driver alone", cfg: Config{SoulSourceURL: "127.0.0.1:7443"}},
		{name: "soul driver with no-soul", cfg: Config{SoulSourceURL: "127.0.0.1:7443", NoSoul: true}},
		{name: "agent driver alone", cfg: Config{AgentSourceURL: "127.0.0.1:7443"}},
		// The default-true conventional discovery is SUPERSEDED, never fatal
		// (the §1.F asymmetry vs skills, whose conventional is opt-in).
		{name: "agent driver with conventional", cfg: Config{AgentSourceURL: "127.0.0.1:7443", AgentsConventional: true}},
		{
			name:    "agent driver with explicit dirs",
			cfg:     Config{AgentSourceURL: "127.0.0.1:7443", AgentsDirs: []string{"/tmp/agents"}},
			wantErr: "mutually exclusive",
		},
		// The command driver COMPOSES with file commands — deliberately NO
		// exclusivity rule.
		{name: "command driver alone", cfg: Config{CommandSourceURL: "127.0.0.1:7443"}},
		{name: "command driver with commands dir", cfg: Config{CommandSourceURL: "127.0.0.1:7443", CommandsDir: "/tmp/cmds"}},
		{
			name:    "skill driver with explicit dirs",
			cfg:     Config{SkillSourceURL: "127.0.0.1:7443", SkillsDirs: []string{"/tmp/sk"}},
			wantErr: "mutually exclusive",
		},
		{
			name:    "skill driver with conventional",
			cfg:     Config{SkillSourceURL: "127.0.0.1:7443", SkillsConventional: true},
			wantErr: "mutually exclusive",
		},
		{
			name:    "soul driver with soul file",
			cfg:     Config{SoulSourceURL: "127.0.0.1:7443", SoulPath: "/tmp/soul.md"},
			wantErr: "mutually exclusive",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateDriverConfig(c.cfg)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("validateDriverConfig = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("validateDriverConfig = %v, want an error containing %q", err, c.wantErr)
			}
		})
	}
}

type countingAssetSkillSource struct {
	tool.SkillSource
	assetReads atomic.Int32
}

func (s *countingAssetSkillSource) ReadSkillAsset(ctx context.Context, skill, asset string) ([]byte, error) {
	s.assetReads.Add(1)
	return s.SkillSource.ReadSkillAsset(ctx, skill, asset)
}

// TestRemoteSkillSourceDefaultAndNoFSLogicalAssetWiring drives the production
// driver seam through both catalog profiles. Skill activation stays lazy, and a
// no-fs session reads one logical asset through Skill without file or shell tools.
func TestRemoteSkillSourceDefaultAndNoFSLogicalAssetWiring(t *testing.T) {
	source := &countingAssetSkillSource{SkillSource: sourceconformance.NewFixtureSource()}
	addr := startSourceDriver(t, func(gs *grpc.Server) {
		driverv1.RegisterSkillSourceServiceServer(gs, grpcdriver.NewSkillSourceServer(source))
	})
	ctx := context.Background()
	provider := mockllm.New(mockllm.TextTurn("x"))
	cfg := Config{SkillSourceURL: addr}

	defaultCat, assets, _, _, closeFn, err := buildCatalog(ctx, cfg, regForTest(provider, providerMock, cfg.Model), provider, hookexec.New(nil), agents.NewRegistry(nil), memstore.New(), nil)
	if err != nil {
		t.Fatalf("buildCatalog(skill driver): %v", err)
	}
	defer closeFn()
	defaultSkill, ok := defaultCat.Lookup(skills.ToolName)
	if !ok {
		t.Fatal("default catalog is missing the remote-backed Skill tool")
	}
	assertSkillSpec := func(profile string, tl tool.Tool) {
		t.Helper()
		spec := tl.Spec()
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		}
		if err := json.Unmarshal(spec.Schema, &schema); err != nil {
			t.Fatalf("%s Skill schema: %v", profile, err)
		}
		if _, ok := schema.Properties["asset"]; !ok || !slices.Equal(schema.Required, []string{"name"}) {
			t.Fatalf("%s Skill schema does not make asset optional: %s", profile, spec.Schema)
		}
		for _, want := range []string{"Call with {name}", "call again with {name, asset}"} {
			if !strings.Contains(spec.Description, want) {
				t.Errorf("%s Skill description missing %q: %q", profile, want, spec.Description)
			}
		}
		for _, forbidden := range []string{"path", "Read", "Bash", "base director"} {
			if strings.Contains(spec.Description, forbidden) {
				t.Errorf("%s Skill description contains retired guidance %q: %q", profile, forbidden, spec.Description)
			}
		}
	}
	assertSkillSpec("default", defaultSkill)
	readTool, ok := defaultCat.Lookup("Read")
	if !ok {
		t.Fatal("default catalog is missing Read")
	}
	if schema := string(readTool.Spec().Schema); strings.Contains(schema, "activated skill") || strings.Contains(schema, "base director") {
		t.Fatalf("production Read schema advertises retired skill roots: %s", schema)
	}

	noFSCat, noFSClose := assembleCatalog(ctx, cfg, regForTest(provider, providerMock, cfg.Model), memstore.New(), hookexec.New(nil), &assets, catalogSession{
		provider: provider, providerID: providerMock, model: cfg.Model, noFS: true,
	})
	defer func() { _ = noFSClose() }()
	for _, name := range []string{"Read", "Bash"} {
		if _, ok := noFSCat.Lookup(name); ok {
			t.Fatalf("no-fs catalog contains %s", name)
		}
	}
	noFSSkill, ok := noFSCat.Lookup(skills.ToolName)
	if !ok {
		t.Fatal("no-fs catalog is missing the remote-backed Skill tool")
	}
	assertSkillSpec("no-fs", noFSSkill)

	profiles := []struct {
		name string
		tl   tool.Tool
		env  tool.Environment
	}{
		{"default", defaultSkill, tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "default"}, memfs.NewWorkspace("/workspace"), nil)},
		{"no-fs", noFSSkill, tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "no-fs"}, nofs.New(), nil)},
	}
	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			before := source.assetReads.Load()
			activated, err := profile.tl.Execute(ctx, session.NewToolCall("activate", skills.ToolName, []byte(`{"name":"review"}`)), profile.env)
			if err != nil || activated.IsError {
				t.Fatalf("activate remote skill: result=%+v err=%v", activated, err)
			}
			if got := source.assetReads.Load(); got != before {
				t.Fatalf("activation called ReadSkillAsset %d times, want zero", got-before)
			}
			for _, forbidden := range []string{"Base directory", "absolute path", "Read tool", "via Bash"} {
				if strings.Contains(activated.Content, forbidden) {
					t.Errorf("activation leaked path-based guidance %q: %q", forbidden, activated.Content)
				}
			}

			for _, assetCase := range []struct {
				name string
				want string
			}{
				{"references/checklist.md", "correctness first"},
				{"scripts/lint.sh", "echo lint"},
			} {
				before = source.assetReads.Load()
				args := fmt.Sprintf(`{"name":"review","asset":%q}`, assetCase.name)
				asset, err := profile.tl.Execute(ctx, session.NewToolCall("asset", skills.ToolName, []byte(args)), profile.env)
				if err != nil || asset.IsError {
					t.Fatalf("read remote skill asset %q: result=%+v err=%v", assetCase.name, asset, err)
				}
				if got := source.assetReads.Load(); got != before+1 {
					t.Fatalf("asset %q called ReadSkillAsset %d times, want exactly one", assetCase.name, got-before)
				}
				if !strings.Contains(asset.Content, assetCase.want) {
					t.Fatalf("asset %q content missing: %q", assetCase.name, asset.Content)
				}
			}
		})
	}
}

func TestRemoteSkillSourceBuildRunDefaultAndNoFS(t *testing.T) {
	for _, profile := range []server.SessionProfile{server.ProfileDefault, server.ProfileNoFS} {
		t.Run(string(profile), func(t *testing.T) {
			source := &observedAssetSkillSource{
				SkillSource: sourceconformance.NewFixtureSource(),
				reads:       map[string]int{},
			}
			addr := startSourceDriver(t, func(gs *grpc.Server) {
				driverv1.RegisterSkillSourceServiceServer(gs, grpcdriver.NewSkillSourceServer(source))
			})

			var (
				reqMu sync.Mutex
				reqs  []port.LLMRequest
			)
			provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
				reqMu.Lock()
				reqs = append(reqs, req)
				reqMu.Unlock()
			})},
				mockllm.ToolCallTurn(session.NewToolCall("activate", skills.ToolName, []byte(`{"name":"review"}`))),
				mockllm.ToolCallTurn(session.NewToolCall("asset", skills.ToolName, []byte(`{"name":"review","asset":"references/checklist.md"}`))),
				mockllm.TextTurn("done"),
			)
			ctx := context.Background()
			built, err := Build(ctx, Config{
				Workspace:      t.TempDir(),
				Model:          "mock",
				MockProvider:   provider,
				NoSoul:         true,
				MemoryDir:      t.TempDir(),
				SkillSourceURL: addr,
			})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			defer built.Close()

			var sess *session.Session
			if profile == server.ProfileNoFS {
				sess, err = built.Service.CreateSessionWithProfile(ctx, "", session.ModeDefault, session.Limits{}, server.ProviderSelector{}, profile)
			} else {
				sess, err = built.Service.CreateSessionWithProfile(ctx, t.TempDir(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, profile)
			}
			if err != nil {
				t.Fatalf("CreateSessionWithProfile: %v", err)
			}
			run, err := built.Service.StartRun(ctx, sess.ID, "activate review and read its checklist")
			if err != nil {
				t.Fatalf("StartRun: %v", err)
			}
			events := runEvents(run)
			results := map[session.ToolCallID]session.ToolResult{}
			for _, ev := range events {
				if ev.Type == session.EvToolResult && ev.ToolResult != nil {
					results[ev.ToolResult.CallID] = *ev.ToolResult
				}
			}
			if got := results["activate"]; got.IsError || !strings.Contains(got.Content, "references/checklist.md") {
				t.Fatalf("activation result = %+v", got)
			}
			if got := results["asset"]; got.IsError || !strings.Contains(got.Content, "correctness first") {
				t.Fatalf("asset result = %+v", got)
			}

			reqMu.Lock()
			captured := append([]port.LLMRequest(nil), reqs...)
			reqMu.Unlock()
			if len(captured) != 3 {
				t.Fatalf("provider requests = %d, want activation, asset, final", len(captured))
			}
			for i, req := range captured {
				var skillSpec *tool.ToolSpec
				for j := range req.Tools {
					if req.Tools[j].Name == skills.ToolName {
						skillSpec = &req.Tools[j]
						break
					}
				}
				if skillSpec == nil {
					t.Fatalf("provider request %d omitted Skill from tool specs", i)
				}
				var schema struct {
					Properties map[string]json.RawMessage `json:"properties"`
					Required   []string                   `json:"required"`
				}
				if err := json.Unmarshal(skillSpec.Schema, &schema); err != nil {
					t.Fatalf("request %d Skill schema: %v", i, err)
				}
				if _, ok := schema.Properties["asset"]; !ok || !slices.Equal(schema.Required, []string{"name"}) {
					t.Fatalf("request %d Skill schema does not expose optional asset: %s", i, skillSpec.Schema)
				}
				for _, want := range []string{"Call with {name}", "call again with {name, asset}"} {
					if !strings.Contains(skillSpec.Description, want) {
						t.Errorf("request %d Skill description missing %q: %q", i, want, skillSpec.Description)
					}
				}
				for _, forbidden := range []string{"path", "Read", "Bash"} {
					if strings.Contains(skillSpec.Description, forbidden) {
						t.Errorf("request %d Skill description contains retired %q guidance: %q", i, forbidden, skillSpec.Description)
					}
				}
			}

			source.mu.Lock()
			defer source.mu.Unlock()
			wantOps := []string{
				"body:review",
				"list:review",
				"list:review",
				"read:review/references/checklist.md",
			}
			if !slices.Equal(source.operations, wantOps) {
				t.Fatalf("source operations = %v, want %v (activation must perform zero asset reads)", source.operations, wantOps)
			}
			if got := source.reads["review/references/checklist.md"]; got != 1 {
				t.Fatalf("exact asset pair read %d times, want once", got)
			}
		})
	}
}

type observedAssetSkillSource struct {
	tool.SkillSource
	mu         sync.Mutex
	operations []string
	reads      map[string]int
}

func (s *observedAssetSkillSource) SkillBody(ctx context.Context, name string) (string, error) {
	s.mu.Lock()
	s.operations = append(s.operations, "body:"+name)
	s.mu.Unlock()
	return s.SkillSource.SkillBody(ctx, name)
}

func (s *observedAssetSkillSource) ListSkillAssets(ctx context.Context, name string) ([]tool.SkillAsset, error) {
	s.mu.Lock()
	s.operations = append(s.operations, "list:"+name)
	s.mu.Unlock()
	return s.SkillSource.ListSkillAssets(ctx, name)
}

func (s *observedAssetSkillSource) ReadSkillAsset(ctx context.Context, skill, asset string) ([]byte, error) {
	pair := skill + "/" + asset
	s.mu.Lock()
	s.operations = append(s.operations, "read:"+pair)
	s.reads[pair]++
	s.mu.Unlock()
	return s.SkillSource.ReadSkillAsset(ctx, skill, asset)
}

// TestResolveDriverSkillSeamUnreachableFatal pins the loud-misconfig posture:
// an explicitly configured skill driver that cannot answer the build-time
// ListSkills snapshot fails the seam (and therefore Build), never a silent
// no-skills degradation.
func TestResolveDriverSkillSeamUnreachableFatal(t *testing.T) {
	// A listener that is immediately closed: the port refuses connections.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	if _, err := resolveSkillSeam(context.Background(), Config{SkillSourceURL: addr}, nil); err == nil {
		t.Fatal("resolveSkillSeam(unreachable driver) = nil error, want a fatal snapshot failure")
	}
}

// TestSelectSoulSourceDriverPrecedence pins §J: the driver occupies the USER
// slot (it SHADOWS a present, trusted project soul), carries soulDriver
// provenance with Trusted=true, and still respects --no-soul and the
// soul:apply gate.
func TestSelectSoulSourceDriverPrecedence(t *testing.T) {
	const persona = "Calm, precise, driver-served."
	addr := startSourceDriver(t, func(gs *grpc.Server) {
		driverv1.RegisterSoulSourceServiceServer(gs, grpcdriver.NewSoulSourceServer(soulDriverBody(persona)))
	})

	// A workspace WITH a trusted project soul that must be shadowed.
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, ".mecatl"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".mecatl", "soul.md"), []byte("PROJECT persona"), 0o644); err != nil {
		t.Fatalf("write project soul: %v", err)
	}

	cfg := Config{SoulSourceURL: addr, Workspace: ws, TrustProject: true, driverConns: driverConnsForTest(t)}
	src, meta := selectSoulSource(cfg, newFakeIO().io(), fakeGate(governance.Allow))
	if src == nil || !meta.Present {
		t.Fatalf("driver soul must be selected, got src=%v meta=%+v", src, meta)
	}
	if meta.Provenance != soulDriver || !meta.Trusted {
		t.Errorf("meta = %+v, want Provenance=driver Trusted=true", meta)
	}
	if meta.Drifted {
		t.Error("the drift baseline is SKIPPED for driver provenance; Drifted must be false")
	}
	body, err := src.Load(context.Background())
	if err != nil || body != persona {
		t.Errorf("Load = %q, %v; want the driver persona (project soul shadowed)", body, err)
	}

	// --no-soul wins over the driver.
	if src, meta := selectSoulSource(Config{SoulSourceURL: addr, NoSoul: true}, newFakeIO().io(), fakeGate(governance.Allow)); src != nil || meta.Present {
		t.Errorf("--no-soul must win over the driver, got src=%v meta=%+v", src, meta)
	}

	// The soul:apply gate runs UNCHANGED before the driver branch.
	if src, meta := selectSoulSource(Config{SoulSourceURL: addr}, newFakeIO().io(), fakeGate(governance.Deny)); src != nil || meta.Present {
		t.Errorf("soul:apply Deny must withhold the driver soul, got src=%v meta=%+v", src, meta)
	}

	// The snapshot projection carries the new provenance.
	if got := soulProvenanceProto(soulDriver); got.String() != "SOUL_PROVENANCE_DRIVER" {
		t.Errorf("soulProvenanceProto(soulDriver) = %v, want SOUL_PROVENANCE_DRIVER", got)
	}
}

// TestBuildSoulDriverProbeFatal pins the build-time posture: an explicitly
// configured soul driver that cannot answer the probe fails the WHOLE build.
func TestBuildSoulDriverProbeFatal(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	_, err = Build(context.Background(), Config{
		Workspace:     t.TempDir(),
		Model:         "mock",
		UseMock:       true,
		SoulSourceURL: addr,
	})
	if err == nil || !strings.Contains(err.Error(), "soul-source driver") {
		t.Fatalf("Build(unreachable soul driver) error = %v, want the fatal probe error", err)
	}
}

// countingSkillSource wraps a tool.SkillSource and counts SkillBody fetches,
// so the driver preload index's LAZY contract is observable.
type countingSkillSource struct {
	tool.SkillSource
	bodyCalls map[string]int
}

func (c *countingSkillSource) SkillBody(ctx context.Context, name string) (string, error) {
	c.bodyCalls[name]++
	return c.SkillSource.SkillBody(ctx, name)
}

// TestDriverSkillIndexLazy pins the driver preload index's laziness: only the
// skill names an agent definition actually references are fetched — each
// EXACTLY ONCE (de-duped across repeated references), and an unreferenced
// skill transfers no body at all.
func TestDriverSkillIndexLazy(t *testing.T) {
	src := &countingSkillSource{SkillSource: sourceconformance.NewFixtureSource(), bodyCalls: map[string]int{}}
	metas, err := src.ListSkills(context.Background())
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	reg := agents.NewRegistry([]agents.AgentDef{
		{Name: "spec-a", Skills: []string{"review", "ghost"}},
		{Name: "spec-b", Skills: []string{"review"}}, // re-reference: must not re-fetch
	})

	idx := driverSkillIndex(context.Background(), Config{}, src, metas, reg)
	if got := src.bodyCalls["review"]; got != 1 {
		t.Errorf("referenced skill body fetched %d times, want exactly 1", got)
	}
	for _, never := range []string{"commit-style", "research", "ghost"} {
		if got := src.bodyCalls[never]; got != 0 {
			t.Errorf("skill %q body fetched %d times, want 0 (lazy: %s)", never, got,
				"only def-referenced, source-known names transfer")
		}
	}
	if body, ok := idx["review"]; !ok || body == "" {
		t.Errorf("index missing the referenced body: %+v", idx)
	}
	if _, ok := idx["commit-style"]; ok {
		t.Errorf("index carries an unreferenced body: %+v", idx)
	}
}

// TestResolveAgentSeamDriver drives the REAL agent-driver branch end to end
// over a loopback fixture server: the ONE ListAgentDefs snapshot becomes the
// registry, every def carries the unconditional driver origin, the detail
// channel names the driver target (never a path), and the default-true
// conventional discovery is narrated as SUPERSEDED (the §1.F asymmetry —
// never a fatal).
func TestResolveAgentSeamDriver(t *testing.T) {
	addr := startSourceDriver(t, func(gs *grpc.Server) {
		driverv1.RegisterAgentSourceServiceServer(gs, grpcdriver.NewAgentSourceServer(sourceconformance.NewAgentFixtureSource()))
	})
	diag := newCapturingDiagnostics()
	cfg := Config{AgentSourceURL: addr, AgentsConventional: true, Diagnostics: diag, driverConns: driverConnsForTest(t)}
	reg, closeFn, err := resolveAgentSeam(context.Background(), cfg)
	if err != nil {
		t.Fatalf("resolveAgentSeam: %v", err)
	}
	if closeFn != nil {
		t.Cleanup(closeFn)
	}
	if reg.Len() != len(sourceconformance.AgentFixture) {
		t.Fatalf("registry holds %d defs, want %d", reg.Len(), len(sourceconformance.AgentFixture))
	}
	for _, d := range reg.List() {
		if d.Origin != tool.AgentOriginDriver {
			t.Errorf("def %q Origin = %q, want driver (stamped unconditionally)", d.Name, d.Origin)
		}
		if got, want := reg.Detail(d.Name), "driver: "+addr; got != want {
			t.Errorf("Detail(%q) = %q, want %q (the driver detail names the target, never a path)", d.Name, got, want)
		}
	}
	if got := diag.countContaining("conventional discovery superseded"); got != 1 {
		t.Errorf("superseded narration emitted %d times, want exactly 1", got)
	}
	if got := diag.countContaining("agent definitions ENABLED"); got != 1 {
		t.Errorf("ENABLED narration emitted %d times, want exactly 1", got)
	}
	// The harness-side-shell capability is narrated per hooks-bearing def
	// (exactly ONE fixture def — "full-stack" — carries hooks), names only.
	if got := diag.countContaining("agent def carries lifecycle hooks (harness-side shell)"); got != 1 {
		t.Errorf("hooks-capability narration emitted %d times, want exactly 1 (one hooks-bearing fixture def)", got)
	}
}

// TestBuildAgentDriverUnreachableFatal pins the loud-misconfig posture at BOTH
// levels: the seam itself errors on an unreachable driver, and a full Build
// surfaces it as the fatal it is (defs bake per-def child engines — a silent
// no-defs degradation would hide the misconfiguration).
func TestBuildAgentDriverUnreachableFatal(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	if _, _, serr := resolveAgentSeam(context.Background(), Config{AgentSourceURL: addr, driverConns: driverConnsForTest(t)}); serr == nil {
		t.Fatal("resolveAgentSeam(unreachable driver) = nil error, want a fatal snapshot failure")
	}

	_, berr := Build(context.Background(), Config{
		Workspace:      t.TempDir(),
		Model:          "mock",
		UseMock:        true,
		AgentSourceURL: addr,
	})
	if berr == nil || !strings.Contains(berr.Error(), "agent definitions from driver") {
		t.Fatalf("Build(unreachable agent driver) error = %v, want the fatal seam error", berr)
	}
}

// TestBuildCommandDriverProbeFatal pins the build-time posture: an explicitly
// configured command driver that cannot answer the Probe fails the WHOLE
// build (runtime faults, by contrast, fail soft inside the client).
func TestBuildCommandDriverProbeFatal(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	_, err = Build(context.Background(), Config{
		Workspace:        t.TempDir(),
		Model:            "mock",
		UseMock:          true,
		CommandSourceURL: addr,
	})
	if err == nil || !strings.Contains(err.Error(), "command-source driver") {
		t.Fatalf("Build(unreachable command driver) error = %v, want the fatal probe error", err)
	}
}

// stubCommandSource is a fixed in-memory prompt.CommandSource for the
// composition-order tests.
type stubCommandSource struct {
	bodies map[string]string
	list   []prompt.Command
}

func (s stubCommandSource) ListCommands(context.Context) ([]prompt.Command, error) {
	return s.list, nil
}

func (s stubCommandSource) CommandBody(_ context.Context, name string) (string, bool, error) {
	body, ok := s.bodies[name]
	return body, ok, nil
}

// TestCommandDriverCompositionOrder pins the §1.F composition: file-backed
// commands FIRST, the driver source SECOND (a local command file shadows a
// same-named driver command; a driver command expands when no file matches),
// and the palette merges both with the same first-wins precedence.
func TestCommandDriverCompositionOrder(t *testing.T) {
	cfg := Config{CommandsDir: "cmds"}
	cfg.commandSource = stubCommandSource{
		bodies: map[string]string{
			"dup":         "DRIVER dup body",
			"driver-only": "DRIVER body for $1",
		},
		list: []prompt.Command{
			{Name: "driver-only", Description: "driver only"},
			{Name: "dup", Description: "driver dup"},
		},
	}
	exp := buildCommandExpander(cfg, nil)
	ws := memfs.NewWorkspace("/proj")
	if err := ws.Write(context.Background(), "cmds/dup.md", []byte("---\ndescription: file dup\n---\nFILE dup body $ARGUMENTS")); err != nil {
		t.Fatalf("write command file: %v", err)
	}
	ctx := context.Background()

	// A same-named file command SHADOWS the driver command.
	out, ok, err := exp.Expand(ctx, ws, "/dup x")
	if err != nil || !ok || out != "FILE dup body x" {
		t.Errorf("Expand(/dup) = (%q, %v, %v), want the FILE body (file shadows driver)", out, ok, err)
	}
	// A driver-only command expands through the source (shared substitution).
	out, ok, err = exp.Expand(ctx, ws, "/driver-only y")
	if err != nil || !ok || out != "DRIVER body for y" {
		t.Errorf("Expand(/driver-only) = (%q, %v, %v), want the driver body", out, ok, err)
	}
	// The palette merges both, first-wins on the collision.
	lister, isLister := exp.(prompt.CommandLister)
	if !isLister {
		t.Fatal("composed expander must implement prompt.CommandLister")
	}
	cmds, err := lister.List(ctx, ws)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(cmds) != 2 || cmds[0].Name != "driver-only" || cmds[1].Name != "dup" {
		t.Fatalf("palette = %+v, want [driver-only, dup]", cmds)
	}
	if cmds[1].Description != "file dup" {
		t.Errorf("dup description = %q, want the FILE one (first-wins)", cmds[1].Description)
	}
}

// argScanDiag is a port.Diagnostics double that renders EVERY emitted line —
// message AND args (and With-bound attributes) — into a flat string store, so
// a test can scan the whole diagnostics surface for a sentinel.
type argScanDiag struct {
	mu    sync.Mutex
	lines []string
}

func (d *argScanDiag) Log(_ context.Context, _ port.Level, msg string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lines = append(d.lines, msg+" "+fmt.Sprint(args...))
}

func (d *argScanDiag) With(args ...any) port.Diagnostics {
	d.mu.Lock()
	d.lines = append(d.lines, fmt.Sprint(args...))
	d.mu.Unlock()
	return d
}

func (d *argScanDiag) joined() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.lines, "\n")
}

// TestDefMCPHeadersNeverLogged is the §0.5 guard: an inline MCP server's
// Headers are SECRET-SHAPED — the per-def MCP wiring logs server names, urls,
// counts, and errors, but a header VALUE must never reach the diagnostics
// stream.
//
// SCOPE NOTE: this exercises the WARN/FAILURE branches only — the inline
// server is unreachable (loopback port 1, refused immediately) and the
// reference names no configured server, so the connect-error, all-failed, and
// unknown-reference WARNs all fire and are scanned. The SUCCESS branches
// (a connected inline manager / a resolved reference) need a live MCP server
// and are not exercised offline; their log lines are the same
// name/url/count-shaped Info calls in defMCPTools, which take no header
// argument by construction.
func TestDefMCPHeadersNeverLogged(t *testing.T) {
	const sentinel = "SECRET-HEADER-SENTINEL-c2"
	diag := &argScanDiag{}
	cfg := Config{Model: "m", Diagnostics: diag}
	reg := agents.NewRegistry([]agents.AgentDef{{
		Name:        "ops",
		Description: "operates over MCP",
		MCPServers: []agents.AgentMCPServer{
			{Name: "ghost-ref"}, // unknown reference: logs a WARN naming the server
			{
				Name:    "inline",
				URL:     "http://127.0.0.1:1/mcp", // refused immediately: connect WARN
				Headers: map[string]string{"Authorization": "Bearer " + sentinel},
			},
		},
	}})
	provider := mockllm.New(mockllm.TextTurn("x"))
	engines, _, closeFn := agentSubagentEnginesForTest(context.Background(), cfg, provider, reg, nil, hookexec.New(nil), nil, nil)
	if closeFn != nil {
		defer func() { _ = closeFn() }()
	}
	if len(engines) != 1 {
		t.Fatalf("want the def engine built despite MCP failures, got %d", len(engines))
	}

	out := diag.joined()
	if !strings.Contains(out, "MCP") {
		t.Fatalf("the MCP wiring WARNs did not fire — the no-leak scan would be vacuous:\n%s", out)
	}
	if strings.Contains(out, sentinel) {
		t.Errorf("an inline MCP header VALUE leaked into diagnostics:\n%s", out)
	}
}

// countingCommandDriver is a CommandSourceService fixture that counts RPCs,
// so the build-once-stash contract is observable on the wire: ListCommands
// must fire exactly once (the build-time Probe) however many session engines
// are minted, and GetCommandBody once per actual expansion.
type countingCommandDriver struct {
	driverv1.UnimplementedCommandSourceServiceServer
	listCalls atomic.Int32
	bodyCalls atomic.Int32
}

func (s *countingCommandDriver) ListCommands(context.Context, *driverv1.ListCommandsRequest) (*driverv1.ListCommandsResponse, error) {
	s.listCalls.Add(1)
	return &driverv1.ListCommandsResponse{Commands: []*driverv1.CommandMeta{
		{Name: "driver-cmd", Description: "a driver-served command"},
	}}, nil
}

func (s *countingCommandDriver) GetCommandBody(_ context.Context, req *driverv1.GetCommandBodyRequest) (*driverv1.GetCommandBodyResponse, error) {
	s.bodyCalls.Add(1)
	if req.GetName() != "driver-cmd" {
		return nil, status.Errorf(codes.NotFound, "unknown command %q", req.GetName())
	}
	return &driverv1.GetCommandBodyResponse{Body: "DRIVER says $1"}, nil
}

// TestBuildCommandDriverProbeOnceAcrossSessionEngines pins the #42 drift
// class for the command seam over the PRODUCTION wiring: Build dials+probes
// the driver EXACTLY ONCE and stashes the client (cfg.commandSource); minting
// per-session engines through the service's factory must compose the stash —
// never re-dial or re-probe (the wire count stays at the single Probe).
func TestBuildCommandDriverProbeOnceAcrossSessionEngines(t *testing.T) {
	srv := &countingCommandDriver{}
	addr := startSourceDriver(t, func(gs *grpc.Server) {
		driverv1.RegisterCommandSourceServiceServer(gs, srv)
	})
	built, err := Build(context.Background(), Config{
		Workspace:        t.TempDir(),
		Model:            "mock",
		UseMock:          true,
		CommandSourceURL: addr,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	if got := srv.listCalls.Load(); got != 1 {
		t.Fatalf("Build issued %d ListCommands RPCs, want exactly 1 (the Probe)", got)
	}

	// TWO per-session engines through the production factory (a non-zero
	// provider selector forces the per-session path).
	for i := range 2 {
		if _, serr := built.Service.CreateSessionWithProvider(context.Background(), t.TempDir(),
			session.ModeDefault, defaultLimits(), server.ProviderSelector{ProviderID: providerMock}); serr != nil {
			t.Fatalf("CreateSessionWithProvider #%d: %v", i, serr)
		}
	}
	if got := srv.listCalls.Load(); got != 1 {
		t.Errorf("after minting 2 session engines ListCommands = %d, want STILL 1 (the factory must compose the stashed, already-probed client — never re-dial/re-probe)", got)
	}
}

// TestSessionEngineCommandExpanderUsesStashedDriverSource is the second half
// of the stash proof: each per-session engine minted by the PRODUCTION
// sessionEngineFactory carries a CommandExpander composed from the stashed
// driver client, and actually EXPANDS a driver-sourced command — the expanded
// body (not the raw "/cmd") is what lands in the session history. The stash
// here is performed with the SAME three steps Build uses (drivers().dial →
// NewCommandSource → Probe → cfg.commandSource), with a test-controlled
// provider so the runs are scriptable.
func TestSessionEngineCommandExpanderUsesStashedDriverSource(t *testing.T) {
	srv := &countingCommandDriver{}
	addr := startSourceDriver(t, func(gs *grpc.Server) {
		driverv1.RegisterCommandSourceServiceServer(gs, srv)
	})
	ctx := context.Background()
	cfg := Config{Model: "m", driverConns: driverConnsForTest(t)}
	conn, _, err := cfg.drivers().dial(cfg, addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	cmdSrc := grpcdriver.NewCommandSource(conn, grpcdriver.CommandOptions{Diagnostics: cfg.diag()})
	if perr := cmdSrc.Probe(ctx); perr != nil {
		t.Fatalf("Probe: %v", perr)
	}
	cfg.commandSource = cmdSrc

	provider := mockllm.New(mockllm.TextTurn("done one"), mockllm.TextTurn("done two"))
	factory := sessionEngineFactory(cfg, regForTest(provider, providerMock, cfg.Model), provider,
		memstore.New(), permpolicy.NewPolicy(defaultRules(), nil), hookexec.New(nil), nil,
		prompt.RootAssembler{}, catalogAssets{}, nil)

	for i := range 2 {
		res, ferr := factory(ctx, server.ProviderSelector{ProviderID: providerMock}, nil, server.ProfileDefault, "", session.ModeDefault)
		if ferr != nil {
			t.Fatalf("factory #%d: %v", i, ferr)
		}
		sess := session.New(session.SessionID(fmt.Sprintf("cmd-sess-%d", i)), session.ModeDefault, "/ws",
			session.Limits{MaxTurns: 3}, time.Now())
		drainRun(res.Engine.Run(ctx, sess, memEnvironment("/ws"), agent.RunRequest{Text: "/driver-cmd fix-it", Parts: nil}))
		expanded := false
		for _, m := range sess.Conversation.Messages {
			if m.Role == session.RoleUser && m.Text == "DRIVER says fix-it" {
				expanded = true
			}
			if m.Role == session.RoleUser && strings.HasPrefix(m.Text, "/driver-cmd") {
				t.Errorf("session %d recorded the RAW command %q — the driver expansion did not run", i, m.Text)
			}
		}
		if !expanded {
			t.Errorf("session %d history is missing the EXPANDED driver body", i)
		}
		if res.Close != nil {
			_ = res.Close()
		}
	}
	if got := srv.listCalls.Load(); got != 1 {
		t.Errorf("ListCommands = %d, want exactly 1 (the Probe; minting+expanding must not re-list)", got)
	}
	if got := srv.bodyCalls.Load(); got != 2 {
		t.Errorf("GetCommandBody = %d, want 2 (one expansion per session)", got)
	}
}
