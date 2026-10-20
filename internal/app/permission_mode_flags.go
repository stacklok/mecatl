package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
)

// PermissionModeFlags is the --permission-mode flag surface a server command
// (mecated, mecak8s, mecatequi) parsed: the explicit token and the deprecated
// aliases it replaces. Each *Set field records that the operator typed the flag,
// so a flag default is never mistaken for a use of the alias.
type PermissionModeFlags struct {
	PermissionMode    string
	PermissionModeSet bool
	Posture           string
	PostureSet        bool
	// YoloSet is the deprecated --yolo alias; only commands that still accept it
	// set it.
	YoloSet bool
}

// Validate fails fast on an unknown --permission-mode token and on a
// --permission-mode combined with one of its deprecated aliases, so the operator
// never has to guess which of two permission sources won.
func (f PermissionModeFlags) Validate() error {
	if !f.PermissionModeSet {
		return nil
	}
	if _, err := ParsePermissionMode(f.PermissionMode); err != nil {
		return fmt.Errorf("--permission-mode: %w", err)
	}
	if f.PostureSet {
		return errors.New("--permission-mode and --posture are both set: pass only --permission-mode (--posture is its deprecated alias)")
	}
	if f.YoloSet {
		return errors.New("--permission-mode and --yolo are both set: pass only --permission-mode (--yolo is a deprecated alias for --permission-mode yolo)")
	}
	return nil
}

// WarnDeprecated emits exactly one deprecation WARN per deprecated alias the
// operator passed, naming the --permission-mode token that replaces it. The
// aliases still resolve exactly as before.
func (f PermissionModeFlags) WarnDeprecated(diag port.Diagnostics) {
	if diag == nil {
		return
	}
	if f.PostureSet {
		diag.Log(context.Background(), port.LevelWarn,
			"--posture is DEPRECATED; use --permission-mode instead",
			"posture", f.Posture, "replacement", "--permission-mode "+PostureReplacementToken(ParsePosture(f.Posture)))
	}
	if f.YoloSet {
		diag.Log(context.Background(), port.LevelWarn,
			"--yolo is DEPRECATED; use --permission-mode yolo instead",
			"replacement", "--permission-mode yolo")
	}
}

// PermissionModeHelpOptions holds the per-command facts the shared
// --permission-mode usage line depends on.
type PermissionModeHelpOptions struct {
	// DefaultToken is the token that applies when the flag is unset.
	DefaultToken string
	// OneShot describes a command that runs a single session, so the start mode
	// is the run's session rather than every new session.
	OneShot bool
	// RootRefusal says the command refuses auto and yolo for an unsandboxed root.
	RootRefusal bool
	// YoloAlias says the command still accepts the deprecated --yolo.
	YoloAlias bool
}

// PermissionModeHelp is the --permission-mode usage line shared by the server
// commands. It names both halves a token sets and their lifetimes, and lists
// every valid token from the composition table so help and parsing cannot drift.
func PermissionModeHelp(o PermissionModeHelpOptions) string {
	starts := "new sessions start in"
	if o.OneShot {
		starts = "the run's session starts in"
	}
	var b strings.Builder
	b.WriteString("Permission mode, one of: " + strings.Join(PermissionModeNames(), ", ") + ". ")
	b.WriteString("Sets two things: the mode " + starts + ", and the process-wide server ceiling, fixed at startup, " +
		"which is the most permissive mode a session may switch to (accept-edits for plan, default, and accept-edits). ")
	b.WriteString("Default: " + o.DefaultToken + ". ")
	b.WriteString("auto and yolo allow tools without asking. auto needs an enforcing guardrails checker " +
		"(--guardrails-model or the guardrail model slot) and refuses to start without one, including with --guardrails off; yolo runs without one. ")
	if o.RootRefusal {
		b.WriteString("Both are refused for root unless MECATL_SANDBOX=1 or IS_SANDBOX=1. ")
	}
	b.WriteString("Deny rules and configured ask rules still apply. A mode never grants project trust; use --trust-project. ")
	if o.YoloAlias {
		b.WriteString("Replaces the deprecated --posture and --yolo.")
	} else {
		b.WriteString("Replaces the deprecated --posture.")
	}
	return b.String()
}
