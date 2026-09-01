// Command mecated is the standalone mecatl server binary and a composition root:
// it parses the CLI/env configuration, builds the telemetry sink, delegates the
// engine + service assembly to internal/app (the SHARED composition layer also
// used by the embedded server in cmd/mecatui), and serves the resulting
// HarnessService over gRPC and HTTP/SSE concurrently, with graceful shutdown on
// SIGINT/SIGTERM.
//
// The agent loop, tool catalog, permission policy, provider, store, MCP and skills
// wiring all live in internal/app so the TUI can host the same server in-process;
// mecated owns only the things specific to a network daemon: flag parsing,
// TLS/auth/rate-limit, the HTTP + metrics listeners, and offline operator
// subcommands such as `import` and `skills promote`.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/trace"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/daemonconfig"
	"github.com/stacklok/mecatl/internal/adapter/mcpperf"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/skills"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/adapter/telemetry"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/buildinfo"
	"github.com/stacklok/mecatl/internal/cliconfig"
	"github.com/stacklok/mecatl/internal/configgen"
)

// TRUST MODEL (security): the mecated API exposes command and file execution
// against the configured workspace. The default listen addresses below bind the
// loopback interface (single-user localhost). Authentication is OPTIONAL and
// OFF by default for that loopback case: enable a bearer token (--auth-token /
// MECATL_AUTH_TOKEN) and/or TLS/mTLS (--tls-cert/--tls-key/--client-ca) before
// binding a non-loopback address. Binding non-loopback with NO authentication
// is permitted (an operator may front it with a mesh) but logs a prominent
// WARNING, since it exposes command/file execution to the network.
const (
	defaultGRPCAddr = "127.0.0.1:8080"
	defaultHTTPAddr = "127.0.0.1:8081"
)

// defaultMetricsAddr is the loopback listen address for the Prometheus /metrics
// endpoint. Unlike the harness API it is read-only and carries no secrets, but
// it is still bound to loopback by default. An empty --metrics-addr disables it.
const defaultMetricsAddr = "127.0.0.1:9090"

// config is the parsed command-line / environment configuration for mecated. The
// engine-build subset is mapped onto app.Config by appConfig; the rest (listen
// addresses, TLS, auth, rate limiting, metrics, tracing) is serve-time state
// owned by this binary.
type config struct {
	logLevel        slog.Level
	logLevelWarning string
	grpcAddr        string
	httpAddr        string
	// grpcUnixSocket serves gRPC on a UNIX-domain socket INSTEAD of a TCP port
	// (issue #821 Scenario 8) — what a locally spawned daemon wants: no port, and
	// reachability governed by filesystem permission rather than by "any local
	// process". Mutually exclusive with an explicitly configured --grpc-addr,
	// rejected at startup by validateDaemonHosting.
	grpcUnixSocket string
	// grpcAddrFromFile records that the daemon config file supplied grpc_addr, so
	// the --grpc-unix-socket exclusion sees a FILE-configured TCP address too —
	// tcpGRPCConfigured folds it with cliExplicit["grpc-addr"].
	grpcAddrFromFile bool
	// readyFile is the path of the atomically-published readiness document, written
	// only after composition and every listener are up. Empty writes nothing.
	readyFile string
	// lifetimePipeFD is an INHERITED read-end descriptor whose EOF means the
	// spawning parent died; the daemon then stops through the ordinary shutdown
	// path. 0 disables it (0/1/2 are the standard streams, never a lifetime pipe).
	lifetimePipeFD  int
	workspace       string
	model           string
	defaultProvider string
	defaultModel    string
	// defaultProviderFlagSet is true when --default-provider was passed explicitly
	// (set after parse via fs.Visit), so composition lets CLI out-rank the
	// operator-global settings.yaml models.default_provider: key.
	defaultProviderFlagSet bool
	useOpenAI              bool
	// providerFlags holds shared provider flag bindings; providerCredentials is
	// the once-resolved snapshot projected by appConfig without further I/O.
	providerFlags       *cliconfig.ProviderFlags
	providerCredentials cliconfig.ResolvedCredentials
	// toolhiveLLMFlags holds --toolhive-llm / --toolhive-llm-base-url (issue
	// #262: auto-detecting the ToolHive LLM gateway proxy), applied onto
	// app.Config in appConfig alongside providerFlags.
	toolhiveLLMFlags     *cliconfig.ToolhiveLLMFlags
	useMock              bool
	mockScript           string
	mockProvider         port.LLMProvider
	storeDir             string
	shell                string
	noBash               bool
	authorityEvaluator   string
	cedarAuthorityPolicy string

	// Context management: the compaction strategy and the token counter. Both
	// default to the current behaviour exactly (heuristic compactor + heuristic
	// counter); "cascade"/"tiktoken" opt into the tiered cascade / real tokenizer.
	compaction string
	tokenizer  string
	// contextWindowOverride forces a fixed compaction context window (tokens); 0 keeps
	// the live/catalogued/128k resolution. See the flag help for the dual purpose.
	contextWindowOverride int

	// Security: API authentication, transport security, and rate limiting.
	authToken string  // bearer token required on every RPC/request (empty disables auth)
	tlsCert   string  // PEM server certificate; enables TLS on gRPC + HTTP when set with tlsKey
	tlsKey    string  // PEM server private key (paired with tlsCert)
	clientCA  string  // PEM client CA bundle; enables mutual TLS (require+verify client certs)
	rateLimit float64 // sustained per-client request rate (req/s); 0 disables rate limiting
	rateBurst int     // token-bucket burst size; 0 -> derived from rateLimit
	// oidc carries the caller-identity flags (--oidc-issuer/--oidc-jwks-uri/
	// --oidc-audience). Zero value = identity off, the unchanged path.
	oidc cliconfig.OIDCConfig

	// LLM resilience knobs (see package internal/adapter/llmresilience).
	llmMaxAttempts       int
	llmPerAttemptTimeout time.Duration
	llmStreamIdleTimeout time.Duration
	llmBreakerThreshold  int
	llmBreakerCooldown   time.Duration

	// Provider-side prompt caching (ADR 0100).
	noPromptCache     bool
	anthropicCacheTTL string

	// maxRunTokens is the loop-level cumulative token ceiling for a single run (the
	// shared runaway brake). 0 (default) disables it.
	maxRunTokens int

	// maxTeamTokens is the team-wide cumulative token ceiling for a single team run
	// (the round-boundary brake). 0 (default) disables it.
	maxTeamTokens int

	// Observability: the Prometheus /metrics listen address (empty disables it),
	// plus the OTLP trace exporter knobs (empty endpoint disables tracing).
	metricsAddr  string
	otlpEndpoint string // OTLP collector endpoint (empty disables tracing)
	otlpProtocol string // OTLP transport: "grpc" (default) or "http"
	otlpInsecure bool   // skip TLS when dialing the OTLP collector (dev only)

	// Runtime-introspection admin surface (loopback only, on the --metrics-addr
	// listener): pprof + expvar + a runtime/metrics snapshot + a FlightRecorder.
	// mutexProfileFraction arms runtime.SetMutexProfileFraction (0 = off);
	// blockProfileRate arms runtime.SetBlockProfileRate in ns (0 = off); both add
	// runtime overhead when > 0. flightRecorder arms the bounded execution-trace
	// ring buffer (default ON — low, bounded overhead).
	mutexProfileFraction int
	blockProfileRate     int
	flightRecorder       bool

	// perfMCP mounts the read-only perf MCP server (internal/adapter/mcpperf) at
	// /mcp on the loopback admin listener, so an agent can introspect THIS
	// process's runtime/latency/profile state over MCP. OFF by default. It requires
	// --metrics-addr (the admin listener it rides) AND that address to be loopback:
	// the surface is UNAUTHENTICATED and can embed goroutine-derived names/timing,
	// so serve() FAILS CLOSED if --perf-mcp is set on a non-loopback --metrics-addr
	// (decision 6 + the security review's CWE-306 Low finding).
	perfMCP bool

	// goroutineWarnThreshold arms a background watchdog that logs slog.Warn when
	// runtime.NumGoroutine() exceeds it (decision 10: a live leak alarm, not just
	// the test-time goleak gate). 0 (default) disables it. The runtime collector
	// already exports the goroutine COUNT as a series; this is the ALARM on top.
	goroutineWarnThreshold int
	// goroutineWarnInterval is how often the watchdog samples NumGoroutine.
	goroutineWarnInterval time.Duration

	// Memory: per-project memory store directory (empty disables memory tools).
	memoryDir string

	// Remote store drivers (Phase B): gRPC driver endpoints replacing the local
	// session/memory stores (mutually exclusive with --store-dir/--memory-dir;
	// app.Build validates). The driver auth/TLS knobs apply to every driver
	// connection; the token also reads MECATL_DRIVER_AUTH_TOKEN when the flag
	// is unset (mirroring --auth-token / MECATL_AUTH_TOKEN).
	sessionStoreURL  string
	memoryStoreURL   string
	skillSourceURL   string
	soulSourceURL    string
	agentSourceURL   string
	commandSourceURL string
	eventLogURL      string
	scheduleStoreURL string
	learningStoreURL string
	driverAuthToken  string
	driverTLS        bool
	driverTLSCA      string
	driverTLSCert    string
	driverTLSKey     string

	// Session leasing (cloud-native Phase 4): OPTIONAL cross-process single-writer
	// enforcement for multi-replica deployments over a shared store. Empty =
	// no leasing (the byte-identical single-writer-by-affinity default). Exactly
	// one backend: URL (driver), k8s namespace, or flock dir.
	sessionLeaseURL           string
	sessionLeaseDir           string
	sessionLeaseK8sNamespace  string
	sessionLeaseTTL           time.Duration
	sessionLeaseRenewInterval time.Duration

	// Scheduled tasks (issue #189, Phase 1f; ADR 0073): the in-process scheduler
	// ticks the durable ScheduleStore and fires due schedules. ON by default on
	// any schedule-capable store (--store-dir / --redis-url / a driver store that
	// exposes the accessor); a store with no ScheduleStore (the in-memory
	// default) stays on the byte-identical no-scheduling path. noScheduler is
	// the opt-out.
	noScheduler                 bool
	schedulerTickInterval       time.Duration
	schedulerMinInterval        time.Duration
	schedulerMaxConcurrentFires int

	// Soul (issue #14, Phase 1): a user-scoped, agent-READ-ONLY persona fragment.
	// ON by default reading the conventional ~/.config/mecatl/soul.md (fail-soft if
	// absent). soulFile overrides the path; noSoul disables it entirely.
	//
	// Drift baseline (issue #14, Phase 3, Item 1): the harness records the soul's
	// content hash in a sidecar (<soulPath>.sha256) trust-on-first-use; a later run
	// whose hash differs logs a drift WARN and still loads. approveSoul (re)writes the
	// baseline to the current hash (accept the edit); soulStrict makes a DRIFTED soul
	// contribute no fragment.
	soulFile    string
	noSoul      bool
	approveSoul bool
	soulStrict  bool

	// User model (issue #14, Phase 2): a user-scoped, cross-project memory of
	// durable FACTS about the operator (explicit user-memory tools plus a live
	// bounded operator profile in the volatile system suffix). ON by default at the conventional
	// ~/.config/mecatl/usermodel; noUserModel disables it; userModelDir overrides
	// the dir. userModelReview is the deprecated alias for completed-trajectory
	// auto review; userModelReviewInterval is its session-count debounce.
	// userModelConsolidateInterval independently authorizes the process-wide
	// cross-project "user/" dream consolidator; learning.mode does not gate it.
	userModelDir                 string
	noUserModel                  bool
	userModelReview              bool
	userModelReviewInterval      int
	userModelConsolidateInterval time.Duration

	// Skills: explicit directories of progressive-disclosure skill units laid out
	// as <dir>/<name>/SKILL.md (repeatable; highest precedence). Empty + no
	// conventional set disables the Skill tool. skillsConventional adds the
	// built-in conventional project/user locations (lower precedence), default OFF
	// to keep skills strictly opt-in (a trust boundary — see resolve.go / usage.md).
	skillsDirs         stringList
	skillsConventional bool

	// Agent definitions (Tier 1): named subagent specialists discovered from
	// <dir>/<name>.md files. agentsDirs are explicit dirs (repeatable; highest
	// precedence); agentsConventional adds the conventional project/user locations
	// (.mecatl/agents, .claude/agents, XDG/user) — ON by default and INERT when no
	// such dir exists, mirroring the teams/fork "on-but-inert" philosophy. subagentModel
	// globally overrides the model of every Subagent/member child that does not pin its
	// own; modelAliases maps short aliases (sonnet/opus/fast/...) to concrete ids.
	// The *cliconfig.KeyValueList pointers are the flag bindings returned by
	// cliconfig.RegisterModelFlags (issue #93: the type lives in cliconfig so the
	// two mains cannot drift).
	agentsDirs         stringList
	agentsConventional bool
	subagentModel      string
	modelAliases       *cliconfig.KeyValueList
	// modelSlots binds a named internal lightweight call (compaction/ask-reviewer/
	// guardrail) — or a tier (cheap/fast/reasoning) a slot falls through to — to a
	// model selector (an alias or a concrete id), via the repeatable --model-slot
	// flag. Resolved THROUGH modelAliases in the composition layer (ADR 0030).
	modelSlots *cliconfig.KeyValueList

	// Headless ask reviewer (issue #31): subagentAskReviewer names the model (or
	// --model-alias) of the OPT-IN one-turn reviewer that adjudicates a HEADLESS
	// child's otherwise-blanket-auto-denied permission ask; empty (default)
	// disables it. subagentAskReviewerMaxDenies is the per-run consecutive-deny
	// circuit-breaker threshold. subagentAskReviewerPolicyFile points at a TRUSTED
	// policy rubric file; parseFlags reads it (cmd mains may use os) and the
	// CONTENT travels on subagentAskReviewerPolicy into app.Config.
	subagentAskReviewer           string
	subagentAskReviewerMaxDenies  int
	subagentAskReviewerPolicyFile string
	subagentAskReviewerPolicy     string

	// Subagent model router (ADR 0031; enable model per ADR 0042): the router is ENABLED
	// by configuring a `models.router:` taxonomy in the user-global settings.yaml — the
	// guardrails-parity enable model (no flag to forget). The --subagent-model-router
	// flag is a KILL-SWITCH: subagentModelRouter holds its value and
	// subagentModelRouterSet records whether it was given. =false sets RouterDisabled
	// (forces the router OFF despite a taxonomy); a bare flag / =true is a harmless no-op
	// (the router stays governed by the taxonomy); unset leaves the router governed by
	// taxonomy presence.
	subagentModelRouter    bool
	subagentModelRouterSet bool

	// Guardrails (issue #27): guardrailsModel names the tool-less checker model that
	// inspects PreToolUse (outbound-args exfil) and PostToolUse (inbound-result
	// injection) tool content. Configuring a model here OR via a bound `guardrail`
	// model slot ENABLES guardrails (configure = enable, ADR 0046); empty + no slot
	// disables them. guardrailsOff is the master kill-switch (--guardrails=off) that
	// forces guardrails off regardless of config. The RULE LIST + cost knobs live in
	// the OPERATOR-TIER `guardrails:` subtree of the user-global settings.yaml (a flag
	// cannot express a rule list); they are NEVER read from the project-tier file (a
	// security downgrade).
	guardrailsModel string
	guardrailsMode  string // the raw --guardrails value ("off" → guardrailsOff)
	guardrailsOff   bool

	// headless declares that NO human approver is attached to this deployment's
	// sessions: a child's unresolved permission ask must NOT be surfaced to the
	// client (there is nobody to answer it — it would park until run-end), and
	// instead engages the auto-deny path / the opt-in --subagent-ask-reviewer.
	// DEFAULT false: a normal mecated serving an interactive client (mecatui, an
	// IDE) surfaces asks for a human to answer. Set it for an autonomous / CI
	// deployment where clients drive runs but never answer permission prompts — it
	// is what makes --subagent-ask-reviewer actually engage.
	headless bool

	// Skills self-improvement loop (opt-in): when skillsDraftDir is non-empty the
	// writable SkillDraft tool is registered, writing model-authored candidate
	// SKILL.md files into this QUARANTINE directory (NEVER a catalog Source). An
	// operator promotes a candidate into an active --skills-dir with the
	// `mecated skills promote` subcommand. Empty disables the tool. The dir must be
	// disjoint from every active skills dir (fatal config error on overlap).
	skillsDraftDir       string
	skillsDraftThreshold float64

	// Memory consolidation (dream): background distillation interval. 0 disables.
	// Only meaningful when memoryDir is set; a positive value with an empty
	// memoryDir is a no-op (logged as a warning).
	memoryConsolidateInterval time.Duration

	// Child-session retention/GC (issue #38): age threshold, per-family count
	// cap, and sweep cadence for persisted subagent-/parallel-/team- child
	// snapshots. Only meaningful for a durable store (--store-dir or a prunable
	// remote driver); the in-memory default never accumulates across restarts.
	childRetention             time.Duration
	childRetentionMaxPerFamily int
	childGCInterval            time.Duration

	// Main-session retention/GC (issue #79): age threshold and a global count cap
	// for persisted TOP-LEVEL (operator/service) session snapshots, which the
	// child sweep never touches. Durable-store-only, like the child knobs. Both
	// default 0 (DISABLED) — mecated's behaviour is byte-unchanged unless an
	// operator opts in; mecatui defaults them on for its durable per-workspace store.
	mainRetention         time.Duration
	mainRetentionMaxTotal int

	// Schedule-fire retention/GC (ADR 0059 decision #7 Phase-2): age threshold
	// for persisted "sched--"-prefixed fire-session snapshots. Default 7d when
	// scheduling is on (applied below); 0 disables (fire sessions never swept).
	scheduleFireRetention         time.Duration
	scheduleFireRetentionSet      bool // true when --schedule-fire-retention was passed explicitly
	scheduleFireRetentionMaxTotal int
	retentionCLISet               app.RetentionCLISet
	acknowledgeMainRetention      bool

	// Slash commands: directory of <name>.md command templates, and an explicit
	// enable switch. commandsDir set OR enableCommands true wires the
	// DirCommandExpander; otherwise the default NoopExpander is left in place.
	commandsDir    string
	enableCommands bool

	// WebSearch (issue #26): the vendor-neutral HTTP JSON search backend behind the
	// always-present WebSearch tool. websearchURL is the search endpoint (empty =>
	// the tool reports "not configured"); the API key is read from WEBSEARCH_API_KEY
	// (a secret, never a flag value); websearchAuthHeader/websearchQueryParam tune
	// the request shape for a generic JSON endpoint.
	websearchURL        string
	websearchAPIKey     string
	websearchAuthHeader string
	websearchQueryParam string

	// WebSearch backend ladder (issue #26): web search is ON by default (Exa
	// anonymous). searxngURL/braveAPIKey/exaAPIKey are read from SEARXNG_URL/
	// BRAVE_API_KEY/EXA_API_KEY (secrets/URLs, never flag values) and SWITCH the
	// backend; websearchMode is the raw --websearch value ("off" → websearchOff),
	// the kill switch mirroring --guardrails.
	searxngURL    string
	braveAPIKey   string
	exaAPIKey     string
	websearchMode string // raw --websearch value ("off" → websearchOff)
	websearchOff  bool

	// Parallel: enable the Parallel fan-out tool (parallel isolated child branches).
	enableParallel bool
	// forkPreservedCap bounds how many PRESERVED winner forks (join=first/judge)
	// survive at once; the oldest beyond the cap is LRU-reaped. 0 => the default.
	forkPreservedCap int

	// Teams: enable the experimental agent-teams capability (CreateTeam /
	// SpawnTeammate / RunTeam). Opt-in, default off.
	enableTeams bool

	// MCP: remote MCP servers to connect to and register tools from. The
	// repeatable name=URL flag + the MCP_<NAME>_TOKEN bearer convention live in
	// the shared cliconfig.MCPServerList (issue #341) so mecated, mecatequi, and
	// mecak8s cannot drift on the parse/token semantics.
	mcpServers *cliconfig.MCPServerList

	// MCP resources: register the ListMcpResources/ReadMcpResource meta-tools when
	// a connected server exposes resources. Default ON — the tools are registered
	// only when there is at least one resource to expose.
	mcpResourceTools bool
	// MCP prompts: compose the MCP prompt expander so "/mcp__<server>__<prompt>"
	// inputs expand to the server-rendered prompt. Default ON; expansion only fires
	// when the MCP prompt namespace is actually used.
	mcpPrompts bool

	// ACP: serve the Agent Client Protocol over stdio instead of the TCP/HTTP
	// listeners. When set, mecated speaks JSON-RPC 2.0 to an ACP editor (Zed, etc.)
	// that spawned it as a subprocess; the normal network daemon path is skipped.
	acp bool

	// helpAll is true when --help-all was passed; it requests exhaustive flag listing
	// and exits 0 before the daemon starts.
	helpAll bool

	// ToolHive: discover MCP servers from the running ToolHive workloads (the
	// embedded ToolHive library lists already-running workloads and reads their
	// HTTP proxy URLs — mecatl never spawns a workload). Default ON; it fails soft
	// to zero servers when no container runtime is reachable.
	toolHiveEnabled bool
	// toolHiveGroup is the ToolHive group to discover from (empty -> "default").
	toolHiveGroup string

	// File-based permission config (issue #13). permissionConfigs are explicit
	// operator-pointed YAML files (repeatable, fully trusted). permissionsConventional
	// auto-discovers the per-project .mecatl/settings.yaml (and the user-global
	// file), re-resolved per session against each session's workspace root — ON by
	// default. importClaudePermissions also imports Claude-Code settings.json (with
	// the lossy fail-safe table). trustProject honours a project's ALLOW rules; a
	// project's deny/ask is always honoured regardless. Default OFF (the safe
	// stance): an untrusted repo's allows are ignored.
	permissionConfigs       stringList
	permissionsConventional bool
	importClaudePermissions bool
	trustProject            bool
	allowAllTools           bool

	// posture is the graduated operator posture ladder (strict < trusted < auto <
	// yolo). --posture sets it; --yolo and --trust-project are ALIASES that raise the
	// tier (resolvePosture in composition folds them MAX-tier). Empty = unset (the
	// composition default PostureStrict, unless an alias or the operator-global
	// settings.yaml posture: key raises it). The resolved tier is reported by the
	// structured `operator posture` startup diagnostic emitted by app.Build.
	posture string
	// deploymentID is the operator-set opaque label surfaced on GetServerInfo
	// (ADR 0248). Sanitised by sanitizeDeploymentID before it reaches app.Config.
	deploymentID string
	// corsOrigins is the EXACT-match browser origin allowlist for the HTTP API
	// (ADR 0248). Empty (the default) installs no CORS middleware at all.
	corsOrigins stringList
	// postureFlagSet is true when --posture was passed explicitly (set after parse via
	// fs.Visit), so composition lets CLI out-rank the settings.yaml posture: key.
	postureFlagSet bool
	// reasoningEffort is the operator-tier reasoning-effort default (ADR 0055): ""
	// or "auto" (unset → the provider default) or low/medium/high/xhigh/max.
	// Operator-tier only: the operator-global settings.yaml reasoning-effort: key
	// folds in, a project-tier key is WARN-ignored. A per-session CreateSession
	// reasoning_effort out-ranks it.
	reasoningEffort string
	// reasoningEffortFlagSet is true when --reasoning-effort was passed explicitly,
	// so composition lets CLI out-rank the settings.yaml reasoning-effort: key.
	reasoningEffortFlagSet bool
	// noSteer disables the mid-run steer inbox (steer-while-running, issue #512) —
	// the opt-OUT of a DEFAULT-ON knob (steer is armed unless this is passed or the
	// operator-tier settings.yaml steer: false folds in).
	noSteer bool
	// noSteerFlagSet is true when --no-steer was passed explicitly, so composition
	// lets CLI out-rank the settings.yaml steer: key.
	noSteerFlagSet bool

	// planModeAutoApprove is the OPT-IN, OPERATOR-TIER-ONLY, DEFAULT-OFF flag that
	// auto-approves a plan-mode PresentPlan ask when the run ends without a human
	// (issue #206 Wave 6a). It is a deliberate autonomous-approval capability — an
	// operator deployment decision, NEVER load-bearing for safety. Only meaningful
	// headless (--headless); an interactive deployment surfaces the plan to the
	// human instead.
	planModeAutoApprove bool

	// configPath is the explicit --config PATH selecting a daemon config file (issue
	// #338). Empty = no file loaded; the daemon runs from flags+env as before. It
	// is accepted only for serve/legacy; `mecated acp --config` is rejected.
	configPath string
	// configPathFlagSet is true when --config was passed explicitly (set after parse
	// via fs.Visit), so we can reject it in ACP mode and log the selected path.
	configPathFlagSet bool
	// cliExplicit tracks which migrated flags (now also settable via daemon config)
	// were set explicitly on the CLI. Keyed by flag name, populated via fs.Visit.
	cliExplicit map[string]bool
}

