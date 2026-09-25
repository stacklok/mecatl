package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/modelhook"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// permission_mode.go is the COMPOSITION-LAYER named permission-mode vocabulary
// (ADR 0365). A token is an operator shorthand that writes an exact pair of
// EXISTING values: the process-wide Posture and the default session
// PermissionMode (server.Config.DefaultMode). It introduces no third source of
// permission state: the table below is the whole mechanism, and it is a lookup,
// not an ordering. Permission relaxation and project trust are independent axes,
// so no total order over the tokens exists and none may be inferred from their
// position in the table.

// PermissionModeToken is one named combination of posture and default session
// mode.
type PermissionModeToken struct {
	// Name is the CLI/YAML spelling.
	Name string
	// Posture is the process-wide half, fixed at startup.
	Posture Posture
	// SessionMode is the session half: only the default a new session starts in.
	SessionMode session.PermissionMode
}

// permissionModeTable is the ADR 0365 contract. Redundant products (an
// accept-edits variant of auto or yolo) are deliberately absent: allow-all
// already permits edits.
var permissionModeTable = []PermissionModeToken{
	{Name: "plan", Posture: PostureStrict, SessionMode: session.ModePlan},
	{Name: "default", Posture: PostureStrict, SessionMode: session.ModeDefault},
	{Name: "accept-edits", Posture: PostureStrict, SessionMode: session.ModeAccept},
	// The three single-word tokens above strict share their posture's name.
	{Name: PostureTrusted.String(), Posture: PostureTrusted, SessionMode: session.ModeDefault},
	{Name: "trusted-accept-edits", Posture: PostureTrusted, SessionMode: session.ModeAccept},
	{Name: PostureAuto.String(), Posture: PostureAuto, SessionMode: session.ModeDefault},
	{Name: PostureYolo.String(), Posture: PostureYolo, SessionMode: session.ModeDefault},
}

// PermissionModeNames lists the valid tokens in table order, for help text and
// error messages.
func PermissionModeNames() []string {
	names := make([]string, len(permissionModeTable))
	for i, tok := range permissionModeTable {
		names[i] = tok.Name
	}
	return names
}

// ParsePermissionMode resolves a token. An unknown or empty value is an error
// naming the valid set; unlike the deprecated --posture it never fails closed
// silently, because an operator who typed a mode name must learn it did nothing.
func ParsePermissionMode(s string) (PermissionModeToken, error) {
	want := strings.ToLower(strings.TrimSpace(s))
	for _, tok := range permissionModeTable {
		if tok.Name == want {
			return tok, nil
		}
	}
	return PermissionModeToken{}, fmt.Errorf("unknown permission mode %q: valid values are %s", s, strings.Join(PermissionModeNames(), ", "))
}

// PermissionModeForPair returns the token naming (posture, mode), if one exists.
// The deprecated alias surface can produce pairs no token names (for example
// auto with a plan session); those stay expressible and report ok=false.
func PermissionModeForPair(p Posture, mode session.PermissionMode) (PermissionModeToken, bool) {
	if mode == "" {
		mode = session.ModeDefault
	}
	for _, tok := range permissionModeTable {
		if tok.Posture == p && tok.SessionMode == mode {
			return tok, true
		}
	}
	return PermissionModeToken{}, false
}

// foldPermissionMode applies the permission-mode token BEFORE the posture fold.
// Precedence: an explicit --permission-mode flag, then the operator-tier
// permissionMode: key, then the deprecated surface (--posture/--yolo and the
// operator-tier posture: key, handled by foldOperatorPosture). A token writes
// cfg.Posture and cfg.DefaultSessionMode and marks the posture explicit, so the
// deprecated posture: key cannot override it. --trust-project and --yolo still
// fold MAX-tier on top in resolvePosture, exactly as they do over --posture.
func foldPermissionMode(cfg Config) (Config, error) {
	raw := ""
	source := ""
	switch {
	case cfg.PermissionModeFlagSet:
		raw, source = cfg.PermissionMode, "--permission-mode"
	case cfg.PostureFlagSet:
		// An explicit (deprecated) --posture is a CLI flag and out-ranks the
		// operator YAML, the rule --posture already follows for posture:.
	default:
		if res, ok := cfg.permResolver.(*permconfig.Resolver); ok && res != nil {
			if v := strings.TrimSpace(res.OperatorPermissionMode()); v != "" {
				raw, source = v, "settings permissionMode"
			}
		}
	}
	if source == "" {
		return foldOperatorPosture(cfg), nil
	}
	tok, err := ParsePermissionMode(raw)
	if err != nil {
		return cfg, fmt.Errorf("%s: %w", source, err)
	}
	cfg.Posture = tok.Posture
	cfg.PostureFlagSet = true
	cfg.DefaultSessionMode = tok.SessionMode
	cfg.permissionModeName = tok.Name
	return cfg, nil
}

