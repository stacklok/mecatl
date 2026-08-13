// Command mecak8s is the storage-free, Kubernetes-native mecatl agent binary
// (ADR 0048): a THIN peer of cmd/mecated that composes the SAME app.Build
// assembly with k8s-native defaults — Redis session store + durable event log,
// coordination.k8s.io Lease session leasing, a dynamic /readyz (drain-gated +
// Redis-pinged), and a bounded GracefulStop that cancels in-flight runs on
// SIGTERM so a rolling update completes within terminationGracePeriodSeconds.
//
// What it does NOT have (stripped from mecated): no `skills promote` / `config`
// / `perf-mcp` subcommands, no ACP (mecated-only), and NO --store-dir (storage-free:
// state lives in Redis and the k8s API server). It shares the SAME provider +
// model-alias/model-slot flag wiring (internal/cliconfig) so the three-mains
// wiring cannot drift. Telemetry (issue #343, ADR 0097) is OPT-IN: a loopback
// --metrics-addr mounts the admin mux's /metrics for scrape, and --otlp-* pushes
// traces/metrics to a collector. Both default off — the pre-telemetry posture.
//
// Honest shutdown contract (ADR 0048 §4d): new runs are rejected (503 via the
// drain gate) the moment SIGTERM (or the preStop httpGet /drain) fires.
// In-flight runs are CANCELLED, not drained to completion — a multi-minute LLM
// turn cannot survive a rolling update within terminationGracePeriodSeconds:
// 60. The pod is disposable; the session is not — it is Recover-able on the
// successor (issue #51) from the Redis snapshot + durable event log.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/app"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

// k8s-native listen defaults. A pod binds 0.0.0.0 (not loopback — the
// endpoint controller probes it) and relies on the NetworkPolicy + RBAC for
// isolation, unlike mecated's single-user loopback trust model. Auth is still
// configurable via --auth-token / --tls-* for a non-mesh deployment.
const (
	defaultGRPCAddr = "0.0.0.0:8080"
	defaultHTTPAddr = "0.0.0.0:8081"
)

// defaultK8sLeaseNamespace is the conventional namespace for the
// coordination.k8s.io Leases mecak8s holds per session. Overridable via
// --session-lease-k8s-namespace; the RBAC Role must grant leases access there.
const defaultK8sLeaseNamespace = "mecatl"

// drainPropagationDelay is how long the /drain handler blocks before returning
// (the endpoint-controller propagation window): after /readyz flips to
// not-ready the endpoint controller removes the pod from the Service, and only
// then is it safe to start cancelling in-flight runs. The preStop hook returns
// after this window so the kubelet's SIGTERM (which starts the bounded
// GracefulStop) lands after traffic has drained.
const drainPropagationDelay = 3 * time.Second

// gracefulStopTimeout bounds grpcSrv.GracefulStop(): after it elapses the
// server is hard-stopped (in-flight runs cancelled). It must stay below
// terminationGracePeriodSeconds (60) leaving room for HTTP shutdown + Close.
const gracefulStopTimeout = 30 * time.Second