// stringList is a repeatable string flag.Value, preserving order across multiple
// occurrences. A single occurrence behaves exactly like a plain StringVar, so a
// flag using it stays backward-compatible with single-value invocations.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

func main() {
	if buildinfo.IsVersion(os.Args) {
		buildinfo.PrintVersion(os.Stdout, "mecated")
		return
	}
	res := resolveCommand(os.Args)

	// A usage error (unknown command / unknown-or-missing subcommand) fails
	// closed BEFORE the daemon boots: print the actionable error and exit
	// non-zero without constructing any listener. A bare/leading-flag
	// invocation additionally prints the top-level help (the operator needs the
	// command list, not just the error line).
	if res.err != nil {
		fmt.Fprintln(os.Stderr, "mecated:", res.err)
		if errors.Is(res.err, errBareInvocation) {
			fmt.Fprintln(os.Stderr)
			writeTopLevelHelp(os.Stderr)
		}
		os.Exit(2)
	}

	// A fully-handled one-shot offline subcommand: run it against the real
	// streams and exit with its error. --help from a subcommand is a successful
	// action (flag.ErrHelp): usage already printed, exit 0.
	if res.handled {
		if err := res.run(os.Stdin, os.Stdout, os.Stderr); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return
			}
			slog.Error("mecated subcommand failed", "err", err)
			os.Exit(1)
		}
		return
	}

	// Daemon path: thread the resolved mode + remaining argv into run() explicitly.
	if err := run(res.mode, res.remaining); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// --help already printed usage; exit success.
			return
		}
		slog.Error("mecated exited with error", "err", err)
		os.Exit(1)
	}
}

// runConfigInit implements `mecated config init [--print] [--force]`: it writes the
// generated commented settings.yaml skeleton to the operator-tier path
// (<XDG_CONFIG_HOME>/mecatl/settings.yaml). --print emits to out and writes NOTHING;
// without --force it REFUSES to overwrite an existing file (the error names the path);
// --force overwrites. The destination path is resolved via the SAME xdgconfig call +
// the SAME relative-path const (configgen.SettingsRelPath ← permconfig.UserSettingsRelPath)
// the resolver reads, so the write path provably equals the read path.
func runConfigInit(argv []string, out io.Writer) error {
	fs := flag.NewFlagSet("mecated config init", flag.ContinueOnError)
	fs.SetOutput(out)
	var printOnly, force bool
	fs.BoolVar(&printOnly, "print", false, "print the skeleton to stdout and write NO file (a paste-ready reference)")
	fs.BoolVar(&force, "force", false, "overwrite an existing settings.yaml (default: refuse, naming the path)")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	skeleton := configgen.Skeleton()
	if printOnly {
		_, err := io.WriteString(out, skeleton)
		return err
	}

	cfgDir := xdgconfig.UserConfigDir(xdgconfig.OSEnv)
	if cfgDir == "" {
		return fmt.Errorf("cannot resolve the user config directory (set $XDG_CONFIG_HOME or $HOME); use --print to emit the skeleton to stdout instead")
	}
	path := filepath.Join(cfgDir, configgen.SettingsRelPath)

	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists; pass --force to overwrite it (or --print to emit to stdout without writing)", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("checking %s: %w", path, err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(skeleton), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	_, _ = fmt.Fprintf(out, "wrote operator settings skeleton to %s\n", path)
	_, _ = fmt.Fprintf(out, "edit it, then (re)start mecated. See docs/configuration-reference.md for the full key reference.\n")
	return nil
}

// runConfigDaemonInit implements `mecated config daemon init [--print] [--force]`:
// it scaffolds a minimal, commented v1 daemon.yaml (issue #338, ADR 0088) at the
// documented conventional path <XDG_CONFIG_HOME>/mecatl/daemon.yaml. It does NOT
// cause automatic loading — the file is loaded ONLY when `mecated serve --config
// PATH` is supplied explicitly. --print emits the skeleton to out and writes
// NOTHING; without --force it REFUSES to overwrite an existing file (the error
// names the path); --force overwrites. It reuses the daemonconfig schema (the
// embedded skeleton) and the SAME xdgconfig resolution as config init, so the
// write path equals the conventional path `config daemon validate` defaults to.
// `config init` keeps ownership of settings.yaml; this owns daemon topology only.
func runConfigDaemonInit(argv []string, out io.Writer) error {
	fs := flag.NewFlagSet("mecated config daemon init", flag.ContinueOnError)
	fs.SetOutput(out)
	var printOnly, force bool
	fs.BoolVar(&printOnly, "print", false, "print the daemon.yaml skeleton to stdout and write NO file (a paste-ready reference)")
	fs.BoolVar(&force, "force", false, "overwrite an existing daemon.yaml (default: refuse, naming the path)")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	skeleton := daemonconfig.Skeleton()
	if printOnly {
		_, err := io.WriteString(out, skeleton)
		return err
	}

	cfgDir := xdgconfig.UserConfigDir(xdgconfig.OSEnv)
	if cfgDir == "" {
		return fmt.Errorf("cannot resolve the user config directory (set $XDG_CONFIG_HOME or $HOME); use --print to emit the skeleton to stdout instead")
	}
	path := filepath.Join(cfgDir, daemonconfig.DaemonConfigRelPath)

	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists; pass --force to overwrite it (or --print to emit to stdout without writing)", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("checking %s: %w", path, err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(skeleton), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	_, _ = fmt.Fprintf(out, "wrote daemon config skeleton to %s\n", path)
	_, _ = fmt.Fprintf(out, "it is NOT auto-loaded; start the server with 'mecated serve --config %s' to use it.\n", path)
	_, _ = fmt.Fprintf(out, "validate it with 'mecated config daemon validate'. See docs/usage/mecated.md for the full reference.\n")
	return nil
}

// runConfigDaemonValidate implements `mecated config daemon validate [--file
// PATH]`: it strictly parses and validates a daemon.yaml file (schema + the
// effective semantic validation possible WITHOUT starting/binding). --file
// selects the file (default: the conventional <XDG_CONFIG_HOME>/mecatl/daemon.yaml
// for convenience). On success it names the file + version and reminds how to use
// it; on failure it reports the schema/semantic error. It NEVER prints secrets or
// raw file content — the daemon config carries no token value, and the success
// line carries only the path + version. It reuses daemonconfig.Load (strict parse)
// + daemonconfig.Validate (rate-limit/burst bounds), the SAME schema/runtime
// validation the serve path applies, so a validated file provably loads.
func runConfigDaemonValidate(argv []string, out io.Writer) error {
	fs := flag.NewFlagSet("mecated config daemon validate", flag.ContinueOnError)
	fs.SetOutput(out)
	var file string
	fs.StringVar(&file, "file", "", "path to the daemon.yaml to validate (default: the conventional $XDG_CONFIG_HOME/mecatl/daemon.yaml)")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	path := file
	if path == "" {
		// Default to the conventional path for convenience (NOT auto-load — this
		// is a validate action, not a serve path).
		cfgDir := xdgconfig.UserConfigDir(xdgconfig.OSEnv)
		if cfgDir == "" {
			return fmt.Errorf("cannot resolve the user config directory (set $XDG_CONFIG_HOME or $HOME); pass --file PATH to name the daemon.yaml to validate")
		}
		path = filepath.Join(cfgDir, daemonconfig.DaemonConfigRelPath)
	}

	cfg, err := daemonconfig.Load(path)
	if err != nil {
		return err
	}
	if err := daemonconfig.Validate(cfg); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "%s: valid daemon config (version %s)\n", path, cfg.Version)
	_, _ = fmt.Fprintf(out, "start the server with 'mecated serve --config %s' to use it.\n", path)
	return nil
}

// runSkillsPromote implements `mecated skills promote --skills-draft-dir
// <quarantine> --skills-dir <active> [--yes] <name>`: the operator-trust action that
// shows the full candidate, asks for confirmation (unless --yes), re-validates it
// (provenance + structure + injection scan), and moves it into the active skills
// tree. It requires filesystem access the model does not have, so it is the only
// path from model-authored quarantine to the trusted, live skill catalog. Flags
// precede the positional <name> (Go's flag parser stops at the first positional).
func runSkillsPromote(argv []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("mecated skills promote", flag.ContinueOnError)
	fs.SetOutput(out)
	var quarantine, active string
	var assumeYes bool
	fs.StringVar(&quarantine, "skills-draft-dir", "", "the QUARANTINE directory the candidate was drafted into")
	fs.StringVar(&active, "skills-dir", "", "the ACTIVE skills directory to promote the candidate into")
	fs.BoolVar(&assumeYes, "yes", false, "skip the interactive content review and promote without confirmation (scripted/CI use only)")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	name := fs.Arg(0)
	if name == "" || quarantine == "" || active == "" {
		return fmt.Errorf("usage: mecated skills promote --skills-draft-dir <quarantine> --skills-dir <active> [--yes] <name>")
	}

	// Show the operator the FULL untrusted candidate (frontmatter + body) before
	// promoting: promotion is what makes model-authored text TRUSTED, so a human
	// must actually read it. The injection scan in skills.Promote is a backstop,
	// not a review.
	raw, err := skills.ReadCandidate(quarantine, name)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "Warning: `skills promote` is the deprecated legacy quarantine workflow; it never activates evaluated-lifecycle repository records.")
	_, _ = fmt.Fprintf(out, "\n--- candidate skill %q (model-authored, UNTRUSTED until promoted) ---\n%s\n--- end candidate ---\n\n", name, raw)

	if !assumeYes {
		_, _ = fmt.Fprintf(out, "Promote %q into %s? This makes the above content TRUSTED and loadable by every future session. [y/N]: ", name, active)
		line, _ := bufio.NewReader(in).ReadString('\n')
		if ans := strings.ToLower(strings.TrimSpace(line)); ans != "y" && ans != "yes" {
			return fmt.Errorf("promotion of %q aborted by operator", name)
		}
	}

	if err := skills.Promote(quarantine, active, name); err != nil {
		return err
	}
	slog.Info("skill promoted to the active catalog (takes effect on next server start)",
		"name", name, "from", quarantine, "to", active)
	return nil
}

