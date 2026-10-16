package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/app"
)

// permissionModeHelp is the --permission-mode usage line. It names
// both halves a token sets and their lifetimes, and lists every valid token from
// the composition table so help and parsing cannot drift.
var permissionModeHelp = "Permission mode, one of: " + strings.Join(app.PermissionModeNames(), ", ") +
	". Sets two things: the mode new sessions start in, and the process-wide server ceiling, fixed at startup, which is the most permissive mode a session may switch to (accept-edits for plan, default, and accept-edits). Default: auto. " +
	"auto and yolo allow tools without asking and require MECATL_SANDBOX when running as root. auto needs an enforcing guardrails checker (--guardrails-model or the guardrail model slot) and refuses to start without one, including with --guardrails off; yolo runs without one. " +
	"Deny rules and configured ask rules still apply. A mode never grants project trust; use --trust-project. Replaces the deprecated --posture"

// validatePermissionModeFlags fails fast on an unknown --permission-mode token
// and on a --permission-mode combined with the deprecated --posture alias.
func validatePermissionModeFlags(cfg config) error {
	if !cfg.permissionModeFlagSet {
		return nil
	}
	if _, err := app.ParsePermissionMode(cfg.permissionMode); err != nil {
		return fmt.Errorf("--permission-mode: %w", err)
	}
	if cfg.postureFlagSet {
		return fmt.Errorf("--permission-mode and --posture are both set: pass only --permission-mode (--posture is its deprecated alias)")
	}
	return nil
}

// warnDeprecatedPermissionFlags emits exactly one deprecation WARN when the
// operator passed the deprecated --posture explicitly. The --posture DEFAULT
// (auto) is not a use of the alias and stays silent.
func warnDeprecatedPermissionFlags(diag port.Diagnostics, cfg config) {
	if diag == nil || !cfg.postureFlagSet {
		return
	}
	diag.Log(context.Background(), port.LevelWarn,
		"--posture is DEPRECATED; use --permission-mode instead",
		"posture", cfg.posture, "replacement", "--permission-mode "+app.PostureReplacementToken(app.ParsePosture(cfg.posture)))
}
