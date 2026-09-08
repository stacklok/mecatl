# Product Metrics (Opt-Out Adoption Telemetry) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a fully independent, opt-out-by-default OTLP pipeline that reports bounded adoption/usage counters (install heartbeat, feature flags, coarse session/run/tool-call/token counts) to Stacklok's public metrics collector, with zero coupling to mecatl's existing operator-facing telemetry.

**Architecture:** A new `internal/adapter/productmetrics` package owns its own OTel `MeterProvider` (built via the already-vendored `toolhive-core/telemetry/providers`, pointed at a hardcoded `https://metrics.stacklok.com/v1/metrics` endpoint with a build-time-baked header key) and its own `port.EventSink`/`port.ToolCallRecorder` implementation that extracts only bounded, closed-enum counts — no tool/session/model names, no free text. Composition (`internal/cliconfig`, then each of the four `cmd/*` mains) tees this second sink alongside the existing operator telemetry sink; the two share no struct, registry, or destination.

**Tech Stack:** Go 1.26, `go.opentelemetry.io/otel/{metric,sdk/metric}`, `github.com/stacklok/toolhive-core/telemetry/providers` (already a dependency), `github.com/google/uuid` (already an indirect dependency, promoted to direct).

**Spec:** `docs/superpowers/specs/2026-09-08-product-metrics-otel-design.md` — read it alongside this plan; the plan implements it with one deliberate scope refinement (see Global Constraints).

## Global Constraints

- **Opt-out, enabled by default.** Precedence, highest first: CLI flag `--product-metrics` (explicit) > `DO_NOT_TRACK` env var (any non-empty value disables) > operator `settings.yaml` `telemetry.productMetrics.enabled` > default `true`.
- **Operator-tier only.** A project-tier `.mecatl/settings.yaml` `telemetry:` block is parsed but IGNORED with a WARN — mirrors the existing `guardrails:`/`openrouter:`/`posture:` discipline exactly.
- **Zero import relationship** between `internal/adapter/productmetrics` and `internal/adapter/telemetry`. Combined only via a fan-out at the composition edge.
- **No free text ever becomes a metric attribute.** Every label value is a bounded Go closed-set type defined in this package, or an already-existing bounded enum (`session.StopReason`, the token-kind strings).
- **Metric names are namespaced `mecatl.adoption.*`** (passes the collector's `^mecatl\..*` server-side filter; visually distinct from the operator-facing `mecatl.tool.*`/`mecatl.runs`/etc. family).
- **Scope refinement vs the spec:** the spec's illustrative catalog listed `teams`/`subagents`/`learning` as heartbeat *feature flags*. This plan drops them from the heartbeat (no reliable, already-verified boolean signal exists at CLI-flag level for "learning enabled", and "teams"/"subagents" are core engine capabilities, not togglable features) and instead captures Subagent/Team **adoption** via the event tap (`mecatl.adoption.subagent_used`/`team_used`, already in the spec's coarse-usage catalog) — the more honest signal. The heartbeat's `feature_enabled` set for this plan is `memory`, `guardrails`, `mcp`, `scheduling` — every one backed by a real, already-verified `cmd/mecated` config field (see Task 11).
- **Exporter failures must never affect the app.** Lazy-dial exporter (matches the existing OTLP exporters in `internal/adapter/telemetry/otlp.go`); bounded `Shutdown` (~3s).
- **`task lint && task test`** must stay green after every task.

---

### Task 1: `productmetrics` package skeleton — closed enums, `FeatureSnapshot`, `Config`

**Files:**
- Create: `internal/adapter/productmetrics/config.go`
- Test: `internal/adapter/productmetrics/config_test.go`

**Interfaces:**
- Produces: `type Feature string` + consts `FeatureMemory`, `FeatureGuardrails`, `FeatureMCP`, `FeatureScheduling`; `type ProviderFamily string` + consts `ProviderAnthropic`, `ProviderOpenAI`, `ProviderOpenRouter`, `ProviderOther`; `type DeploymentMode string` + consts `ModeInteractive`, `ModeHeadless`, `ModeK8s`; `type Binary string` + consts `BinaryMecated`, `BinaryMecatui`, `BinaryMecatequi`, `BinaryMecak8s`; `type FeatureSnapshot struct{ Memory, Guardrails, MCP, Scheduling bool; Provider ProviderFamily; Mode DeploymentMode }` with method `func (s FeatureSnapshot) enabled() map[Feature]bool`; `type Config struct{ Binary Binary; Version string; InstallID string }`.

- [ ] **Step 1: Write the failing test**

```go
package productmetrics

import "testing"

func TestFeatureSnapshotEnabledIsClosedAndBounded(t *testing.T) {
	snap := FeatureSnapshot{Memory: true, MCP: true, Provider: ProviderAnthropic, Mode: ModeInteractive}
	got := snap.enabled()

	want := map[Feature]bool{
		FeatureMemory:     true,
		FeatureGuardrails: false,
		FeatureMCP:        true,
		FeatureScheduling: false,
	}
	if len(got) != len(want) {
		t.Fatalf("enabled() returned %d entries, want %d (%v)", len(got), len(want), got)
	}
	for f, v := range want {
		if got[f] != v {
			t.Errorf("enabled()[%q] = %v, want %v", f, got[f], v)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestFeatureSnapshotEnabledIsClosedAndBounded -v`
Expected: FAIL — package doesn't exist yet / `enabled` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// Package productmetrics is a fully independent, opt-out-by-default OTel
// metrics adapter reporting bounded adoption/usage counters to Stacklok's
// public metrics collector. It shares no import, struct, MeterProvider, or
// destination with internal/adapter/telemetry (mecatl's operator-facing
// observability pipeline) — the two are combined only at the composition
// edge (internal/cliconfig), by fanning both into the engine's
// port.EventSink/port.ToolCallRecorder seams.
//
// Every exported type in this package that can become a metric attribute is
// a closed Go string-alias enum. Nothing here carries a session id, model
// id/alias, tool or MCP-server name, file path, or free text.
package productmetrics

// Feature is the closed set of major toggleable features reported at
// heartbeat time. Never a def/model/tool name — only these four values.
type Feature string

const (
	FeatureMemory     Feature = "memory"
	FeatureGuardrails Feature = "guardrails"
	FeatureMCP        Feature = "mcp"
	FeatureScheduling Feature = "scheduling"
)

// ProviderFamily is the closed set of configured LLM provider families.
// Never a model id or alias.
type ProviderFamily string

const (
	ProviderAnthropic  ProviderFamily = "anthropic"
	ProviderOpenAI     ProviderFamily = "openai"
	ProviderOpenRouter ProviderFamily = "openrouter"
	ProviderOther      ProviderFamily = "other"
)

// DeploymentMode is the closed set of process shapes.
type DeploymentMode string

const (
	ModeInteractive DeploymentMode = "interactive"
	ModeHeadless    DeploymentMode = "headless"
	ModeK8s         DeploymentMode = "k8s"
)

// Binary is the closed set of the four mecatl entry points.
type Binary string

const (
	BinaryMecated   Binary = "mecated"
	BinaryMecatui   Binary = "mecatui"
	BinaryMecatequi Binary = "mecatequi"
	BinaryMecak8s   Binary = "mecak8s"
)

// FeatureSnapshot is a closed-shape, read-only snapshot of which major
// features are enabled and which provider family / deployment mode this
// process runs as. It carries no free text and no model id/alias.
type FeatureSnapshot struct {
	Memory     bool
	Guardrails bool
	MCP        bool
	Scheduling bool
	Provider   ProviderFamily
	Mode       DeploymentMode
}

// enabled returns every Feature mapped to whether this snapshot reports it
// enabled. It is the single place Heartbeat iterates, so adding a Feature
// const without adding it here is caught by the exhaustiveness this map
// documents (and by TestFeatureSnapshotEnabledIsClosedAndBounded above).
func (s FeatureSnapshot) enabled() map[Feature]bool {
	return map[Feature]bool{
		FeatureMemory:     s.Memory,
		FeatureGuardrails: s.Guardrails,
		FeatureMCP:        s.MCP,
		FeatureScheduling: s.Scheduling,
	}
}

// Config configures a Provider/Recorder pair for one process.
type Config struct {
	// Binary identifies which of the four entry points this process is.
	Binary Binary
	// Version is the mecatl build version (resource attribute service.version).
	Version string
	// InstallID is this process's persisted anonymous install identifier.
	InstallID string
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestFeatureSnapshotEnabledIsClosedAndBounded -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/productmetrics/config.go internal/adapter/productmetrics/config_test.go
git commit -m "feat(productmetrics): add closed enums, FeatureSnapshot, Config"
```

---

### Task 2: Install identity persistence

**Files:**
- Create: `internal/adapter/productmetrics/installid.go`
- Test: `internal/adapter/productmetrics/installid_test.go`

**Interfaces:**
- Consumes: `xdgconfig.ResolveEnv`, `xdgconfig.UserStateDir(env)` (`internal/adapter/xdgconfig`, already read: `func UserStateDir(env ResolveEnv) string`).
- Produces: `func LoadOrCreateInstallID(env xdgconfig.ResolveEnv, readFile func(string) ([]byte, error), writeFile func(string, []byte, os.FileMode) error, mkdirAll func(string, os.FileMode) error) (id string, firstRun bool, err error)` and a default-wiring convenience `func LoadOrCreateInstallIDDefault() (id string, firstRun bool, err error)`.

- [ ] **Step 1: Write the failing test**

```go
package productmetrics

import (
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

func TestLoadOrCreateInstallIDCreatesOnFirstRun(t *testing.T) {
	env := xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "/home/tester", nil },
	}
	written := map[string][]byte{}
	readFile := func(path string) ([]byte, error) {
		data, ok := written[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return data, nil
	}
	writeFile := func(path string, data []byte, _ os.FileMode) error {
		written[path] = data
		return nil
	}
	mkdirAll := func(string, os.FileMode) error { return nil }

	id, firstRun, err := LoadOrCreateInstallID(env, readFile, writeFile, mkdirAll)
	if err != nil {
		t.Fatalf("LoadOrCreateInstallID: %v", err)
	}
	if !firstRun {
		t.Error("firstRun = false on an empty store, want true")
	}
	if _, perr := uuid.Parse(id); perr != nil {
		t.Errorf("id %q is not a valid UUID: %v", id, perr)
	}

	// Second call reads back the SAME id and reports firstRun=false.
	id2, firstRun2, err := LoadOrCreateInstallID(env, readFile, writeFile, mkdirAll)
	if err != nil {
		t.Fatalf("second LoadOrCreateInstallID: %v", err)
	}
	if firstRun2 {
		t.Error("firstRun = true on second call, want false")
	}
	if id2 != id {
		t.Errorf("second call returned id %q, want %q (unchanged)", id2, id)
	}
}

func TestLoadOrCreateInstallIDRegeneratesOnCorruptFile(t *testing.T) {
	env := xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "/home/tester", nil },
	}
	readFile := func(string) ([]byte, error) { return []byte("not-a-uuid"), nil }
	var gotWrite []byte
	writeFile := func(_ string, data []byte, _ os.FileMode) error { gotWrite = data; return nil }
	mkdirAll := func(string, os.FileMode) error { return nil }

	id, firstRun, err := LoadOrCreateInstallID(env, readFile, writeFile, mkdirAll)
	if err != nil {
		t.Fatalf("LoadOrCreateInstallID: %v", err)
	}
	if !firstRun {
		t.Error("firstRun = false on a corrupt file, want true (treated as absent)")
	}
	if _, perr := uuid.Parse(id); perr != nil {
		t.Errorf("id %q is not a valid UUID: %v", id, perr)
	}
	if string(gotWrite) != id {
		t.Errorf("written content %q != returned id %q", gotWrite, id)
	}
}