// runPerfMCPPrintConfig implements `mecated perf-mcp print-config [--metrics-addr
// host:port]`: it prints the paste-ready client .mcp.json snippet pointing at the
// loopback perf MCP server's /mcp endpoint. Per decision 6 (loopback, no auth) the
// snippet carries NO Authorization header — adding one is a future off-loopback
// concern. --metrics-addr sets the host:port in the printed URL (default
// 127.0.0.1:9090, matching defaultMetricsAddr). Output goes to stdout so it can be
// redirected into a client config.
func runPerfMCPPrintConfig(argv []string, out io.Writer) error {
	fs := flag.NewFlagSet("mecated perf-mcp print-config", flag.ContinueOnError)
	fs.SetOutput(out)
	var addr string
	fs.StringVar(&addr, "metrics-addr", defaultMetricsAddr, "the loopback admin listen address the perf MCP server is mounted on (host:port); sets the host:port in the printed URL")
	if err := fs.Parse(argv); err != nil {
		return err
	}

	// Build the snippet via the JSON encoder so the structure (and absence of a
	// headers/Authorization field) is enforced by the type, not a fragile format
	// string. The "type":"http" transport matches the streamable-HTTP handler.
	type perfServer struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	cfg := struct {
		McpServers map[string]perfServer `json:"mcpServers"`
	}{
		McpServers: map[string]perfServer{
			"mecatl-perf": {Type: "http", URL: "http://" + addr + "/mcp"},
		},
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(cfg)
}

// run parses flags, builds the engine/service via internal/app, and serves until a
// termination signal arrives. mode is the resolved canonical command word
// ("serve" or "acp") and remaining is the flag tail (argv with the command word
// already stripped). It is separated from main so it can return errors cleanly.
func run(mode commandMode, remaining []string) error {
	cfg, err := parseFlagsMode(mode, remaining)
	if err != nil {
		return err
	}

	// The canonical command word selects the mode: `mecated acp` runs the ACP
	// stdio surface; `mecated serve` runs the network daemon.
	cfg.acp = mode == modeACP

	// Daemon config file (issue #338): load ONLY when --config is explicitly
	// supplied (no auto-load). Rejected for ACP mode (listener topology does not
	// apply). Loaded AFTER flag parse and BEFORE effective-value validation /
	// TLS / logging, so a malformed or unknown-key file fails before app.Build /
	// listener creation. The load/merge step is a testable helper with an injected
	// loader (review fix #2); effective-value cross-validation runs AFTER the
	// merge so a file-supplied value cannot bypass the guards (review fix #1).
	if err := loadAndMergeDaemonConfig(&cfg, cfg.acp, daemonconfig.Load); err != nil {
		return err
	}
	// Effective-value validation (perf-MCP loopback/empty-metrics + rate-limit
	// sanity) runs on the POST-merge config, before app.Build / listener binding.
	// This is the pure helper that closes the file-source bypass: validating in
	// parseFlagsMode (CLI-only values) left metrics_addr: 0.0.0.0:9090 + --perf-mcp
	// able to slip through via the file (review fix #1).
	if err := validateEffectiveConfig(cfg); err != nil {
		return err
	}
	if cfg.mockScript != "" {
		cfg.mockProvider, err = loadMockScript(cfg.mockScript)
		if err != nil {
			return err
		}
	}

	logger := cliconfig.NewTextLogger(os.Stderr, cfg.logLevel, cfg.logLevelWarning)
	// slog.SetDefault stays for the daemon: this is the DELIBERATE, PERMANENT
	// third-party-slog bridge — a server's operational output belongs on
	// stderr/journald, so any ambient slog.Default() use (a transitive dependency, the
	// perf surface's nil-Logger fallback) is correctly routed there. cmd/ mains are the
	// only layer allowed to call slog.SetDefault; all of internal/ flows through the
	// injected port.Diagnostics (ban-guarded). The TUI, by contrast, redirects the
	// default to a FILE because it owns the alt-screen. See docs/adr/0020-diagnostics.md.
	slog.SetDefault(logger)
	// Diagnostics and ambient slog share this configured logger.
	// The warning, if any, was emitted by the logger factory above.
	// Diagnostics sink for the composition's build-once facts and the relocated
	// operational logging. It wraps the SAME stderr/text/Info logger installed above,
	// so the facts print identically — but flow through the injected port.Diagnostics
	// rather than slog.Default().
	diag := slogdiag.NewFromLogger(logger)

	// Log the selected daemon config path when one was loaded (issue #338).
	// The path was validated during merge; log it so operators can confirm
	// which file was read.
	if cfg.configPathFlagSet {
		slog.Info("daemon config loaded", "path", cfg.configPath)
	}

	// Operator posture: refuse/WARN for the AUTHORITATIVE composed tier (the
	// --posture flag + --yolo/--trust-project aliases + the operator-global
	// settings.yaml posture: key — the SAME tier app.Build resolves). Checked AFTER the
	// slog handler is installed and BEFORE app.Build, so it covers both the ACP and
	// network serving modes. The root/no-sandbox refusal here is a fast path; app.Build
	// re-checks it as the fail-closed backstop. The structured `operator posture`
	// diagnostic emitted by app.Build after resolveTrust is the sole observation/debug
	// surface for the resolved tier + per-defence state.
	if perr := applyPostureCLI(cfg, diag); perr != nil {
		return perr
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Observability: install the OTel metrics + tracing pipeline, the process
	// gauges, the runtime profiling knobs, the flight recorder, and the goroutine-
	// leak watchdog. Extracted into one helper so run()'s cyclomatic complexity
	// stays under the lint gate; the helper owns the setup branches and returns
	// the handles run() threads into app.Build and serve(). run() owns the
	// shutdown defers (telemetry flush + flight-recorder stop) so they unwind on
	// the daemon's exit, not the helper's.
	obs, err := setupObservability(ctx, cfg, diag)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if serr := obs.providers.Shutdown(shutdownCtx); serr != nil {
			slog.Warn("telemetry shutdown", "err", serr)
		}
	}()
	if obs.recorder != nil {
		defer obs.recorder.Stop()
	}

	tracing := telemetry.NewTracing(otel.GetTracerProvider())

	// Role-scoped main pair (issue #47): the MAIN engine records through the
	// role="main" view so EVERY series carries the role label uniformly —
	// children get their own bounded-family views via the scoper below.
	mainScoped := obs.metrics.WithRole(telemetry.RoleMain)

	// Slow-turn ring buffer: when the perf MCP server is mounted it observes
	// EvTurnEnd as one more EventSink fanned out alongside metrics/tracing, so its
	// list_slow_turns tool sees the SAME TurnEndPayload the latency histograms do.
	// It stores scalars only (redaction by shape) and spawns no goroutine. Built
	// only when --perf-mcp is set so a bare daemon carries no extra sink. Main
	// turns enter it with role="main"; child turns ride the scoper's fan-out.
	var slowTurns *telemetry.SlowTurnBuffer
	sinks := []port.EventSink{mainScoped, tracing}
	if cfg.perfMCP {
		slowTurns = telemetry.NewSlowTurnBuffer(telemetry.DefaultSlowTurnCapacity, time.Now)
		sinks = append(sinks, slowTurns.WithRole(telemetry.RoleMain))
	}
	sink := telemetry.NewSink(sinks...)

	// Child role scoper (issue #47): the composition hands each CHILD engine a
	// role-scoped (EventSink, ToolCallRecorder) pair keyed on the BOUNDED family
	// label internal/app's roleFamily already resolved ("subagent"/"member"/…).
	// The returned sink ALSO fans into the shared slow-turn ring buffer (when
	// mounted) so child turns appear in list_slow_turns carrying their role.
	roleScoper := func(familyRole string) (port.EventSink, port.ToolCallRecorder) {
		scoped := obs.metrics.WithRole(familyRole)
		childSinks := []port.EventSink{scoped}
		if slowTurns != nil {
			childSinks = append(childSinks, slowTurns.WithRole(familyRole))
		}
		return telemetry.NewSink(childSinks...), scoped
	}

	composition := appConfig(cfg, sink, mainScoped, roleScoper, obs.metrics, diag)
	built, err := app.Build(ctx, composition)
	if err != nil {
		return err
	}
	defer built.Close()

	// ACP mode: serve the Agent Client Protocol over stdio instead of the network
	// daemon. The same engine/service assembly (app.Build) backs it; only the wire
	// surface differs. No TLS/auth/rate-limit — stdio is a local parent-process
	// boundary. Logs still go to stderr (set above), keeping stdout pure JSON-RPC.
	if cfg.acp {
		// session/load (resume) is offered only when a durable session store is
		// configured: the in-memory store would lose snapshots across a restart, so
		// loadSession stays false there. A remote session-store driver is durable
		// (it replaces the JSONL dir), so it qualifies too.
		return serveACP(ctx, built.Service, cfg.storeDir != "" || cfg.sessionStoreURL != "", diag)
	}

	return serve(ctx, cfg, built.Service, obs.providers.Registry, obs.recorder, slowTurns)
}

// observability holds the handles setupObservability returns and run() threads
// into app.Build / serve / serveACP.
type observability struct {
	providers telemetry.Providers
	metrics   *telemetry.Metrics
	recorder  *telemetry.FlightRecorder
}

// setupObservability installs the OTel metrics pipeline (always on) + the OTLP
// trace exporter (when --otlp-endpoint is set), the process gauges, the runtime
// profiling knobs, the FlightRecorder, and the goroutine-leak watchdog. It
// returns the handles run() needs; the caller owns the providers.Shutdown and
// recorder.Stop defers (so shutdown winds down on the daemon's exit, not here).
// Extracted from run() to keep its cyclomatic complexity under the lint gate.
func setupObservability(ctx context.Context, cfg config, diag port.Diagnostics) (observability, error) {
	providers, err := telemetry.Setup(ctx, telemetry.OTLPConfig{
		Endpoint:    cfg.otlpEndpoint,
		Protocol:    cfg.otlpProtocol,
		Insecure:    cfg.otlpInsecure,
		ServiceName: "mecatl",
	})
	if err != nil {
		return observability{}, fmt.Errorf("setup telemetry: %w", err)
	}
	if cfg.otlpEndpoint == "" {
		slog.Info("tracing disabled (--otlp-endpoint empty); metrics + runtime collector active")
	} else {
		slog.Info("tracing enabled (OTLP exporter installed)", "endpoint", cfg.otlpEndpoint, "protocol", cfg.otlpProtocol, "insecure", cfg.otlpInsecure)
	}

	// The OTel meter provider feeds the EventSink/Logger adapter; its prometheus
	// exporter renders those series on providers.Registry, served by /metrics.
	metrics, err := telemetry.NewMetrics(providers.Meter)
	if err != nil {
		return observability{}, fmt.Errorf("setup metrics: %w", err)
	}
	// Process-RSS gauge (mecatl.process.rss): Linux-only, no-op elsewhere. It
	// rides the same MeterProvider so it renders on /metrics (decision 9).
	if rerr := telemetry.RegisterProcessGauges(providers.Meter, diag); rerr != nil {
		return observability{}, fmt.Errorf("setup process gauges: %w", rerr)
	}

	// Runtime profiling knobs: arm mutex/block sampling only when explicitly
	// requested (both add overhead; default 0 = off). pprof exposes the resulting
	// profiles at /debug/pprof/{mutex,block} on the loopback admin mux.
	if cfg.mutexProfileFraction > 0 {
		runtime.SetMutexProfileFraction(cfg.mutexProfileFraction)
		slog.Info("mutex profiling enabled", "fraction", cfg.mutexProfileFraction)
	}
	if cfg.blockProfileRate > 0 {
		runtime.SetBlockProfileRate(cfg.blockProfileRate)
		slog.Info("block profiling enabled", "rate_ns", cfg.blockProfileRate)
	}

	// FlightRecorder: arm the bounded execution-trace ring buffer (default ON) so
	// /debug/flightrecorder can snapshot recent activity. Stopped on shutdown so
	// the runtime trace subscription is torn down (goleak-clean).
	var recorder *telemetry.FlightRecorder
	if cfg.flightRecorder {
		// Use the process-singleton accessor: only one flight recorder may be
		// active process-wide (a stdlib constraint), and a second consumer (the
		// embedded server) must coalesce onto it rather than silently lose a Start.
		rec, rerr := telemetry.ProcessFlightRecorder(trace.FlightRecorderConfig{})
		switch {
		case errors.Is(rerr, telemetry.ErrFlightRecorderAlreadyActive):
			// Already armed elsewhere in this process — reuse the shared instance.
			recorder = rec
			slog.Info("flight recorder already active process-wide; reusing the shared instance (loopback /debug/flightrecorder)")
		case rerr != nil:
			slog.Warn("flight recorder failed to start; continuing without it", "err", rerr)
		default:
			recorder = rec
			slog.Info("flight recorder armed (loopback /debug/flightrecorder)")
		}
	}

	// Live goroutine-leak alarm (decision 10): the runtime collector already
	// exports the goroutine COUNT as a /metrics series; this is the operator-facing
	// ALARM on top — a background watchdog logging slog.Warn when the count exceeds
	// a configured ceiling. Disabled by default (threshold 0). It is bound to ctx
	// so it unwinds on shutdown — it would be ironic for the leak alarm to leak.
	if cfg.goroutineWarnThreshold > 0 {
		telemetry.StartGoroutineWatchdog(ctx, cfg.goroutineWarnThreshold, cfg.goroutineWarnInterval, runtime.NumGoroutine, slog.Default())
		slog.Info("goroutine-leak watchdog armed", "threshold", cfg.goroutineWarnThreshold, "interval", cfg.goroutineWarnInterval)
	}
	return observability{providers: providers, metrics: metrics, recorder: recorder}, nil
}

// mecatedServerImplementation is the stable family reported to authenticated clients.
const mecatedServerImplementation = "mecated"

// appConfig constructs the command root's declarative app.Config. app.Build loads the
// injected provider credential after resolving operator definitions.
func appConfig(cfg config, sink port.EventSink, recorder port.ToolCallRecorder, roleScoper func(string) (port.EventSink, port.ToolCallRecorder), metrics *telemetry.Metrics, diag port.Diagnostics) app.Config {
	out := app.Config{
		Workspace:                     cfg.workspace,
		ClientMCPOnCreate:             clientMCPOnCreateForListeners(cfg),
		Model:                         cfg.model,
		DefaultProvider:               cfg.defaultProvider,
		DefaultModel:                  cfg.defaultModel,
		DefaultProviderFlagSet:        cfg.defaultProviderFlagSet,
		UseOpenAI:                     cfg.useOpenAI,
		UseMock:                       cfg.useMock,
		MockProvider:                  cfg.mockProvider,
		StoreDir:                      cfg.storeDir,
		Shell:                         cfg.shell,
		NoBash:                        cfg.noBash,
		AuthorityEvaluator:            cfg.authorityEvaluator,
		CedarAuthorityPolicy:          cfg.cedarAuthorityPolicy,
		OwnershipEnforced:             cfg.oidc.Enabled(),
		Compaction:                    cfg.compaction,
		Tokenizer:                     cfg.tokenizer,
		ContextWindowOverride:         cfg.contextWindowOverride,
		LLMMaxAttempts:                cfg.llmMaxAttempts,
		LLMPerAttemptTimeout:          cfg.llmPerAttemptTimeout,
		LLMStreamIdleTimeout:          cfg.llmStreamIdleTimeout,
		LLMBreakerThreshold:           cfg.llmBreakerThreshold,
		LLMBreakerCooldown:            cfg.llmBreakerCooldown,
		MaxRunTokens:                  cfg.maxRunTokens,
		MaxTeamTokens:                 cfg.maxTeamTokens,
		PromptCacheDisabled:           cfg.noPromptCache,
		AnthropicCacheTTL:             cfg.anthropicCacheTTL,
		MemoryDir:                     cfg.memoryDir,
		MemoryConsolidateInterval:     cfg.memoryConsolidateInterval,
		ChildRetention:                cfg.childRetention,
		ChildRetentionMaxPerFamily:    cfg.childRetentionMaxPerFamily,
		MainRetention:                 cfg.mainRetention,
		MainRetentionMaxTotal:         cfg.mainRetentionMaxTotal,
		ScheduleFireRetention:         cfg.scheduleFireRetention,
		ScheduleFireRetentionMaxTotal: cfg.scheduleFireRetentionMaxTotal,
		RetentionCLISet:               cfg.retentionCLISet,
		AcknowledgeMainRetention:      cfg.acknowledgeMainRetention,
		ChildGCInterval:               cfg.childGCInterval,
		SessionStoreURL:               cfg.sessionStoreURL,
		MemoryStoreURL:                cfg.memoryStoreURL,
		EventLogURL:                   cfg.eventLogURL,
		ScheduleStoreURL:              cfg.scheduleStoreURL,
		LearningStoreURL:              cfg.learningStoreURL,
		SessionLeaseURL:               cfg.sessionLeaseURL,
		SessionLeaseDir:               cfg.sessionLeaseDir,
		SessionLeaseK8sNamespace:      cfg.sessionLeaseK8sNamespace,
		SessionLeaseTTL:               cfg.sessionLeaseTTL,
		SessionLeaseRenewInterval:     cfg.sessionLeaseRenewInterval,
		SchedulerEnabled:              !cfg.noScheduler,
		SchedulerTickInterval:         cfg.schedulerTickInterval,
		SchedulerMinInterval:          cfg.schedulerMinInterval,
		SchedulerMaxConcurrentFires:   cfg.schedulerMaxConcurrentFires,
		SkillSourceURL:                cfg.skillSourceURL,
		SoulSourceURL:                 cfg.soulSourceURL,
		AgentSourceURL:                cfg.agentSourceURL,
		CommandSourceURL:              cfg.commandSourceURL,
		DriverAuthToken:               cfg.driverAuthToken,
		DriverTLS:                     cfg.driverTLS,
		DriverTLSCA:                   cfg.driverTLSCA,
		DriverTLSCert:                 cfg.driverTLSCert,
		DriverTLSKey:                  cfg.driverTLSKey,
		SoulPath:                      cfg.soulFile,
		NoSoul:                        cfg.noSoul,
		ApproveSoul:                   cfg.approveSoul,
		SoulStrict:                    cfg.soulStrict,
		UserModelDir:                  cfg.userModelDir,
		NoUserModel:                   cfg.noUserModel,
		UserModelReview:               cfg.userModelReview,
		UserModelReviewInterval:       cfg.userModelReviewInterval,
		UserModelConsolidateInterval:  cfg.userModelConsolidateInterval,
		SkillsDirs:                    cfg.skillsDirs,
		SkillsConventional:            cfg.skillsConventional,
		SkillsDraftDir:                cfg.skillsDraftDir,
		SkillsDraftThreshold:          cfg.skillsDraftThreshold,
		AgentsDirs:                    cfg.agentsDirs,
		AgentsConventional:            cfg.agentsConventional,
		SubagentModel:                 cfg.subagentModel,
		SubagentAskReviewerModel:      cfg.subagentAskReviewer,
		SubagentAskReviewerMaxDenies:  cfg.subagentAskReviewerMaxDenies,
		SubagentAskReviewerPolicy:     cfg.subagentAskReviewerPolicy,
		// Subagent model router (ADR 0042): the router is enabled by the operator-tier
		// models.router: taxonomy (folded by foldOperatorModelRouter); this flag is a
		// KILL-SWITCH. =false forces the router OFF (RouterDisabled). A bare flag / =true
		// is a harmless no-op (the router stays governed by the taxonomy). Unset leaves
		// the router governed by taxonomy presence. The YAML disabled: key ORs in during
		// the fold.
		RouterDisabled: cfg.subagentModelRouterSet && !cfg.subagentModelRouter,
		// Guardrails (issue #27): the checker model + master kill-switch. The rule list
		// and cost knobs are operator-tier YAML only (the `guardrails:` subtree of the
		// user-global settings.yaml), folded onto Config by foldOperatorGuardrails — a
		// flag cannot express a rule list.
		GuardrailsModel:          cfg.guardrailsModel,
		GuardrailsDisabled:       cfg.guardrailsOff,
		ModelAliases:             cfg.modelAliases.AsMap(),
		ModelSlots:               cfg.modelSlots.AsMap(),
		CommandsDir:              cfg.commandsDir,
		EnableCommands:           cfg.enableCommands,
		EnableParallel:           cfg.enableParallel,
		WebSearchURL:             cfg.websearchURL,
		WebSearchAPIKey:          cfg.websearchAPIKey,
		WebSearchAuthHeader:      cfg.websearchAuthHeader,
		WebSearchQueryParam:      cfg.websearchQueryParam,
		SearXNGURL:               cfg.searxngURL,
		BraveAPIKey:              cfg.braveAPIKey,
		ExaAPIKey:                cfg.exaAPIKey,
		WebSearchOff:             cfg.websearchOff,
		ForkPreservedCap:         cfg.forkPreservedCap,
		EnableTeams:              cfg.enableTeams,
		MCPServers:               cfg.mcpServers.Servers(),
		MCPProfileLoader:         cliconfig.NewMCPProfileResolver(cfg.mcpServers, os.LookupEnv),
		ProviderCredentialLoader: cliconfig.NewProviderCredentialResolver(cfg.providerFlags, cfg.providerCredentials),
		ProviderOverrides:        cfg.providerFlags.EndpointOverrides(),
		MCPResourceTools:         cfg.mcpResourceTools,
		MCPPrompts:               cfg.mcpPrompts,
		ToolHiveEnabled:          cfg.toolHiveEnabled,
		ToolHiveGroup:            cfg.toolHiveGroup,
		PermissionsConventional:  cfg.permissionsConventional,
		ImportClaudePermissions:  cfg.importClaudePermissions,
		TrustProject:             cfg.trustProject,
		PermissionConfigs:        cfg.permissionConfigs,
		AllowAllTools:            cfg.allowAllTools,
		// Posture ladder: --posture sets the tier directly; --yolo/--trust-project are
		// aliases composition folds MAX-tier (resolvePosture). postureFlagSet lets CLI
		// out-rank the operator-global settings.yaml posture: key. Privileged is the
		// "root && !sandbox" predicate fed to Build's AUTHORITATIVE root-refusal, so a
		// YAML-only allow-all tier cannot escape it.
		Posture:        app.ParsePosture(cfg.posture),
		PostureFlagSet: cfg.postureFlagSet,
		DeploymentID:   cfg.deploymentID,
		// Reasoning-effort tier (ADR 0055): operator-tier only; reasoningEffortFlagSet
		// lets CLI out-rank the operator-global settings.yaml reasoning-effort: key.
		ReasoningEffort:        cfg.reasoningEffort,
		ReasoningEffortFlagSet: cfg.reasoningEffortFlagSet,
		Privileged:             privilegedProcess(),
		// mecated serves the bidi Converse + HTTP-SSE surfaces, whose clients CAN
		// answer a permission ask (ResumeApproval) — so by default a subagent's
		// unresolved Bash ask is SURFACED to the attached human rather than
		// auto-denied. --headless inverts this for an autonomous / CI deployment whose
		// clients drive runs but never answer permission prompts: surfacing there would
		// park the child until run-end, so we run NON-interactive (Interactive=false),
		// engaging the auto-deny path and the opt-in --subagent-ask-reviewer. (The
		// offline demo likewise leaves app.Config.Interactive false.)
		Interactive: !cfg.headless,
		// Headless is explicit deployment identity. The posture ladder raises
		// workspace trust only when this is false.
		Headless:               cfg.headless,
		Sink:                   sink,
		ToolCallRecorder:       recorder,
		MetricsRoleScoper:      roleScoper,
		ScheduleMetricsEmitter: metrics.EmitSchedule,
		LearningMetricsEmitter: metrics.EmitLearning,
		Diagnostics:            diag,
		// Plan-mode auto-approve (issue #206 Wave 6a): the OPT-IN operator flag.
		PlanModeAutoApprove: cfg.planModeAutoApprove,
		// Steer (steer-while-running, issue #512): the opt-OUT of the default-ON
		// mid-run inbox. noSteerFlagSet lets CLI out-rank the settings.yaml steer: key.
		DisableSteer:        cfg.noSteer,
		DisableSteerFlagSet: cfg.noSteerFlagSet,
	}
	out.ServerImplementation = mecatedServerImplementation
	// Project the once-resolved credentials and parsed base URLs without I/O.
	// An OPENAI_API_KEY in the environment implies the user wants the real provider —
	// the same flip the previous inline read did, now keyed off the resolved keys.
	keys := cfg.providerCredentials
	cfg.providerFlags.ApplyResolved(&out, keys)
	if keys.OpenAI != "" {
		out.UseOpenAI = true
	}
	if keys.AuthFileWarning != "" {
		slog.Warn(keys.AuthFileWarning)
	}
	cfg.toolhiveLLMFlags.Apply(&out)
	return out
}

// applyPostureCLI resolves the AUTHORITATIVE posture tier (the --posture flag +
// --yolo/--trust-project aliases + the operator-global settings.yaml posture: key) and
// runs its refuse / WARN surface, extracted from run() to keep that function's
// cyclomatic complexity bounded. A non-nil err is the root/no-sandbox refusal (a
// fast path — app.Build re-checks it authoritatively as the fail-closed backstop).
func applyPostureCLI(cfg config, diag port.Diagnostics) error {
	effPosture := app.ResolveAuthoritativePosture(posturePreCheckConfig(cfg, diag))
	if !app.IsKnownPostureToken(cfg.posture) {
		slog.Warn("unknown --posture value; failing closed to strict", "value", cfg.posture)
	}
	if rerr := app.PostureRefusalReason(effPosture, privilegedProcess()); rerr != nil {
		return rerr
	}
	switch effPosture {
	case app.PostureStrict:
		// Silent: the safe default.
	case app.PostureTrusted:
		slog.Info("operator posture: trusted (a discovered project's ALLOW rules are honoured; no allow-all, no substitution loosening)")
	case app.PostureAuto:
		slog.Warn("OPERATOR POSTURE: auto — allow-all is ACTIVE server-wide (the built-in mutate-ask floor + the MAIN agent's substitution floor are waived). A Deny in any scope and any deliberately configured Ask still apply. The CHILD prompt-injection defense stays ON: a subagent's $()/backtick/heredoc still resolves through the child-ask model. Recommended for UNATTENDED single-tenant use.")
	case app.PostureYolo:
		slog.Warn("OPERATOR POSTURE: yolo — allow-all server-wide AND the CHILD prompt-injection defense is OFF: $()/backtick/heredoc commands AUTO-RUN in subagents/branches. A Deny in any scope and any deliberately configured Ask still apply. ISOLATED, EPHEMERAL, SINGLE-TENANT deployments ONLY. NOTE behaviour change: --yolo now ALSO loosens the child substitution floor.")
	}
	return nil
}

// posturePreCheckConfig maps just the posture-relevant fields of the cmd config onto a
// minimal app.Config for the fast-path-refusal read. It carries the SAME
// permission-config discovery knobs Build uses (so the transient resolver finds the
// same operator-global settings.yaml posture: key) plus the --posture/--yolo/
// --trust-project inputs. It does NOT need the provider/engine fields — app.Build owns
// the authoritative resolution + the engine; this is only the early read.
func posturePreCheckConfig(cfg config, diag port.Diagnostics) app.Config {
	return app.Config{
		Workspace:               cfg.workspace,
		PermissionsConventional: cfg.permissionsConventional,
		ImportClaudePermissions: cfg.importClaudePermissions,
		PermissionConfigs:       cfg.permissionConfigs,
		Posture:                 app.ParsePosture(cfg.posture),
		PostureFlagSet:          cfg.postureFlagSet,
		DeploymentID:            cfg.deploymentID,
		AllowAllTools:           cfg.allowAllTools,
		TrustProject:            cfg.trustProject,
		Headless:                cfg.headless,
		Interactive:             !cfg.headless,
		Diagnostics:             diag,
	}
}

// privilegedProcess reports the cmd-computed "root WITHOUT a declared sandbox"
// predicate (euid 0 && MECATL_SANDBOX/IS_SANDBOX unset) threaded into the posture
// root-refusal (the cmd owns the os/env reads; internal/app takes the bool). It is the
// SAME value fed to app.Config.Privileged so the fast-path and Build agree.
func privilegedProcess() bool {
	return os.Geteuid() == 0 && !sandboxDeclared()
}

func sandboxDeclared() bool {
	return os.Getenv("MECATL_SANDBOX") == "1" || os.Getenv("IS_SANDBOX") == "1"
}

// mergeDaemonConfig folds the daemon config file (loaded only when --config is
// explicitly supplied) into the parsed CLI config. For each migrated field, the
// config file value is applied IFF the corresponding CLI flag was NOT explicitly
// set (tracked in cfg.cliExplicit). An explicit CLI empty/zero overrides the file
// value. Built-in defaults < config file < explicit CLI. The daemon config's RAW
// content and any secret-bearing fields are never logged; effective security-
// POSTURE values (addresses, TLS presence, rate-limit) may be — see the
// daemonconfig package doc. Extracted from run() to keep cyclomatic complexity
// under the lint gate.
func mergeDaemonConfig(cfg *config, dc *daemonconfig.Config) {
	if cfg.cliExplicit == nil {
		cfg.cliExplicit = make(map[string]bool)
	}
	if dc.GRPCAddr != nil {
		// Recorded even when the CLI wins, because the exclusion in AC8.1 is about
		// what the operator ASKED for: a file that names grpc_addr alongside
		// --grpc-unix-socket is the same contradiction whichever value would win.
		cfg.grpcAddrFromFile = true
		if !cfg.cliExplicit["grpc-addr"] {
			cfg.grpcAddr = *dc.GRPCAddr
		}
	}
	if !cfg.cliExplicit["http-addr"] && dc.HTTPAddr != nil {
		cfg.httpAddr = *dc.HTTPAddr
	}
	if !cfg.cliExplicit["metrics-addr"] && dc.MetricsAddr != nil {
		cfg.metricsAddr = *dc.MetricsAddr
	}
	if !cfg.cliExplicit["tls-cert"] && dc.TLSCert != nil {
		cfg.tlsCert = *dc.TLSCert
	}
	if !cfg.cliExplicit["tls-key"] && dc.TLSKey != nil {
		cfg.tlsKey = *dc.TLSKey
	}
	if !cfg.cliExplicit["client-ca"] && dc.ClientCA != nil {
		cfg.clientCA = *dc.ClientCA
	}
	if !cfg.cliExplicit["rate-limit"] && dc.RateLimit != nil {
		cfg.rateLimit = *dc.RateLimit
	}
	if !cfg.cliExplicit["rate-burst"] && dc.RateBurst != nil {
		cfg.rateBurst = *dc.RateBurst
	}
}

// configLoader is the daemon config file-loading seam. The production caller
// passes daemonconfig.Load; tests inject a stub to prove the --config path is
// rejected in ACP, never auto-loaded when --config is absent, and merged on
// success — without touching the filesystem.
type configLoader func(path string) (*daemonconfig.Config, error)

// loadAndMergeDaemonConfig is the explicit-config load/reject/merge step,
// extracted from run() so it is unit-testable with an injected loader. It loads
// the daemon config file ONLY when --config was supplied explicitly, rejects it
// in ACP mode (listener topology does not apply to stdio), and folds the file
// into cfg. When --config is absent it is a no-op (no conventional auto-load),
// preserving byte-identical zero-config behaviour. It runs BEFORE
// validateEffectiveConfig so a malformed/unknown-key file fails before any
// effective-value validation, TLS build, or listener binding.
func loadAndMergeDaemonConfig(cfg *config, acp bool, load configLoader) error {
	if !cfg.configPathFlagSet {
		return nil // no --config ⇒ no auto-load, byte-identical to pre-config behaviour
	}
	if acp {
		return fmt.Errorf("--config is not supported in ACP mode (listener topology does not apply to stdio)")
	}
	dc, err := load(cfg.configPath)
	if err != nil {
		return err
	}
	mergeDaemonConfig(cfg, dc)
	return nil
}

// tcpGRPCConfigured reports whether the operator asked for a TCP gRPC listener,
// from EITHER source — an explicit --grpc-addr or a daemon config file's
// grpc_addr. It is distinct from "--grpc-addr is non-empty", which is always
// true: the flag carries a loopback default. --grpc-unix-socket suppresses that
// default and contradicts a deliberate request, which is the exclusion AC8.1
// specifies.
func (c config) tcpGRPCConfigured() bool {
	return c.cliExplicit["grpc-addr"] || c.grpcAddrFromFile
}

// clientMCPOnCreateForListeners derives whether this deployment accepts
// CLIENT-PROVIDED MCP servers on a session-creating API request (issue #821).
//
// The rule is a UNIX-SOCKET gRPC listener WITH HTTP DISABLED, and nothing else.
// The rule is intentionally UDS-only; loopback TCP is still a network listener.
//
//   - A workspace path lends the daemon's FILESYSTEM authority over a root the
//     operator already chose. Loopback is accepted there as ADR 0237's shipped
//     precedent.
//   - An MCP endpoint plus its auth headers lends the daemon's OUTBOUND NETWORK
//     authority: the caller names a host and the daemon connects to it carrying
//     caller-supplied credentials. That is a strictly larger grant, and loopback
//     TCP is reachable by EVERY local process and every local user account on the
//     host — for the HTTP surface, by a browser page as well. A UNIX socket is
//     guarded by filesystem permissions on a path the spawning process owns
//     (listenUnixSocket creates the parent directory owner-only).
//
// So this is the fail-closed reading of AC9.2 — "the same request over a TCP
// listener is refused" — and of ADR 0248, which already publishes the words "a
// feature that is only reachable on a UDS listener is advertised only on that
// listener". A loopback TCP daemon is a TCP daemon.
//
// It costs the intended consumer nothing: the SDK-spawned daemon shape from
// Scenario 8 is exactly --grpc-unix-socket with --http-addr "", which is the one
// topology this returns true for.
//
// DEPLOYMENT-SCOPED, not per-connection, per ADR 0237's Decision: authority is "a
// deployment/composition policy, not an inference made from a request or from the
// server package's socket state". One *Service backs both listeners, so adding
// ANY TCP listener gives the feature up on all of them, the UNIX socket included.
// A per-connection answer would contradict 0237 as written and would need its own
// ADR.
//
// The two tests are asymmetric because the two listeners are. HTTP is always TCP
// (serve() binds it with net.Listen("tcp", ...)), so an empty --http-addr — the
// disable path — is the only way for it not to be a network surface. gRPC has no
// disable path, which is why its test is the POSITIVE grpcUnixSocket != "" rather
// than an absence check on grpcAddr: --grpc-unix-socket is what suppresses the TCP
// bind (listenGRPC), while an empty --grpc-addr is a WILDCARD bind, not an absent
// listener.
//
// The SAME value feeds the mcp_servers_on_create advertisement, so the daemon
// cannot advertise a field it will refuse.
func clientMCPOnCreateForListeners(cfg config) bool {
	return cfg.grpcUnixSocket != "" && cfg.httpAddr == ""
}

// validateEffectiveConfig runs the EFFECTIVE-value cross-validation that must
// see the post-merge config: the perf-MCP loopback/empty-metrics guard and the
// rate-limit/rate-burst sanity bounds. It is a PURE helper (no I/O, no side
// effects) called in run() AFTER loadAndMergeDaemonConfig and BEFORE app.Build /
// listener binding, so a file-supplied metrics_addr or rate_limit that bypassed
// the earlier CLI-only guard is still caught (review fix #1). The rate_limit=0
// and rate_burst=0 meanings (disable / derive) are preserved: only negative and
// non-finite (NaN/Inf) values are rejected (review fix #5).
// buildAPIHandler assembles the authenticated HTTP API handler, wrapping it in
// the CORS policy when one is configured.
//
// The ORDER IS LOAD-BEARING: CORS wraps OUTSIDE auth. A browser preflight is an
// unauthenticated OPTIONS request — the CORS specification forbids sending
// credentials on it — so a policy installed inside the auth middleware would 401
// every preflight and cross-origin access would never work at all. Wrapping
// outside is safe because a preflight is answered from headers alone: it never
// reaches a handler, never touches a session, and never returns data. The real
// request that follows still passes through auth normally.
//
// A nil policy (no --cors-origins, the default) returns the authenticated
// handler unchanged, so the default path is byte-identical.
func buildAPIHandler(corsPolicy *server.CORSPolicy, auth *server.Authenticator, svc *server.Service) http.Handler {
	return corsPolicy.Middleware(auth.Middleware(server.NewHTTPHandler(svc)))
}

func validateEffectiveConfig(cfg config) error {
	if err := validateDeploymentID(cfg.deploymentID); err != nil {
		return err
	}
	// Daemon-hosting topology (issue #821 Scenario 8): the socket/TCP exclusion,
	// the socket path bounds, the lifetime-pipe descriptor, and the ready-file
	// path. Validated here so a file-supplied value cannot bypass it either.
	if err := validateDaemonHosting(cfg); err != nil {
		return err
	}
	// Build the policy purely to validate it: a malformed origin must fail at
	// STARTUP, where the operator is present, rather than becoming a silently
	// dead allowlist entry that looks identical to a working one.
	if _, err := server.NewCORSPolicy(cfg.corsOrigins); err != nil {
		return err
	}
	// --perf-mcp rides the admin listener, so it is meaningless without one.
	if cfg.perfMCP && cfg.metricsAddr == "" {
		return errors.New("--perf-mcp requires --metrics-addr (the loopback admin listener it mounts /mcp on)")
	}
	// FAIL CLOSED on a non-loopback --metrics-addr with --perf-mcp set, BEFORE
	// serve() binds any listener, so the refusal is a pure config error with no
	// side effects (matching the embed path). The /mcp surface is UNAUTHENTICATED
	// and can embed goroutine-derived function names and timing (decision 6 + the
	// security review's CWE-306 Low finding), so it must never be reachable off
	// loopback — including via a file-supplied metrics_addr.
	if cfg.perfMCP && !isLoopbackHostPort(cfg.metricsAddr) {
		return fmt.Errorf("--perf-mcp refuses a non-loopback --metrics-addr=%s: it exposes unauthenticated runtime data; bind loopback or add auth (future work)", cfg.metricsAddr)
	}
	// Rate-limit/burst sanity: 0 is meaningful (disable / derive), but a negative
	// or non-finite value is an operator misconfiguration. Reject before the
	// server is constructed so a malformed file or CLI value never reaches the
	// rate limiter.
	if cfg.rateLimit < 0 || math.IsNaN(cfg.rateLimit) || math.IsInf(cfg.rateLimit, 0) {
		return fmt.Errorf("rate_limit %v is invalid: must be >= 0 and finite (0 disables rate limiting)", cfg.rateLimit)
	}
	if cfg.rateBurst < 0 {
		return fmt.Errorf("rate_burst %d is invalid: must be >= 0 (0 derives a sane default from rate_limit)", cfg.rateBurst)
	}
	return nil
}

// parseFlags turns argv into a config, resolving env-derived defaults. It is
// the serve-mode test seam: it parses exactly as `mecated serve` would (the
// serve --help hook renders the serve common help). Existing callers that
// exercise the flag-parsing logic (not the help-renderer selection) use this
// entry point.
func parseFlags(argv []string) (config, error) {
	return parseFlagsMode(modeServe, argv)
}

// parseFlagsMode is parseFlags with an explicit command mode, selecting which
// help renderer the --help hook invokes. run() calls it with the resolved mode.
func parseFlagsMode(mode commandMode, argv []string) (config, error) {
	_, cfg, err := parseFlagsModeOut(mode, argv, os.Stderr)
	return cfg, err
}

// parseFlagsModeOut is parseFlagsMode with an injected output writer. It returns
// the built *flag.FlagSet alongside the config so progressive-help tests can run
// the validateFlagMeta completeness invariant over the FULL real FlagSet (every
// flag parseFlagsMode registers) instead of a synthetic subset. It is the small
// test seam: tests capture the REAL Usage / --help / --help-all render output
// (produced by the production renderers over that full FlagSet) into a
// strings.Builder, and inspect the FlagSet, without copying the registration
// block. Production calls it with os.Stderr and discards the returned FlagSet.
func parseFlagsModeOut(mode commandMode, argv []string, out io.Writer) (*flag.FlagSet, config, error) {
	fs := flag.NewFlagSet("mecated", flag.ContinueOnError)
	fs.SetOutput(out)
	var cfg config

	cwd, _ := os.Getwd()

	logLevelFlags := cliconfig.RegisterLogLevelFlag(fs)

	fs.StringVar(&cfg.grpcAddr, "grpc-addr", defaultGRPCAddr,
		"gRPC listen address (defaults to loopback; set --auth-token and/or --tls-cert before binding non-loopback)")
	fs.StringVar(&cfg.httpAddr, "http-addr", defaultHTTPAddr,
		"HTTP/SSE listen address (defaults to loopback; set --auth-token and/or --tls-cert before binding non-loopback). EMPTY DISABLES the HTTP/SSE listener AND the --metrics-addr admin listener together, so a gRPC-only daemon opens no HTTP port at all")
	fs.StringVar(&cfg.grpcUnixSocket, "grpc-unix-socket", "",
		"absolute path of a UNIX-domain socket to serve gRPC on INSTEAD of a TCP port; opens no TCP port. Mutually exclusive with an explicitly configured --grpc-addr (rejected at startup). The socket is created owner-only, inside an owner-only directory this process creates if missing; a stale socket left by a dead process is removed, while one a live process is accepting on refuses the start")
	fs.StringVar(&cfg.readyFile, "ready-file", "",
		"absolute path to write a JSON readiness document to, ATOMICALLY (temp file + rename) and only AFTER composition and every listener are up, so a spawning parent can wait on the path instead of racing a connect loop. Carries the pid, the transport, the bound gRPC/HTTP addresses, and the non-secret compatibility descriptor — never a credential. Empty writes nothing")
	fs.IntVar(&cfg.lifetimePipeFD, "lifetime-pipe-fd", 0,
		"file descriptor of an INHERITED pipe whose read end this daemon watches: EOF means the spawning parent exited or crashed, and the daemon then stops through the ordinary graceful-shutdown path. The parent holds the write end and never writes to it — it has nothing to remember. 0 (default) disables; 0/1/2 are the standard streams and are rejected")
	fs.StringVar(&cfg.workspace, "workspace", cwd, "default session workspace root")
	fs.StringVar(&cfg.model, "model", "", "model identifier sent to the provider (empty: use the provider-appropriate default)")
	fs.StringVar(&cfg.defaultProvider, "default-provider", "", "server-configured deployment-wide default provider id shared by every client (e.g. openai, openrouter, anthropic); overrides the built-in provider preference for zero-selector sessions while a client-side selector still wins. Validated FAIL-FAST at startup: an unknown or unavailable provider refuses to start")
	fs.StringVar(&cfg.defaultModel, "default-model", "", "server-configured deployment-wide default model id for the default provider, shared by every client; sits BELOW client-side defaults and ABOVE the per-provider built-in default. Validated FAIL-FAST at startup: a model not catalogued for the default provider refuses to start (stricter than per-session selectors, which allow passthrough)")
	fs.BoolVar(&cfg.useOpenAI, "openai", false, "use the OpenAI Responses provider (key from OPENAI_API_KEY)")
	// Shared provider base-URL flags + credential reads (cliconfig): registered here,
	// applied onto app.Config in appConfig. mecated keeps its own help wording.
	cfg.providerFlags = cliconfig.RegisterProviderFlags(fs, cliconfig.ProviderFlagHelp{
		OpenAIBaseURL:     "override the OpenAI API base URL (compatible endpoints)",
		OpenRouterBaseURL: "override the OpenRouter API base URL (default https://openrouter.ai/api/v1; key from OPENROUTER_API_KEY)",
		AnthropicBaseURL:  "override the native Anthropic API base URL (compatible/proxy endpoints; key from ANTHROPIC_API_KEY)",
		OpenCodeBaseURL:   "override the OpenCode Go API base URL (default https://opencode.ai/zen/go/v1; key from OPENCODE_API_KEY)",
	})
	// ToolHive LLM gateway (issue #262): registered adjacent to the provider
	// flags above (the credential-source family) AND grouped near --toolhive/
	// --toolhive-group below in --help (the vendor-name family) — it straddles
	// both, so mecated's help text disambiguates it from --toolhive explicitly.
	cfg.toolhiveLLMFlags = cliconfig.RegisterToolhiveLLMFlags(fs, cliconfig.DefaultToolhiveLLMFlagHelp)
	fs.BoolVar(&cfg.useMock, "mock", false, "use a canned offline mock provider (no network; for smoke tests only)")
	fs.StringVar(&cfg.mockScript, "mock-script", "", "path to a JSON mockllm script (offline; implies --mock and supports text, tool-call, and delayed turns)")
	fs.StringVar(&cfg.storeDir, "store-dir", "", "directory for the JSONL session store (empty -> in-memory store)")
	fs.StringVar(&cfg.sessionStoreURL, "session-store-url", "", "host:port of a remote session-store gRPC driver (mecatl.driver.v1.SessionStoreService); replaces the local store, so it is mutually exclusive with --store-dir. Loopback may ride plaintext; pair a non-loopback target with --driver-tls (and --driver-auth-token as needed)")
	fs.StringVar(&cfg.shell, "shell", "/bin/sh", "shell used to execute Bash-tool commands; empty disables Bash (shell-less mode)")
	fs.StringVar(&cfg.authorityEvaluator, "authority-evaluator", "local", "authority evaluator: local (default), noop, or cedar; cedar requires --cedar-authority-policy")
	fs.StringVar(&cfg.cedarAuthorityPolicy, "cedar-authority-policy", "", "path to the static operator Cedar authority policy; read once at startup when --authority-evaluator=cedar")
	fs.BoolVar(&cfg.noBash, "no-bash", false, "disable the Bash tool entirely (shell-less mode); overrides --shell")

	fs.StringVar(&cfg.compaction, "compaction", "heuristic", "compaction strategy: \"heuristic\" (default, single-summary) or \"cascade\" (tiered snip→strip→collapse→summarize)")
	fs.StringVar(&cfg.tokenizer, "tokenizer", "heuristic", "token counter for the compaction trigger: \"heuristic\" (default, dependency-free) or \"tiktoken\" (offline tiktoken vocab)")
	fs.IntVar(&cfg.contextWindowOverride, "context-window-override", 0, "override the model's context window in tokens for BOTH the compaction trigger (compaction fires at 80% of it) AND the footer context-meter denominator echoed to clients. Set this to the model's ACTUAL window when a model under-reports its window or sits behind a proxy that does. 0 (default) keeps the configured/live/catalogued/128k resolution unchanged. A small value (below a few thousand tokens) forces the agent to compact on nearly every turn — degraded, only useful for stress-testing compaction.")

	fs.IntVar(&cfg.llmMaxAttempts, "llm-max-attempts", 3, "max LLM stream-establish attempts (initial call plus retries)")
	fs.DurationVar(&cfg.llmPerAttemptTimeout, "llm-per-attempt-timeout", 300*time.Second, "per-attempt timeout for ESTABLISHING an LLM stream (connect + first chunk only; never cuts an actively-streaming turn). 0 disables; large-context reasoning models can take a long time to first token")
	fs.DurationVar(&cfg.llmStreamIdleTimeout, "llm-stream-idle-timeout", 180*time.Second, "max idle gap between LLM stream chunks after the first chunk; a longer stall terminates the turn (0 disables)")
	fs.IntVar(&cfg.llmBreakerThreshold, "llm-breaker-threshold", 5, "consecutive LLM failures that open the circuit breaker (0 disables)")
	fs.DurationVar(&cfg.llmBreakerCooldown, "llm-breaker-cooldown", 30*time.Second, "how long the LLM circuit breaker stays open before half-opening")
	fs.IntVar(&cfg.maxRunTokens, "max-run-tokens", 0, "Maximum cumulative input+output tokens per agent run. Inherited by subagents and team members. A run that crosses it ends cleanly with stop=budget. Default: unlimited; pass a positive value to cap. (0 also means unlimited.)")
	fs.IntVar(&cfg.maxTeamTokens, "max-team-tokens", 0, "Maximum cumulative input+output tokens per team run, summed across all members and rounds. When crossed the team stops scheduling new rounds — the in-flight round and the lead's synthesis still complete, and the report states the budget stop. Applies to the Team tool and gRPC CreateTeam; a per-call Team max_team_tokens may only tighten it. Orthogonal to --max-run-tokens. Default: unlimited; pass a positive value to cap. (0 also means unlimited.)")

	fs.BoolVar(&cfg.noPromptCache, "no-prompt-cache", false, "disable provider-side prompt caching (ADR 0100): every adapter's cache dialect degrades to None, reproducing the pre-caching wire exactly. Caching is ON by default")
	fs.StringVar(&cfg.anthropicCacheTTL, "anthropic-cache-ttl", "", "TTL stamped on every Anthropic ephemeral cache_control breakpoint: \"5m\" or \"1h\". Empty (default) omits the ttl field — the API's own 5m default applies. Any other value is ignored with a WARN")

	fs.StringVar(&cfg.metricsAddr, "metrics-addr", defaultMetricsAddr, "Prometheus /metrics listen address (empty disables the metrics endpoint)")

	fs.StringVar(&cfg.otlpEndpoint, "otlp-endpoint", "", "OTLP trace collector endpoint, e.g. localhost:4317 (empty disables tracing)")
	fs.StringVar(&cfg.otlpProtocol, "otlp-protocol", telemetry.ProtocolGRPC, "OTLP transport: \"grpc\" (default) or \"http\"")
	fs.BoolVar(&cfg.otlpInsecure, "otlp-insecure", false, "skip TLS when dialing the OTLP collector (development only)")

	fs.IntVar(&cfg.mutexProfileFraction, "mutex-profile-fraction", 0, "runtime.SetMutexProfileFraction: report 1/N mutex contention events for /debug/pprof/mutex. 0 (default) disables it. Adds per-contention sampling overhead; enable only when investigating lock contention")
	fs.IntVar(&cfg.blockProfileRate, "block-profile-rate", 0, "runtime.SetBlockProfileRate in nanoseconds: sample one blocking event per N ns blocked for /debug/pprof/block. 0 (default) disables it. Adds per-block-event overhead; enable only when investigating blocking")
	fs.BoolVar(&cfg.flightRecorder, "flight-recorder", true, "arm the execution-trace FlightRecorder (bounded in-memory ring buffer) so /debug/flightrecorder can snapshot recent activity. ON by default (low, bounded overhead). Pass --flight-recorder=false to disable")

	fs.BoolVar(&cfg.perfMCP, "perf-mcp", false, "mount the read-only perf MCP server at /mcp on the loopback admin listener, so an agent can introspect THIS process's runtime/latency/profile state over MCP (list_slow_turns, runtime/heap/CPU profiles, FlightRecorder). OFF by default. Requires --metrics-addr, and that address MUST be loopback: the surface is UNAUTHENTICATED (decision 6) and can embed goroutine-derived function names/timing, so a non-loopback --metrics-addr with --perf-mcp is REFUSED. Print a paste-ready client .mcp.json with `mecated perf-mcp print-config`")

	fs.IntVar(&cfg.goroutineWarnThreshold, "goroutine-warn-threshold", 0, "live goroutine-leak alarm: log a slog.Warn whenever runtime.NumGoroutine() exceeds this count (decision 10 of docs/adr/0018-perf-observability.md). 0 (default) disables the alarm; the runtime collector still exports the goroutine count as a /metrics series regardless. A healthy mecated holds a low-hundreds goroutine count; pick a high ceiling (e.g. 10000) so the alarm only fires on a genuine leak, not normal concurrency")
	fs.DurationVar(&cfg.goroutineWarnInterval, "goroutine-warn-interval", 30*time.Second, "how often the goroutine-leak watchdog samples runtime.NumGoroutine(). Only consulted when --goroutine-warn-threshold > 0")

	fs.StringVar(&cfg.memoryDir, "memory-dir", "", "per-project memory store directory (empty disables the Remember/Recall tools)")
	fs.DurationVar(&cfg.memoryConsolidateInterval, "memory-consolidate-interval", 0, "interval for background memory consolidation (dream); 0 disables. Only meaningful with --memory-dir")
	fs.DurationVar(&cfg.childRetention, "child-retention", 168*time.Hour, "how long persisted CHILD session snapshots (subagent-*/parallel-*/team-* ids — the InspectSubagent/resume handles) are retained before the GC sweep deletes them; main sessions are never touched. Only meaningful with a durable store (--store-dir or a prunable --session-store-url driver). 0 disables the age pass")
	fs.IntVar(&cfg.childRetentionMaxPerFamily, "child-retention-max-per-family", 500, "max persisted child session snapshots kept per delegation family (subagent/parallel/team); the oldest beyond the cap are deleted, skipping in-flight runs. Durable-store-only, like --child-retention. 0 disables the cap")
	fs.DurationVar(&cfg.mainRetention, "main-retention", 0, "how long persisted MAIN (top-level operator/service) session snapshots are retained before the GC sweep deletes them; child sessions are governed by --child-retention instead. Only meaningful with a durable store (--store-dir or a prunable --session-store-url driver). 0 (default) disables the main age pass entirely, so main sessions are never touched")
	fs.IntVar(&cfg.mainRetentionMaxTotal, "main-retention-max-total", 0, "max persisted MAIN (top-level) session snapshots kept store-wide; the oldest beyond the cap are deleted, skipping in-flight runs. Durable-store-only, like --main-retention. 0 (default) disables the cap, so main sessions are never touched")
	fs.DurationVar(&cfg.scheduleFireRetention, "schedule-fire-retention", 0, "SCHEDULED TASKS: how long persisted \"sched--\"-prefixed fire-session snapshots are retained before the GC sweep deletes them (a distinct family from --main-retention/--child-retention); a LIVE fire (one mid-run) is never deleted. Defaults to 7d (168h) when unset and scheduling can be active (the on-by-default posture) so a durable store does not grow without bound; an explicit 0 disables the pass — fire sessions are never swept. Only meaningful with a durable store (--store-dir or a prunable --session-store-url driver)")
	fs.IntVar(&cfg.scheduleFireRetentionMaxTotal, "schedule-fire-retention-max-total", 0, "max persisted \"sched--\"-prefixed fire-session snapshots kept store-wide; the oldest beyond the cap are deleted, skipping in-flight fires. The symmetric peer of --main-retention-max-total for the schedule-fire family: the age horizon (--schedule-fire-retention) bounds the tail, this cap bounds the head (a per-minute cron accumulates ~10k sessions/week the horizon never trims). Durable-store-only. 0 (default) disables the cap")
	fs.DurationVar(&cfg.childGCInterval, "child-gc-interval", time.Hour, "how often the session retention GC re-sweeps after the startup sweep; 0 disables periodic cadence (startup sweep remains for compatibility). Operator settings: retention.sweep_cadence")
	fs.BoolVar(&cfg.acknowledgeMainRetention, "acknowledge-main-retention", false, "explicitly acknowledge destructive automatic cleanup of MAIN sessions after reviewing the logged planner summary; required when main age or count retention is enabled")
	fs.StringVar(&cfg.memoryStoreURL, "memory-store-url", "", "host:port of a remote memory-store gRPC driver (mecatl.driver.v1.MemoryStoreService); replaces the local flock store, so it is mutually exclusive with --memory-dir. Enables the Remember/Recall tools like --memory-dir does. Same auth/TLS posture as --session-store-url (equal URLs share one connection)")
	fs.StringVar(&cfg.eventLogURL, "event-log-url", "", "host:port of a remote event-log gRPC driver (mecatl.driver.v1.EventLogService) for the durable per-session event timeline (reasoning, ask/verdict pairs, delegation lifecycle); INDEPENDENT of the session store. Empty keeps the local default (the --store-dir jsonl log, or in-memory). Append happens at the relay (a fault WARNs, never aborts the run); Read is server-streaming. Same auth/TLS posture as --session-store-url (equal URLs share one connection)")
	fs.StringVar(&cfg.scheduleStoreURL, "schedule-store-url", "", "host:port of a remote schedule-store gRPC driver (mecatl.driver.v1.ScheduleStoreService + ScheduleOneShotReArmerService) for the durable schedule registry (scheduled tasks); INDEPENDENT of the session store — when set, replaces the ScheduleStore() discovery from the configured store. Empty keeps the byte-identical default (the configured store's own ScheduleStore() accessor, or no scheduling). The driver's Claim/ClaimNow/ReArmOneShot run the atomic advance server-side. Same auth/TLS posture as --session-store-url (equal URLs share one connection)")
	fs.StringVar(&cfg.learningStoreURL, "learning-store-url", "", "host:port of one distributed learning gRPC driver providing AttemptRepositoryService, ProposalRepositoryService, and SkillRepositoryService. The complete set must be explicitly advertised at startup; a partial or legacy driver fails closed with no local-repository fallback. Repository partitions are opaque on this transport. Same auth/TLS posture as --session-store-url (equal URLs share one connection)")
	fs.StringVar(&cfg.sessionLeaseURL, "session-lease-url", "", "host:port of a remote session-lease gRPC driver (mecatl.driver.v1.SessionLeaseService) for cross-process single-writer enforcement (cloud-native Phase 4, multi-replica). Empty = NO leasing (the byte-identical single-writer-by-affinity default: route every session to one replica). Mutually exclusive with --session-lease-dir / --session-lease-k8s-namespace. Same auth/TLS posture as --session-store-url (equal URLs share one connection)")
	fs.StringVar(&cfg.sessionLeaseDir, "session-lease-dir", "", "directory for a SINGLE-HOST flock session lease (cross-process single-writer enforcement among processes on ONE machine; flock auto-releases on crash). NOT safe across hosts — use --session-lease-k8s-namespace or --session-lease-url for multi-host/multi-replica. Empty = no leasing")
	fs.StringVar(&cfg.sessionLeaseK8sNamespace, "session-lease-k8s-namespace", "", "Kubernetes namespace for coordination.k8s.io Lease-backed session leasing (the in-cluster multi-replica path). Uses in-cluster config (or the default kubeconfig out-of-cluster); the ServiceAccount needs get,create,update,delete on leases in coordination.k8s.io for this namespace (never list/watch — see docs/usage.md). Empty = no leasing")
	fs.DurationVar(&cfg.sessionLeaseTTL, "session-lease-ttl", 30*time.Second, "session-lease lifetime: a crashed/killed holder's lease becomes claimable after this long. Only meaningful when a lease backend is selected")
	fs.DurationVar(&cfg.sessionLeaseRenewInterval, "session-lease-renew-interval", 0, "how often the per-session renewer refreshes a held lease; 0 = --session-lease-ttl / 3. Keep it well below the TTL so a slow store does not lose the lease and cancel the run. Only meaningful when a lease backend is selected")
	// Scheduled tasks (issue #189, Phase 1f; ADR 0073). The scheduler is ON by
	// default whenever the configured store exposes a ScheduleStore; the flag
	// surface is the opt-OUT knob.
	fs.BoolVar(&cfg.noScheduler, "no-scheduler", false, "SCHEDULED TASKS: disable the in-process scheduler that ticks the durable ScheduleStore (the jsonlstore --store-dir or redisstore --redis-url backend) and fires due schedules. The scheduler is ON by default on any schedule-capable store — a fire mints a fresh \"sched--\" top-level session and drives it to completion with subagent-grade defaults; a store with no ScheduleStore (the in-memory default) never ticks. With --no-scheduler the create/list/fire API still works (manual management is independent of the tick loop). The leader-lease reuses the session-lease backend on a distinct id; with no lease backend it runs single-replica by affinity. See ADR 0059 + ADR 0073")
	fs.DurationVar(&cfg.schedulerTickInterval, "scheduler-tick-interval", 30*time.Second, "SCHEDULED TASKS: how often the tick loop polls the ScheduleStore for due schedules; 0 = the 30s default. Inert under --no-scheduler or a store with no ScheduleStore")
	fs.DurationVar(&cfg.schedulerMinInterval, "scheduler-min-interval", time.Minute, "SCHEDULED TASKS: the frequency floor the create-seam enforces (a schedule whose cadence is tighter than this is rejected, fail-closed — by BOTH the Schedule tool's create and the REST/gRPC create). Defaults to 1m so an on-by-default scheduler + the floor-Allow Schedule tool cannot mint an unbounded tight-cadence recurring fire out of the box; set explicitly to tighten, or to 0 to disable the floor")
	fs.IntVar(&cfg.schedulerMaxConcurrentFires, "scheduler-max-concurrent-fires", 4, "SCHEDULED TASKS: max schedules fired in parallel per tick. Inert under --no-scheduler or a store with no ScheduleStore")
	fs.StringVar(&cfg.driverAuthToken, "driver-auth-token", "", "bearer token sent on every store-driver RPC (or MECATL_DRIVER_AUTH_TOKEN; empty disables driver auth). Refused over cleartext to a non-loopback driver — pair with --driver-tls")
	fs.BoolVar(&cfg.driverTLS, "driver-tls", false, "enable transport TLS on the store-driver connections (--session-store-url/--memory-store-url)")
	fs.StringVar(&cfg.driverTLSCA, "driver-tls-ca", "", "PEM CA bundle to verify the store driver's server certificate (with --driver-tls; empty uses the system roots)")
	fs.StringVar(&cfg.driverTLSCert, "driver-tls-cert", "", "PEM client certificate for mutual TLS to the store driver (with --driver-tls and --driver-tls-key)")
	fs.StringVar(&cfg.driverTLSKey, "driver-tls-key", "", "PEM client private key (paired with --driver-tls-cert)")

	fs.StringVar(&cfg.soulFile, "soul-file", "", "path to a user-scoped, agent-READ-ONLY persona/\"soul\" file injected as turn-0 context (empty = the conventional $XDG_CONFIG_HOME/mecatl/soul.md, fallback ~/.config/mecatl/soul.md). Fail-soft: a missing/empty/oversized/injection-flagged file degrades to no fragment, never an error. No tool can write it")
	fs.StringVar(&cfg.soulSourceURL, "soul-source-url", "", "host:port of a remote soul-source gRPC driver (mecatl.driver.v1.SoulSourceService); occupies the USER slot of the soul selection, so it is mutually exclusive with --soul-file (--no-soul still wins). Probed at startup (fatal if unreachable); runtime faults degrade fail-soft to no fragment. The body is RE-VALIDATED locally (byte cap, injection scan, fence integrity); the drift baseline is SKIPPED for driver souls (--soul-strict/--approve-soul are no-ops for this provenance). Same auth/TLS posture as --session-store-url (equal URLs share one connection)")
	fs.BoolVar(&cfg.noSoul, "no-soul", false, "disable the user-scoped persona/soul fragment entirely (otherwise it is read from the conventional location, fail-soft if absent)")
	fs.BoolVar(&cfg.approveSoul, "approve-soul", false, "(re)write the soul DRIFT BASELINE to the current soul's content hash, accepting the file as-is. The baseline is a harness-owned sidecar next to the soul (<soul-path>.sha256); a later run whose hash differs logs a drift WARN. Use this once after intentionally editing your soul")
	fs.BoolVar(&cfg.soulStrict, "soul-strict", false, "refuse a DRIFTED soul: if the soul's content hash differs from the recorded baseline, contribute NO soul fragment this run (instead of the default warn-and-load). Pair with --approve-soul to accept an edit")

	fs.StringVar(&cfg.userModelDir, "user-model-dir", "", "directory for the user-scoped, CROSS-PROJECT user-model store of durable FACTS about the operator (empty = the conventional $XDG_CONFIG_HOME/mecatl/usermodel, fallback ~/.config/mecatl/usermodel). Exposes explicit user-memory lifecycle tools and a live bounded operator profile in the volatile system suffix. Holds FACTS about the operator, never rules — how the agent behaves comes from its soul + system rules")
	fs.BoolVar(&cfg.noUserModel, "no-user-model", false, "disable the user-model entirely (explicit tools and live operator profile)")
	fs.BoolVar(&cfg.userModelReview, "user-model-review", false, "compatibility alias for operator settings learning.mode: auto (scheduled for removal after one release window). Conflicts with an explicit non-auto learning.mode. Signal-gates completed trajectories into the bounded staged-reflection coordinator; valid eligible facts promote under auto policy")
	fs.IntVar(&cfg.userModelReviewInterval, "user-model-review-interval", 1, "process-wide completed-session debounce for automatic user-model review: review every Nth eligible completion (1 = every completion). Applies to learning.mode: auto and the compatibility --user-model-review alias")
	fs.DurationVar(&cfg.userModelConsolidateInterval, "user-model-consolidate-interval", 0, "independent process-wide interval for background consolidation (dream) of the cross-project user-model store's user/ namespace; 0 disables. Requires the user-model store and provider; learning.mode (including a project off ceiling) does not gate this explicit maintenance schedule")

	fs.Var(&cfg.skillsDirs, "skills-dir", "directory to discover progressive-disclosure skills from, laid out as <name>/SKILL.md (repeatable; highest precedence); empty disables the Skill tool unless --skills-conventional is set. TRUST BOUNDARY: a SKILL.md steers the model like AGENTS.md/CLAUDE.md — point this only at directories you trust")
	fs.BoolVar(&cfg.skillsConventional, "skills-conventional", false, "also discover skills from the conventional locations: <workspace>/"+skills.ProjectDirMecatl+", <workspace>/"+skills.ProjectDirClaude+", $XDG_CONFIG_HOME/mecatl/skills (or ~/.config/mecatl/skills), and ~/.claude/skills (lower precedence than --skills-dir). Default OFF — opt in only for trusted locations (same trust class as AGENTS.md/CLAUDE.md)")
	fs.StringVar(&cfg.skillSourceURL, "skill-source-url", "", "host:port of a remote skill-source gRPC driver (mecatl.driver.v1.SkillSourceService); replaces local skills discovery, so it is mutually exclusive with --skills-dir/--skills-conventional. The driver's skill set is snapshotted at startup (fatal if unreachable); bundled assets are fetched on demand by logical name. TRUST BOUNDARY: a driver-served SKILL.md steers the model like AGENTS.md/CLAUDE.md — point this only at a driver you trust. Same auth/TLS posture as --session-store-url (equal URLs share one connection)")

	fs.StringVar(&cfg.skillsDraftDir, "skills-draft-dir", "", "enable the writable SkillDraft tool and set the QUARANTINE directory for model-authored candidate skills. Empty disables the tool. TRUST BOUNDARY: must be OUTSIDE the workspace root (so the model's workspace-confined Write/Edit cannot reach it; fatal otherwise) and disjoint from every --skills-dir (fatal on overlap). Drafts are quarantined (never live); an operator reviews and promotes one with `mecated skills promote --skills-draft-dir <dir> --skills-dir <active> <name>`")
	fs.Float64Var(&cfg.skillsDraftThreshold, "skills-draft-similarity-threshold", skills.DefaultSimilarityThreshold, "2-gram Jaccard similarity above which a SkillDraft warns of a near-duplicate existing skill (warn-only, does not block)")

	fs.Var(&cfg.agentsDirs, "agents-dir", "directory to discover named agent definitions (subagent specialists) from, laid out as <name>.md with YAML frontmatter (repeatable; highest precedence). A def is reusable as a Subagent delegate (Subagent(agent=<name>)) and as a team-member role (AgentType). TRUST BOUNDARY: a def body steers the model like AGENTS.md/CLAUDE.md — point this only at directories you trust")
	fs.BoolVar(&cfg.agentsConventional, "agents-conventional", true, "also discover agent definitions from the conventional locations: <workspace>/"+agents.ProjectDirMecatl+", <workspace>/"+agents.ProjectDirClaude+", $XDG_CONFIG_HOME/mecatl/agents (or ~/.config/mecatl/agents), and ~/.claude/agents (lower precedence than --agents-dir). ON by default and INERT when no such dir exists (like teams/fork). Pass --agents-conventional=false to disable. TRUST BOUNDARY: same trust class as AGENTS.md/CLAUDE.md")
	fs.StringVar(&cfg.agentSourceURL, "agent-source-url", "", "host:port of a remote agent-definition gRPC driver (mecatl.driver.v1.AgentSourceService); the definition set is SNAPSHOTTED at startup (fatal if unreachable). Mutually exclusive with --agents-dir; the default-on conventional discovery is SUPERSEDED (not an error) — the driver becomes the only definition source. TRUST BOUNDARY: stronger than model steering — a def's hooks execute as UNGATED shell on the harness host (hookexec, every lifecycle phase, no permission ask); a compromised agent-source driver executes arbitrary shell on the harness host via def hooks, so treat it as harness-equivalent infrastructure. Same auth/TLS posture as --session-store-url (equal URLs share one connection)")
	fs.StringVar(&cfg.subagentModel, "subagent-model", "", "global default model for every Subagent / Parallel-branch / team-member child that does not pin its own model (via an agent definition or a per-call override) — the analogue of CLAUDE_CODE_SUBAGENT_MODEL; the Parallel judge stays on the session model. May be a concrete id or an alias from --model-alias; resolved on the session's provider (same-provider only). Empty inherits the parent --model; a non-empty value that does not resolve to a usable model id (unknown alias, or an alias meaning inherit) FAILS STARTUP. settings.yaml home: `models.subagent:` (operator-tier); this flag wins when both are set")
	// Shared model alias/slot flags (cliconfig): mecated uses the default help
	// wording, so a zero ModelFlagHelp is enough.
	cfg.modelAliases, cfg.modelSlots = cliconfig.RegisterModelFlags(fs, cliconfig.ModelFlagHelp{})
	fs.StringVar(&cfg.subagentAskReviewer, "subagent-ask-reviewer", "", "OPT-IN headless ask reviewer (issue #31): model id or --model-alias of a tool-less ONE-TURN reviewer that adjudicates a HEADLESS subagent/member/branch permission ask the 4-step model would otherwise blanket auto-deny. Allow = this call only (never learned); deny/error keeps the call denied (fail-safe). Configured Deny/Ask rules and an interactive approver always win; resolved on the session's provider (same-provider only). Empty (default) disables it; a value that does not resolve to a usable model id FAILS STARTUP. Deliberately a server flag, NOT a permission-config key: it grants an autonomous approval capability, an operator deployment decision")
	fs.IntVar(&cfg.subagentAskReviewerMaxDenies, "subagent-ask-reviewer-max-denies", agent.DefaultAskReviewMaxDenies, "circuit breaker for --subagent-ask-reviewer: after this many CONSECUTIVE non-allow reviewer outcomes (denies/errors/timeouts) in one run, further asks skip the reviewer and fall through to the plain auto-deny; an allow resets the count. <=0 uses the default (3)")
	fs.StringVar(&cfg.subagentAskReviewerPolicyFile, "subagent-ask-reviewer-policy", "", "path to a TRUSTED policy rubric file for --subagent-ask-reviewer; its CONTENT replaces the built-in read-only/verification rubric the reviewer applies. Empty keeps the built-in rubric. Read once at startup; an unreadable file FAILS STARTUP")
	fs.BoolVar(&cfg.subagentModelRouter, "subagent-model-router", false, "Semantic model router KILL-SWITCH (ADR 0042, superseding 0031's enable model): the router is ENABLED by configuring a `models.router:` category taxonomy in the OPERATOR-TIER user-global settings.yaml (the guardrails-parity enable model — configure = enable), NOT by this flag. Pass --subagent-model-router=false to force the router OFF despite a taxonomy (the kill-switch; also expressible as models.router.disabled: true in YAML). When ENABLED, a tiny one-turn classifier (on the `router` model slot) reads each plain Subagent delegation's task prompt + the operator taxonomy and picks the child's model BEFORE the child is minted (decide-once, same-provider; only for a plain delegation — no per-call model/agent, no fork/resume). FAIL-SOFT: any classifier failure, unknown category, or the per-run breaker (3 consecutive misses) inherits the default model")
	fs.BoolVar(&cfg.headless, "headless", false, "run NON-interactive: declare that clients drive sessions but never answer permission prompts (autonomous / CI deployments). A child subagent/member/branch permission ask is then NOT surfaced to the client (nobody would answer it — it would park until run-end) but resolved by the auto-deny path / the opt-in --subagent-ask-reviewer. DEFAULT off: a normal mecated serving an interactive client (mecatui, an IDE) surfaces asks for a human. Setting --subagent-ask-reviewer WITHOUT --headless has no effect (asks surface to the client instead) — a startup WARNING says so")
	fs.BoolVar(&cfg.planModeAutoApprove, "plan-mode-auto-approve", false, "OPT-IN autonomous plan approval (issue #206 Wave 6a): when a plan-mode run ends HEADLESS (no human to review), auto-approve the plan via ApprovePlan(ModeDefault) instead of leaving it parked. This is a deliberate autonomous-approval capability — an operator deployment decision, NEVER load-bearing for safety. It does NOT fire when interactive (a human can approve), NOT in non-plan modes, NOT for non-plan asks. DEFAULT off: a headless plan ask is auto-denied. Requires --headless to engage (an interactive deployment surfaces the plan to the human). A LOUD diagnostic (plan_mode_auto_approve: ON (NO HUMAN REVIEW)) is emitted at startup")
	fs.StringVar(&cfg.guardrailsModel, "guardrails-model", "", "GUARDRAILS (issue #27): model id or --model-alias of a tool-less checker that inspects OUTBOUND tool-call args (PreToolUse, data exfil) and INBOUND tool results (PostToolUse, prompt injection) and enforces a verdict per the operator-tier `guardrails:` rule list. Configuring a model here OR via a bound `guardrail` model slot (--model-slot guardrail=… / models.slots.guardrail) ENABLES guardrails (configure = enable, the router-parity model of ADR 0042; see ADR 0046) — empty + no slot disables them. A value that does not resolve to a usable model id FAILS STARTUP. A bound `guardrail` slot SUPERSEDES this flag's model when both are set (this flag then supplies only the enable gate). The RULE LIST + cost knobs live in the user-global settings.yaml `guardrails:` subtree (operator-tier ONLY — a project repo cannot configure or weaken a checker); --guardrails-model overrides the YAML model")
	fs.StringVar(&cfg.guardrailsMode, "guardrails", "", "GUARDRAILS master switch (the KILL-SWITCH only): pass `--guardrails=off` to force the issue-#27 content checker OFF regardless of --guardrails-model / the `guardrail` model slot / the guardrails: YAML config. The POSITIVE enable path is configuring a checker model — via `--guardrails-model` OR a bound `guardrail` model slot (--model-slot guardrail=… / models.slots.guardrail) — NOT this flag (configure = enable, ADR 0046). Any other value is a startup error")

	fs.StringVar(&cfg.commandsDir, "commands-dir", "", "directory of slash-command templates (<name>.md); setting it enables command expansion. Empty + --enable-commands uses the defaults (.mecatl/commands, .claude/commands)")
	fs.BoolVar(&cfg.enableCommands, "enable-commands", false, "enable slash-command expansion using the default directories (.mecatl/commands, .claude/commands) when --commands-dir is empty")
	fs.StringVar(&cfg.commandSourceURL, "command-source-url", "", "host:port of a remote slash-command gRPC driver (mecatl.driver.v1.CommandSourceService); COMPOSES with file-backed commands rather than replacing them — a local command file shadows a same-named driver command, and MCP prompts stay last. Consulted LIVE on every expansion/listing (no snapshot); probed once at startup (fatal if unreachable), runtime faults fail soft (raw text passes through). TRUST BOUNDARY: an expanded command body becomes the user prompt — point this only at a driver you trust. Same auth/TLS posture as --session-store-url (equal URLs share one connection)")

	fs.BoolVar(&cfg.enableParallel, "enable-parallel", true, "register the Parallel fan-out tool (parallel isolated child branches)")
	fs.StringVar(&cfg.websearchURL, "websearch-url", "", "WEBSEARCH (issue #26): base URL of a vendor-neutral HTTP JSON search endpoint (e.g. a SearXNG /search URL or a generic JSON search API) backing the always-present WebSearch tool. This is the EXPLICIT OVERRIDE — it wins over the SEARXNG_URL/BRAVE_API_KEY env tiers and the Exa anonymous default. The API key is read from WEBSEARCH_API_KEY, never a flag value. The adapter carries its own per-call timeout and concurrency limit. Setup walkthrough: docs/usage.md \"Enabling web search\"")
	fs.StringVar(&cfg.websearchMode, "websearch", "", "WEBSEARCH master switch: pass `--websearch=off` to DISABLE web search entirely (the kill switch — no outbound search calls, the tool reports it is disabled). Web search is ON by default (Exa anonymous tier; set EXA_API_KEY to upgrade the default tier, or SEARXNG_URL / BRAVE_API_KEY to switch backends). Any value other than \"off\" (or unset) leaves web search enabled. See docs/usage.md \"Enabling web search\"")
	fs.StringVar(&cfg.websearchAuthHeader, "websearch-auth-header", "", "WEBSEARCH: HTTP header the WEBSEARCH_API_KEY is sent in (default \"Authorization\" as a Bearer token; set e.g. \"X-API-Key\" to send the raw key). Ignored when no key is set. See docs/usage.md \"Enabling web search\"")
	fs.StringVar(&cfg.websearchQueryParam, "websearch-query-param", "", "WEBSEARCH: URL query parameter the search string is placed in (default \"q\"). Tune for a generic JSON search endpoint that expects a different parameter name. See docs/usage.md \"Enabling web search\"")
	fs.IntVar(&cfg.forkPreservedCap, "fork-preserved-cap", agent.DefaultPreservedForkCap, "max PRESERVED winner forks (join=first/judge) kept on disk at once; the oldest beyond this is LRU-reaped. Preserved forks stay inspectable until reaped")
	fs.BoolVar(&cfg.enableTeams, "enable-teams", true, "register the experimental agent-teams capability (CreateTeam/SpawnTeammate/RunTeam); on by default and inert until a client drives a team. Pass --enable-teams=false to disable")
	fs.BoolVar(&cfg.noSteer, "no-steer", false, "disable the mid-run steer inbox (steer-while-running, issue #512): a client `steer` frame on the Converse stream then reports too_late and ServerCapabilities.steer reads false. Steer is ON by default; this is the opt-OUT. The operator-tier settings.yaml `steer: false` scalar is the YAML twin (CLI out-ranks YAML; a project-tier steer: key is ignored)")

	cfg.mcpServers = cliconfig.RegisterMCPServerFlag(fs, "")
	fs.BoolVar(&cfg.mcpResourceTools, "mcp-resource-tools", true, "register the ListMcpResources/ReadMcpResource meta-tools when a connected MCP server exposes resources (no-op when none do). TRUST BOUNDARY: a remote resource's contents enter the model context like any other MCP output — enable only for servers you trust")
	fs.BoolVar(&cfg.mcpPrompts, "mcp-prompts", true, "expand \"/mcp__<server>__<prompt> key=value\" inputs into the server-rendered prompt (static snapshot taken at connect). TRUST BOUNDARY: an MCP prompt steers the model like a slash command — enable only for servers you trust")

	fs.BoolVar(&cfg.toolHiveEnabled, "toolhive", true, "discover MCP servers from the running ToolHive workloads (the embedded ToolHive library lists already-running workloads and reads their HTTP proxy URLs; mecatl NEVER starts or spawns a workload). Fails soft to zero servers when no container runtime is reachable. TRUST BOUNDARY: registering tools from running workloads is the same trust class as --mcp-server — every discovered workload's tools enter the model context")
	fs.StringVar(&cfg.toolHiveGroup, "toolhive-group", "", "ToolHive group to discover workloads from (empty -> the \"default\" group). Only consulted when --toolhive is set")

	fs.Var(&cfg.permissionConfigs, "permission-config", "path to a YAML permission-config file (.mecatl/settings.yaml schema: a permissions.{allow,ask,deny} list of \"Tool(pattern)\" specs) to load at the CLI scope — the HIGHEST config precedence, fully trusted (repeatable). Always loaded regardless of --permissions-conventional. A CLI rule out-ranks a project/user rule of the same effect; a config allow can LOOSEN ONLY the built-in Bash/Edit/Write ask, but a deny/ask in ANY scope still wins and a config allow never suppresses a configured ask")
	fs.BoolVar(&cfg.permissionsConventional, "permissions-conventional", true, "auto-discover the per-project permission config: <workspace>/.mecatl/settings.local.yaml (gitignored, personal — higher precedence) and <workspace>/.mecatl/settings.yaml (checked-in, shared), plus — with --import-claude-permissions — the matching .claude/settings.local.json and .claude/settings.json, plus the user-global file ($XDG_CONFIG_HOME/mecatl/settings.yaml). RE-RESOLVED PER SESSION against each session's workspace root (and revalidated on file mtime change), so two sessions in different repos get different decisions. ON by default and INERT when no such file exists. TRUST BOUNDARY: a project's ALLOW rules are honoured ONLY with --trust-project; its deny/ask rules are ALWAYS honoured")
	fs.BoolVar(&cfg.importClaudePermissions, "import-claude-permissions", false, "also import Claude-Code settings.json permissions (project <workspace>/.claude/settings{,.local}.json and user ~/.claude/settings.json) when --permissions-conventional is set. LOSSY (fail-safe): a WebFetch(domain:...) ALLOW is DEMOTED to ask, a Read(~/...) rule is left INERT (\"~\" unexpanded), an unparseable spec is DROPPED — every case is logged")
	fs.BoolVar(&cfg.trustProject, "trust-project", false, "honour a discovered PROJECT's ALLOW rules (its deny/ask rules are always honoured regardless). Default OFF (the safe stance): an untrusted repo's permission grants are ignored. TRUST BOUNDARY: enabling this lets a checked-in .mecatl/settings.yaml auto-approve tool calls — only pass it for a repo you trust")
	fs.BoolVar(&cfg.allowAllTools, "yolo", false,
		"ALIAS for --posture yolo (dangerous): allow-all server-wide AND loosen the substitution floor for CHILDREN too — a subagent's $()/backtick/heredoc command AUTO-RUNS (child prompt-injection defense OFF). A Deny in ANY scope and any DELIBERATELY configured Ask still apply (see docs/adr/0022-allow-all-posture.md). Isolated/ephemeral/single-tenant ONLY. Refused when running as root (euid 0) unless MECATL_SANDBOX=1 (or IS_SANDBOX=1) declares an isolated environment.")
	fs.StringVar(&cfg.posture, "posture", "",
		"OPERATOR POSTURE LADDER (strict < trusted < auto < yolo): strict (default) prompts every mutate; trusted honours a project's ALLOW rules (= --trust-project); auto adds allow-all + main substitution loosening (recommended UNATTENDED default, child injection-defense ON); yolo additionally auto-runs $()/backtick/heredoc in CHILDREN (injection-defense OFF, isolated single-tenant only). --yolo/--trust-project are aliases. auto/yolo are refused as root outside MECATL_SANDBOX. An unknown value fails closed to strict with a WARN.")

	fs.StringVar(&cfg.deploymentID, "deployment-id", "",
		fmt.Sprintf("OPTIONAL opaque label for this deployment, echoed on GetCompatibilityInfo so a client can tell one mecated from another (ADR 0248). Empty by default and NEVER inferred: mecatl will not derive it from hostname, pod name, or environment, because a label you did not choose leaks infrastructure topology to every authenticated caller. Printable single-line text only, max %d bytes; a longer or non-printable value is rejected at startup.", maxDeploymentIDLen))

	fs.Var(&cfg.corsOrigins, "cors-origins", "allow a browser at this EXACT origin (scheme://host[:port]) to call the HTTP API; repeatable. Matching is exact — no wildcard, no suffix or subdomain match, because this API can start agent runs and a suffix match on \"example.com\" would also admit \"evil-example.com\". \"*\" and \"null\" are refused: this policy sends credentials, and wildcard-with-credentials is forbidden. Empty (the default) installs no CORS middleware and responses are unchanged. LOCAL DEVELOPMENT ONLY — the production browser path is a same-origin BFF that injects bearer credentials server-side")

	fs.StringVar(&cfg.reasoningEffort, "reasoning-effort", "",
		"OPERATOR REASONING-EFFORT TIER (ADR 0055): auto (default — unset, the provider's own default applies) or low/medium/high/xhigh/max. OpenAI supports low/medium/high only, so xhigh/max are clamped down to high (with a WARN); Anthropic maps all five. Empty = unset (honours the operator-global settings.yaml reasoning-effort: key if present). A per-session CreateSession reasoning_effort out-ranks this default. A model with no reasoning support drops it. Operator-tier only; a project-tier reasoning-effort: key is ignored with a WARN. An unknown value fail-softs to unset with a WARN.")

	fs.StringVar(&cfg.authToken, "auth-token", "", "bearer token required on every gRPC/HTTP request (or MECATL_AUTH_TOKEN; empty disables auth)")
	fs.StringVar(&cfg.tlsCert, "tls-cert", "", "PEM server certificate; with --tls-key enables TLS on the gRPC + HTTP servers")
	fs.StringVar(&cfg.tlsKey, "tls-key", "", "PEM server private key (paired with --tls-cert)")
	fs.StringVar(&cfg.clientCA, "client-ca", "", "PEM client-CA bundle; enables mutual TLS (require + verify client certs)")
	cliconfig.RegisterOIDCFlags(fs, &cfg.oidc)
	fs.Float64Var(&cfg.rateLimit, "rate-limit", 0, "sustained per-client request rate in req/s (0 disables rate limiting)")
	fs.IntVar(&cfg.rateBurst, "rate-burst", 0, "rate-limit token-bucket burst size (0 derives a sane default from --rate-limit)")

	// --help-all requests exhaustive flag listing and exits 0 before the daemon
	// starts; it is a real flag so it parses normally and is checked post-parse.
	fs.BoolVar(&cfg.helpAll, "help-all", false,
		"show the exhaustive flag reference (every registered flag) and exit")

	// Daemon config (issue #338): explicit operator-selected config file.
	// Only accepted for serve/legacy; ACP mode rejects it.
	fs.StringVar(&cfg.configPath, "config", "", "path to a daemon config YAML file (v1 schema: grpc_addr, http_addr, metrics_addr, tls_cert, tls_key, client_ca, rate_limit, rate_burst). Explicit CLI flags override file values; auth TOKEN is not accepted in YAML")

	// `mecated serve --help` / `mecated acp --help` show progressive task-oriented
	// common help.  --help-all (above) shows the exhaustive reference.  The
	// renderers are package-local functions shared with the tests.
	fs.Usage = func() {
		out := fs.Output()
		if mode == modeACP {
			writeAcpCommonHelp(out, fs)
			return
		}
		writeServeCommonHelp(out, fs)
	}

	if err := fs.Parse(argv); err != nil {
		// Return the fully-registered FlagSet even on a parse/help error so the
		// progressive-help completeness invariant (validateFlagMeta) can run over
		// the full real registration path via the --help-triggered ErrHelp path.
		return fs, config{}, err
	}

	// Resolve the shared level after parsing so invalid values remain a
	// non-fatal startup condition and can be warned about by the root logger.
	cfg.logLevel, cfg.logLevelWarning = logLevelFlags.Resolve()

	// --help-all was parsed as a normal flag; render and return ErrHelp (exit 0).
	if cfg.helpAll {
		out := fs.Output()
		if mode == modeACP {
			writeAcpHelpAll(out, fs)
		} else {
			writeServeHelpAll(out, fs)
		}
		return nil, config{}, flag.ErrHelp
	}
	if cfg.mockScript != "" {
		// Match every existing UseMock short-circuit as well as replacing the
		// canned provider itself. A script is an additive way to select mock mode.
		cfg.useMock = true
	}

	// Post-parse MCP finalize (issue #358): resolve the --mcp-server-insecure-http
	// relaxations against the collected --mcp-server entries and run the deferred
	// token-bearing scheme gate. Deferring it here (instead of inside Set) is what
	// makes the opt-in order-independent on argv.
	if err := cfg.mcpServers.Finalize(); err != nil {
		return fs, config{}, err
	}

	// Record whether --posture was set EXPLICITLY (vs left at its empty default) so
	// composition can let CLI out-rank the operator-global settings.yaml posture: key
	// and WARN if an alias raised above an explicit lower --posture.
	recordExplicitFlags(fs, &cfg)

	// Default the schedule-fire retention to 7d when the operator did not set it
	// explicitly (ADR 0059 decision #7 Phase-2, ADR 0073): the scheduler is ON by
	// default on a schedule-capable store, and a durable store accumulates a
	// "sched--" session per fire, so a sane default keeps it bounded. An explicit
	// --schedule-fire-retention=0 leaves fire sessions untouched (the sweep is
	// disabled).
	applyScheduleFireRetentionDefault(&cfg)

	// --perf-mcp / --metrics-addr effective-value cross-validation (loopback,
	// non-empty) is deferred to validateEffectiveConfig, called in run() AFTER
	// the daemon config merge so a file-supplied metrics_addr: 0.0.0.0:9090 with
	// --perf-mcp cannot bypass the loopback guard. Validating here (on CLI-only
	// values) would leave the file-source bypass open — see review fix #1.

	// The three provider credentials (OPENAI/OPENROUTER/ANTHROPIC_API_KEY) are read by
	// cliconfig.ProviderFlags.Apply (called from appConfig), keeping the env reads +
	// the six Config fields wired in ONE shared place across all three mains. The
	// "OpenAI key implies the real provider" flip moved there too (off the resolved
	// key). The registry still auto-detects the keys via its envDetector; reading them
	// in the cmd layer makes credential custody explicit.
	//
	// WebSearch (issue #26): the search backend's API key is a SECRET, read from the
	// environment (never a flag value), mirroring the provider keys' custody rule.
	cfg.websearchAPIKey = os.Getenv("WEBSEARCH_API_KEY")
	// WebSearch backend ladder (issue #26): the SearXNG URL and the Brave/Exa keys
	// are secrets/URLs read from the environment, never flag values. Web search is ON
	// by default (Exa anonymous) — these only SWITCH the backend.
	cfg.searxngURL = os.Getenv("SEARXNG_URL")
	cfg.braveAPIKey = os.Getenv("BRAVE_API_KEY")
	cfg.exaAPIKey = os.Getenv("EXA_API_KEY")
	// An auth token from the environment is honored when the flag is unset, so a
	// secret need not appear in the process argv.
	if cfg.authToken == "" {
		cfg.authToken = os.Getenv("MECATL_AUTH_TOKEN")
	}
	// The store-driver bearer token mirrors the same custody rule.
	if cfg.driverAuthToken == "" {
		cfg.driverAuthToken = os.Getenv("MECATL_DRIVER_AUTH_TOKEN")
	}
	// The ask-reviewer policy rubric travels as a STRING into app.Config (the
	// composition layer never touches os); the cmd main reads the file here, once,
	// failing fast on an unreadable path (loud-misconfig posture).
	policy, err := readAskReviewerPolicy(cfg.subagentAskReviewerPolicyFile)
	if err != nil {
		return nil, config{}, err
	}
	cfg.subagentAskReviewerPolicy = policy
	cfg.providerCredentials = cfg.providerFlags.Resolve()
	// Guardrails master switch: only `--guardrails=off` is meaningful (the kill-switch
	// — it forces guardrails off regardless of --guardrails-model / the YAML config).
	// An empty value leaves guardrails governed by the model + rule config. Any OTHER
	// value is a startup error rather than a silent no-op (so `--guardrails=on`, a
	// natural-but-wrong attempt to ENABLE, fails loudly instead of doing nothing).
	switch strings.ToLower(strings.TrimSpace(cfg.guardrailsMode)) {
	case "":
		cfg.guardrailsOff = false
	case "off":
		cfg.guardrailsOff = true
	default:
		return nil, config{}, fmt.Errorf("--guardrails %q: only \"off\" is accepted (the kill-switch); to ENABLE guardrails set --guardrails-model OR bind the `guardrail` model slot (--model-slot guardrail=… / models.slots.guardrail) — configuring a checker model is the enable (ADR 0046). Leave --guardrails unset to keep guardrails governed by the model/slot config", cfg.guardrailsMode)
	}
	// WebSearch master switch (issue #26): only `--websearch=off` is meaningful (the
	// kill switch — it forces web search off regardless of the backend ladder). An
	// empty value leaves web search ON (Exa anonymous default). Any OTHER value is a
	// startup error rather than a silent no-op (mirroring --guardrails).
	switch strings.ToLower(strings.TrimSpace(cfg.websearchMode)) {
	case "":
		cfg.websearchOff = false
	case "off":
		cfg.websearchOff = true
	default:
		return nil, config{}, fmt.Errorf("--websearch %q: only \"off\" is accepted (the kill switch); web search is ON by default (Exa anonymous tier). Set SEARXNG_URL or BRAVE_API_KEY to switch backends, or --websearch-url for an explicit endpoint. Leave --websearch unset to keep web search enabled", cfg.websearchMode)
	}
	return fs, cfg, nil
}

// recordExplicitFlags walks the parsed FlagSet and records which operator-knob
// flags were set EXPLICITLY (vs left at their empty default), so composition can
// let the CLI out-rank the operator-global settings.yaml keys. Extracted from
// parseFlagsMode to keep its cyclomatic complexity under the lint gate.
func recordExplicitFlags(fs *flag.FlagSet, cfg *config) {
	cfg.cliExplicit = make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		cfg.cliExplicit[f.Name] = true
		switch f.Name {
		case "posture":
			cfg.postureFlagSet = true
		case "subagent-model-router":
			// Tri-state (ADR 0042): record that the kill-switch flag was given so
			// appConfig can distinguish "unset" (router governed by the taxonomy) from
			// "=false" (kill-switch); "=true/bare" is inert (the taxonomy still governs).
			cfg.subagentModelRouterSet = true
		case "config":
			cfg.configPathFlagSet = true
		case "main-retention":
			cfg.retentionCLISet.MainMaxAge = true
		case "main-retention-max-total":
			cfg.retentionCLISet.MainMaxCount = true
		case "child-retention":
			cfg.retentionCLISet.ChildMaxAge = true
		case "child-retention-max-per-family":
			cfg.retentionCLISet.ChildMaxCount = true
		case "schedule-fire-retention":
			cfg.retentionCLISet.ScheduledMaxAge = true
		case "schedule-fire-retention-max-total":
			cfg.retentionCLISet.ScheduledMaxCount = true
		case "child-gc-interval":
			cfg.retentionCLISet.SweepCadence = true
		case "no-steer":
			cfg.noSteerFlagSet = true
		}
		if f.Name == "reasoning-effort" {
			cfg.reasoningEffortFlagSet = true
		}
		if f.Name == "schedule-fire-retention" {
			cfg.scheduleFireRetentionSet = true
		}
		if f.Name == "default-provider" {
			cfg.defaultProviderFlagSet = true
		}
	})
}

