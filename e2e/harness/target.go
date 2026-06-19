//go:build e2e

// Package harness is the live-e2e test harness for mecatl: it spawns (or dials)
// a mecated server, drives real runs over the gRPC Converse stream through the
// SAME client package mecatui uses (cmd/mecatui/client — so the TUI's wire path
// is covered transitively), and records self-diagnosing JSONL transcripts per
// scenario under .scratch/e2e-artifacts/.
//
// It deliberately imports ONLY contracts/gen (indirectly via the client),
// cmd/mecatui/client, and stdlib — never internal/... or engine/agent. The
// assertions in e2e/*_test.go are event-stream / side-effect assertions; the
// harness supplies the events and the side-effect locations (StateDir).
package harness

import (
	"fmt"
	"os"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// StateKind names a server-side state directory the local target owns. Remote
// targets return "" for every kind (the suite Skips file-level assertions).
type StateKind string

const (
	// StateWorkspace is the session workspace root passed to CreateSession.
	StateWorkspace StateKind = "workspace"
	// StateMemory is the per-project memory store dir (--memory-dir).
	StateMemory StateKind = "memory"
	// StateUserModel is the cross-project user-model dir (--user-model-dir).
	StateUserModel StateKind = "usermodel"
	// StateStore is the JSONL session store dir (--store-dir).
	StateStore StateKind = "store"
	// StateLease is the SHARED single-host flock session-lease dir
	// (--session-lease-dir); two Locals sharing a store also share this so the
	// cross-process lease is genuinely contended (cloud-native Phase 4).
	StateLease StateKind = "lease"
	// StateArtifacts is the harness-owned artifact dir for this suite run.
	StateArtifacts StateKind = "artifacts"
)

// Target abstracts WHERE the mecated under test runs: a locally spawned
// ./bin/mecated (Local) or an operator-provided remote server (Remote, selected
// by MECATL_E2E_TARGET). Specs talk to it only through this interface.
type Target interface {
	// Client is the connected gRPC client (the same package mecatui dials with).
	Client() *client.Client
	// Workspace is the absolute session workspace root to pass to CreateSession.
	Workspace() string
	// MetricsURL is the Prometheus /metrics endpoint ("" when unknown/disabled).
	MetricsURL() string
	// StateDir maps a StateKind to its local path; "" when not locally readable
	// (remote target) — callers Skip the file-level assertion then.
	StateDir(kind StateKind) string
	// LogTail returns the last n bytes of the captured server log — the
	// combined stdout+stderr of the spawned mecated ("" on remote targets).
	// Used by failure reports and the soul-diagnostic spec.
	LogTail(n int) string
	// IsLocal reports whether the harness owns the server process (and its dirs).
	IsLocal() bool
	// Close tears the target down (SIGTERM + wait for Local; conn close for both).
	Close() error
}

// NewTarget builds the suite's target from the environment: MECATL_E2E_TARGET
// set → Remote (host:port); otherwise a freshly spawned Local mecated.
func NewTarget() (Target, error) {
	if addr := os.Getenv("MECATL_E2E_TARGET"); addr != "" {
		return NewRemote(addr)
	}
	if os.Getenv("OPENROUTER_API_KEY") == "" {
		return nil, fmt.Errorf("OPENROUTER_API_KEY is not set: the local e2e target spawns mecated against OpenRouter; export OPENROUTER_API_KEY first (e.g. from your key file — see e2e/README.md)")
	}
	return NewLocal()
}

// Env knobs (all optional; defaults are the verified-cheap lanes):
//
//	MECATL_E2E_TARGET           host:port of an existing mecated (skips Local spawn)
//	MECATL_E2E_MODEL            default-lane model id (default anthropic/claude-3.5-haiku)
//	MECATL_E2E_MODEL_SECONDARY  second-lane model id (default openai/gpt-4.1-mini; "skip" disables)
//	MECATL_E2E_MAX_RUN_TOKENS   --max-run-tokens for the local server (default 50000)
//	MECATL_E2E_MAX_TEAM_TOKENS  --max-team-tokens for the local server (default 60000)
//	MECATL_E2E_WORKSPACE        remote-target session workspace root (required for Remote)
//	MECATL_E2E_METRICS_URL      remote-target /metrics URL (optional)

// DefaultModel returns the default-lane model id (env-overridable). The lanes
// were verified LIVE, and reality inverted the original "OpenAI-family default"
// plan (full trail in e2e/README.md):
//
//   - openai/gpt-4o-mini: tool-bearing /responses requests are 404-blocked
//     outright by this account's data policy.
//   - openai/gpt-4.1-mini (and siblings): exist and tool-call, but OpenRouter
//     routes them to endpoints whose input-side prompt filter DETERMINISTICALLY
//     kills mecatl-shaped requests over innocuous imperative phrasings
//     ("Do not use any tools", "Never call Subagent", …) with `response
//     incomplete: content_filter` — including MODEL-AUTHORED child prompts
//     (subagent goals), which no amount of suite-side rewording can control.
//   - anthropic/claude-3.5-haiku (Bedrock endpoints): no such filter observed;
//     tools verified working through the same openrouter provider id.
//
// So the multi-turn tool scenarios run on the anthropic-family lane and the
// OpenAI-family lane is the secondary single-turn smoke (which always passed).
func DefaultModel() string {
	if m := os.Getenv("MECATL_E2E_MODEL"); m != "" {
		return m
	}
	return "anthropic/claude-3.5-haiku"
}

// SecondaryModel returns the second-lane (OpenAI-family) model id, or "" when
// the lane is disabled (MECATL_E2E_MODEL_SECONDARY=skip/none/off).
func SecondaryModel() string {
	m := os.Getenv("MECATL_E2E_MODEL_SECONDARY")
	switch m {
	case "":
		return "openai/gpt-4.1-mini"
	case "skip", "none", "off":
		return ""
	}
	return m
}

// ProviderID is the provider the suite pins every session to. The local target
// is spawned with ONLY OPENROUTER_API_KEY in its environment, so this is also
// the server's default — but the explicit selector keeps remote targets honest.
const ProviderID = "openrouter"
