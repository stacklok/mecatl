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
	". Sets the mode the run's session starts in and the process-wide ceiling on the modes it may enter. Default: default. " +
	"auto and yolo allow tools without asking. auto needs an enforcing guardrails checker (--guardrails-model or the guardrail model slot) and refuses to start without one; yolo runs without one. " +
	"Deny and configured ask rules still apply. A mode never grants project trust; use --trust-project. Replaces the deprecated --posture."

// validatePermissionModeFlags fails fast on an unknown --permission-mode token
// and on a --permission-mode combined with the deprecated --posture alias.
func validatePermissionModeFlags(f flags) error {
	if !f.permissionModeFlagSet {
		return nil
	}
	if _, err := app.ParsePermissionMode(f.permissionMode); err != nil {
		return fmt.Errorf("--permission-mode: %w", err)
	}
	if f.postureFlagSet {
		return fmt.Errorf("--permission-mode and --posture are both set: pass only --permission-mode (--posture is its deprecated alias)")
	}
	return nil
}

// warnDeprecatedPermissionFlags emits exactly one deprecation WARN when the
// operator passed the deprecated --posture explicitly.
func warnDeprecatedPermissionFlags(diag port.Diagnostics, f flags) {
	if diag == nil || !f.postureFlagSet {
		return
	}
	diag.Log(context.Background(), port.LevelWarn,
		"--posture is DEPRECATED; use --permission-mode instead",
		"posture", f.posture, "replacement", "--permission-mode "+app.PostureReplacementToken(app.ParsePosture(f.posture)))
}