// applyScheduleFireRetentionDefault sets the schedule-fire retention to 7 days
// when the operator did not pass --schedule-fire-retention explicitly. The
// scheduler is ON by default on any schedule-capable store (ADR 0073), so the
// default activates on the default path too — not only under an explicit
// enable flag. An explicit --schedule-fire-retention=0 disables the sweep.
// Extracted from parseFlags to keep its cyclomatic complexity under the gate
// (ADR 0059 decision #7 Phase-2).
func applyScheduleFireRetentionDefault(cfg *config) {
	if !cfg.scheduleFireRetentionSet && cfg.scheduleFireRetention == 0 {
		cfg.scheduleFireRetention = 7 * 24 * time.Hour
	}
}

// readAskReviewerPolicy reads the --subagent-ask-reviewer-policy rubric file and
// returns its content as a string. An empty path returns "" (the built-in rubric
// stands); an unreadable file is a config error (fail-fast — a silently dropped
// operator rubric would leave the reviewer on a policy the operator did not set).
func readAskReviewerPolicy(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("--subagent-ask-reviewer-policy %q: %w", path, err)
	}
	return string(b), nil
}

// serve starts the gRPC and HTTP servers (and, when --metrics-addr is set, the
// loopback admin endpoint — /metrics plus the pprof/expvar/FlightRecorder
// runtime-introspection surface — on its own listener) concurrently and blocks
// until ctx is cancelled (a signal) or a server fails, then shuts them all down
// gracefully. recorder may be nil (FlightRecorder disabled), in which case
// /debug/flightrecorder is not mounted.
//
// The harness API is protected by the server.Authenticator (bearer auth + rate
// limiting, both off by default) and optionally by TLS / mutual TLS. HTTP
// liveness/readiness probes are mounted OUTSIDE the auth/rate-limit layer so
// orchestrators can probe without credentials. The gRPC health service shares
// the server-wide interceptors and therefore requires credentials when auth is on.
func serve(ctx context.Context, cfg config, svc *server.Service, reg *prometheus.Registry, recorder *telemetry.FlightRecorder, slowTurns *telemetry.SlowTurnBuffer) error {
	tlsCfg, auth, corsPolicy, err := buildEdge(ctx, cfg)
	if err != nil {
		return err
	}
	// The caller-identity validator owns a background JWKS refresh that only its
	// own Close() stops — cancelling ctx does not. No-op when identity is off.
	defer auth.Close()
	logSecurityPosture(cfg, tlsCfg)

	// --- gRPC: auth+rate interceptors, standard health service ---
	grpcOpts := []grpc.ServerOption{
		grpc.UnaryInterceptor(auth.UnaryInterceptor()),
		grpc.StreamInterceptor(auth.StreamInterceptor()),
	}
	if tlsCfg != nil {
		grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	}
	grpcSrv := grpc.NewServer(grpcOpts...)
	mecatlv1.RegisterHarnessServiceServer(grpcSrv, server.NewHarnessServer(svc))
	mecatlv1.RegisterScheduleServiceServer(grpcSrv, server.NewScheduleServer(svc))
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("mecatl.v1.HarnessService", healthpb.HealthCheckResponse_SERVING)
	healthSrv.SetServingStatus("mecatl.v1.ScheduleService", healthpb.HealthCheckResponse_SERVING)

	// --- HTTP: health endpoints mounted OUTSIDE auth/rate-limit; the API mux
	// wrapped in the auth middleware. The readiness probe reports ready as soon
	// as the engine/service are wired (they are, by the time serve runs).
	//
	// An EMPTY --http-addr disables the HTTP/SSE surface entirely (AC8.2): a
	// gRPC-over-socket daemon spawned by a local parent has no use for a second
	// transport, and leaving one bound would falsify the no-TCP-port guarantee
	// --grpc-unix-socket exists to give. httpSrv is nil in that case, and every
	// consumer below (the serve goroutine, shutdown) branches on nil rather than
	// on the address, so there is one decision, not three. ---
	var httpSrv *http.Server
	if cfg.httpAddr != "" {
		httpMux := http.NewServeMux()
		server.NewHealthHandler(func() bool { return true }).RegisterHealth(httpMux)
		httpMux.Handle("/", buildAPIHandler(corsPolicy, auth, svc))
		httpSrv = &http.Server{
			Addr:              cfg.httpAddr,
			Handler:           httpMux,
			ReadHeaderTimeout: 10 * time.Second,
			TLSConfig:         tlsCfg,
		}
	}

	metricsSrv, adminPaths := buildAdminServer(cfg, reg, recorder, slowTurns)

	// TLS encrypts the transport and authenticates the server; it does not
	// authenticate callers unless mutual TLS requires a verified client cert.
	callerAuthenticated := callerAuthenticationConfigured(cfg, tlsCfg)
	logListenerPosture(cfg, callerAuthenticated)

	// Every listener is bound BEFORE anything serves, so the ready file (written
	// below) can honestly mean "reachable" — a bind failure is still a startup
	// error at this point, not a dead daemon a parent has already been told about.
	lis, err := bindListeners(cfg, httpSrv != nil, metricsSrv != nil)
	if err != nil {
		return err
	}
	defer lis.closeUnserved()

	// The inherited lifetime pipe: EOF on it means the spawning parent is gone.
	// Adopted here, after the binds, so a startup failure exits without having
	// claimed a descriptor the parent may still be using.
	parent, err := openLifetimePipe(cfg.lifetimePipeFD)
	if err != nil {
		return err
	}
	defer parent.Close()
	if parent.Enabled() {
		slog.Info("watching the inherited lifetime pipe; EOF on it stops this daemon gracefully", "fd", cfg.lifetimePipeFD)
	}

	// AC8.3: published only now — composition is complete (app.Build ran before
	// serve) and every listener is bound. A parent that sees this file can dial
	// immediately.
	if err := publishReadyFile(cfg, svc, lis); err != nil {
		return err
	}

	errCh := make(chan error, 3)

	lis.serveGRPC(grpcSrv, errCh)
	lis.serveHTTP(httpSrv, tlsCfg, errCh)
	lis.serveAdmin(metricsSrv, cfg, adminPaths, errCh)

	select {
	case <-ctx.Done():
		slog.Info("shutdown signal received; stopping servers")
	case <-parent.Closed():
		// AC8.5: the parent-crash path. Same graceful shutdown as a signal — the
		// daemon has an in-flight run's session to persist either way, and a
		// parent that died is precisely when abandoning that would be felt.
		slog.Info("lifetime pipe closed (the spawning parent exited); stopping servers")
	case err := <-errCh:
		slog.Error("server failed; shutting down", "err", err)
		shutdown(grpcSrv, httpSrv, metricsSrv)
		return err
	}

	shutdown(grpcSrv, httpSrv, metricsSrv)
	return nil
}