// checkerRefusal is the allow-all admission gate (ADR 0365 decision 4). A
// posture that waives the built-in mutate-ask floor leaves the guardrails
// checker as the only inspection of tool content, so starting one with no
// checker and no explicit kill-switch is refused. It keys on the RESOLVED
// posture with no exemption for a flag default; shipped defaults declare their
// choice instead.
func checkerRefusal(cfg Config, checkerConfigured bool) error {
	if cfg.Posture < PostureAuto || checkerConfigured || cfg.GuardrailsDisabled {
		return nil
	}
	msg := fmt.Sprintf("permission mode %q allows every tool without asking, but no guardrails checker is configured, so nothing would inspect tool content. Fix it one of three ways: set a checker with --guardrails-model <model>; bind the `guardrail` model slot (--model-slot guardrail=<model> or models.slots.guardrail in settings.yaml); or run unsupervised on purpose with --guardrails off",
		cfg.resolvedPermissionModeName())
	if cfg.Posture >= PostureYolo {
		msg += ". " + yoloCheckerAdvisoryNote
	}
	return fmt.Errorf("%s", msg)
}

// yoloCheckerAdvisoryNote states what a configured checker does NOT do at the
// gate-free tier, where demoteForPosture makes it advisory. Shared by the
// refusal and the startup line so they cannot disagree.
const yoloCheckerAdvisoryNote = "At yolo a configured checker is observability-only: it has no pre-tool veto, no approve-once human ask, and no fail-closed on checker failure, so a checker outage looks the same as a clean result"

// headlessTrustRefusal is the headless admission gate (ADR 0365 decision 5).
// It runs AFTER resolveTrust, so any legitimate trust source satisfies it, and
// applies only to the trust-naming posture: trust is not auto's or yolo's
// defining increment, so those WARN instead (narrateWithheldTrust).
func headlessTrustRefusal(cfg Config) error {
	if !cfg.Headless || cfg.Posture != PostureTrusted || cfg.TrustProject {
		return nil
	}
	return fmt.Errorf("permission mode %q honours this project's instructions and rules, but a headless root never gains project trust from the permission mode and no trust source is present. Fix it one of three ways: pass --trust-project; add this workspace to trustedWorkspaces in your user-global settings.yaml; or choose a mode that does not name project trust, such as default or auto",
		cfg.resolvedPermissionModeName())
}

// narrateWithheldTrust WARNs when a headless allow-all root starts without
// project trust: it is a legitimate production state (ADR 0095), but the
// operator should see that project instructions and rules are not loaded.
func narrateWithheldTrust(cfg Config) {
	if !cfg.Headless || cfg.Posture < PostureAuto || cfg.TrustProject {
		return
	}
	cfg.diag().Log(context.Background(), port.LevelWarn,
		"permission mode: project trust WITHHELD on this headless root; project instructions and rules are not loaded. Pass --trust-project or add the workspace to trustedWorkspaces to load them",
		"permission_mode", cfg.resolvedPermissionModeName())
}

// resolvedPermissionModeName names the effective pair for messages: the token
// the operator chose, else the token equivalent to the resolved pair, else a
// description of a pair no token names (reachable only via deprecated aliases).
func (cfg Config) resolvedPermissionModeName() string {
	if cfg.permissionModeName != "" {
		return cfg.permissionModeName
	}
	if tok, ok := PermissionModeForPair(cfg.Posture, cfg.DefaultSessionMode); ok {
		return tok.Name
	}
	mode := cfg.DefaultSessionMode
	if mode == "" {
		mode = session.ModeDefault
	}
	return fmt.Sprintf("posture %s with session mode %s", cfg.Posture, mode)
}

// The three checker states the startup line reports (ADR 0365 AC2.6).
const (
	checkerEnforcing = "enforcing"
	checkerAdvisory  = "advisory"
	checkerDisabled  = "disabled"
)

