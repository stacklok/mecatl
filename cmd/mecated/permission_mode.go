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
var permissionModeHelp = "permission mode, one of: " + strings.Join(app.PermissionModeNames(), ", ") +
	". Sets two things: the process-wide posture, fixed at startup (strict for plan, default, and accept-edits; trusted for trusted and trusted-accept-edits; auto; yolo), " +
	"and the session mode new sessions start in, which each session may change. Default: default. " +
	"auto and yolo allow tools without asking, so they need a guardrails checker (--guardrails-model) or an explicit --guardrails off, and are refused for root unless MECATL_SANDBOX=1 or IS_SANDBOX=1. " +
	"Deny rules and configured ask rules still apply. Replaces the deprecated --posture and --yolo; --trust-project still combines with it."

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
			"--posture is DEPRECATED; use --permission-mode instead (ADR 0365)",
			"posture", cfg.posture, "replacement", "--permission-mode "+postureReplacementToken(cfg.posture))
	}
	if cfg.yoloFlagSet {
		diag.Log(context.Background(), port.LevelWarn,
			"--yolo is DEPRECATED; use --permission-mode yolo instead (ADR 0365)",
			"replacement", "--permission-mode yolo")
	}
}

// postureReplacementToken names the token equivalent to a legacy --posture
// value with the default session mode (strict maps to default).
func postureReplacementToken(raw string) string {
	if tok, ok := app.PermissionModeForPair(app.ParsePosture(raw), session.ModeDefault); ok {
		return tok.Name
	}
	return "default"
}