// boundListeners holds the listeners serve bound before anything started
// serving, so readiness is observable and a bind failure is still a clean
// startup error. httpLis / metricsLis are nil when their surface is disabled.
type boundListeners struct {
	grpc    grpcListener
	http    net.Listener
	metrics net.Listener
	// served records that ownership passed to the servers, so closeUnserved is a
	// no-op on the success path (the servers own their listeners from then on).
	served bool
}

// bindListeners binds every enabled listener up front. A failure closes whatever
// already bound: a half-bound daemon that returns an error must not leave a
// socket file or a held port behind for the next attempt to trip over.
func bindListeners(cfg config, wantHTTP, wantMetrics bool) (boundListeners, error) {
	var lis boundListeners
	grpcLis, err := listenGRPC(cfg)
	if err != nil {
		return boundListeners{}, err
	}
	lis.grpc = grpcLis
	if wantHTTP {
		httpLis, httpErr := net.Listen("tcp", cfg.httpAddr)
		if httpErr != nil {
			lis.closeUnserved()
			return boundListeners{}, fmt.Errorf("listen http %q: %w", cfg.httpAddr, httpErr)
		}
		lis.http = httpLis
	}
	if wantMetrics {
		metricsLis, metricsErr := net.Listen("tcp", cfg.metricsAddr)
		if metricsErr != nil {
			lis.closeUnserved()
			return boundListeners{}, fmt.Errorf("listen admin %q: %w", cfg.metricsAddr, metricsErr)
		}
		lis.metrics = metricsLis
	}
	return lis, nil
}

