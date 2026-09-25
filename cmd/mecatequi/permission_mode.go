package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/app"
)

// permissionModeHelp is the --permission-mode usage line (ADR 0365). It names
// both halves a token sets and their lifetimes, and lists every valid token from
// the composition table so help and parsing cannot drift.
var permissionModeHelp = "Permission mode, one of: " + strings.Join(app.PermissionModeNames(), ", ") +
	". Sets two things: the process-wide posture, fixed at startup (strict for plan, default, and accept-edits; trusted for trusted and trusted-accept-edits; auto; yolo), " +
	"and the session mode the run's session starts in. Default: default. " +
	"auto and yolo allow tools without asking, so they need a guardrails checker (--guardrails-model or the guardrail model slot) or an explicit --guardrails off. " +
	"trusted and trusted-accept-edits need a trust source such as --trust-project on this headless root. " +
	"Deny and configured ask rules still apply. Replaces the deprecated --posture; --trust-project still combines with it."

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
	replacement := "default"
	if tok, ok := app.PermissionModeForPair(app.ParsePosture(f.posture), session.ModeDefault); ok {
		replacement = tok.Name
	}
	diag.Log(context.Background(), port.LevelWarn,
		"--posture is DEPRECATED; use --permission-mode instead (ADR 0365)",
		"posture", f.posture, "replacement", "--permission-mode "+replacement)
}