// config is the parsed command-line configuration for mecak8s. It is a thin
// subset of mecated's config: the engine-build knobs (provider, model,
// workspace, posture, headless, redis store, k8s lease) plus the serve-time
// knobs (listeners, TLS, auth, drain). It deliberately drops mecated's
// telemetry/metrics/admin surface and its subcommands.
type config struct {
	grpcAddr               string
	httpAddr               string
	workspace              string
	model                  string
	defaultProvider        string
	defaultModel           string
	defaultProviderFlagSet bool
	useOpenAI              bool
	// providerFlags holds shared provider flag bindings; providerCredentials is
	// the once-resolved snapshot projected by appConfig without further I/O.
	providerFlags       *cliconfig.ProviderFlags
	providerCredentials cliconfig.ResolvedCredentials
	// toolhiveLLMFlags holds --toolhive-llm / --toolhive-llm-base-url (issue
	// #262). A k8s pod naturally has no ToolHive config file, so this is
	// inert by default; --toolhive-llm=false is recommended on a shared node.
	toolhiveLLMFlags *cliconfig.ToolhiveLLMFlags
	// modelAliases/modelSlots are the repeatable --model-alias/--model-slot
	// bindings (cliconfig.RegisterModelFlags), threaded onto app.Config.
	modelAliases *cliconfig.KeyValueList
	modelSlots   *cliconfig.KeyValueList
	// mcpServers holds the repeatable --mcp-server name=URL entries (issue #341,
	// the factory MCP wiring), via the SAME cliconfig.MCPServerList helper as
	// mecated/mecatequi: a per-server bearer rides the MCP_<NAME>_TOKEN env (a
	// scheduler like titlani injects a short-lived per-run identity there), token
	// optional. Threaded onto app.Config.MCPServers in appConfig.
	mcpServers *cliconfig.MCPServerList
	useMock    bool
	shell      string
	noBash     bool

	// Storage-free state (ADR 0048): --redis-url points the session store +
	// durable event log at a Redis managed service. NO --store-dir.
	redisURL string

	// Session leasing: a coordination.k8s.io Lease per session in this
	// namespace (the in-cluster multi-replica path). Defaults to "mecatl".
	sessionLeaseK8sNamespace  string
	sessionLeaseTTL           time.Duration
	sessionLeaseRenewInterval time.Duration

	// LLM resilience knobs (see internal/adapter/llmresilience).
	llmMaxAttempts       int
	llmPerAttemptTimeout time.Duration
	llmStreamIdleTimeout time.Duration
	llmBreakerThreshold  int
	llmBreakerCooldown   time.Duration

	// Provider-side prompt caching (ADR 0100).
	noPromptCache     bool
	anthropicCacheTTL string

	// maxRunTokens is the loop-level cumulative token ceiling (0 = disabled).
	maxRunTokens int
	// maxTeamTokens is the team-wide cumulative token ceiling (0 = disabled).
	maxTeamTokens int

	// headless: mecak8s is a headless daemon (DEFAULT true, inverted from
	// mecated). A child's unresolved permission ask is auto-denied / routed to
	// the opt-in --subagent-ask-reviewer rather than parked until run-end.
	headless bool

	// Headless ask reviewer (issue #31): OPT-IN one-turn reviewer for a child's
	// otherwise-auto-denied ask. Empty disables it.
	subagentAskReviewer           string
	subagentAskReviewerMaxDenies  int
	subagentAskReviewerPolicyFile string
	subagentAskReviewerPolicy     string

	// Guardrails (issue #27): the checker model + master kill-switch.
	guardrailsModel string
	guardrailsMode  string
	guardrailsOff   bool

	// Subagent model router (ADR 0042): kill-switch flag.
	subagentModelRouter    bool
	subagentModelRouterSet bool

	// Subagent global default model (the CLAUDE_CODE_SUBAGENT_MODEL analogue).
	subagentModel string

	// Posture ladder (strict < trusted < auto < yolo). DEFAULT "auto" (the
	// recommended UNATTENDED single-tenant tier — allow-all + child injection-
	// defense ON). postureFlagSet records an explicit --posture so composition
	// lets CLI out-rank the operator-global settings.yaml posture: key.
	posture        string
	postureFlagSet bool

	// Reasoning-effort tier (ADR 0055): operator-tier reasoning-effort default ("" =
	// unset, the provider's own default applies). reasoningEffortFlagSet records an
	// explicit --reasoning-effort so composition lets CLI out-rank the operator-global
	// settings.yaml reasoning-effort: key (mirrors posture).
	reasoningEffort        string
	reasoningEffortFlagSet bool

	// Security: API authentication + transport security (rate limiting is
	// omitted — a pod is fronted by the Service/mesh, not a raw public port).
	authToken string
	tlsCert   string
	tlsKey    string
	clientCA  string
	// oidc carries the caller-identity flags (--oidc-issuer/--oidc-jwks-uri/
	// --oidc-audience), shared verbatim with mecated. Zero value = identity off.
	oidc cliconfig.OIDCConfig

	// Child-session retention/GC over the Redis store (PrunableStore). Defaults
	// mirror mecated so a durable store does not grow without bound.
	childRetention                time.Duration
	childRetentionMaxPerFamily    int
	childGCInterval               time.Duration
	mainRetention                 time.Duration
	mainRetentionMaxTotal         int
	scheduleFireRetention         time.Duration
	scheduleFireRetentionSet      bool
	scheduleFireRetentionMaxTotal int

	// Skills/agents/soul/user-model: the discovery knobs. mecak8s is a daemon
	// over a workspace mount; these default OFF / conventional like mecated.
	skillsDirs         stringList
	skillsConventional bool
	agentsDirs         stringList
	agentsConventional bool
	noSoul             bool
	soulFile           string
	noUserModel        bool
	userModelDir       string

	// Permission config (issue #13): conventional discovery + trust-project.
	permissionConfigs       stringList
	permissionsConventional bool
	trustProject            bool
	importClaudePermissions bool

	// enableParallel / enableTeams: the fan-out / agent-teams toggles.
	enableParallel bool
	enableTeams    bool

	// Scheduled tasks (issue #189, Phase 1f; ADR 0073): the in-process scheduler.
	// mecak8s is the multi-replica home — the leader-lease (the k8s session-lease
	// backend) elects one ticker. ON by default on a schedule-capable store (the
	// --redis-url backend); noScheduler is the opt-out.
	noScheduler                 bool
	schedulerTickInterval       time.Duration
	schedulerMinInterval        time.Duration
	schedulerMaxConcurrentFires int

	// Headless telemetry (issue #343, ADR 0097): OPT-IN. --metrics-addr mounts a
	// SEPARATE loopback /metrics listener (the admin mux — Prometheus scrape,
	// ADR 0018 decision 6: loopback only, fail-closed on a non-loopback bind).
	// --otlp-* push traces/metrics to a collector (opt-in twin for non-scrape
	// deployments). All empty (the default) leaves the pipeline off — byte-identical
	// to the pre-telemetry posture (no /metrics listener, no OTLP).
	metricsAddr         string
	otlpEndpoint        string
	otlpProtocol        string
	otlpInsecure        bool
	otlpMetricsEndpoint string
	otlpMetricsProtocol string
	otlpShutdownTimeout time.Duration
}