// closeUnserved releases listeners that never reached a server. Once
// serveGRPC/serveHTTP/serveAdmin have run, the servers own them and their own
// Shutdown/GracefulStop closes them — closing here as well would race that.
func (l *boundListeners) closeUnserved() {
	if l.served {
		return
	}
	for _, c := range []io.Closer{l.grpc.Listener, l.http, l.metrics} {
		if c != nil {
			_ = c.Close()
		}
	}
}

// serveGRPC hands the bound gRPC listener to the server.
func (l *boundListeners) serveGRPC(grpcSrv *grpc.Server, errCh chan<- error) {
	l.served = true
	transport, addr := l.grpc.transport, l.grpc.Addr().String()
	go func() {
		slog.Info("gRPC server listening", "transport", transport, "addr", addr)
		if serveErr := grpcSrv.Serve(l.grpc.Listener); serveErr != nil && !errors.Is(serveErr, grpc.ErrServerStopped) {
			errCh <- fmt.Errorf("grpc serve: %w", serveErr)
		}
	}()
}

// serveHTTP hands the bound HTTP listener to the server, or logs the disabled
// posture when there is none. ServeTLS with empty cert/key paths uses the
// certificate already loaded into TLSConfig.Certificates by buildTLSConfig,
// exactly as ListenAndServeTLS did.
func (l *boundListeners) serveHTTP(httpSrv *http.Server, tlsCfg *tls.Config, errCh chan<- error) {
	if httpSrv == nil {
		slog.Info("HTTP/SSE listener DISABLED (--http-addr empty); gRPC is the only API surface, and the admin/metrics listener is disabled with it")
		return
	}
	l.served = true
	addr := l.http.Addr().String()
	go func() {
		slog.Info("HTTP/SSE server listening", "addr", addr, "tls", tlsCfg != nil)
		var serveErr error
		if tlsCfg != nil {
			serveErr = httpSrv.ServeTLS(l.http, "", "")
		} else {
			serveErr = httpSrv.Serve(l.http)
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http serve: %w", serveErr)
		}
	}()
}