// checkerState reports the guardrails checker as exactly one of three named
// states (ADR 0365 AC2.6), plus the reason when it is disabled.
func checkerState(cfg Config, configured bool) (state, reason string) {
	switch {
	case cfg.GuardrailsDisabled:
		return checkerDisabled, "kill-switch (--guardrails off)"
	case !configured:
		return checkerDisabled, "no checker configured"
	case cfg.Posture >= PostureYolo:
		return checkerAdvisory, "demoted by posture yolo"
	}
	specs, _ := effectiveGuardrailSpecs(cfg)
	if highestSeverityGuardrailMode(specs) == string(modelhook.ModeAdvisory) {
		return checkerAdvisory, "configured advisory"
	}
	return checkerEnforcing, ""
}

// reviewerState reports the subagent ask reviewer for the startup line.
func reviewerState(cfg Config) string {
	switch {
	case cfg.askReviewerDefaultOn:
		return "on (default for headless allow-all)"
	case cfg.SubagentAskReviewerModel != "" && cfg.Interactive:
		return "inert (interactive deployment)"
	case cfg.SubagentAskReviewerModel != "":
		return "on (configured)"
	case cfg.askReviewerOptOut:
		return "off (--subagent-ask-reviewer off)"
	default:
		return "off"
	}
}

// narratePermissionMode is the ONE build-once line reporting the resolved token,
// both halves it set with their lifetimes, and the checker and reviewer states
// as separately scannable items (ADR 0365 AC5.6). The reviewer and the checker
// are different mechanisms and are named separately; neither is described in
// the other's terms.
func narratePermissionMode(cfg Config, checkerConfigured bool) {
	mode := cfg.DefaultSessionMode
	if mode == "" {
		mode = session.ModeDefault
	}
	checker, checkerReason := checkerState(cfg, checkerConfigured)
	attrs := []any{
		"permission_mode", cfg.resolvedPermissionModeName(),
		"posture", cfg.Posture.String(),
		"posture_scope", "process-wide, fixed at startup; cycling the session mode never changes it",
		"session_mode", string(mode),
		"session_mode_scope", "default for new sessions; a session may change it",
		"guardrails_checker", checker,
		"subagent_ask_reviewer", reviewerState(cfg),
	}
	if checkerReason != "" {
		attrs = append(attrs, "guardrails_checker_reason", checkerReason)
	}
	if checker == checkerAdvisory && cfg.Posture >= PostureYolo {
		attrs = append(attrs, "guardrails_checker_note", yoloCheckerAdvisoryNote)
	}
	cfg.diag().Log(context.Background(), port.LevelInfo, "permission mode", attrs...)
	if cfg.Posture == PostureAuto {
		cfg.diag().Log(context.Background(), port.LevelInfo,
			"permission mode auto: subagents KEEP the command-substitution guard the main agent loses, so a subagent's $(...), backtick, or subshell command that cannot be proved read-only is not auto-run. Headless, the subagent ask reviewer adjudicates it instead of denying it (--subagent-ask-reviewer)")
	}
}

// isAskReviewerOptOut reports the explicit --subagent-ask-reviewer off value,
// mirroring the --guardrails off kill-switch.
func isAskReviewerOptOut(s string) bool {
	return strings.EqualFold(strings.TrimSpace(s), "off")
}

// resolveAskReviewerDefault decides whether the headless subagent ask reviewer
// is on by default (ADR 0365 decision 6): headless (no human approver), an
// allow-all posture, no explicit reviewer model, and no explicit opt-out. It
// resolves its model through the existing ask-reviewer slot with the
// parent-model fallback; when neither resolves, behaviour stays today's
// denial and a WARN names the fix.
func resolveAskReviewerDefault(cfg Config) Config {
	if cfg.Interactive || cfg.Posture < PostureAuto || cfg.SubagentAskReviewerModel != "" || cfg.askReviewerOptOut {
		return cfg
	}
	if _, ok := resolveSlotModel(cfg, slotAskReviewer, cfg.Model); !ok && strings.TrimSpace(cfg.Model) == "" {
		cfg.diag().Log(context.Background(), port.LevelWarn,
			"subagent ask reviewer: the headless allow-all default could not resolve a reviewer model (no ask-reviewer slot and no session model), so a subagent's unresolved permission request is denied as before. Set --subagent-ask-reviewer <model> or bind the ask-reviewer model slot")
		return cfg
	}
	cfg.askReviewerDefaultOn = true
	cfg.diag().Log(context.Background(), port.LevelInfo,
		"subagent ask reviewer ON by default (headless allow-all): a model may approve a subagent's permission request that nobody else can answer, and each adjudication spends tokens. Configured Deny and Ask rules still win. Opt out with --subagent-ask-reviewer off")
	return cfg
}
