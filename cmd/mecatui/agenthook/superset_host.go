package agenthook

import (
	"os"
	"path/filepath"
	"strings"
)

// This file holds the ONE host-specific half of the package: discovering where a
// given host tool keeps its hook command, and which environment entries that
// command expects. The event schema in hook.go is vendor-neutral and shared; a
// second supported host becomes a sibling detect function here, not a new
// package and not a change to the schema.

// New returns a Notifier bound to whichever supported host tool this process is
// running under, or nil when there is none (the ordinary standalone mecatui run
// — every method on a nil Notifier is a no-op). env is the process environment
// as name=value entries (os.Environ() in production); it is passed in so tests
// need not mutate global state.
func New(env []string) *Notifier {
	return detectSuperset(envMap(env))
}

// Superset host.
//
// Superset (the editor) funnels every integrated agent's lifecycle through one
// Superset-owned script, ~/.superset/hooks/notify.sh, which normalizes the event
// and POSTs it to the host-service that raises the OS notification and drives the
// pane's busy/idle chrome. That script is a NORMALIZER over the shared schema:
// it reads `hook_event_name` (or camelCase `hookEventName`, or Codex's older
// `type`) from stdin or argv[1], and collapses the vendor vocabulary into its
// own Start | PermissionRequest | Stop triple — which is why emitting the
// canonical cross-vendor names from hook.go is correct here.
const (
	// supersetTerminalIDVar is set only inside a Superset v2 terminal. It is the
	// participation gate every notify.sh caller applies (the script itself exits
	// 0 immediately when the SUPERSET_* markers are absent), so honouring it
	// here keeps a mecatui launched outside Superset completely inert.
	supersetTerminalIDVar = "SUPERSET_TERMINAL_ID"
	// supersetHomeVar locates the Superset state directory holding hooks/.
	supersetHomeVar = "SUPERSET_HOME_DIR"
	// supersetHarnessVar names the agent whose hook config fired. notify.sh
	// cross-checks it against the SUPERSET_AGENT_ID its wrapper exported and
	// DROPS a mismatch, so a foreign agent replaying our config (or our binary
	// invoked from another agent's tool call) cannot relabel the terminal.
	supersetHarnessVar = "SUPERSET_HOOK_HARNESS"
)

// detectSuperset resolves the Superset notify.sh for this process, or nil when
// this is not a Superset terminal or the script is missing/non-executable
// (yielding nil rather than a Notifier that would shell a dead path every turn).
// The home-directory fallback mirrors notify.sh's own ${SUPERSET_HOME_DIR:-$HOME/.superset}.
func detectSuperset(env map[string]string) *Notifier {
	if strings.TrimSpace(env[supersetTerminalIDVar]) == "" {
		return nil // not a Superset v2 terminal — inert.
	}
	home := strings.TrimSpace(env[supersetHomeVar])
	if home == "" {
		if h := strings.TrimSpace(env["HOME"]); h != "" {
			home = filepath.Join(h, ".superset")
		}
	}
	if home == "" {
		return nil
	}
	script := filepath.Join(home, "hooks", "notify.sh")
	if !isExecutableFile(script) {
		return nil
	}
	return newNotifier(script, []string{supersetHarnessVar + "=" + agentID})
}

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}