// serveAdmin hands the bound loopback admin listener to the server, or logs the
// disabled posture when there is none.
func (l *boundListeners) serveAdmin(metricsSrv *http.Server, cfg config, adminPaths string, errCh chan<- error) {
	if metricsSrv == nil {
		if cfg.metricsAddr != "" && cfg.httpAddr == "" {
			slog.Info("admin/metrics endpoint DISABLED with the HTTP listener (--http-addr empty)", "metrics_addr", cfg.metricsAddr)
		} else {
			slog.Info("metrics endpoint DISABLED (--metrics-addr empty)")
		}
		return
	}
	l.served = true
	addr := l.metrics.Addr().String()
	go func() {
		if cfg.perfMCP {
			slog.Info("admin server listening (loopback; /mcp is UNAUTHENTICATED perf MCP — keep loopback)", "addr", addr, "paths", adminPaths)
		} else {
			slog.Info("admin server listening (loopback)", "addr", addr, "paths", adminPaths)
		}
		if serveErr := metricsSrv.Serve(l.metrics); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- fmt.Errorf("metrics serve: %w", serveErr)
		}
	}()
}

// buildAdminServer assembles the loopback admin/observability server —
// /metrics plus the pprof/expvar/FlightRecorder runtime-introspection surface,
// and, when --perf-mcp is set, the read-only perf MCP endpoint. It returns nil
// when the surface is disabled, together with the space-separated path list the
// startup log reports.
//
// The admin mux is disabled by an empty --metrics-addr (as always) AND by an
// empty --http-addr (AC8.2). Tying it to the HTTP surface is deliberate: both
// are TCP listeners the operator did not have to ask for, and "--http-addr
// disables the HTTP listeners" would be a lie if a second one on port 9090
// survived it — a spawned local daemon would still be holding a port.
//
// SECURITY: pprof/FlightRecorder/expvar output can embed prompt text, file
// paths, and goroutine stacks. This listener is loopback-bound by default and
// MUST stay loopback — these endpoints are never mounted on the public
// gRPC/HTTP service surface (decision 6 in docs/adr/0018-perf-observability.md).
func buildAdminServer(cfg config, reg *prometheus.Registry, recorder *telemetry.FlightRecorder, slowTurns *telemetry.SlowTurnBuffer) (*http.Server, string) {
	adminPaths := "/metrics /debug/pprof /debug/vars /debug/flightrecorder"
	if cfg.metricsAddr == "" || cfg.httpAddr == "" {
		return nil, adminPaths
	}
	adminMux := telemetry.NewAdminMux(reg, recorder)
	// Perf MCP server: mount /mcp on the SAME loopback admin mux. It is
	// UNAUTHENTICATED and its output can embed goroutine-derived function names
	// and timing, so a non-loopback --metrics-addr with --perf-mcp is REFUSED
	// (decision 6 + the security review's CWE-306 Low finding). That refusal is
	// enforced fail-closed in parseFlags (config validation), BEFORE serve()
	// binds anything — so by the time we reach here the address is loopback.
	if cfg.perfMCP {
		adminMux.Handle("/mcp", mcpperf.Handler(mcpperf.Deps{
			Snapshot:  telemetry.Snapshot,
			Gatherer:  reg,
			Recorder:  recorder, // nil-able: /debug/flightrecorder disabled ⇒ capture tool reports unavailable
			Profiler:  mcpperf.NewProfiler(),
			SlowTurns: slowTurnSource(slowTurns),
			Clock:     time.Now,
			Logger:    slog.Default(),
		}))
		adminPaths += " /mcp"
	}
	return &http.Server{
		Addr:              cfg.metricsAddr,
		Handler:           adminMux,
		ReadHeaderTimeout: 10 * time.Second,
	}, adminPaths
}