// stringList is a repeatable string flag.Value, preserving order across
// multiple occurrences (mirrors cmd/mecated's stringList).
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// parseFlags turns argv into a config, resolving env-derived defaults. It
// mirrors cmd/mecated's parseFlags shape (ContinueOnError FlagSet, env keys for
// secrets, an fs.Visit pass for the posture-set bit) but for mecak8s's k8s-
// native surface: --redis-url, --session-lease-k8s-namespace default "mecatl",
// --headless default true, --posture default "auto".
func parseFlags(argv []string) (config, error) {
	fs := flag.NewFlagSet("mecak8s", flag.ContinueOnError)
	var cfg config

	cwd, _ := os.Getwd()

	fs.StringVar(&cfg.grpcAddr, "grpc-addr", defaultGRPCAddr,
		"gRPC listen address (a pod binds 0.0.0.0; set --auth-token and/or --tls-cert for a non-mesh deployment)")
	fs.StringVar(&cfg.httpAddr, "http-addr", defaultHTTPAddr,
		"HTTP/SSE listen address (carries /healthz, /readyz, /drain outside auth; the API mux inside auth)")
	fs.StringVar(&cfg.workspace, "workspace", cwd, "default session workspace root")
	fs.StringVar(&cfg.model, "model", "", "model identifier sent to the provider (empty: provider-appropriate default)")
	fs.StringVar(&cfg.defaultProvider, "default-provider", "", "server-configured deployment-wide default provider id (e.g. openai, openrouter, anthropic); validated FAIL-FAST at startup")
	fs.StringVar(&cfg.defaultModel, "default-model", "", "server-configured deployment-wide default model id for the default provider; validated FAIL-FAST at startup")
	fs.BoolVar(&cfg.useOpenAI, "openai", false, "use the OpenAI Responses provider (key from OPENAI_API_KEY)")
	// Shared provider base-URL flags + credential reads (cliconfig): registers
	// --openai-base-url / --openrouter-base-url / --anthropic-base-url and reads
	// OPENAI/OPENROUTER/ANTHROPIC_API_KEY — the SAME helper mecated/mecatequi
	// use, so mecak8s shares the three-mains wiring.
	cfg.providerFlags = cliconfig.RegisterProviderFlags(fs, cliconfig.ProviderFlagHelp{
		AuthFile: "provider credentials YAML path (API-key providers only; Codex OAuth is unsupported)",
	})
	// ToolHive LLM gateway (issue #262): a k8s pod naturally has no ToolHive
	// config file, so this is inert unless an operator mounts one or passes
	// --toolhive-llm-base-url explicitly.
	cfg.toolhiveLLMFlags = cliconfig.RegisterToolhiveLLMFlags(fs, cliconfig.DefaultToolhiveLLMFlagHelp)
	cfg.modelAliases, cfg.modelSlots = cliconfig.RegisterModelFlags(fs, cliconfig.ModelFlagHelp{})
	// Remote MCP servers (issue #341): the shared repeatable name=URL flag +
	// MCP_<NAME>_TOKEN bearer convention, identical to mecated/mecatequi.
	cfg.mcpServers = cliconfig.RegisterMCPServerFlag(fs, "")
	fs.BoolVar(&cfg.useMock, "mock", false, "use a canned offline mock provider (no network, no API key; for the e2e / smoke tests)")
	fs.StringVar(&cfg.shell, "shell", "/bin/sh", "shell used to execute Bash-tool commands; empty disables Bash (shell-less mode)")
	fs.BoolVar(&cfg.noBash, "no-bash", false, "disable the Bash tool entirely (shell-less mode); overrides --shell")

	// Storage-free state (ADR 0048): --redis-url is the session store + durable
	// event log. NO --store-dir (mutually exclusive, rejected at Build).
	fs.StringVar(&cfg.redisURL, "redis-url", "", "Redis address (host:port) for the session store + durable event log (ADR 0048, storage-free). Mutually exclusive with --store-dir / --session-store-url. The Store doubles as its own EventLog (like jsonlstore)")

	// Session leasing: coordination.k8s.io Lease per session. DEFAULT "mecatl".
	fs.StringVar(&cfg.sessionLeaseK8sNamespace, "session-lease-k8s-namespace", defaultK8sLeaseNamespace,
		"Kubernetes namespace for coordination.k8s.io Lease-backed session leasing (the in-cluster multi-replica single-writer path). Uses in-cluster config (or the default kubeconfig out-of-cluster); the ServiceAccount needs get,create,update,delete on leases in coordination.k8s.io for this namespace. Empty = no leasing")
	fs.DurationVar(&cfg.sessionLeaseTTL, "session-lease-ttl", 30*time.Second, "session-lease lifetime: a crashed/killed holder's lease becomes claimable after this long")
	fs.DurationVar(&cfg.sessionLeaseRenewInterval, "session-lease-renew-interval", 0, "how often the per-session renewer refreshes a held lease; 0 = --session-lease-ttl / 3")

	// Scheduled tasks (issue #189, Phase 1f): mecak8s is the multi-replica home.
	fs.BoolVar(&cfg.noScheduler, "no-scheduler", false, "SCHEDULED TASKS: disable the in-process scheduler that ticks the durable ScheduleStore (the --redis-url backend) and fires due schedules. The scheduler is ON by default when the store exposes a ScheduleStore — a fire mints a fresh \"sched--\" top-level session driven to completion with subagent-grade defaults; with --no-scheduler the create/list/fire API still works. The leader-lease reuses the k8s session-lease backend on a distinct id, electing one ticker across replicas. See ADR 0059 + ADR 0073")
	fs.DurationVar(&cfg.schedulerTickInterval, "scheduler-tick-interval", 30*time.Second, "SCHEDULED TASKS: how often the tick loop polls the ScheduleStore for due schedules; 0 = the 30s default. Inert under --no-scheduler or a store with no ScheduleStore")
	fs.DurationVar(&cfg.schedulerMinInterval, "scheduler-min-interval", time.Minute, "SCHEDULED TASKS: the frequency floor the create-seam enforces (a tighter cadence is rejected, fail-closed — by BOTH the Schedule tool's create and the REST/gRPC create). Defaults to 1m so an on-by-default scheduler + the floor-Allow Schedule tool cannot mint an unbounded tight-cadence recurring fire out of the box; set explicitly to tighten, or to 0 to disable the floor")
	fs.IntVar(&cfg.schedulerMaxConcurrentFires, "scheduler-max-concurrent-fires", 4, "SCHEDULED TASKS: max schedules fired in parallel per tick")

	// LLM resilience knobs (mirrors mecated's defaults).
	fs.IntVar(&cfg.llmMaxAttempts, "llm-max-attempts", 3, "max LLM stream-establish attempts (initial call plus retries)")
	fs.DurationVar(&cfg.llmPerAttemptTimeout, "llm-per-attempt-timeout", 300*time.Second, "per-attempt timeout for ESTABLISHING an LLM stream (connect + first chunk only; never cuts an actively-streaming turn). 0 disables")
	fs.DurationVar(&cfg.llmStreamIdleTimeout, "llm-stream-idle-timeout", 180*time.Second, "max idle gap between LLM stream chunks after the first chunk; a longer stall terminates the turn (0 disables)")
	fs.IntVar(&cfg.llmBreakerThreshold, "llm-breaker-threshold", 5, "consecutive LLM failures that open the circuit breaker (0 disables)")
	fs.DurationVar(&cfg.llmBreakerCooldown, "llm-breaker-cooldown", 30*time.Second, "how long the LLM circuit breaker stays open before half-opening")
	fs.IntVar(&cfg.maxRunTokens, "max-run-tokens", 0, "max cumulative input+output tokens per agent run; a run that crosses it ends cleanly with stop=budget. 0 = unlimited")
	fs.IntVar(&cfg.maxTeamTokens, "max-team-tokens", 0, "max cumulative input+output tokens per team run; 0 = unlimited")

	fs.BoolVar(&cfg.noPromptCache, "no-prompt-cache", false, "disable provider-side prompt caching (ADR 0100): every adapter's cache dialect degrades to None, reproducing the pre-caching wire exactly. Caching is ON by default")
	fs.StringVar(&cfg.anthropicCacheTTL, "anthropic-cache-ttl", "", "TTL stamped on every Anthropic ephemeral cache_control breakpoint: \"5m\" or \"1h\". Empty (default) omits the ttl field — the API's own 5m default applies. Any other value is ignored with a WARN")

	// Headless: DEFAULT true (mecak8s is a headless daemon — no human approver).
	fs.BoolVar(&cfg.headless, "headless", true, "run NON-interactive (DEFAULT on): a child subagent/member/branch permission ask is auto-denied / routed to the opt-in --subagent-ask-reviewer rather than parked until run-end. Pass --headless=false only if a client (mecatui, an IDE) answers asks")

	// Headless ask reviewer (issue #31).
	fs.StringVar(&cfg.subagentAskReviewer, "subagent-ask-reviewer", "", "OPT-IN headless ask reviewer: model id / alias of a tool-less one-turn reviewer adjudicating a child permission ask the headless auto-deny would otherwise reject. Empty disables it")
	fs.IntVar(&cfg.subagentAskReviewerMaxDenies, "subagent-ask-reviewer-max-denies", agent.DefaultAskReviewMaxDenies, "circuit breaker for --subagent-ask-reviewer: consecutive non-allow outcomes that disable the reviewer for the rest of the run; <=0 uses the default (3)")
	fs.StringVar(&cfg.subagentAskReviewerPolicyFile, "subagent-ask-reviewer-policy", "", "path to a TRUSTED policy rubric file for --subagent-ask-reviewer; its CONTENT replaces the built-in rubric. Read once at startup; an unreadable file fails startup")

	// Guardrails (issue #27).
	fs.StringVar(&cfg.guardrailsModel, "guardrails-model", "", "GUARDRAILS: tool-less checker model id / alias inspecting outbound args + inbound results. Configuring a model here OR via a bound `guardrail` model slot ENABLES guardrails; empty + no slot disables them")
	fs.StringVar(&cfg.guardrailsMode, "guardrails", "", "GUARDRAILS KILL-SWITCH only: pass --guardrails=off to force the checker OFF regardless of --guardrails-model / the `guardrail` slot / the YAML config")

	// Subagent model router (ADR 0042): kill-switch.
	fs.BoolVar(&cfg.subagentModelRouter, "subagent-model-router", false, "Semantic model router KILL-SWITCH (ADR 0042): the router is ENABLED by an operator-tier models.router: taxonomy, NOT by this flag. Pass --subagent-model-router=false to force it OFF despite a taxonomy")
	fs.StringVar(&cfg.subagentModel, "subagent-model", "", "global default model for every Subagent / Parallel-branch / team-member child that does not pin its own model; empty inherits the parent --model")

	// Posture: DEFAULT "auto" (the recommended UNATTENDED single-tenant tier).
	fs.StringVar(&cfg.posture, "posture", "auto", "OPERATOR POSTURE LADDER (strict < trusted < auto < yolo): strict prompts every mutate; trusted honours a project's ALLOW rules; auto adds allow-all + main substitution loosening (the DEFAULT, recommended UNATTENDED single-tenant tier, child injection-defense ON); yolo additionally auto-runs $()/backtick/heredoc in children. auto/yolo are refused as root outside MECATL_SANDBOX. An unknown value fails closed to strict with a WARN")

	// Reasoning-effort tier (ADR 0055): operator-tier only; help text verbatim from mecated.
	fs.StringVar(&cfg.reasoningEffort, "reasoning-effort", "",
		"OPERATOR REASONING-EFFORT TIER (ADR 0055): auto (default — unset, the provider's own default applies) or low/medium/high/xhigh/max. OpenAI supports low/medium/high only, so xhigh/max are clamped down to high (with a WARN); Anthropic maps all five. Empty = unset (honours the operator-global settings.yaml reasoning-effort: key if present). A per-session CreateSession reasoning_effort out-ranks this default. A model with no reasoning support drops it. Operator-tier only; a project-tier reasoning-effort: key is ignored with a WARN. An unknown value fail-softs to unset with a WARN.")

	// Security (no rate-limit: a pod is fronted by the Service/mesh).
	fs.StringVar(&cfg.authToken, "auth-token", "", "bearer token required on every RPC/request (empty disables auth; or MECATL_AUTH_TOKEN). Enable before binding a non-mesh address")
	fs.StringVar(&cfg.tlsCert, "tls-cert", "", "PEM server certificate; enables TLS on gRPC + HTTP when set with --tls-key")
	fs.StringVar(&cfg.tlsKey, "tls-key", "", "PEM server private key (paired with --tls-cert)")
	fs.StringVar(&cfg.clientCA, "client-ca", "", "PEM client CA bundle; enables mutual TLS (require+verify client certs)")
	cliconfig.RegisterOIDCFlags(fs, &cfg.oidc)

	// Child/main retention over the (prunable) Redis store — mirrors mecated.
	fs.DurationVar(&cfg.childRetention, "child-retention", 168*time.Hour, "how long persisted CHILD session snapshots are retained before the GC sweep deletes them; 0 disables the age pass")
	fs.IntVar(&cfg.childRetentionMaxPerFamily, "child-retention-max-per-family", 500, "max persisted child session snapshots kept per delegation family; 0 disables the cap")
	fs.DurationVar(&cfg.childGCInterval, "child-gc-interval", time.Hour, "how often the session retention GC re-sweeps after the startup sweep; 0 = startup only")
	fs.DurationVar(&cfg.mainRetention, "main-retention", 0, "how long persisted MAIN session snapshots are retained; 0 (default) disables the main age pass")
	fs.IntVar(&cfg.mainRetentionMaxTotal, "main-retention-max-total", 0, "max persisted MAIN session snapshots kept store-wide; 0 (default) disables the cap")
	fs.DurationVar(&cfg.scheduleFireRetention, "schedule-fire-retention", 0, "SCHEDULED TASKS: how long persisted \"sched--\"-prefixed fire-session snapshots are retained before the GC sweep deletes them (a distinct family from --main-retention/--child-retention); a LIVE fire (one mid-run) is never deleted. Defaults to 7d/168h when unset (the scheduler is ON by default); an explicit 0 disables the pass — fire sessions are never swept")
	fs.IntVar(&cfg.scheduleFireRetentionMaxTotal, "schedule-fire-retention-max-total", 0, "max persisted \"sched--\"-prefixed fire-session snapshots kept store-wide; the oldest beyond the cap are deleted, skipping in-flight fires. The symmetric peer of --main-retention-max-total: the age horizon bounds the tail, this cap bounds the head. 0 (default) disables the cap")

	// Skills / agents / soul / user-model (default OFF / conventional, like mecated).
	fs.Var(&cfg.skillsDirs, "skills-dir", "directory to discover progressive-disclosure skills from (repeatable; highest precedence). TRUST BOUNDARY: a SKILL.md steers the model — point this only at directories you trust")
	fs.BoolVar(&cfg.skillsConventional, "skills-conventional", false, "also discover skills from the conventional locations (lower precedence than --skills-dir). Default OFF")
	fs.Var(&cfg.agentsDirs, "agents-dir", "directory to discover named agent definitions from (repeatable; highest precedence). TRUST BOUNDARY: a def body steers the model — point this only at directories you trust")
	fs.BoolVar(&cfg.agentsConventional, "agents-conventional", true, "also discover agent definitions from the conventional locations. ON by default and INERT when no such dir exists")
	fs.StringVar(&cfg.soulFile, "soul-file", "", "path to a user-scoped persona/\"soul\" file (empty = the conventional location, fail-soft if absent)")
	fs.BoolVar(&cfg.noSoul, "no-soul", false, "disable the soul fragment entirely")
	fs.BoolVar(&cfg.noUserModel, "no-user-model", false, "disable the user-model entirely")
	fs.StringVar(&cfg.userModelDir, "user-model-dir", "", "directory for the user-scoped user-model store (empty = the conventional location)")

	// Permission config (issue #13).
	fs.Var(&cfg.permissionConfigs, "permission-config", "explicit operator-pointed permission YAML file (repeatable, fully trusted)")
	fs.BoolVar(&cfg.permissionsConventional, "permissions-conventional", true, "auto-discover the per-project .mecatl/settings.yaml + the user-global file (re-resolved per session). ON by default")
	fs.BoolVar(&cfg.importClaudePermissions, "import-claude-permissions", false, "also import Claude-Code settings.json (with the lossy fail-safe table)")
	fs.BoolVar(&cfg.trustProject, "trust-project", false, "honour a discovered project's ALLOW rules (a project's deny/ask is always honoured). Default OFF (the safe stance); an alias for --posture=trusted")

	// Fan-out / teams toggles.
	fs.BoolVar(&cfg.enableParallel, "enable-parallel", false, "enable the Parallel fan-out tool (parallel isolated child branches)")
	fs.BoolVar(&cfg.enableTeams, "enable-teams", false, "enable the experimental agent-teams capability (CreateTeam / SpawnTeammate / RunTeam)")

	// Headless telemetry (issue #343, ADR 0097): OPT-IN. --metrics-addr mounts a
	// SEPARATE loopback /metrics listener (the admin mux — Prometheus scrape).
	// --otlp-* push traces/metrics to a collector (the opt-in twin for non-scrape
	// deployments). All empty (default) leaves the pipeline off.
	fs.StringVar(&cfg.metricsAddr, "metrics-addr", "", "Prometheus /metrics listen address for a SEPARATE loopback admin listener (empty disables it). MUST be loopback — a non-loopback bind is REJECTED at parse time (ADR 0018 decision 6: pprof/expvar/metrics output is secret-shaped). e.g. \"127.0.0.1:9090\"")
	fs.StringVar(&cfg.otlpEndpoint, "otlp-endpoint", "", "OTLP trace collector endpoint (empty disables tracing). OPT-IN push to a collector")
	fs.StringVar(&cfg.otlpProtocol, "otlp-protocol", "grpc", "OTLP transport for traces: \"grpc\" (default) or \"http\"")
	fs.BoolVar(&cfg.otlpInsecure, "otlp-insecure", false, "skip TLS when dialing the OTLP collector (development only)")
	fs.StringVar(&cfg.otlpMetricsEndpoint, "otlp-metrics-endpoint", "", "OTLP METRICS collector endpoint (empty disables metrics push). An opt-in twin to --metrics-addr for non-scrape deployments; the prometheus reader stays on either way")
	fs.StringVar(&cfg.otlpMetricsProtocol, "otlp-metrics-protocol", "grpc", "OTLP transport for metrics: \"grpc\" (default) or \"http\"")
	fs.DurationVar(&cfg.otlpShutdownTimeout, "otlp-shutdown-timeout", 5*time.Second, "bound on the telemetry flush at SIGTERM (so a dead collector cannot hang shutdown). 0 disables the bound")

	if err := fs.Parse(argv); err != nil {
		return config{}, err
	}

	// Post-parse MCP finalize (issue #358): resolve the --mcp-server-insecure-http
	// relaxations against the collected --mcp-server entries and run the deferred
	// token-bearing scheme gate. Deferring it here (instead of inside Set) is what
	// makes the opt-in order-independent on argv.
	if err := cfg.mcpServers.Finalize(); err != nil {
		return config{}, err
	}

	// Record whether --posture / --subagent-model-router were set EXPLICITLY so
	// composition lets CLI out-rank the operator-global settings.yaml keys.
	fs.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "posture":
			cfg.postureFlagSet = true
		case "reasoning-effort":
			cfg.reasoningEffortFlagSet = true
		case "subagent-model-router":
			cfg.subagentModelRouterSet = true
		}
		if fl.Name == "schedule-fire-retention" {
			cfg.scheduleFireRetentionSet = true
		}
		if fl.Name == "default-provider" {
			cfg.defaultProviderFlagSet = true
		}
	})

	// Default the schedule-fire retention to 7d when the operator did not set it
	// explicitly (ADR 0059 decision #7 Phase-2, ADR 0073): the scheduler is ON by
	// default on a schedule-capable store, and a durable store accumulates a
	// "sched--" session per fire, so a sane default keeps it bounded. mecak8s is
	// the multi-replica scheduling home, so the default is especially relevant
	// here. An explicit --schedule-fire-retention=0 disables the sweep.
	if !cfg.scheduleFireRetentionSet && cfg.scheduleFireRetention == 0 {
		cfg.scheduleFireRetention = 7 * 24 * time.Hour
	}

	// Read the trusted ask-reviewer policy rubric, if any (an unreadable file
	// fails startup, mirroring mecated).
	if cfg.subagentAskReviewerPolicyFile != "" {
		body, err := os.ReadFile(cfg.subagentAskReviewerPolicyFile)
		if err != nil {
			return config{}, fmt.Errorf("read --subagent-ask-reviewer-policy %q: %w", cfg.subagentAskReviewerPolicyFile, err)
		}
		cfg.subagentAskReviewerPolicy = string(body)
	}

	// --guardrails=off is the master kill-switch.
	cfg.guardrailsOff = cfg.guardrailsMode == "off"

	// --auth-token may ride MECATL_AUTH_TOKEN (mirrors mecated).
	if cfg.authToken == "" {
		cfg.authToken = os.Getenv("MECATL_AUTH_TOKEN")
	}
	cfg.providerCredentials = cfg.providerFlags.Resolve()
	if cfg.providerCredentials.HasOpenAICodex() {
		return config{}, errors.New("mecak8s: openai-codex OAuth is unsupported; use an API-key provider or mecated/mecatui")
	}

	// --metrics-addr MUST be loopback (ADR 0018 decision 6): the admin mux serves
	// pprof/expvar/metrics output that can embed prompt text, file paths, and
	// goroutine stacks — secret-shaped. A non-loopback bind is REJECTED at parse
	// time (fail-closed) via the shared cliconfig.IsLoopbackAddr gate, mirroring
	// mecated's --perf-mcp loopback refusal.
	if cfg.metricsAddr != "" && !cliconfig.IsLoopbackAddr(cfg.metricsAddr) {
		return config{}, fmt.Errorf("--metrics-addr %q is not loopback: the admin mux (/metrics, /debug/pprof, /debug/vars) exposes unauthenticated runtime data; bind loopback (e.g. 127.0.0.1:9090) or leave it empty", cfg.metricsAddr)
	}

	return cfg, nil
}