func TestLoadOrCreateInstallIDFailsClosedWithNoStateDir(t *testing.T) {
	env := xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
	}
	_, _, err := LoadOrCreateInstallID(env, nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error when no state dir can be resolved, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestLoadOrCreateInstallID -v`
Expected: FAIL — `LoadOrCreateInstallID` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package productmetrics

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

// installIDRelPath is the state-dir-relative path to the persisted anonymous
// install identifier — machine-written runtime state, not human config, so
// it lives under XDG_STATE_HOME (mirroring mecatui's
// $XDG_STATE_HOME/mecatl/mecatui.log precedent), not XDG_CONFIG_HOME.
const installIDRelPath = "mecatl/telemetry-id"

// LoadOrCreateInstallID reads the persisted install UUID, creating one if
// absent or unparseable. The id is a bare random v4 UUID: it carries no
// machine or user information, and is trivially reset by deleting the file
// (the next opt-in mints a new one). firstRun is true whenever a new id was
// just minted — the caller uses it to decide whether to print the one-time
// disclosure notice. readFile/writeFile/mkdirAll are injected for testing;
// LoadOrCreateInstallIDDefault binds the real filesystem.
func LoadOrCreateInstallID(
	env xdgconfig.ResolveEnv,
	readFile func(string) ([]byte, error),
	writeFile func(string, []byte, os.FileMode) error,
	mkdirAll func(string, os.FileMode) error,
) (id string, firstRun bool, err error) {
	base := xdgconfig.UserStateDir(env)
	if base == "" {
		return "", false, fmt.Errorf("productmetrics: cannot resolve a state directory (no XDG_STATE_HOME and no home dir)")
	}
	path := filepath.Join(base, installIDRelPath)

	if readFile != nil {
		if data, rerr := readFile(path); rerr == nil {
			if existing := strings.TrimSpace(string(data)); existing != "" {
				if _, perr := uuid.Parse(existing); perr == nil {
					return existing, false, nil
				}
				// Corrupt file: fall through and regenerate.
			}
		}
	}

	fresh := uuid.NewString()
	if mkdirAll != nil {
		if merr := mkdirAll(filepath.Dir(path), 0o700); merr != nil {
			return "", false, fmt.Errorf("productmetrics: create state dir: %w", merr)
		}
	}
	if writeFile != nil {
		if werr := writeFile(path, []byte(fresh), 0o600); werr != nil {
			return "", false, fmt.Errorf("productmetrics: write install id: %w", werr)
		}
	}
	return fresh, true, nil
}

// LoadOrCreateInstallIDDefault binds LoadOrCreateInstallID to the real
// process environment and filesystem.
func LoadOrCreateInstallIDDefault() (id string, firstRun bool, err error) {
	return LoadOrCreateInstallID(xdgconfig.OSEnv, os.ReadFile, os.WriteFile, os.MkdirAll)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestLoadOrCreateInstallID -v`
Expected: PASS (all three tests)

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/productmetrics/installid.go internal/adapter/productmetrics/installid_test.go
git commit -m "feat(productmetrics): persist an anonymous random install id"
```

---

### Task 3: OTLP provider construction via toolhive-core

**Files:**
- Create: `internal/adapter/productmetrics/provider.go`
- Test: `internal/adapter/productmetrics/provider_test.go`
- Modify: `Taskfile.yml:46` (extend `BUILD_LDFLAGS` with the baked-key `-X` flag)

**Interfaces:**
- Consumes: `providers.NewCompositeProvider(ctx, ...ProviderOption) (*providers.CompositeProvider, error)`, `providers.WithServiceName`, `WithServiceVersion`, `WithOTLPEndpoint`, `WithMetricsEnabled`, `WithHeaders`, `WithCustomAttributes` (`github.com/stacklok/toolhive-core/telemetry/providers`, already verified); `Config` from Task 1.
- Produces: `type Provider struct{...}`, `func NewProvider(ctx context.Context, cfg Config) (*Provider, error)`, `func (p *Provider) Meter() metric.MeterProvider`, `func (p *Provider) Shutdown(ctx context.Context) error`.

- [ ] **Step 1: Write the failing test**

```go
package productmetrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewProviderFailsClosedWithNoBakedKey(t *testing.T) {
	orig := bakedKey
	bakedKey = ""
	defer func() { bakedKey = orig }()

	_, err := NewProvider(context.Background(), Config{Binary: BinaryMecated, Version: "test"})
	if err == nil {
		t.Fatal("expected an error when no ingest key is baked into the build, got nil")
	}
}

func TestNewProviderExportsToConfiguredEndpoint(t *testing.T) {
	origKey := bakedKey
	bakedKey = "test-key"
	defer func() { bakedKey = origKey }()

	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get(headerKeyName)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	origEndpoint := endpoint
	endpoint = srv.URL
	defer func() { endpoint = origEndpoint }()

	p, err := NewProvider(context.Background(), Config{
		Binary:    BinaryMecated,
		Version:   "test",
		InstallID: "11111111-1111-1111-1111-111111111111",
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer p.Shutdown(context.Background())

	meter := p.Meter().Meter("test")
	counter, cerr := meter.Int64Counter("mecatl.adoption.test")
	if cerr != nil {
		t.Fatalf("Int64Counter: %v", cerr)
	}
	counter.Add(context.Background(), 1)
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if gotHeader != "test-key" {
		t.Errorf("collector received %s=%q, want %q", headerKeyName, gotHeader, "test-key")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestNewProvider -v`
Expected: FAIL — `NewProvider`/`bakedKey`/`endpoint`/`headerKeyName` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package productmetrics

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/metric"

	"github.com/stacklok/toolhive-core/telemetry/providers"
)

// endpoint and headerKeyName are the ONE destination this pipeline can ever
// send to (stacklok/infra#5604): a dedicated, internet-facing OTLP/HTTP
// ingest at metrics.stacklok.com, gated by a single shared key baked into
// the binary. Neither is operator-configurable — an operator's own
// --otlp-endpoint has zero effect on this path, and this path has zero
// effect on the operator's own OTLP/Prometheus pipeline (a completely
// separate MeterProvider, never installed as global). endpoint is a var
// (not a const) so tests can point it at an httptest server.
var (
	endpoint      = "https://metrics.stacklok.com/v1/metrics"
	headerKeyName = "x-mecatl-metrics-key"
)

// bakedKey is the shared ingest key baked into the binary at build time via
// `-X github.com/stacklok/mecatl/internal/adapter/productmetrics.bakedKey=…`
// (see Taskfile.yml's BUILD_LDFLAGS). An empty key — every local/dev/CI-test
// build that does not set the ldflag — disables the pipeline entirely:
// NewProvider refuses to construct, so a non-release build can never
// accidentally phone home with an invalid or absent key.
var bakedKey = ""

// Provider wraps the toolhive-core OTLP metrics provider. Its MeterProvider
// is NEVER installed as the process-global provider (mirrors
// internal/adapter/telemetry's own discipline in otlp.go), so it cannot
// collide with an operator's own OTel setup.
type Provider struct {
	composite *providers.CompositeProvider
}

// NewProvider builds the product-metrics MeterProvider for one process. A
// network-unreachable endpoint is NOT an error here — the OTLP/HTTP
// exporter dials lazily on first export, matching the existing exporters in
// internal/adapter/telemetry/otlp.go.
func NewProvider(ctx context.Context, cfg Config) (*Provider, error) {
	if bakedKey == "" {
		return nil, fmt.Errorf("productmetrics: no ingest key baked into this build (see BUILD_LDFLAGS in Taskfile.yml)")
	}
	composite, err := providers.NewCompositeProvider(ctx,
		providers.WithServiceName("mecatl"),
		providers.WithServiceVersion(cfg.Version),
		providers.WithOTLPEndpoint(endpoint),
		providers.WithMetricsEnabled(true),
		providers.WithHeaders(map[string]string{headerKeyName: bakedKey}),
		providers.WithCustomAttributes(map[string]string{
			"mecatl.install.id": cfg.InstallID,
			"mecatl.binary":     string(cfg.Binary),
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("productmetrics: build provider: %w", err)
	}
	return &Provider{composite: composite}, nil
}

// Meter returns the underlying metric.MeterProvider for instrument construction.
func (p *Provider) Meter() metric.MeterProvider { return p.composite.MeterProvider() }

// Shutdown flushes and stops the provider, bounded by the caller's ctx.
func (p *Provider) Shutdown(ctx context.Context) error { return p.composite.Shutdown(ctx) }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestNewProvider -v`
Expected: PASS (both tests)

- [ ] **Step 5: Extend the release build's ldflags**

In `Taskfile.yml`, change line 46 from:

```yaml
  BUILD_LDFLAGS: '-X github.com/stacklok/mecatl/internal/buildinfo.BuildID={{.BUILD_ID}}'
```

to:

```yaml
  BUILD_LDFLAGS: '-X github.com/stacklok/mecatl/internal/buildinfo.BuildID={{.BUILD_ID}} -X github.com/stacklok/mecatl/internal/adapter/productmetrics.bakedKey={{.MECATL_METRICS_KEY}}'
```

`MECATL_METRICS_KEY` is an env-driven Taskfile variable (empty for every local/dev build — the byte-identical "disabled" posture from Step 3 — set only by the release CI job from a repo secret). Add near the top of `Taskfile.yml` alongside the existing `vars:` block:

```yaml
  MECATL_METRICS_KEY: '{{.MECATL_METRICS_KEY | default ""}}'
```

- [ ] **Step 6: Run the full build to confirm it still compiles with an empty key**

Run: `task build`
Expected: succeeds; `bin/mecated` etc. are built with `bakedKey=""` (unchanged local-dev behavior).

- [ ] **Step 7: Commit**

```bash
git add internal/adapter/productmetrics/provider.go internal/adapter/productmetrics/provider_test.go Taskfile.yml
git commit -m "feat(productmetrics): build the OTLP provider via toolhive-core, baked-key gated"
```

---

### Task 4: `Recorder` — `port.EventSink` (sessions/runs/tokens)

**Files:**
- Create: `internal/adapter/productmetrics/metrics.go`
- Test: `internal/adapter/productmetrics/metrics_test.go`

**Interfaces:**
- Consumes: `metric.MeterProvider` (Task 3's `Provider.Meter()`), `session.Event`/`session.EventType`/`session.ResultPayload`/`session.StopReason`/`session.Usage` (`engine/session`, already verified), `port.EventSink` (`engine/port`).
- Produces: `type Recorder struct{...}`, `func NewRecorder(mp metric.MeterProvider) (*Recorder, error)`, `func (r *Recorder) Emit(ctx context.Context, ev session.Event)` (satisfies `port.EventSink`).

- [ ] **Step 1: Write the failing test**

```go
package productmetrics

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stacklok/mecatl/engine/session"
)

func newTestRecorder(t *testing.T) (*Recorder, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	r, err := NewRecorder(mp)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	return r, reader
}

func collect(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Aggregation {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	out := make(map[string]metricdata.Aggregation)
	for _, sm := range rm.ScopeMetrics {
		for _, md := range sm.Metrics {
			out[md.Name] = md.Data
		}
	}
	return out
}

func sumValue(t *testing.T, agg metricdata.Aggregation) int64 {
	t.Helper()
	sum, ok := agg.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("aggregation is %T, want Sum[int64]", agg)
	}
	var total int64
	for _, dp := range sum.DataPoints {
		total += dp.Value
	}
	return total
}

func sumPoint(t *testing.T, agg metricdata.Aggregation, key, value string) int64 {
	t.Helper()
	sum, ok := agg.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("aggregation is %T, want Sum[int64]", agg)
	}
	for _, dp := range sum.DataPoints {
		if v, present := dp.Attributes.Value(attribute.Key(key)); present && v.AsString() == value {
			return dp.Value
		}
	}
	t.Fatalf("no data point with %s=%q", key, value)
	return 0
}

func TestRecorderEmitSessionsStarted(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	r.Emit(context.Background(), session.Event{Type: session.EvSessionInit})

	agg, ok := collect(t, reader)["mecatl.adoption.sessions_started"]
	if !ok {
		t.Fatal("mecatl.adoption.sessions_started missing")
	}
	if got := sumValue(t, agg); got != 2 {
		t.Errorf("sessions_started = %d, want 2", got)
	}
}

func TestRecorderEmitRunsCompletedByStopReason(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.Emit(context.Background(), session.Event{
		Type:   session.EvResult,
		Result: &session.ResultPayload{Stop: session.StopEndTurn},
	})
	r.Emit(context.Background(), session.Event{
		Type:   session.EvResult,
		Result: &session.ResultPayload{Stop: session.StopError},
	})

	agg := collect(t, reader)["mecatl.adoption.runs_completed"]
	if got := sumPoint(t, agg, "stop", "end_turn"); got != 1 {
		t.Errorf("runs_completed{stop=end_turn} = %d, want 1", got)
	}
	if got := sumPoint(t, agg, "stop", "error"); got != 1 {
		t.Errorf("runs_completed{stop=error} = %d, want 1", got)
	}
}

func TestRecorderEmitTokensByKind(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.Emit(context.Background(), session.Event{
		Type: session.EvResult,
		Result: &session.ResultPayload{
			Stop: session.StopEndTurn,
			Usage: session.Usage{
				InputTokens:      100,
				OutputTokens:     50,
				CacheReadTokens:  20,
				CacheWriteTokens: 5,
				ReasoningTokens:  10,
			},
		},
	})

	agg := collect(t, reader)["mecatl.adoption.tokens"]
	cases := map[string]int64{"input": 100, "output": 50, "cache_read": 20, "cache_write": 5, "reasoning": 10}
	for kind, want := range cases {
		if got := sumPoint(t, agg, "kind", kind); got != want {
			t.Errorf("tokens{kind=%s} = %d, want %d", kind, got, want)
		}
	}
}

func TestRecorderEmitSubagentAndTeamUsed(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.Emit(context.Background(), session.Event{Type: session.EvSubagentStart})
	r.Emit(context.Background(), session.Event{Type: session.EvTeamStart})

	collected := collect(t, reader)
	if got := sumValue(t, collected["mecatl.adoption.subagent_used"]); got != 1 {
		t.Errorf("subagent_used = %d, want 1", got)
	}
	if got := sumValue(t, collected["mecatl.adoption.team_used"]); got != 1 {
		t.Errorf("team_used = %d, want 1", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestRecorderEmit -v`
Expected: FAIL — `Recorder`/`NewRecorder` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package productmetrics

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// meterName is the instrumentation scope name for this package's meter.
const meterName = "github.com/stacklok/mecatl/internal/adapter/productmetrics"

// Attribute keys. Every value ever attached under these keys is drawn from a
// bounded closed set (session.StopReason, the fixed token-kind strings, or
// this package's own Feature/ProviderFamily/DeploymentMode enums) — never a
// session id, model id, tool name, or free text.
const (
	attrStop     = "stop"
	attrKind     = "kind"
	attrFeature  = "feature"
	attrProvider = "family"
	attrMode     = "mode"
)

// Recorder is the product-metrics adapter: it implements port.EventSink
// (this file) and port.ToolCallRecorder (toolcall.go), deriving ONLY the
// bounded counts in the design's catalog. It never reads a tool name,
// session id, model id, or any free-text field.
type Recorder struct {
	heartbeat       metric.Int64Counter
	featureEnabled  metric.Int64Counter
	providerConfig  metric.Int64Counter
	deploymentMode  metric.Int64Counter
	sessionsStarted metric.Int64Counter
	runsCompleted   metric.Int64Counter
	toolCalls       metric.Int64Counter
	tokens          metric.Int64Counter
	subagentUsed    metric.Int64Counter
	teamUsed        metric.Int64Counter
}

// Compile-time interface checks.
var (
	_ port.EventSink        = (*Recorder)(nil)
	_ port.ToolCallRecorder = (*Recorder)(nil)
)

// NewRecorder constructs every instrument from the given MeterProvider. It
// returns an error if any instrument fails to construct — the OTel meter API
// is fallible.
func NewRecorder(mp metric.MeterProvider) (*Recorder, error) {
	meter := mp.Meter(meterName)
	r := &Recorder{}
	var err error

	if r.heartbeat, err = meter.Int64Counter("mecatl.adoption.heartbeat",
		metric.WithDescription("Process liveness heartbeat.")); err != nil {
		return nil, fmt.Errorf("productmetrics: heartbeat counter: %w", err)
	}
	if r.featureEnabled, err = meter.Int64Counter("mecatl.adoption.feature_enabled",
		metric.WithDescription("Major feature enabled, by closed feature name, per heartbeat.")); err != nil {
		return nil, fmt.Errorf("productmetrics: feature_enabled counter: %w", err)
	}
	if r.providerConfig, err = meter.Int64Counter("mecatl.adoption.provider_configured",
		metric.WithDescription("Configured LLM provider family, by closed family name, per heartbeat.")); err != nil {
		return nil, fmt.Errorf("productmetrics: provider_configured counter: %w", err)
	}
	if r.deploymentMode, err = meter.Int64Counter("mecatl.adoption.deployment_mode",
		metric.WithDescription("Process deployment mode, by closed mode name, per heartbeat.")); err != nil {
		return nil, fmt.Errorf("productmetrics: deployment_mode counter: %w", err)
	}
	if r.sessionsStarted, err = meter.Int64Counter("mecatl.adoption.sessions_started",
		metric.WithDescription("Total sessions started.")); err != nil {
		return nil, fmt.Errorf("productmetrics: sessions_started counter: %w", err)
	}
	if r.runsCompleted, err = meter.Int64Counter("mecatl.adoption.runs_completed",
		metric.WithDescription("Total runs completed, by bounded stop reason.")); err != nil {
		return nil, fmt.Errorf("productmetrics: runs_completed counter: %w", err)
	}
	if r.toolCalls, err = meter.Int64Counter("mecatl.adoption.tool_calls",
		metric.WithDescription("Total tool calls executed (no tool identity attached).")); err != nil {
		return nil, fmt.Errorf("productmetrics: tool_calls counter: %w", err)
	}
	if r.tokens, err = meter.Int64Counter("mecatl.adoption.tokens",
		metric.WithDescription("Total tokens accounted, by bounded kind."),
		metric.WithUnit("{token}")); err != nil {
		return nil, fmt.Errorf("productmetrics: tokens counter: %w", err)
	}
	if r.subagentUsed, err = meter.Int64Counter("mecatl.adoption.subagent_used",
		metric.WithDescription("Runs that used the Subagent delegation family at least once.")); err != nil {
		return nil, fmt.Errorf("productmetrics: subagent_used counter: %w", err)
	}
	if r.teamUsed, err = meter.Int64Counter("mecatl.adoption.team_used",
		metric.WithDescription("Runs that used the Team delegation family at least once.")); err != nil {
		return nil, fmt.Errorf("productmetrics: team_used counter: %w", err)
	}
	return r, nil
}

// Emit derives coarse, bounded counts from a single domain Event. It reads
// ONLY ev.Type, ev.Result.Stop, and ev.Result.Usage — never a session id,
// model id/alias, tool name, or any free-text field (ev.Result.Text/Error
// are never touched).
func (r *Recorder) Emit(ctx context.Context, ev session.Event) {
	switch ev.Type {
	case session.EvSessionInit:
		r.sessionsStarted.Add(ctx, 1)
	case session.EvResult:
		r.recordResult(ctx, ev.Result)
	case session.EvSubagentStart:
		r.subagentUsed.Add(ctx, 1)
	case session.EvTeamStart:
		r.teamUsed.Add(ctx, 1)
	}
}

func (r *Recorder) recordResult(ctx context.Context, res *session.ResultPayload) {
	if res == nil {
		r.runsCompleted.Add(ctx, 1, metric.WithAttributes(attribute.String(attrStop, string(session.StopNone))))
		return
	}
	r.runsCompleted.Add(ctx, 1, metric.WithAttributes(attribute.String(attrStop, string(res.Stop))))
	u := res.Usage
	r.tokens.Add(ctx, int64(u.InputTokens), metric.WithAttributes(attribute.String(attrKind, "input")))
	r.tokens.Add(ctx, int64(u.OutputTokens), metric.WithAttributes(attribute.String(attrKind, "output")))
	r.tokens.Add(ctx, int64(u.CacheReadTokens), metric.WithAttributes(attribute.String(attrKind, "cache_read")))
	r.tokens.Add(ctx, int64(u.CacheWriteTokens), metric.WithAttributes(attribute.String(attrKind, "cache_write")))
	r.tokens.Add(ctx, int64(u.ReasoningTokens), metric.WithAttributes(attribute.String(attrKind, "reasoning")))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestRecorderEmit -v`
Expected: PASS (all four tests)

- [ ] **Step 5: Commit**

```bash
git add internal/adapter/productmetrics/metrics.go internal/adapter/productmetrics/metrics_test.go
git commit -m "feat(productmetrics): Recorder.Emit derives bounded session/run/token counts"
```

---

### Task 5: `Recorder` — `port.ToolCallRecorder` and heartbeat

**Files:**
- Create: `internal/adapter/productmetrics/toolcall.go`
- Create: `internal/adapter/productmetrics/heartbeat.go`
- Test: `internal/adapter/productmetrics/toolcall_test.go`
- Test: `internal/adapter/productmetrics/heartbeat_test.go`

**Interfaces:**
- Consumes: `port.ToolCallRecorder.ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration)` (verified signature); `Recorder` from Task 4; `FeatureSnapshot` from Task 1.
- Produces: `func (r *Recorder) ToolCall(session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration)`; `func (r *Recorder) Heartbeat(snap FeatureSnapshot)`; `const DefaultHeartbeatInterval = 24 * time.Hour`; `func RunHeartbeat(ctx context.Context, r *Recorder, interval time.Duration, snap FeatureSnapshot)`.

- [ ] **Step 1: Write the failing test (ToolCall)**

```go
package productmetrics

import (
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestRecorderToolCallCountsWithoutIdentity(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.ToolCall(
		session.SessionID("sensitive-session-id"),
		session.ToolCall{Name: "read_secret_file"},
		session.ToolResult{Content: "super secret content", IsError: true},
		10*time.Millisecond, 20*time.Millisecond,
	)
	r.ToolCall(session.SessionID("other"), session.ToolCall{Name: "another_tool"}, session.ToolResult{}, 0, 0)

	agg := collect(t, reader)["mecatl.adoption.tool_calls"]
	if got := sumValue(t, agg); got != 2 {
		t.Errorf("tool_calls = %d, want 2", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestRecorderToolCallCountsWithoutIdentity -v`
Expected: FAIL — `Recorder.ToolCall` undefined.

- [ ] **Step 3: Write minimal implementation (toolcall.go)**

```go
package productmetrics

import (
	"context"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// ToolCall records ONLY that a tool call happened — no tool name, no
// session id, no result content, no duration. It satisfies
// port.ToolCallRecorder. The three typed parameters it ignores (id, call,
// result) are accepted only because the port's signature requires them; not
// one of their fields is ever read.
func (r *Recorder) ToolCall(_ session.SessionID, _ session.ToolCall, _ session.ToolResult, _, _ time.Duration) {
	r.toolCalls.Add(context.Background(), 1)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestRecorderToolCallCountsWithoutIdentity -v`
Expected: PASS

- [ ] **Step 5: Write the failing test (Heartbeat)**

```go
package productmetrics

import (
	"context"
	"testing"
	"time"
)

func TestRecorderHeartbeatRecordsClosedLabelsOnly(t *testing.T) {
	r, reader := newTestRecorder(t)
	r.Heartbeat(FeatureSnapshot{
		Memory: true, MCP: true,
		Provider: ProviderAnthropic, Mode: ModeInteractive,
	})

	collected := collect(t, reader)
	if got := sumValue(t, collected["mecatl.adoption.heartbeat"]); got != 1 {
		t.Errorf("heartbeat = %d, want 1", got)
	}
	featureAgg := collected["mecatl.adoption.feature_enabled"]
	if got := sumPoint(t, featureAgg, "feature", "memory"); got != 1 {
		t.Errorf("feature_enabled{feature=memory} = %d, want 1", got)
	}
	if got := sumPoint(t, featureAgg, "feature", "mcp"); got != 1 {
		t.Errorf("feature_enabled{feature=mcp} = %d, want 1", got)
	}
	// guardrails/scheduling were false in the snapshot: TestRecorderNeverAttachesUnboundedAttributesOrSensitiveContent
	// (Task 6) is the exhaustive "no other data point exists" check; this test
	// only asserts the enabled ones are present with the right value.
	if got := sumPoint(t, collected["mecatl.adoption.provider_configured"], "family", "anthropic"); got != 1 {
		t.Errorf("provider_configured{family=anthropic} = %d, want 1", got)
	}
	if got := sumPoint(t, collected["mecatl.adoption.deployment_mode"], "mode", "interactive"); got != 1 {
		t.Errorf("deployment_mode{mode=interactive} = %d, want 1", got)
	}
}

func TestRunHeartbeatFiresImmediatelyThenStopsOnCtxDone(t *testing.T) {
	r, reader := newTestRecorder(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled BEFORE RunHeartbeat: only the immediate fire happens.

	RunHeartbeat(ctx, r, time.Hour, FeatureSnapshot{Mode: ModeHeadless})

	if got := sumValue(t, collect(t, reader)["mecatl.adoption.heartbeat"]); got != 1 {
		t.Errorf("heartbeat = %d, want exactly 1 (immediate fire only)", got)
	}
}
```

- [ ] **Step 6: Run test to verify it fails**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestRecorderHeartbeat -v` and `-run TestRunHeartbeat`
Expected: FAIL — `Heartbeat`/`RunHeartbeat` undefined.

- [ ] **Step 7: Write minimal implementation (heartbeat.go)**

```go
package productmetrics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// DefaultHeartbeatInterval is the steady-state heartbeat cadence for
// long-running processes (mecated, mecatui, mecak8s). mecatequi (short-lived)
// passes 0 — a single immediate fire only, no ticker.
const DefaultHeartbeatInterval = 24 * time.Hour

// Heartbeat records the periodic liveness + feature/provider/mode signal.
// Every attribute value comes from the closed Feature/ProviderFamily/
// DeploymentMode enums — never a def/model/tool name.
func (r *Recorder) Heartbeat(snap FeatureSnapshot) {
	ctx := context.Background()
	r.heartbeat.Add(ctx, 1)
	for feature, on := range snap.enabled() {
		if on {
			r.featureEnabled.Add(ctx, 1, metric.WithAttributes(attribute.String(attrFeature, string(feature))))
		}
	}
	r.providerConfig.Add(ctx, 1, metric.WithAttributes(attribute.String(attrProvider, string(snap.Provider))))
	r.deploymentMode.Add(ctx, 1, metric.WithAttributes(attribute.String(attrMode, string(snap.Mode))))
}

// RunHeartbeat fires one heartbeat immediately, then one every interval,
// until ctx is done. interval<=0 disables the ticker (a single fire only —
// mecatequi's shape). Meant to run in its own goroutine, owned by the
// caller (composition), which cancels ctx on shutdown.
func RunHeartbeat(ctx context.Context, r *Recorder, interval time.Duration, snap FeatureSnapshot) {
	r.Heartbeat(snap)
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Heartbeat(snap)
		}
	}
}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `cd internal/adapter/productmetrics && go test ./... -v`
Expected: PASS (every test in the package so far)

- [ ] **Step 9: Commit**

```bash
git add internal/adapter/productmetrics/toolcall.go internal/adapter/productmetrics/heartbeat.go \
        internal/adapter/productmetrics/toolcall_test.go internal/adapter/productmetrics/heartbeat_test.go
git commit -m "feat(productmetrics): ToolCall counting and the heartbeat ticker"
```

---

### Task 6: Privacy guard test — exhaustive bounded-attribute check

**Files:**
- Create: `internal/adapter/productmetrics/bounded_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1, 4, 5 (`Recorder`, `FeatureSnapshot`, closed enums).
- Produces: no new production code — a test-only safety net that fails CI the moment a future change attaches an unbounded attribute or an unlisted key.

This test is the automated version of the design doc's privacy invariant: it drives the Recorder with values chosen to look like they'd leak something sensitive if the code were wrong (a "secret"-looking session id, a suspicious tool name, free-text tool output, an odd stop reason), then asserts across *every* collected data point of *every* instrument that (a) every attribute key is in a fixed allowlist, and (b) no attribute value or metric name contains any of the injected "sensitive" substrings.

- [ ] **Step 1: Write the test**

```go
package productmetrics

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stacklok/mecatl/engine/session"
)

// allowedAttributeKeys is the COMPLETE set of attribute keys any instrument
// in this package may ever carry. A future change that attaches a new label
// must add it here explicitly — the same "closed set is a reviewed
// decision" discipline as internal/adapter/telemetry's attrRole.
var allowedAttributeKeys = map[string]bool{
	attrStop:     true,
	attrKind:     true,
	attrFeature:  true,
	attrProvider: true,
	attrMode:     true,
}

// sensitiveMarkers are strings injected into every field the Recorder must
// NEVER read. If any of these ever shows up in a collected metric name or
// attribute value, something started reading a field it shouldn't.
var sensitiveMarkers = []string{
	"sensitive-session-id-marker",
	"secret-tool-name-marker",
	"secret-tool-content-marker",
	"secret-error-text-marker",
}

func TestRecorderNeverAttachesUnboundedAttributesOrSensitiveContent(t *testing.T) {
	r, reader := newTestRecorder(t)

	// Drive every observation path with deliberately sensitive-looking data.
	r.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	r.Emit(context.Background(), session.Event{
		Type: session.EvResult,
		Result: &session.ResultPayload{
			Stop:  session.StopError,
			Text:  "sensitive-session-id-marker should never be read",
			Error: "secret-error-text-marker: connection to 10.0.0.5 failed",
			Usage: session.Usage{InputTokens: 1, OutputTokens: 1},
		},
	})
	r.Emit(context.Background(), session.Event{Type: session.EvSubagentStart})
	r.Emit(context.Background(), session.Event{Type: session.EvTeamStart})
	r.ToolCall(
		session.SessionID("sensitive-session-id-marker"),
		session.ToolCall{Name: "secret-tool-name-marker"},
		session.ToolResult{Content: "secret-tool-content-marker", IsError: true},
		10*time.Millisecond, 20*time.Millisecond,
	)
	r.Heartbeat(FeatureSnapshot{
		Memory: true, Guardrails: true, MCP: true, Scheduling: true,
		Provider: ProviderOther, Mode: ModeK8s,
	})

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	for _, sm := range rm.ScopeMetrics {
		for _, md := range sm.Metrics {
			assertNoSensitiveSubstring(t, md.Name)
			sum, ok := md.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %s: aggregation is %T, want Sum[int64]", md.Name, md.Data)
			}
			for _, dp := range sum.DataPoints {
				iter := dp.Attributes.Iter()
				for iter.Next() {
					kv := iter.Attribute()
					key := string(kv.Key)
					if !allowedAttributeKeys[key] {
						t.Errorf("metric %s carries attribute key %q, not in allowedAttributeKeys", md.Name, key)
					}
					assertNoSensitiveSubstring(t, kv.Value.AsString())
				}
			}
			// mecatl.adoption.tool_calls carries NO attributes at all — the
			// strongest form of "no tool identity ever attaches."
			if md.Name == "mecatl.adoption.tool_calls" {
				for _, dp := range sum.DataPoints {
					if dp.Attributes.Len() != 0 {
						t.Errorf("mecatl.adoption.tool_calls data point carries %d attributes, want 0: %v",
							dp.Attributes.Len(), dp.Attributes)
					}
				}
			}
		}
	}
}

func assertNoSensitiveSubstring(t *testing.T, s string) {
	t.Helper()
	for _, marker := range sensitiveMarkers {
		if strings.Contains(s, marker) {
			t.Errorf("value %q contains sensitive marker %q", s, marker)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails or passes**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestRecorderNeverAttachesUnboundedAttributesOrSensitiveContent -v`
Expected: PASS immediately (Tasks 4-5's implementation already satisfies it) — this task adds no production code, only the safety net. If it fails, the failure output names exactly which instrument/attribute leaked; fix `metrics.go`/`toolcall.go`/`heartbeat.go` until it passes, never loosen this test.

- [ ] **Step 3: Commit**

```bash
git add internal/adapter/productmetrics/bounded_test.go
git commit -m "test(productmetrics): exhaustive guard against unbounded/sensitive attributes"
```

---

### Task 7: `permconfig` schema — `telemetry:` operator subtree

**Files:**
- Modify: `internal/adapter/permconfig/schema.go` (add `Telemetry *TelemetrySection` field to the top-level `Config` struct near line 154's `OpenRouter` field, plus the new `TelemetrySection`/`ProductMetricsSection` types near line 1202's `OpenRouterSection`)
- Test: `internal/adapter/permconfig/telemetry_schema_test.go`

**Interfaces:**
- Produces: `type TelemetrySection struct{ ProductMetrics *ProductMetricsSection }`, `type ProductMetricsSection struct{ Enabled *bool }`, both with strict `UnmarshalYAML` (mirroring `OpenRouterSection`, already verified).

- [ ] **Step 1: Write the failing test**

```go
package permconfig

import "testing"

func TestParseYAMLTelemetryProductMetricsEnabled(t *testing.T) {
	data := []byte("telemetry:\n  productMetrics:\n    enabled: false\n")
	cfg, err := parseYAML(data)
	if err != nil {
		t.Fatalf("parseYAML: %v", err)
	}
	if cfg.Telemetry == nil || cfg.Telemetry.ProductMetrics == nil {
		t.Fatal("Telemetry.ProductMetrics is nil")
	}
	if cfg.Telemetry.ProductMetrics.Enabled == nil || *cfg.Telemetry.ProductMetrics.Enabled != false {
		t.Errorf("Enabled = %v, want explicit false", cfg.Telemetry.ProductMetrics.Enabled)
	}
}

func TestParseYAMLTelemetryUnknownKeyErrors(t *testing.T) {
	data := []byte("telemetry:\n  productmetric:\n    enabled: false\n") // typo: productmetric
	if _, err := parseYAML(data); err == nil {
		t.Fatal("expected a strict-parse error for the unknown telemetry.productmetric key, got nil")
	}
}

func TestParseYAMLTelemetryProductMetricsUnknownKeyErrors(t *testing.T) {
	data := []byte("telemetry:\n  productMetrics:\n    enable: false\n") // typo: enable
	if _, err := parseYAML(data); err == nil {
		t.Fatal("expected a strict-parse error for the unknown enable key, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd internal/adapter/permconfig && go test ./... -run TestParseYAMLTelemetry -v`
Expected: FAIL — `Config.Telemetry` undefined.

- [ ] **Step 3: Add the field to the top-level `Config` struct**

In `internal/adapter/permconfig/schema.go`, immediately after the `OpenRouter *OpenRouterSection` field (the one ending around line 154), add:

```go
	// Telemetry holds the OPERATOR-TIER `telemetry:` subtree (opt-out product/
	// adoption metrics). Like OpenRouter/Guardrails/Posture it is honoured ONLY
	// from the user-global + CLI tiers; a project-tier file's telemetry: block
	// is IGNORED with a WARN (a project repo cannot flip a user's own telemetry
	// choice in either direction). Parsed STRICTLY (unknown keys error). A nil
	// Telemetry means the key was absent — composition then falls through the
	// DO_NOT_TRACK env var and finally defaults to enabled.
	Telemetry *TelemetrySection `yaml:"telemetry"`
```

- [ ] **Step 4: Add the new section types**

In `internal/adapter/permconfig/schema.go`, immediately after the `OpenRouterSection`/`OpenRouterModelRoute` block (after the code ending around line 1249), add:

```go
// TelemetrySection is the `telemetry:` operator-tier YAML subtree: the opt-out
// switch for community/adoption product metrics. Parsed STRICTLY (unknown
// keys error), mirroring OpenRouterSection/GuardrailsSection.
type TelemetrySection struct {
	// ProductMetrics is the opt-out product/adoption metrics config.
	ProductMetrics *ProductMetricsSection `yaml:"productMetrics"`
}

func (s *TelemetrySection) strictFields() map[string]any {
	return map[string]any{
		"productMetrics": &s.ProductMetrics,
	}
}

// UnmarshalYAML decodes the telemetry: mapping STRICTLY: an unknown key
// (e.g. a typo'd product-metrics:) is a parse error, same discipline as
// openrouter:/guardrails:.
func (s *TelemetrySection) UnmarshalYAML(node ast.Node) error {
	return decodeStrictMapping(node, "telemetry", s.strictFields())
}

// ProductMetricsSection is the `telemetry.productMetrics:` subtree.
type ProductMetricsSection struct {
	// Enabled is a *bool so ABSENT (nil) is distinguishable from an explicit
	// false: nil = absent (composition falls through to DO_NOT_TRACK then the
	// enabled-by-default posture); a non-nil value is honoured exactly.
	Enabled *bool `yaml:"enabled"`
}

func (s *ProductMetricsSection) strictFields() map[string]any {
	return map[string]any{
		"enabled": newPermconfigNodePointer(&s.Enabled),
	}
}

// UnmarshalYAML decodes the productMetrics: mapping STRICTLY.
func (s *ProductMetricsSection) UnmarshalYAML(node ast.Node) error {
	return decodeStrictMapping(node, "telemetry.productMetrics", s.strictFields())
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd internal/adapter/permconfig && go test ./... -run TestParseYAMLTelemetry -v`
Expected: PASS (all three tests)

- [ ] **Step 6: Commit**

```bash
git add internal/adapter/permconfig/schema.go internal/adapter/permconfig/telemetry_schema_test.go
git commit -m "feat(permconfig): add the strict telemetry.productMetrics: operator schema"
```

---

### Task 8: `permconfig` resolver — operator-tier-only capture + project-tier WARN-ignore

**Files:**
- Modify: `internal/adapter/permconfig/resolve.go` (Resolver struct field near `operatorOpenRouter` (~line 197), capture method near `captureOpenRouter` (~line 1082), accessor near `OperatorOpenRouter` (~line 388), call sites at both `loadUserRules` locations (~line 907 and ~line 946), and the project-tier WARN block inside `loadProjectRules` (~line 672))
- Test: `internal/adapter/permconfig/telemetry_resolve_test.go`

**Interfaces:**
- Produces: `func (r *Resolver) OperatorProductMetricsEnabled() *bool` (nil = absent — the SOLE accessor composition uses).

- [ ] **Step 1: Write the failing test**

```go
package permconfig

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestOperatorProductMetricsEnabledFromUserGlobal(t *testing.T) {
	env := fakeEnv(t, map[string]string{
		"mecatl/settings.yaml": "telemetry:\n  productMetrics:\n    enabled: false\n",
	})
	r := NewResolver(Options{Conventional: true}, env, nil)
	got := r.OperatorProductMetricsEnabled()
	if got == nil || *got != false {
		t.Fatalf("OperatorProductMetricsEnabled() = %v, want explicit false", got)
	}
}

func TestOperatorProductMetricsEnabledAbsentIsNil(t *testing.T) {
	env := fakeEnv(t, map[string]string{})
	r := NewResolver(Options{Conventional: true}, env, nil)
	if got := r.OperatorProductMetricsEnabled(); got != nil {
		t.Fatalf("OperatorProductMetricsEnabled() = %v, want nil (absent)", got)
	}
}

func TestProjectTierTelemetryBlockIsIgnoredWithWarn(t *testing.T) {
	ws := memfs.NewWorkspace(t.TempDir())
	if err := ws.Write(context.Background(), ".mecatl/settings.yaml",
		[]byte("telemetry:\n  productMetrics:\n    enabled: false\n")); err != nil {
		t.Fatalf("seed project file: %v", err)
	}
	r := NewResolver(Options{Conventional: true}, fakeEnv(t, nil), nil)
	if _, _, err := r.Rules(context.Background(), ws.(tool.WorkspaceReader)); err != nil {
		t.Fatalf("Rules: %v", err)
	}
	// A project-tier telemetry: block must NEVER be captured as operator config.
	if got := r.OperatorProductMetricsEnabled(); got != nil {
		t.Errorf("OperatorProductMetricsEnabled() = %v after a PROJECT-tier block, want nil (project-tier is ignored)", got)
	}
}
```

Note: `fakeEnv`/`NewResolver`/`r.Rules` signatures above must match this package's existing test helpers exactly — before writing this test, read one existing resolver test (e.g. the file containing `TestOperatorOpenRouterFromUserGlobal`-shaped tests, likely `internal/adapter/permconfig/resolve_test.go` or an `openrouter_test.go` sibling) and copy its exact `fakeEnv`/construction idiom rather than inventing a new one.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd internal/adapter/permconfig && go test ./... -run TestOperatorProductMetricsEnabled -v` and `-run TestProjectTierTelemetryBlockIsIgnoredWithWarn`
Expected: FAIL — `OperatorProductMetricsEnabled` undefined.

- [ ] **Step 3: Add the Resolver field**

In `internal/adapter/permconfig/resolve.go`, immediately after the `operatorOpenRouter *OpenRouterSection` field, add:

```go
	// operatorTelemetry is the OPERATOR-TIER telemetry: subtree, read ONCE at
	// construction from the user-global + CLI tiers ONLY (the SOLE capture path
	// is captureTelemetry from loadUserRules — mirroring captureOpenRouter). A
	// project-tier file's telemetry: block is IGNORED with a WARN in
	// loadProjectRules. nil when no operator-tier file carried a telemetry:
	// section. CLI (explicit files) out-ranks user-global (first-non-nil keeps
	// CLI).
	operatorTelemetry *TelemetrySection
```

- [ ] **Step 4: Add the capture method**

Immediately after `captureOpenRouter`, add:

```go
// captureTelemetry records the FIRST operator-tier telemetry: block seen
// during loadUserRules (CLI files out-rank user-global, so first-non-nil
// keeps CLI). Mirrors captureOpenRouter.
func (r *Resolver) captureTelemetry(s *TelemetrySection) {
	if s == nil || r.operatorTelemetry != nil {
		return
	}
	r.operatorTelemetry = s
}
```

- [ ] **Step 5: Add the accessor**

Immediately after `OperatorOpenRouter`, add:

```go
// OperatorProductMetricsEnabled returns the operator-tier
// telemetry.productMetrics.enabled: value (user-global + CLI only), or nil
// when none was configured. It is the SOLE accessor composition uses to
// read the product-metrics opt-out from config — by construction it never
// returns a project-tier value (a project telemetry: block is ignored with
// a WARN in loadProjectRules). nil-safe. Mirrors OperatorOpenRouter().
func (r *Resolver) OperatorProductMetricsEnabled() *bool {
	if r == nil || r.operatorTelemetry == nil || r.operatorTelemetry.ProductMetrics == nil {
		return nil
	}
	return r.operatorTelemetry.ProductMetrics.Enabled
}
```

- [ ] **Step 6: Wire the two `loadUserRules` capture call sites**

At both locations identified in the Files list (immediately after each existing `r.captureOpenRouter(cfg.OpenRouter)` line — one in the explicit-CLI-files branch, one in the user-global-YAML branch), add:

```go
				// Operator-tier telemetry (opt-out product metrics): same
				// first-non-nil-keeps-CLI discipline as openrouter.
				r.captureTelemetry(cfg.Telemetry)
```

- [ ] **Step 7: Wire the project-tier WARN-ignore in `loadProjectRules`**

Immediately after the existing `if cfg.OpenRouter != nil { ... }` WARN block inside `loadProjectRules`, add:

```go
		if cfg.Telemetry != nil {
			r.diag.Log(context.Background(), port.LevelWarn,
				"telemetry: IGNORING a project-tier telemetry: block (operator-tier only — a project repo cannot change a user's own product-metrics opt-out in either direction; set telemetry in your user-global settings.yaml)",
				"file", src.path, "root", ws.Root())
		}
```

- [ ] **Step 8: Run tests to verify they pass**

Run: `cd internal/adapter/permconfig && go test ./... -run TestOperatorProductMetricsEnabled -v` and `-run TestProjectTierTelemetryBlockIsIgnoredWithWarn`
Expected: PASS

- [ ] **Step 9: Run the full permconfig suite to catch any regression**

Run: `cd internal/adapter/permconfig && go test ./...`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/adapter/permconfig/resolve.go internal/adapter/permconfig/telemetry_resolve_test.go
git commit -m "feat(permconfig): resolve the operator-tier telemetry.productMetrics opt-out"
```

---

### Task 9: `cliconfig` — opt-out precedence + `ToolCallRecorder` fan-out

**Files:**
- Create: `internal/cliconfig/productmetrics_config.go`
- Test: `internal/cliconfig/productmetrics_config_test.go`

**Interfaces:**
- Consumes: `port.EventSink`, `port.ToolCallRecorder`, `session.SessionID`/`session.ToolCall`/`session.ToolResult` (`engine/port`, `engine/session`).
- Produces: `type ProductMetricsPrecedence struct{ FlagSet, FlagValue bool; Getenv func(string) string; SettingsEnabled *bool }`, `func ResolveProductMetricsEnabled(p ProductMetricsPrecedence) bool`, `func TeeToolCallRecorder(recorders ...port.ToolCallRecorder) port.ToolCallRecorder`.

- [ ] **Step 1: Write the failing test**

```go
package cliconfig

import "testing"

func boolPtr(b bool) *bool { return &b }

func TestResolveProductMetricsEnabledPrecedence(t *testing.T) {
	getenvSet := func(string) string { return "1" }
	getenvUnset := func(string) string { return "" }

	cases := []struct {
		name string
		p    ProductMetricsPrecedence
		want bool
	}{
		{"flag true wins over everything", ProductMetricsPrecedence{FlagSet: true, FlagValue: true, Getenv: getenvSet, SettingsEnabled: boolPtr(false)}, true},
		{"flag false wins over everything", ProductMetricsPrecedence{FlagSet: true, FlagValue: false, Getenv: getenvUnset, SettingsEnabled: boolPtr(true)}, false},
		{"DO_NOT_TRACK disables when no flag", ProductMetricsPrecedence{Getenv: getenvSet, SettingsEnabled: boolPtr(true)}, false},
		{"settings.yaml honoured when no flag/env", ProductMetricsPrecedence{Getenv: getenvUnset, SettingsEnabled: boolPtr(false)}, false},
		{"default enabled when nothing set", ProductMetricsPrecedence{Getenv: getenvUnset, SettingsEnabled: nil}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveProductMetricsEnabled(tc.p); got != tc.want {
				t.Errorf("ResolveProductMetricsEnabled(%+v) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd internal/cliconfig && go test ./... -run TestResolveProductMetricsEnabledPrecedence -v`
Expected: FAIL — `ProductMetricsPrecedence`/`ResolveProductMetricsEnabled` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package cliconfig

import (
	"os"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// ProductMetricsPrecedence carries the opt-out inputs
// ResolveProductMetricsEnabled folds, highest precedence first: an explicit
// CLI flag, then the DO_NOT_TRACK env var convention (consoledonottrack.com),
// then the operator settings.yaml value, then default-enabled.
type ProductMetricsPrecedence struct {
	// FlagSet/FlagValue report whether --product-metrics was explicitly
	// passed on the command line and its value.
	FlagSet   bool
	FlagValue bool
	// Getenv abstracts os.Getenv for DO_NOT_TRACK / testing. Defaults to
	// os.Getenv when nil.
	Getenv func(string) string
	// SettingsEnabled is permconfig.Resolver.OperatorProductMetricsEnabled()
	// — nil when the operator set no telemetry.productMetrics.enabled value.
	SettingsEnabled *bool
}

// ResolveProductMetricsEnabled applies the opt-out precedence documented on
// ProductMetricsPrecedence. Default (nothing set anywhere) is true — product
// metrics are OPT-OUT, not opt-in.
func ResolveProductMetricsEnabled(p ProductMetricsPrecedence) bool {
	if p.FlagSet {
		return p.FlagValue
	}
	getenv := p.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if getenv("DO_NOT_TRACK") != "" {
		return false
	}
	if p.SettingsEnabled != nil {
		return *p.SettingsEnabled
	}
	return true
}

// TeeToolCallRecorder combines multiple ToolCallRecorders into one — the
// ToolCallRecorder twin of internal/adapter/telemetry.NewSink's EventSink
// fan-out (no such helper existed before product metrics, because until now
// only one ToolCallRecorder ever observed a given engine). nil entries are
// skipped, so a caller can pass an always-present operator recorder
// alongside an optional product-metrics one without a conditional slice
// build.
func TeeToolCallRecorder(recorders ...port.ToolCallRecorder) port.ToolCallRecorder {
	var non []port.ToolCallRecorder
	for _, r := range recorders {
		if r != nil {
			non = append(non, r)
		}
	}
	return multiToolCallRecorder(non)
}

type multiToolCallRecorder []port.ToolCallRecorder

func (m multiToolCallRecorder) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	for _, r := range m {
		r.ToolCall(id, call, result, queued, took)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd internal/cliconfig && go test ./... -run TestResolveProductMetricsEnabledPrecedence -v`
Expected: PASS (all five subtests)

- [ ] **Step 5: Add a small test for `TeeToolCallRecorder`**

```go
func TestTeeToolCallRecorderCallsEveryNonNilRecorder(t *testing.T) {
	var calls []string
	rec := func(name string) port.ToolCallRecorder {
		return recorderFunc(func(session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration) {
			calls = append(calls, name)
		})
	}
	tee := TeeToolCallRecorder(rec("a"), nil, rec("b"))
	tee.ToolCall(session.SessionID(""), session.ToolCall{}, session.ToolResult{}, 0, 0)

	if len(calls) != 2 || calls[0] != "a" || calls[1] != "b" {
		t.Errorf("calls = %v, want [a b] (nil skipped, order preserved)", calls)
	}
}

// recorderFunc adapts a plain func to port.ToolCallRecorder for this test.
type recorderFunc func(session.SessionID, session.ToolCall, session.ToolResult, time.Duration, time.Duration)

func (f recorderFunc) ToolCall(id session.SessionID, call session.ToolCall, result session.ToolResult, queued, took time.Duration) {
	f(id, call, result, queued, took)
}
```

Add the necessary `"github.com/stacklok/mecatl/engine/port"`, `"github.com/stacklok/mecatl/engine/session"`, and `"time"` imports to the test file if not already present from Step 1.

- [ ] **Step 6: Run test to verify it passes**

Run: `cd internal/cliconfig && go test ./... -run TestTeeToolCallRecorderCallsEveryNonNilRecorder -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/cliconfig/productmetrics_config.go internal/cliconfig/productmetrics_config_test.go
git commit -m "feat(cliconfig): product-metrics opt-out precedence + ToolCallRecorder fan-out"
```

---

### Task 10: `cliconfig` — the `BuildProductMetrics` composition helper

**Files:**
- Create: `internal/cliconfig/productmetrics.go`
- Test: `internal/cliconfig/productmetrics_test.go`

**Interfaces:**
- Consumes: everything from `internal/adapter/productmetrics` (Tasks 1-5) and `ResolveProductMetricsEnabled`/`TeeToolCallRecorder` (Task 9).
- Produces: `type ProductMetricsHandles struct{ Sink port.EventSink; ToolCallRecorder port.ToolCallRecorder; Shutdown func(context.Context) error; FirstRun bool }`, `func BuildProductMetrics(ctx, heartbeatCtx context.Context, enabled bool, binary productmetrics.Binary, version string, heartbeatInterval time.Duration, snap productmetrics.FeatureSnapshot) (ProductMetricsHandles, error)`, `const ProductMetricsDisclosureNotice = "..."`.

- [ ] **Step 1: Write the failing test**

```go
package cliconfig

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/productmetrics"
)

func TestBuildProductMetricsDisabledReturnsZeroHandles(t *testing.T) {
	h, err := BuildProductMetrics(context.Background(), context.Background(), false,
		productmetrics.BinaryMecated, "test-version", 0, productmetrics.FeatureSnapshot{})
	if err != nil {
		t.Fatalf("BuildProductMetrics(enabled=false): %v", err)
	}
	if h.Sink != nil || h.ToolCallRecorder != nil {
		t.Errorf("disabled handles carry a non-nil Sink/ToolCallRecorder: %+v", h)
	}
	if h.Shutdown == nil {
		t.Fatal("Shutdown must be non-nil even when disabled (a no-op)")
	}
	if err := h.Shutdown(context.Background()); err != nil {
		t.Errorf("no-op Shutdown returned an error: %v", err)
	}
}

func TestBuildProductMetricsEnabledFailsClosedWithNoBakedKey(t *testing.T) {
	// bakedKey is empty in every non-release build/test — enabling must
	// surface the error rather than silently disabling, so a caller notices
	// its release build is missing the ldflag.
	_, err := BuildProductMetrics(context.Background(), context.Background(), true,
		productmetrics.BinaryMecated, "test-version", 0, productmetrics.FeatureSnapshot{})
	if err == nil {
		t.Fatal("expected an error when enabled=true with no baked ingest key, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd internal/cliconfig && go test ./... -run TestBuildProductMetrics -v`
Expected: FAIL — `BuildProductMetrics` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package cliconfig

import (
	"context"
	"fmt"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/productmetrics"
)

// ProductMetricsDisclosureNotice is printed ONCE — the first run after
// product metrics were enabled and this install's telemetry id did not yet
// exist — to stderr, non-blockingly, before any pipeline is built. Opt-out
// telemetry without a visible disclosure is the pattern that burns
// community trust; this is the whole of that disclosure.
const ProductMetricsDisclosureNotice = `mecatl reports anonymous product-adoption metrics (version, OS/arch, which
major features you have enabled, and coarse session/run/tool-call counts —
never a prompt, file path, tool name, or model id) to help Stacklok understand
community adoption. This is on by default. To opt out: pass
--product-metrics=false, set DO_NOT_TRACK=1, or set
telemetry.productMetrics.enabled: false in your settings.yaml. Details:
<user-docs link, filled in by Task 15>.
`

// ProductMetricsHandles bundles the handles a cmd main threads into its
// EventSink/ToolCallRecorder fan-out (via TeeToolCallRecorder /
// internal/adapter/telemetry.NewSink alongside the operator sink) and its
// shutdown defer. Every field is zero-valued when telemetry is disabled.
type ProductMetricsHandles struct {
	Sink             port.EventSink
	ToolCallRecorder port.ToolCallRecorder
	// Shutdown flushes + stops the provider. Always non-nil (a no-op when
	// disabled), so a caller can defer it unconditionally.
	Shutdown func(context.Context) error
	// FirstRun is true the first time this install's telemetry id was just
	// minted — the caller prints ProductMetricsDisclosureNotice when true.
	FirstRun bool
}

// BuildProductMetrics constructs the full opt-out product-metrics pipeline
// when enabled is true; when false it returns zero handles (the
// byte-identical disabled posture) and no error. heartbeatInterval is
// productmetrics.DefaultHeartbeatInterval for long-running processes, or 0
// for a single-fire-only short-lived process (mecatequi). heartbeatCtx is
// cancelled by the caller on shutdown to stop the periodic ticker goroutine
// this starts.
func BuildProductMetrics(
	ctx, heartbeatCtx context.Context,
	enabled bool,
	binary productmetrics.Binary,
	version string,
	heartbeatInterval time.Duration,
	snap productmetrics.FeatureSnapshot,
) (ProductMetricsHandles, error) {
	noop := func(context.Context) error { return nil }
	if !enabled {
		return ProductMetricsHandles{Shutdown: noop}, nil
	}

	installID, firstRun, err := productmetrics.LoadOrCreateInstallIDDefault()
	if err != nil {
		return ProductMetricsHandles{Shutdown: noop}, fmt.Errorf("product metrics: install id: %w", err)
	}

	provider, err := productmetrics.NewProvider(ctx, productmetrics.Config{
		Binary:    binary,
		Version:   version,
		InstallID: installID,
	})
	if err != nil {
		return ProductMetricsHandles{Shutdown: noop}, fmt.Errorf("product metrics: provider: %w", err)
	}

	recorder, err := productmetrics.NewRecorder(provider.Meter())
	if err != nil {
		_ = provider.Shutdown(ctx)
		return ProductMetricsHandles{Shutdown: noop}, fmt.Errorf("product metrics: recorder: %w", err)
	}

	go productmetrics.RunHeartbeat(heartbeatCtx, recorder, heartbeatInterval, snap)

	return ProductMetricsHandles{
		Sink:             recorder,
		ToolCallRecorder: recorder,
		Shutdown:         provider.Shutdown,
		FirstRun:         firstRun,
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd internal/cliconfig && go test ./... -run TestBuildProductMetrics -v`
Expected: PASS (both tests)

- [ ] **Step 5: Commit**

```bash
git add internal/cliconfig/productmetrics.go internal/cliconfig/productmetrics_test.go
git commit -m "feat(cliconfig): BuildProductMetrics composition helper + disclosure notice"
```

---

### Task 11: Wire into `cmd/mecated`

**Files:**
- Modify: `cmd/mecated/main.go`

**Interfaces:**
- Consumes: `cliconfig.ResolveProductMetricsEnabled`, `cliconfig.BuildProductMetrics`, `cliconfig.TeeToolCallRecorder` (Tasks 9-10); `internal/adapter/permconfig` resolver's `OperatorProductMetricsEnabled()` (Task 8) — mecated already builds a `permconfig.Resolver` for its permission rules; thread its `OperatorProductMetricsEnabled()` value through to this wiring.
- Produces: a new `--product-metrics` flag, defaulting `true`; the fanned-in sink/recorder feeding the existing `sink`/`mainScoped` variables at lines ~927/949.

- [ ] **Step 1: Add the flag**

Near the existing `fs.StringVar(&cfg.otlpEndpoint, "otlp-endpoint", ...)` registration (line 1610), add a new `cfg` field and flag:

```go
	fs.BoolVar(&cfg.productMetrics, "product-metrics", true,
		"report anonymous product-adoption metrics to Stacklok (version, OS/arch, enabled features, coarse session/run/tool-call counts — never a prompt, file path, tool name, or model id). ON by default; opt out with --product-metrics=false, DO_NOT_TRACK=1, or telemetry.productMetrics.enabled: false in settings.yaml")
```

Add `productMetrics bool` to the `config` struct near the existing `otlpEndpoint string` field.

- [ ] **Step 2: Compute `cliExplicit["product-metrics"]` (already-established mechanism)**

No new code needed here: `cmd/mecated/main.go`'s existing `fs.Visit(func(f *flag.Flag) { cfg.cliExplicit[f.Name] = true })` (around line 1877-1879) already marks every explicitly-passed flag by name, so `cfg.cliExplicit["product-metrics"]` is populated for free once Step 1's flag is registered.

- [ ] **Step 3: Resolve the effective enabled value and build the handles**

In `run()`, immediately after `obs, err := setupObservability(ctx, cfg, diag)` (line 899) and before its `defer` block, add:

```go
	productMetricsEnabled := cliconfig.ResolveProductMetricsEnabled(cliconfig.ProductMetricsPrecedence{
		FlagSet:         cfg.cliExplicit["product-metrics"],
		FlagValue:       cfg.productMetrics,
		SettingsEnabled: permResolver.OperatorProductMetricsEnabled(),
	})
	heartbeatCtx, cancelHeartbeat := context.WithCancel(context.Background())
	defer cancelHeartbeat()
	pm, err := cliconfig.BuildProductMetrics(ctx, heartbeatCtx, productMetricsEnabled,
		productmetrics.BinaryMecated, buildinfo.BuildID, productmetrics.DefaultHeartbeatInterval,
		productMetricsSnapshot(cfg))
	if err != nil {
		slog.Warn("product metrics disabled: setup failed", "err", err)
	} else {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if serr := pm.Shutdown(shutdownCtx); serr != nil {
				slog.Warn("product metrics shutdown", "err", serr)
			}
		}()
		if pm.FirstRun {
			fmt.Fprint(os.Stderr, cliconfig.ProductMetricsDisclosureNotice)
		}
	}
```

Replace `permResolver` above with whatever local variable name `cmd/mecated/main.go`'s `run()` already binds its constructed `*permconfig.Resolver` to (read the surrounding ~50 lines of `run()` to find the exact name before writing this — it is used to build the engine's permission policy earlier in the same function).

- [ ] **Step 4: Fan the new sink/recorder into the existing pipeline**

Change the existing:

```go
	sinks := []port.EventSink{mainScoped, tracing}
```

to:

```go
	sinks := []port.EventSink{mainScoped, tracing}
	if pm.Sink != nil {
		sinks = append(sinks, pm.Sink)
	}
```

and change the existing:

```go
	composition := appConfig(cfg, sink, mainScoped, roleScoper, obs.metrics, diag)
```

to:

```go
	composition := appConfig(cfg, sink, cliconfig.TeeToolCallRecorder(mainScoped, pm.ToolCallRecorder), roleScoper, obs.metrics, diag)
```

- [ ] **Step 5: Write `productMetricsSnapshot`**

Add this helper (near `appConfig` or `setupObservability`):

```go
// productMetricsSnapshot derives the closed-set FeatureSnapshot the product-
// metrics heartbeat reports, from fields already resolved on cfg — never a
// model id/alias, only whether each feature is configured at all.
func productMetricsSnapshot(cfg config) productmetrics.FeatureSnapshot {
	provider := productmetrics.ProviderOther
	switch {
	case cfg.useOpenAI:
		provider = productmetrics.ProviderOpenAI
	case strings.Contains(strings.ToLower(cfg.defaultProvider), "openrouter"):
		provider = productmetrics.ProviderOpenRouter
	case strings.Contains(strings.ToLower(cfg.defaultProvider), "openai"):
		provider = productmetrics.ProviderOpenAI
	case cfg.defaultProvider == "" || strings.Contains(strings.ToLower(cfg.defaultProvider), "anthropic"):
		provider = productmetrics.ProviderAnthropic
	}
	return productmetrics.FeatureSnapshot{
		Memory:     cfg.memoryDir != "",
		Guardrails: cfg.guardrailsModel != "",
		MCP:        cfg.mcpServers != nil && len(cfg.mcpServers.Servers()) > 0,
		Scheduling: !cfg.noScheduler,
		Provider:   provider,
		Mode:       productmetrics.ModeInteractive,
	}
}
```

Add the `"strings"` import if not already present, and `"github.com/stacklok/mecatl/internal/adapter/productmetrics"` + `"github.com/stacklok/mecatl/internal/buildinfo"` (for `buildinfo.BuildID`, already used elsewhere in this file per the `BUILD_LDFLAGS` reference) to the import block.

- [ ] **Step 6: Build and run the existing test suite**

Run: `task build && cd cmd/mecated && go test ./... -v`
Expected: builds and existing tests pass (this task changes wiring only, no new mecated-level tests are required beyond what already exercises flag parsing/appConfig — if `cmd/mecated` has a flag-parsing golden test, verify it still passes with the new `--product-metrics` flag appearing in its help output).

- [ ] **Step 7: Manually verify the disabled/default posture**

Run: `go run ./cmd/mecademo` (unaffected — mecademo doesn't wire telemetry) and `bin/mecated --help 2>&1 | grep product-metrics` to confirm the flag is registered and its help text is legible.

- [ ] **Step 8: Commit**

```bash
git add cmd/mecated/main.go
git commit -m "feat(mecated): wire opt-out product metrics alongside operator telemetry"
```

---

### Task 12: Wire into `cmd/mecatui`

**Files:**
- Modify: `cmd/mecatui/embed/embed.go` (around the `wirePerfSinks` function, line ~549, and its `telemetry.Setup` call at line ~400)
- Modify: `cmd/mecatui/config.go` or `cmd/mecatui/main.go` (wherever mecatui's top-level flags are registered — read the file first to find the exact flag-registration function name, mirroring Task 11 Step 1's `--product-metrics` flag)

**Interfaces:**
- Consumes: the same `cliconfig.BuildProductMetrics`/`ResolveProductMetricsEnabled`/`TeeToolCallRecorder` as Task 11.

- [ ] **Step 1: Read the exact flag-registration and `wirePerfSinks` call site**

Before writing code, read `cmd/mecatui/embed/embed.go` lines 373-560 (already excerpted above) and the file that registers mecatui's top-level CLI flags (find it via `grep -n "flag.NewFlagSet\|RegisterProviderFlags" cmd/mecatui/*.go`) to confirm the exact local variable/function names this task's diff must anchor to — mecatui's structure was not fully read during planning; it mirrors mecated's shape closely (same `sinks := []port.EventSink{mainScoped, tracing}` idiom at line 552) but names may differ slightly.

- [ ] **Step 2: Add the `--product-metrics` flag**

Mirror Task 11 Step 1 exactly, in whichever file registers mecatui's top-level flags.

- [ ] **Step 3: Build and thread the handles**

In `wirePerfSinks` (or its caller — whichever holds `cfg *app.Config` and constructs `sinks`), mirror Task 11 Steps 3-4: resolve `productMetricsEnabled`, call `cliconfig.BuildProductMetrics` with `productmetrics.BinaryMecatui` and `productmetrics.ModeInteractive`, append `pm.Sink` to `sinks`, and wrap `cfg.ToolCallRecorder` with `cliconfig.TeeToolCallRecorder`.

- [ ] **Step 4: Print the disclosure notice**

Wherever mecatui prints its own existing privacy warning (`docs/usage.md:282` references one — find its call site via `grep -rn "privacy warning" cmd/mecatui/`), print `cliconfig.ProductMetricsDisclosureNotice` alongside it when `pm.FirstRun` is true, so the two disclosures appear together rather than as two unrelated startup messages.

- [ ] **Step 5: Build and run mecatui's existing tests**

Run: `task build && cd cmd/mecatui && go test ./... -v`
Expected: builds and passes.

- [ ] **Step 6: Commit**

```bash
git add cmd/mecatui/
git commit -m "feat(mecatui): wire opt-out product metrics alongside operator telemetry"
```

---

### Task 13: Wire into `cmd/mecatequi` and `cmd/mecak8s`

**Files:**
- Modify: `cmd/mecatequi/observability.go`, `cmd/mecatequi/flags.go`
- Modify: `cmd/mecak8s/observability.go`, `cmd/mecak8s/flags.go`

**Interfaces:**
- Consumes: `cliconfig.BuildProductMetrics`/`ResolveProductMetricsEnabled`/`TeeToolCallRecorder`.
- Produces: extends the existing `observability` struct in both files with the product-metrics handles.

- [ ] **Step 1: Add the `--product-metrics` flag to both `flags.go` files**

Mirror Task 11 Step 1 in each binary's flag registration (both already register `--otlp-endpoint` etc. per the `HeadlessTelemetryConfig` fields read earlier — add `--product-metrics` alongside them).

- [ ] **Step 2: Extend `cmd/mecatequi/observability.go`**

```go
package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/productmetrics"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// observability carries the telemetry handles realMain threads into appConfig,
// plus the flush-on-exit Shutdown the main owns.
type observability struct {
	cliconfig.HeadlessTelemetryHandles
	productMetrics cliconfig.ProductMetricsHandles
}

func buildObservability(ctx context.Context, f flags, permResolver telemetryOperatorSource) (observability, error) {
	h, err := cliconfig.HeadlessTelemetry(ctx, cliconfig.HeadlessTelemetryConfig{
		ServiceName:         "mecatequi",
		OTLPTraceEndpoint:   f.otlpEndpoint,
		OTLPTraceProtocol:   f.otlpProtocol,
		OTLPTraceInsecure:   f.otlpInsecure,
		OTLPMetricsEndpoint: f.otlpMetricsEndpoint,
		OTLPMetricsProtocol: f.otlpMetricsProtocol,
		OTLPMetricsInsecure: f.otlpInsecure,
	})
	if err != nil {
		return observability{}, err
	}

	enabled := cliconfig.ResolveProductMetricsEnabled(cliconfig.ProductMetricsPrecedence{
		FlagSet:         f.productMetricsSet,
		FlagValue:       f.productMetrics,
		SettingsEnabled: permResolver.OperatorProductMetricsEnabled(),
	})
	pm, pmErr := cliconfig.BuildProductMetrics(ctx, ctx, enabled,
		productmetrics.BinaryMecatequi, f.version, 0, /* single fire, short-lived */
		productmetrics.FeatureSnapshot{Mode: productmetrics.ModeHeadless})
	if pmErr != nil {
		// Mirror the existing telemetry-setup-failure posture: a warning, never
		// a fatal error — product metrics are best-effort and must not block a
		// CI run. Logged by the caller (realMain already has a diag/logger in
		// scope) rather than here, to keep this function's error return
		// meaningful for the OTLP half only.
		pm = cliconfig.ProductMetricsHandles{Shutdown: func(context.Context) error { return nil }}
	}

	return observability{HeadlessTelemetryHandles: h, productMetrics: pm}, nil
}

func flushTelemetry(stderr io.Writer, obs observability, timeout time.Duration) {
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if obs.Shutdown != nil {
		if err := obs.Shutdown(ctx); err != nil {
			_, _ = fmt.Fprintf(stderr, "mecatequi: telemetry flush: %v\n", err)
		}
	}
	if obs.productMetrics.Shutdown != nil {
		if err := obs.productMetrics.Shutdown(ctx); err != nil {
			_, _ = fmt.Fprintf(stderr, "mecatequi: product metrics flush: %v\n", err)
		}
	}
	if obs.productMetrics.FirstRun {
		_, _ = fmt.Fprint(stderr, cliconfig.ProductMetricsDisclosureNotice)
	}
}
```

`telemetryOperatorSource` is a one-method interface (`OperatorProductMetricsEnabled() *bool`) — define it in this file so `observability.go` doesn't need to import `permconfig` directly; pass mecatequi's already-constructed `*permconfig.Resolver` as the argument at the call site (find it via `grep -n "buildObservability(" cmd/mecatequi/*.go` and thread the resolver variable already in scope there).

- [ ] **Step 3: Thread the product-metrics handles into `appConfig`**

At mecatequi's `appConfig`-equivalent assembly point (wherever `HeadlessTelemetryHandles.Sink`/`ToolCallRecorder` currently feed `app.Config.Sink`/`ToolCallRecorder`), fan in the product-metrics handles the same way as Task 11 Step 4:

```go
	sink := telemetry.NewSink(obs.Sink, obs.productMetrics.Sink) // telemetry.NewSink already skips nothing — verify it tolerates a nil element, or filter nils first
	recorder := cliconfig.TeeToolCallRecorder(obs.ToolCallRecorder, obs.productMetrics.ToolCallRecorder)
```

`internal/adapter/telemetry.NewSink`'s `fanOut.Emit` calls every wrapped sink unconditionally — a nil `port.EventSink` element would panic on `.Emit`. Guard it: build the slice with a nil check before calling `NewSink`, exactly like Task 11 Step 4's `if pm.Sink != nil { sinks = append(...) }` pattern, rather than passing a possibly-nil element directly.

- [ ] **Step 4: Repeat Steps 2-3 for `cmd/mecak8s/observability.go`**, using `productmetrics.BinaryMecak8s`, `productmetrics.ModeK8s`, and `productmetrics.DefaultHeartbeatInterval` (mecak8s is long-running, unlike mecatequi) with `heartbeatCtx` tied to the server's shutdown context rather than the short-lived `ctx`.

- [ ] **Step 5: Add `productMetrics`/`productMetricsSet` fields + flag registration to both `flags.go` files**

Mirror Task 11 Step 1 in each.

- [ ] **Step 6: Build and run both binaries' existing tests**

Run: `task build && cd cmd/mecatequi && go test ./... -v && cd ../mecak8s && go test ./... -v`
Expected: builds and passes.

- [ ] **Step 7: Commit**

```bash
git add cmd/mecatequi/ cmd/mecak8s/
git commit -m "feat(mecatequi,mecak8s): wire opt-out product metrics via cliconfig"
```

---

### Task 14: Dry-run / audit recorder

**Files:**
- Create: `internal/adapter/productmetrics/dryrun.go`
- Test: `internal/adapter/productmetrics/dryrun_test.go`
- Modify: `internal/cliconfig/productmetrics.go` (thread a `dryRun bool` parameter into `BuildProductMetrics`)
- Modify: all four `cmd/*` flag-registration sites from Tasks 11-13 (add `--product-metrics-dry-run`)

**Interfaces:**
- Produces: `type DryRunRecorder struct{...}` implementing `port.EventSink` + `port.ToolCallRecorder`, logging every would-be observation via `port.Diagnostics` instead of exporting it — `func NewDryRunRecorder(diag port.Diagnostics) *DryRunRecorder`.

- [ ] **Step 1: Write the failing test**

```go
package productmetrics

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type capturingDiag struct {
	lines []string
}

func (c *capturingDiag) Log(_ context.Context, _ port.Level, msg string, args ...any) {
	c.lines = append(c.lines, msg)
	_ = args
}
func (c *capturingDiag) With(...any) port.Diagnostics { return c }

func TestDryRunRecorderLogsInsteadOfExporting(t *testing.T) {
	diag := &capturingDiag{}
	r := NewDryRunRecorder(diag)

	r.Emit(context.Background(), session.Event{Type: session.EvSessionInit})
	r.ToolCall(session.SessionID("s"), session.ToolCall{Name: "sensitive-name"}, session.ToolResult{Content: "sensitive-content"}, 0, time.Millisecond)

	if len(diag.lines) != 2 {
		t.Fatalf("got %d logged lines, want 2: %v", len(diag.lines), diag.lines)
	}
	for _, line := range diag.lines {
		if contains(line, "sensitive") {
			t.Errorf("dry-run log line leaked sensitive content: %q", line)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestDryRunRecorderLogsInsteadOfExporting -v`
Expected: FAIL — `NewDryRunRecorder` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
package productmetrics

import (
	"context"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// DryRunRecorder implements the same two ports as Recorder but logs every
// would-be observation via port.Diagnostics instead of exporting it over
// OTLP — the --product-metrics-dry-run audit path, so a skeptical operator
// can see exactly what this pipeline would have sent without trusting the
// docs. It logs ONLY the same bounded fields Recorder ever reads (event
// type, stop reason, token kind/amounts) — never a tool name, session id,
// or result content, mirroring Recorder's own restraint exactly.
type DryRunRecorder struct {
	diag port.Diagnostics
}

var (
	_ port.EventSink        = (*DryRunRecorder)(nil)
	_ port.ToolCallRecorder = (*DryRunRecorder)(nil)
)

// NewDryRunRecorder builds a DryRunRecorder over the given Diagnostics sink.
func NewDryRunRecorder(diag port.Diagnostics) *DryRunRecorder {
	return &DryRunRecorder{diag: diag}
}

// Emit logs the bounded event type (and, for EvResult, the stop reason and
// token counts by kind) — the exact same fields Recorder.Emit reads.
func (d *DryRunRecorder) Emit(ctx context.Context, ev session.Event) {
	switch ev.Type {
	case session.EvSessionInit:
		d.diag.Log(ctx, port.LevelInfo, "product metrics (dry-run): would record sessions_started+1")
	case session.EvResult:
		if ev.Result == nil {
			d.diag.Log(ctx, port.LevelInfo, "product metrics (dry-run): would record runs_completed{stop=\"\"}+1")
			return
		}
		u := ev.Result.Usage
		d.diag.Log(ctx, port.LevelInfo, "product metrics (dry-run): would record run + tokens",
			"stop", string(ev.Result.Stop),
			"input_tokens", u.InputTokens, "output_tokens", u.OutputTokens,
			"cache_read_tokens", u.CacheReadTokens, "cache_write_tokens", u.CacheWriteTokens,
			"reasoning_tokens", u.ReasoningTokens)
	case session.EvSubagentStart:
		d.diag.Log(ctx, port.LevelInfo, "product metrics (dry-run): would record subagent_used+1")
	case session.EvTeamStart:
		d.diag.Log(ctx, port.LevelInfo, "product metrics (dry-run): would record team_used+1")
	}
}

// ToolCall logs only that a call happened — no name, no content, matching
// Recorder.ToolCall's restraint exactly.
func (d *DryRunRecorder) ToolCall(_ session.SessionID, _ session.ToolCall, _ session.ToolResult, _, _ time.Duration) {
	d.diag.Log(context.Background(), port.LevelInfo, "product metrics (dry-run): would record tool_calls+1")
}

// Heartbeat logs the closed-enum feature/provider/mode signal, matching
// Recorder.Heartbeat's fields exactly.
func (d *DryRunRecorder) Heartbeat(snap FeatureSnapshot) {
	enabled := make([]string, 0, 4)
	for f, on := range snap.enabled() {
		if on {
			enabled = append(enabled, string(f))
		}
	}
	d.diag.Log(context.Background(), port.LevelInfo, "product metrics (dry-run): would record heartbeat",
		"features_enabled", enabled, "provider_family", string(snap.Provider), "deployment_mode", string(snap.Mode))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd internal/adapter/productmetrics && go test ./... -run TestDryRunRecorderLogsInsteadOfExporting -v`
Expected: PASS

- [ ] **Step 5: Thread a `dryRun` option through `BuildProductMetrics`**

In `internal/cliconfig/productmetrics.go`, change `BuildProductMetrics`'s signature to accept a `dryRun bool` parameter (after `enabled`), and branch near the top of the function body:

```go
func BuildProductMetrics(
	ctx, heartbeatCtx context.Context,
	enabled, dryRun bool,
	binary productmetrics.Binary,
	version string,
	heartbeatInterval time.Duration,
	snap productmetrics.FeatureSnapshot,
	diag port.Diagnostics,
) (ProductMetricsHandles, error) {
	noop := func(context.Context) error { return nil }
	if !enabled {
		return ProductMetricsHandles{Shutdown: noop}, nil
	}
	if dryRun {
		rec := productmetrics.NewDryRunRecorder(diag)
		go func() {
			rec.Heartbeat(snap)
			// A dry run never persists an install id or starts a real ticker —
			// it exists to show ONE representative sample, not to simulate the
			// full 24h cadence.
		}()
		return ProductMetricsHandles{Sink: rec, ToolCallRecorder: rec, Shutdown: noop}, nil
	}
	// ... existing enabled, non-dry-run body unchanged below this point ...
```

Update the two callers from Task 10's test file and every `cmd/*` call site from Tasks 11-13 to pass `false` for `dryRun` (or the resolved flag value) and a `port.Diagnostics` (each `cmd/*/main.go` already constructs one — thread the existing `diag` variable through).

- [ ] **Step 6: Add `--product-metrics-dry-run` to all four binaries' flags**

Mirror Task 11 Step 1's flag pattern in each of the four flag-registration sites touched in Tasks 11-13:

```go
	fs.BoolVar(&cfg.productMetricsDryRun, "product-metrics-dry-run", false,
		"print every product-metrics observation to stderr instead of sending it — verify the no-PII claim yourself before enabling --product-metrics for real")
```

- [ ] **Step 7: Run the full `productmetrics` and `cliconfig` suites**

Run: `cd internal/adapter/productmetrics && go test ./... -v && cd ../../cliconfig && go test ./... -v`
Expected: PASS

- [ ] **Step 8: Run `task build` to confirm every binary still compiles with the new parameter threaded through**

Run: `task build`
Expected: succeeds.

- [ ] **Step 9: Commit**

```bash
git add internal/adapter/productmetrics/dryrun.go internal/adapter/productmetrics/dryrun_test.go \
        internal/cliconfig/productmetrics.go cmd/mecated/main.go cmd/mecatui/ cmd/mecatequi/ cmd/mecak8s/
git commit -m "feat(productmetrics): --product-metrics-dry-run audit mode across all four binaries"
```

---

### Task 15: ADR + user-docs

**Files:**
- Create: `docs/adr/0317-product-metrics.md` (confirm 0317 is still the next free number by running `ls docs/adr | grep -oE '^[0-9]+' | sort -n | tail -1` immediately before creating the file — another PR may have landed a higher number since this plan was written)
- Create: `user-docs/building/what-you-get/product-metrics.md` (confirm the exact directory naming convention by listing `user-docs/building/what-you-get/` first — follow its existing file-naming/frontmatter pattern exactly)
- Modify: `internal/cliconfig/productmetrics.go` (fill in `ProductMetricsDisclosureNotice`'s doc link placeholder with the real path)

**Interfaces:** none — documentation only.

- [ ] **Step 1: Confirm the next free ADR number**

Run: `ls docs/adr | grep -oE '^[0-9]+' | sort -n | tail -1`
Use `<that number> + 1` as the filename prefix (0317 as of this plan's writing).

- [ ] **Step 2: Write the ADR**

Create `docs/adr/0317-product-metrics.md` (or whatever number Step 1 resolved) following `docs/adr/template.md`'s structure (header with `- Status: Accepted` / `- Date:` / `- Scope:` / `- Supersedes: —` / `- Superseded by: —`, then Context/Decision/Consequences/See also, mirroring ADR 0098's structure read during planning). Content: summarize this plan's design — the fully independent adapter, the exact `mecatl.adoption.*` catalog (Tasks 4-5), the opt-out precedence (Task 9), the operator-tier-only settings gate (Tasks 7-8), the privacy guard test (Task 6), the dry-run audit mode (Task 14), and the "why opt-out is defensible here" rationale (disclosure notice + `DO_NOT_TRACK` + a reviewable, tested catalog). Cross-reference ADR 0098 (headless telemetry) and ADR 0020 (diagnostics/the three-channel table this is a deliberate fourth, separate channel from) as prior art it deliberately does NOT reuse.

- [ ] **Step 3: Write the user-docs page**

List `user-docs/building/what-you-get/` first to match its existing frontmatter/heading conventions, then add a short page: what's collected (link the ADR's catalog), the opt-out mechanisms (flag, `DO_NOT_TRACK`, settings.yaml), and how to self-verify via `--product-metrics-dry-run`.

- [ ] **Step 4: Fill in the disclosure notice's doc link**

In `internal/cliconfig/productmetrics.go`, replace `<user-docs link, filled in by Task 15>` in `ProductMetricsDisclosureNotice` with the real path/URL to the page written in Step 3.

- [ ] **Step 5: Run the docs gates**

Run: `task docs` (regenerates the configuration reference + runs the strict link gate) and `task site:build` (Docusaurus build, catches a broken link before CI does).
Expected: both succeed with no broken links/anchors.

- [ ] **Step 6: Run `task lint && task test` one final time across the whole feature**

Run: `task lint && task test`
Expected: green.

- [ ] **Step 7: Commit**

```bash
git add docs/adr/ user-docs/building/what-you-get/ internal/cliconfig/productmetrics.go
git commit -m "docs: add ADR and user-docs for opt-out product/adoption metrics"
```
