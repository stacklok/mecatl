// Package modelhook contains the contextual guardrail rule matcher, exact-action
// grant store, and the narrow verdict checker retained for path-escape policy.
// Contextual action and inbound review run directly at engine choke points; this
// package no longer decorates HookRunner or implements the removed legacy reviewer.
package modelhook

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

// CheckRequest is the narrow input used by the independent path-escape checker.
type CheckRequest struct {
	Phase   Phase
	Tool    string
	Content string
	Prompt  string
}

// CheckResult carries the verdict and every model usage incurred while producing it.
type CheckResult struct {
	Verdict Verdict
	Usage   session.AuxiliaryUsage
}

// VerdictChecker judges one path-escape policy request.
type VerdictChecker interface {
	Check(context.Context, CheckRequest) (CheckResult, error)
}

func shellCmdFromArgs(raw string) (string, bool) {
	return governance.ShellCommandFromArgs(json.RawMessage(raw))
}

func shellFullyReadOnly(cmd string) bool {
	if governance.ReadOnlyShell(cmd) {
		return true
	}
	segments := governance.SplitCommands(cmd)
	if len(segments) == 0 {
		return false
	}
	for _, segment := range segments {
		if !governance.SubstitutionReadOnly(segment) {
			return false
		}
	}
	return true
}

// DefaultShellPrePrompt remains an additive operator-policy rubric for the
// contextual reviewer. It does not implement or invoke a second reviewer.
const DefaultShellPrePrompt = "Inspect the OUTBOUND shell command below. Judge it UNSAFE only if you can " +
	"identify one of these SPECIFIC dangerous actions: (1) sending data OFF THIS MACHINE to an external or " +
	"network destination — e.g. curl/wget/scp/nc uploading file contents or command output to a remote host, " +
	"or piping data into a network request — especially secrets, credentials, keys, or tokens; (2) fetching " +
	"and executing remote code (e.g. `curl ... | sh`, `wget ... | bash`); (3) an irreversible action on a " +
	"REMOTE you may not control — force-push, pushing or merging to a remote, `gh pr merge`, publishing a " +
	"release, deleting a remote branch or repository; (4) a destructive, hard-to-reverse LOCAL operation — " +
	"recursive deletion of a directory tree, overwriting a disk device, or mass recursive chmod/chown; " +
	"(5) writing to a credential, SSH-key, shell-startup, scheduler (cron/systemd), or git-hook file in a way " +
	"that could grant later off-machine access or persistent code execution. Otherwise judge it SAFE. " +
	"Writing or creating ORDINARY local source, config, build-output, or note files is normal local work and " +
	"is not exfiltration. Do not flag a command merely because it writes outside a particular directory."

// ClampReason bounds checker-authored text used by the independent escape policy.
func ClampReason(value string) string {
	const limit = 240
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
