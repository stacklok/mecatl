package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
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

// TestResolveDriverSkillSeam drives the REAL driver branch end to end over a
// loopback fixture server: the metadata snapshot, the single cache read root,
// lazy materialization on first activation (executable bit honored, base dir
// inside the cache), and cache removal on close.
func TestResolveDriverSkillSeam(t *testing.T) {
	addr := startSourceDriver(t, func(gs *grpc.Server) {
		driverv1.RegisterSkillSourceServiceServer(gs, grpcdriver.NewSkillSourceServer(sourceconformance.NewFixtureSource()))
	})
	cfg := Config{SkillSourceURL: addr}
	seam, err := resolveSkillSeam(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("resolveSkillSeam: %v", err)
	}
	if len(seam.metas) != len(sourceconformance.Fixture) {
		t.Fatalf("seam.metas = %d skills, want %d", len(seam.metas), len(sourceconformance.Fixture))
	}

	// readRoots == {cacheBase}: exactly one root, an existing dir (EAGER
	// creation — a late-born root would be unreadable by osfs).
	if len(seam.readRoots) != 1 {
		t.Fatalf("seam.readRoots = %v, want exactly the asset cache", seam.readRoots)
	}
	cacheBase := seam.readRoots[0]
	if fi, serr := os.Stat(cacheBase); serr != nil || !fi.IsDir() {
		t.Fatalf("asset cache %q must exist at build time: %v", cacheBase, serr)
	}

	// LAZY: nothing materialized before the first activation.
	if entries, _ := os.ReadDir(cacheBase); len(entries) != 0 {
		t.Errorf("asset cache must be empty before any activation, got %v", entries)
	}

	act, err := seam.activator.Activate(context.Background(), "review")
	if err != nil {
		t.Fatalf("Activate(review): %v", err)
	}
	if act.BaseDir != filepath.Join(cacheBase, "review") {
		t.Errorf("BaseDir = %q, want %q", act.BaseDir, filepath.Join(cacheBase, "review"))
	}
	script := filepath.Join(act.BaseDir, "scripts", "lint.sh")
	if fi, serr := os.Stat(script); serr != nil || fi.Mode()&0o111 == 0 {
		t.Errorf("materialized script %q must exist with the executable bit: fi=%v err=%v", script, fi, serr)
	}

	// An asset-less driver skill omits the base dir.
	lean, err := seam.activator.Activate(context.Background(), "commit-style")
	if err != nil {
		t.Fatalf("Activate(commit-style): %v", err)
	}
	if lean.BaseDir != "" {
		t.Errorf("asset-less skill BaseDir = %q, want \"\"", lean.BaseDir)
	}

	// Close removes the cache.
	seam.close()
	if _, serr := os.Stat(cacheBase); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("seam close must remove the asset cache, stat err = %v", serr)
	}
}

// TestBuildCatalogSkillDriverRegistersSkillTool proves the REAL wiring
// (buildCatalog) takes the SkillSourceURL branch: the Skill tool registers
// over the driver metas, the assets carry the cache read root, and the
// catalog close removes the cache — the driver-backed analogue of
// TestBuildCatalogMemoryDriverRegistersTools.
func TestBuildCatalogSkillDriverRegistersSkillTool(t *testing.T) {
	addr := startSourceDriver(t, func(gs *grpc.Server) {
		driverv1.RegisterSkillSourceServiceServer(gs, grpcdriver.NewSkillSourceServer(sourceconformance.NewFixtureSource()))
	})
	ctx := context.Background()
	provider := mockllm.New(mockllm.TextTurn("x"))
	cfg := Config{SkillSourceURL: addr}

	cat, assets, _, _, closeFn, err := buildCatalog(ctx, cfg, regForTest(provider, providerMock, cfg.Model), provider, hookexec.New(nil), agents.NewRegistry(nil), memstore.New(), nil)
	if err != nil {
		t.Fatalf("buildCatalog(skill driver): %v", err)
	}
	if _, ok := cat.Lookup(skills.ToolName); !ok {
		t.Error("skill driver enabled (SkillSourceURL set): catalog is missing the Skill tool")
	}
	if len(assets.skills) != len(sourceconformance.Fixture) {
		t.Errorf("assets.skills = %d metas, want %d", len(assets.skills), len(sourceconformance.Fixture))
	}
	if len(assets.skillReadRoots) != 1 {
		t.Fatalf("assets.skillReadRoots = %v, want exactly the asset cache", assets.skillReadRoots)
	}
	cacheBase := assets.skillReadRoots[0]
	if _, serr := os.Stat(cacheBase); serr != nil {
		t.Fatalf("asset cache must exist: %v", serr)
	}
	closeFn()
	if _, serr := os.Stat(cacheBase); !errors.Is(serr, os.ErrNotExist) {
		t.Errorf("catalog close must remove the asset cache, stat err = %v", serr)
	}
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

// TestDriverAssetReadableThroughEarlyWorkspace pins the seam join the EAGER
// cache MkdirTemp exists for: a production osfs Workspace is constructed
// BEFORE any activation (the cache root is registered while still empty),
// a skill then materializes its payloads LATE, and a Read of the materialized
// file by the absolute path the activation header advertises succeeds through
// that pre-existing workspace.
func TestDriverAssetReadableThroughEarlyWorkspace(t *testing.T) {
	addr := startSourceDriver(t, func(gs *grpc.Server) {
		driverv1.RegisterSkillSourceServiceServer(gs, grpcdriver.NewSkillSourceServer(sourceconformance.NewFixtureSource()))
	})
	cfg := Config{SkillSourceURL: addr}
	seam, err := resolveSkillSeam(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("resolveSkillSeam: %v", err)
	}
	t.Cleanup(seam.close)

	// Workspace FIRST: the factory opens its read roots at construction; the
	// cache root exists (eager MkdirTemp) but holds nothing yet.
	ws := osfsWorkspaceFactory(Config{}.diag(), seam.readRoots)(t.TempDir())
	if ws == nil {
		t.Fatal("workspace factory returned nil")
	}

	// Activation SECOND: the bundle materializes after the workspace was built.
	act, err := seam.activator.Activate(context.Background(), "review")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if act.BaseDir == "" {
		t.Fatal("review must materialize a base dir")
	}

	absFile := filepath.Join(act.BaseDir, "references", "checklist.md")
	got, err := ws.Read(context.Background(), absFile)
	if err != nil {
		t.Fatalf("workspace Read(%q) after late materialization: %v (the eager cache-root registration is broken)", absFile, err)
	}
	if want := "- correctness first\n- style second\n"; string(got) != want {
		t.Errorf("Read = %q, want %q", got, want)
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
		drainRun(res.Engine.Run(ctx, sess, memfs.NewWorkspace("/ws"), agent.RunRequest{Text: "/driver-cmd fix-it", Parts: nil}))
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