// logListenerPosture logs the trust assumption of each API listener that exists.
//
// A UNIX socket and a disabled listener each get their own line rather than
// being fed to warnIfNonLoopback: that helper's vocabulary is loopback-vs-not,
// and neither case is either. Calling it with an empty address would emit the
// prominent unauthenticated-network WARNING for a listener that does not exist —
// the kind of false alarm that teaches operators to ignore the real one.
func logListenerPosture(cfg config, callerAuthenticated bool) {
	if cfg.grpcUnixSocket != "" {
		slog.Info("gRPC bound to a UNIX-domain socket (no TCP port); reachability is filesystem permission on the socket path",
			"flag", "grpc-unix-socket", "socket", cfg.grpcUnixSocket, "caller_authenticated", callerAuthenticated)
	} else {
		warnIfNonLoopback("grpc-addr", cfg.grpcAddr, callerAuthenticated)
	}
	if cfg.httpAddr == "" {
		slog.Info("HTTP/SSE listener not configured (--http-addr empty); no HTTP surface is exposed", "flag", "http-addr")
		return
	}
	warnIfNonLoopback("http-addr", cfg.httpAddr, callerAuthenticated)
}

// publishReadyFile writes the readiness document when --ready-file is set
// (AC8.3), and is a no-op otherwise.
//
// The descriptive half comes from Service.CompatibilityInfo — the SAME
// projection GetCompatibilityInfo serves, already vetted for exposure to an
// authenticated caller — not from cfg. That is the AC8.4 discipline: reading the
// raw config here would make the file's safety depend on every future reviewer
// noticing that a newly added field is a credential. The vetted projection has
// exactly one job, and adding a secret to it would break its own tests first.
//
// Capabilities are deliberately dropped from that projection: the ready file is
// an unauthenticated local artefact, and ADR 0245 keeps operator configuration
// out of the equivalent unauthenticated-shaped surface. A parent that wants the
// capability set can ask for it over the socket it just learned about.
func publishReadyFile(cfg config, svc *server.Service, lis boundListeners) error {
	if cfg.readyFile == "" {
		return nil
	}
	info := svc.CompatibilityInfo(context.Background())
	doc := readyDoc{
		Schema:      readyDocSchema,
		PID:         os.Getpid(),
		Transport:   lis.grpc.transport,
		GRPCAddress: lis.grpc.Addr().String(),
		SocketPath:  lis.grpc.socketPath,
		APIMajor:    info.GetApiMajor(),
		Features:    info.GetFeatures(),
		Deployment:  info.GetDeployment(),
	}
	if lis.http != nil {
		doc.HTTPAddress = lis.http.Addr().String()
	}
	if err := writeReadyFile(cfg.readyFile, doc); err != nil {
		return err
	}
	slog.Info("ready file published", "path", cfg.readyFile, "transport", doc.Transport, "grpc_address", doc.GRPCAddress)
	return nil
}

// buildEdge assembles everything guarding the listeners: the TLS config plus the
// edge policy (static bearer token, rate limiter, and — when --oidc-issuer is
// set — the caller-identity validator).
//
// ctx is the SERVER-ROOT context: the validator owns background JWKS refresh, so
// it must outlive any request. EVERY failure here is fatal and the daemon
// refuses to start — silently falling back to the unauthenticated path would
// turn an authenticated deployment into an open one (ADR 0204).
func buildEdge(ctx context.Context, cfg config) (*tls.Config, *server.Authenticator, *server.CORSPolicy, error) {
	tlsCfg, err := buildTLSConfig(cfg)
	if err != nil {
		return nil, nil, nil, err
	}
	// The browser origin allowlist is edge configuration in exactly the same
	// sense as TLS and bearer auth: it decides who may reach the listeners.
	// Building it here means a malformed origin fails startup through the ONE
	// error path serve already handles, and no caller has to remember a second.
	corsPolicy, err := server.NewCORSPolicy(cfg.corsOrigins)
	if err != nil {
		return nil, nil, nil, err
	}
	// Logged BEFORE construction so it appears even if the validator then fails
	// to build.
	warnInsecureIssuer(cfg.oidc)
	if err := cliconfig.ValidateOIDCAuthToken(cfg.oidc, cfg.authToken); err != nil {
		return nil, nil, nil, err
	}
	validator, err := cliconfig.OIDCValidator(ctx, cfg.oidc)
	if err != nil {
		return nil, nil, nil, err
	}
	return tlsCfg, server.NewAuthenticator(server.SecurityConfig{
		AuthToken: cfg.authToken,
		RateLimit: cfg.rateLimit,
		RateBurst: cfg.rateBurst,
		Validator: validator,
	}), corsPolicy, nil
}

func callerAuthenticationConfigured(cfg config, tlsCfg *tls.Config) bool {
	return cfg.authToken != "" || cfg.oidc.Enabled() || (tlsCfg != nil && tlsCfg.ClientAuth == tls.RequireAndVerifyClientCert)
}

// warnIfNonLoopback logs the API trust assumption for the given bind address.
// Loopback binds are logged at info. A non-loopback bind WITH caller
// authentication (bearer, OIDC, or mTLS) is logged at info; a non-loopback bind
// without it is logged as a prominent WARNING, since TLS alone does not identify
// callers and the endpoint exposes command/file execution to anyone who can reach
// it. It never hard-fails: an operator may deliberately make a private network or
// service mesh the shared authority boundary.
func warnIfNonLoopback(flagName, addr string, callerAuthenticated bool) {
	if cliconfig.IsLoopbackAddr(addr) {
		slog.Info("API bound to loopback (single-user localhost trust model)",
			"flag", flagName, "addr", addr, "caller_authenticated", callerAuthenticated)
		return
	}
	if callerAuthenticated {
		slog.Info("API bound to a non-loopback address WITH caller authentication (bearer, OIDC, or mTLS)",
			"flag", flagName, "addr", addr)
		return
	}
	slog.Warn("API bound to a NON-loopback address with NO caller authentication: it exposes UNAUTHENTICATED command/file execution to every network caller — configure --auth-token, OIDC, or --client-ca, or deliberately enforce shared authority at a trusted private-network/mesh boundary; TLS alone is not caller authentication",
		"flag", flagName, "addr", addr)
}

// isLoopbackHostPort is the local alias for the shared cliconfig loopback gate so
// the perf-MCP refusal reads naturally; the canonical implementation lives in
// internal/cliconfig (shared with mecak8s so the two cannot drift). See
// cliconfig.IsLoopbackAddr for the fail-closed semantics.
var isLoopbackHostPort = cliconfig.IsLoopbackAddr

// slowTurnSource bridges the telemetry slow-turn ring buffer to the
// mcpperf.SlowTurnSource read seam. The dependency points inward (cmd →
// telemetry, cmd → mcpperf); telemetry never imports the adapter, so the tiny
// field-copy adapter lives here at the composition boundary. A nil buffer yields
// a nil source so list_slow_turns reports "history not enabled".
func slowTurnSource(b *telemetry.SlowTurnBuffer) mcpperf.SlowTurnSource {
	if b == nil {
		return nil
	}
	return slowTurnBridge{b}
}

// slowTurnBridge maps telemetry.SlowTurn (scalars) to mcpperf.SlowTurn at the
// composition boundary. The shapes are identical by design, so this is a 1:1
// copy — but keeping the two types distinct is what lets telemetry stay ignorant
// of the adapter.
type slowTurnBridge struct{ b *telemetry.SlowTurnBuffer }

func (s slowTurnBridge) Recent(thresholdMs int64) []mcpperf.SlowTurn {
	src := s.b.Recent(thresholdMs)
	out := make([]mcpperf.SlowTurn, len(src))
	for i, t := range src {
		out[i] = mcpperf.SlowTurn{
			TurnIndex:       t.TurnIndex,
			DurationMs:      t.DurationMs,
			TTFTMs:          t.TTFTMs,
			InterTokenMaxMs: t.InterTokenMaxMs,
			EndedAt:         t.EndedAt,
			Role:            t.Role,
		}
	}
	return out
}

// buildTLSConfig assembles the *tls.Config for the gRPC + HTTP servers from the
// TLS flags. It returns nil (plaintext) when neither --tls-cert nor --tls-key is
// set. --tls-cert and --tls-key must be supplied together. When --client-ca is
// set it enables mutual TLS: the server requires and verifies a client
// certificate signed by the given CA bundle.
func buildTLSConfig(cfg config) (*tls.Config, error) {
	if cfg.tlsCert == "" && cfg.tlsKey == "" {
		if cfg.clientCA != "" {
			return nil, errors.New("--client-ca requires --tls-cert/--tls-key (mTLS needs server TLS)")
		}
		return nil, nil
	}
	if cfg.tlsCert == "" || cfg.tlsKey == "" {
		return nil, errors.New("--tls-cert and --tls-key must be supplied together")
	}
	cert, err := tls.LoadX509KeyPair(cfg.tlsCert, cfg.tlsKey)
	if err != nil {
		return nil, fmt.Errorf("load TLS keypair: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}
	if cfg.clientCA != "" {
		caPEM, err := os.ReadFile(cfg.clientCA)
		if err != nil {
			return nil, fmt.Errorf("read client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("client CA %q: no certificates parsed", cfg.clientCA)
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return tlsCfg, nil
}

// logSecurityPosture logs the effective authentication / transport / rate-limit
// posture once at startup so an operator can confirm what is enabled.
func logSecurityPosture(cfg config, tlsCfg *tls.Config) {
	mtls := tlsCfg != nil && tlsCfg.ClientAuth == tls.RequireAndVerifyClientCert
	slog.Info("API security posture",
		"bearer_auth", cfg.authToken != "",
		"caller_identity", cfg.oidc.Enabled(),
		"tls", tlsCfg != nil,
		"mutual_tls", mtls,
		"rate_limit_rps", cfg.rateLimit,
		"rate_burst", cfg.rateBurst,
		"store", storeKind(cfg))
}

// storeKind returns a short label for the configured session store, noting the
// auto-resume implication of an in-memory store.
func storeKind(cfg config) string {
	if cfg.storeDir == "" {
		return "memory (no resume across restart)"
	}
	return "jsonl (resumable across restart)"
}

// shutdown gracefully stops the servers, bounding the HTTP drains with a
// timeout. httpSrv may be nil when --http-addr is empty (the HTTP/SSE surface is
// disabled, AC8.2); metricsSrv may be nil when the admin endpoint is disabled by
// an empty --metrics-addr or with the HTTP surface.
func shutdown(grpcSrv *grpc.Server, httpSrv, metricsSrv *http.Server) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if httpSrv != nil {
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("http graceful shutdown", "err", err)
		}
	}
	if metricsSrv != nil {
		if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("metrics graceful shutdown", "err", err)
		}
	}
	grpcSrv.GracefulStop()
}

// warnInsecureIssuer logs the SSRF-relaxation warning when the operator enabled
// it, and is silent otherwise. It is a function rather than an inline branch so
// the caller does not grow another decision point (gocyclo), and so both server
// mains surface the warning identically.
func warnInsecureIssuer(c cliconfig.OIDCConfig) {
	if w := c.InsecureIssuerWarning(); w != "" {
		slog.Warn(w)
	}
}
