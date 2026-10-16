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
var permissionModeHelp = "permission mode, one of: " + strings.Join(app.PermissionModeNames(), ", ") +
	". Sets two things: the mode new sessions start in, and the process-wide server ceiling, fixed at startup, which is the most permissive mode a session may switch to (accept-edits for plan, default, and accept-edits). Default: default. " +
	"auto and yolo allow tools without asking. auto needs an enforcing guardrails checker (--guardrails-model or the guardrail model slot) and refuses to start without one; yolo runs without one. Both are refused for root unless MECATL_SANDBOX=1 or IS_SANDBOX=1. " +
	"Deny rules and configured ask rules still apply. A mode never grants project trust; use --trust-project. Replaces the deprecated --posture and --yolo."

// validatePermissionModeFlags fails fast on an unknown --permission-mode token
// and on a --permission-mode combined with one of its deprecated aliases, so the
// operator never has to guess which of two permission sources won.
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
	if cfg.yoloFlagSet {
		return fmt.Errorf("--permission-mode and --yolo are both set: pass only --permission-mode (--yolo is a deprecated alias for --permission-mode yolo)")
	}
	return nil
}

// warnDeprecatedPermissionFlags emits exactly one deprecation WARN per
// deprecated alias the operator passed, naming the --permission-mode token that
// replaces it. The aliases still resolve exactly as before.
func warnDeprecatedPermissionFlags(diag port.Diagnostics, cfg config) {
	if diag == nil {
		return
	}
	if cfg.postureFlagSet {
		diag.Log(context.Background(), port.LevelWarn,
			"--posture is DEPRECATED; use --permission-mode instead",
			"posture", cfg.posture, "replacement", "--permission-mode "+app.PostureReplacementToken(app.ParsePosture(cfg.posture)))
	}
	if cfg.yoloFlagSet {
		diag.Log(context.Background(), port.LevelWarn,
			"--yolo is DEPRECATED; use --permission-mode yolo instead",
			"replacement", "--permission-mode yolo")
	}
}