// appConfig maps the CLI config onto the shared app.Config build contract,
// threading a Diagnostics sink into the engine/composition. It is a thin subset
// of mecated's appConfig: the engine-build knobs mecak8s carries, with the
// k8s-native defaults (RedisURL, SessionLeaseK8sNamespace, Headless, auto
// posture) threaded through. Sink / ToolCallRecorder / MetricsRoleScoper come
// from the observability handles (issue #343): nil when telemetry is off (the
// byte-identical no-metrics posture), non-nil when --otlp-* is set.
func appConfig(cfg config, diag port.Diagnostics, obs observability) app.Config {
	out := app.Config{
		Workspace:              cfg.workspace,
		Model:                  cfg.model,
		DefaultProvider:        cfg.defaultProvider,
		DefaultModel:           cfg.defaultModel,
		DefaultProviderFlagSet: cfg.defaultProviderFlagSet,
		UseOpenAI:              cfg.useOpenAI,
		UseMock:                cfg.useMock,
		Shell:                  cfg.shell,
		NoBash:                 cfg.noBash,
		RedisURL:               cfg.redisURL,
		// OwnershipEnforced mirrors cmd/mecated's wiring: the OIDC verifier being
		// enabled IS the caller-isolation on-switch (ADR 0102). Without this line
		// mecak8s attributes ownership correctly but never enforces it — every
		// caller-owned application boundary silently falls back to its
		// ownerless-compatibility path, and deploy/mecak8s-oidc's isolation claim
		// does not hold for this binary.
		OwnershipEnforced:             cfg.oidc.Enabled(),
		SessionLeaseK8sNamespace:      cfg.sessionLeaseK8sNamespace,
		SessionLeaseTTL:               cfg.sessionLeaseTTL,
		SessionLeaseRenewInterval:     cfg.sessionLeaseRenewInterval,
		SchedulerEnabled:              !cfg.noScheduler,
		SchedulerTickInterval:         cfg.schedulerTickInterval,
		SchedulerMinInterval:          cfg.schedulerMinInterval,
		SchedulerMaxConcurrentFires:   cfg.schedulerMaxConcurrentFires,
		LLMMaxAttempts:                cfg.llmMaxAttempts,
		LLMPerAttemptTimeout:          cfg.llmPerAttemptTimeout,
		LLMStreamIdleTimeout:          cfg.llmStreamIdleTimeout,
		LLMBreakerThreshold:           cfg.llmBreakerThreshold,
		LLMBreakerCooldown:            cfg.llmBreakerCooldown,
		MaxRunTokens:                  cfg.maxRunTokens,
		MaxTeamTokens:                 cfg.maxTeamTokens,
		PromptCacheDisabled:           cfg.noPromptCache,
		AnthropicCacheTTL:             cfg.anthropicCacheTTL,
		ChildRetention:                cfg.childRetention,
		ChildRetentionMaxPerFamily:    cfg.childRetentionMaxPerFamily,
		ChildGCInterval:               cfg.childGCInterval,
		MainRetention:                 cfg.mainRetention,
		MainRetentionMaxTotal:         cfg.mainRetentionMaxTotal,
		ScheduleFireRetention:         cfg.scheduleFireRetention,
		ScheduleFireRetentionMaxTotal: cfg.scheduleFireRetentionMaxTotal,
		SkillsDirs:                    cfg.skillsDirs,
		SkillsConventional:            cfg.skillsConventional,
		AgentsDirs:                    cfg.agentsDirs,
		AgentsConventional:            cfg.agentsConventional,
		SubagentModel:                 cfg.subagentModel,
		SubagentAskReviewerModel:      cfg.subagentAskReviewer,
		SubagentAskReviewerMaxDenies:  cfg.subagentAskReviewerMaxDenies,
		SubagentAskReviewerPolicy:     cfg.subagentAskReviewerPolicy,
		RouterDisabled:                cfg.subagentModelRouterSet && !cfg.subagentModelRouter,
		GuardrailsModel:               cfg.guardrailsModel,
		GuardrailsDisabled:            cfg.guardrailsOff,
		ModelAliases:                  cfg.modelAliases.AsMap(),
		ModelSlots:                    cfg.modelSlots.AsMap(),
		// Remote MCP servers (issue #341): the static name=URL entries (with any
		// MCP_<NAME>_TOKEN bearer already resolved into Headers at parse time).
		MCPServers:              cfg.mcpServers.Servers(),
		EnableParallel:          cfg.enableParallel,
		EnableTeams:             cfg.enableTeams,
		SoulPath:                cfg.soulFile,
		NoSoul:                  cfg.noSoul,
		UserModelDir:            cfg.userModelDir,
		NoUserModel:             cfg.noUserModel,
		PermissionsConventional: cfg.permissionsConventional,
		ImportClaudePermissions: cfg.importClaudePermissions,
		TrustProject:            cfg.trustProject,
		PermissionConfigs:       cfg.permissionConfigs,
		Posture:                 app.ParsePosture(cfg.posture),
		PostureFlagSet:          cfg.postureFlagSet,
		// Reasoning-effort tier (ADR 0055): operator-tier only; reasoningEffortFlagSet
		// lets CLI out-rank the operator-global settings.yaml reasoning-effort: key
		// (folded by foldOperatorReasoningEffort in app.Build, like posture).
		ReasoningEffort:        cfg.reasoningEffort,
		ReasoningEffortFlagSet: cfg.reasoningEffortFlagSet,
		Privileged:             privilegedProcess(),
		// Headless is explicit deployment identity; mecak8s defaults true, so
		// posture never raises workspace trust.
		Headless: cfg.headless,
		// Interactive = !headless: the deliberate headless default. A child's
		// unresolved ask is auto-denied / routed to the opt-in ask-reviewer.
		Interactive: !cfg.headless,
		Diagnostics: diag,
		// Observability (issue #343, ADR 0097): OPT-IN. With no --otlp-* flags the
		// handles are zero-valued (nil) — the byte-identical no-metrics posture.
		Sink:              obs.Sink,
		ToolCallRecorder:  obs.ToolCallRecorder,
		MetricsRoleScoper: obs.MetricsRoleScoper,
	}
	// Project only the supported API-key credentials and parsed base URLs from
	// the once-resolved snapshot. An OPENAI_API_KEY implies the real provider.
	keys := cfg.providerCredentials
	cfg.providerFlags.ApplyResolvedAPIKeys(&out, keys)
	if keys.OpenAI != "" {
		out.UseOpenAI = true
	}
	if keys.AuthFileWarning != "" {
		slog.Warn(keys.AuthFileWarning)
	}
	cfg.toolhiveLLMFlags.Apply(&out)
	return out
}

// privilegedProcess reports the cmd-computed "root WITHOUT a declared sandbox"
// predicate (euid 0 && MECATL_SANDBOX/IS_SANDBOX unset), threaded into the
// posture root-refusal (the cmd owns the os/env reads; internal/app takes the
// bool). A k8s pod should run non-root (PSS restricted) OR set MECATL_SANDBOX=1
// when running as root — auto/yolo are refused as bare root. It mirrors
// cmd/mecated's privilegedProcess.
func privilegedProcess() bool {
	return os.Geteuid() == 0 && !sandboxDeclared()
}

func sandboxDeclared() bool {
	return os.Getenv("MECATL_SANDBOX") == "1" || os.Getenv("IS_SANDBOX") == "1"
}
