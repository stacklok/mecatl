package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// permission_mode.go is the COMPOSITION-LAYER named permission-mode vocabulary.
// A token is an operator shorthand that writes EXISTING values: the
// process-wide Posture, the default session PermissionMode
// (server.Config.DefaultMode), and the server ceiling (MaxAllowedMode). The
// table below is the whole mechanism. A token never grants project trust: that
// is a separate decision (--trust-project, trustedWorkspaces, remembered trust).

// PermissionModeToken is one named combination of server ceiling and default
// session mode.
type PermissionModeToken struct {
	// Name is the CLI/YAML spelling.
	Name string
	// Posture is the process-wide posture tier, used for TrustProject floor and
	// for checker/reviewer gating. No longer drives allow-all rule injection
	// (that is now mode-driven in Policy.Evaluate).
	Posture Posture
	// SessionMode is the default mode new sessions start in.
	SessionMode session.PermissionMode
	// MaxMode is the server ceiling: the most-permissive mode sessions on this
	// server are allowed to enter. SetMode rejects a mode above this ceiling.
	MaxMode session.PermissionMode
}

// permissionModeTable is the token contract. 5 modes (plan, default,
// accept-edits, auto, yolo). Redundant products (an accept-edits variant of
// auto or yolo) are deliberately absent: allow-all already permits edits.
// trusted and trusted-accept-edits are dropped as modes; project trust is
// raised via --trust-project combined with any mode.
//
// The strict tiers share one ceiling, accept-edits: plan, default, and
// accept-edits are all reachable on a strict server (the TUI cycles through
// them live), so the ceiling only bounds the allow-all modes.
var permissionModeTable = []PermissionModeToken{
	{Name: "plan", Posture: PostureStrict, SessionMode: session.ModePlan, MaxMode: session.ModeAccept},
	{Name: "default", Posture: PostureStrict, SessionMode: session.ModeDefault, MaxMode: session.ModeAccept},
	{Name: "accept-edits", Posture: PostureStrict, SessionMode: session.ModeAccept, MaxMode: session.ModeAccept},
	// auto and yolo: sessions start in the named mode. The allow-all and
	// loose-substitution behaviors are injected at evaluation time by
	// Policy.Evaluate based on the session's current mode, NOT from Posture.
	{Name: PostureAuto.String(), Posture: PostureAuto, SessionMode: session.ModeAuto, MaxMode: session.ModeAuto},
	{Name: PostureYolo.String(), Posture: PostureYolo, SessionMode: session.ModeYolo, MaxMode: session.ModeYolo},
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
// cfg.Posture, cfg.DefaultSessionMode, and cfg.MaxAllowedMode and marks the
// posture explicit, so the deprecated posture: key cannot override it.
// --trust-project and --yolo still fold MAX-tier on top in resolvePosture,
// exactly as they do over --posture.
func foldPermissionMode(cfg Config) (Config, error) {
	raw := ""
	source := ""
	switch {
	case cfg.PermissionModeFlagSet:
		raw, source = cfg.PermissionMode, "--permission-mode"
	case cfg.PostureFlagSet:
		// Deprecated --posture: set DefaultSessionMode and MaxAllowedMode from the
		// resolved posture for backward compat. The posture is already on cfg.Posture
		// (set by the cmd layer); foldOperatorPosture would no-op since PostureFlagSet.
		cfg.DefaultSessionMode, cfg.MaxAllowedMode = postureToSessionModes(cfg.Posture)
		return foldOperatorPosture(cfg), nil
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
	cfg.MaxAllowedMode = tok.MaxMode
	cfg.permissionModeName = tok.Name
	return cfg, nil
}

// PostureReplacementToken names the --permission-mode token that replaces a
// deprecated posture value, so every root's deprecation warning names the same
// replacement. trusted maps to default: its trust half is now --trust-project.
func PostureReplacementToken(p Posture) string {
	mode, _ := postureToSessionModes(p)
	for _, tok := range permissionModeTable {
		if tok.SessionMode == mode {
			return tok.Name
		}
	}
	return string(session.ModeDefault)
}

// postureToSessionModes maps a Posture to (DefaultSessionMode, MaxAllowedMode)
// for the deprecated --posture flag path, maintaining backward compat.
func postureToSessionModes(p Posture) (session.PermissionMode, session.PermissionMode) {
	switch p {
	case PostureAuto:
		return session.ModeAuto, session.ModeAuto
	case PostureYolo:
		return session.ModeYolo, session.ModeYolo
	default: // PostureStrict, PostureTrusted → default mode, strict ceiling
		return session.ModeDefault, session.ModeAccept
	}
}

// checkerRefusal refuses a deployment whose DEFAULT session mode is auto
// without an enforcing checker (missing, --guardrails off, or advisory-only):
// auto's defining guarantee has no default-mode exemption, and unchecked
// autonomy must be chosen explicitly as yolo. A yolo deployment without a
// checker only WARNs that its sessions cannot enter auto.
func checkerRefusal(cfg Config, checkerConfigured bool) error {
	if cfg.DefaultSessionMode == session.ModeAuto && !autoCheckerEnforcing(cfg, checkerConfigured) {
		state, reason := checkerState(cfg, checkerConfigured)
		if state == checkerEnforcing {
			state, reason = checkerAdvisory, "configured advisory"
		}
		return fmt.Errorf("permission mode %q requires an enforcing guardrails checker, but the checker is %s (%s). Fix it by setting a checker with --guardrails-model <model> or binding the guardrail model slot (in enforcing mode), or choose yolo for autonomous operation without a preventive checker",
			cfg.resolvedPermissionModeName(), state, reason)
	}
	if cfg.MaxAllowedMode == session.ModeYolo && !autoCheckerEnforcing(cfg, checkerConfigured) {
		cfg.diag().Log(context.Background(), port.LevelWarn,
			fmt.Sprintf("permission mode %q: no enforcing guardrails checker, so sessions cannot enter auto; configure one with --guardrails-model <model> to allow it", cfg.resolvedPermissionModeName()))
	}
	return nil
}

// yoloCheckerAdvisoryNote states what a configured checker does NOT do at the
// gate-free tier, where demoteForPosture makes it advisory. Shared by the
// refusal and the startup line so they cannot disagree.
const yoloCheckerAdvisoryNote = "In yolo-mode sessions a configured checker is observability-only: it has no pre-tool veto, no approve-once human ask, and no fail-closed on checker failure, so a checker outage looks the same as a clean result. Sessions switched to another mode, including auto, keep it enforcing"

// headlessTrustRefusal is the headless admission gate.
// It runs AFTER resolveTrust, so any legitimate trust source satisfies it, and
// applies only to the trust-raising posture: trust is not auto's or yolo's
// defining increment, so those WARN instead (narrateWithheldTrust).
func headlessTrustRefusal(cfg Config) error {
	if !cfg.Headless || cfg.Posture != PostureTrusted || cfg.TrustProject {
		return nil
	}
	return fmt.Errorf("the trust-raising posture requires a trust source on a headless root, but none is present. Fix it one of three ways: pass --trust-project; add this workspace to trustedWorkspaces in your user-global settings.yaml; or choose a mode that does not raise project trust, such as default or auto")
}

// narrateWithheldTrust WARNs when a headless allow-all root starts without
// project trust: it is a legitimate production state, but the
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

// The three checker states the startup line reports.
const (
	checkerEnforcing = "enforcing"
	checkerAdvisory  = "advisory"
	checkerDisabled  = "disabled"
)

// checkerState reports the guardrails checker as exactly one of three named
// states, plus the reason when it is disabled.
func checkerState(cfg Config, configured bool) (state, reason string) {
	switch {
	case cfg.GuardrailsDisabled:
		return checkerDisabled, "kill-switch (--guardrails off)"
	case !configured:
		return checkerDisabled, "no checker configured"
	}
	compiled, enforcing := compiledGuardrailEnforcement(cfg)
	switch {
	case !compiled:
		return checkerDisabled, noGuardrailRuleCompiled
	case cfg.MaxAllowedMode == session.ModeYolo:
		return checkerAdvisory, "demoted in yolo-mode sessions; other modes keep it enforcing"
	case !enforcing:
		return checkerAdvisory, "configured advisory"
	}
	return checkerEnforcing, ""
}

// noGuardrailRuleCompiled is the disabled reason when every configured rule
// fails compilation, so no reviewer is installed at runtime.
const noGuardrailRuleCompiled = "no guardrail rule compiled; every configured rule has an invalid match, mode, or phases"

// compiledGuardrailEnforcement compiles the effective rules exactly as the
// installed reviewer does (buildGuardrailsActionReviewer) and reports whether
// any rule survived and whether a surviving rule enforces. Raw specs are not
// enough: settings decoding accepts matchers and phases CompileRule rejects,
// no reviewer is installed when nothing compiles, and CompileRule normalizes
// the mode spelling. The per-rule WARNs stay with the engine build.
func compiledGuardrailEnforcement(cfg Config) (compiled, enforcing bool) {
	cfg.Diagnostics = port.NopDiagnostics{}
	specs, _ := effectiveGuardrailSpecs(cfg)
	rules, ok := compileGuardrailRules(cfg, specs)
	if !ok {
		return false, false
	}
	for _, rule := range rules {
		if !rule.Advisory() {
			return true, true
		}
	}
	return true, false
}

// autoCheckerEnforcing reports whether an auto session runs with an enforcing
// checker: a configured checker plus at least one compiled enforcing rule, the
// same condition under which the runtime installs an enforcing reviewer.
// Unlike checkerState it ignores the yolo demotion, which applies per session
// mode (guardrailsDemoted): an auto session on a yolo server still compiles its
// rules enforcing.
func autoCheckerEnforcing(cfg Config, configured bool) bool {
	if cfg.GuardrailsDisabled || !configured {
		return false
	}
	cfg.guardrailSessionMode = session.ModeAuto
	_, enforcing := compiledGuardrailEnforcement(cfg)
	return enforcing
}

// Subagent ask reviewer states: the stable vocabulary of the
// ServerCapabilities.subagent_ask_reviewer echo and the startup line.
const (
	reviewerOn    = "on"
	reviewerOff   = "off"
	reviewerInert = "inert"
)

// reviewerStatus reports the subagent ask reviewer as one stable state plus a
// human-readable reason (empty when there is none). It is the single source
// for both the startup line and the capabilities echo.
func reviewerStatus(cfg Config) (state, reason string) {
	switch {
	case cfg.askReviewerDefaultOn:
		return reviewerOn, "default for headless allow-all"
	case cfg.SubagentAskReviewerModel != "" && cfg.Interactive:
		return reviewerInert, "interactive deployment"
	case cfg.SubagentAskReviewerModel != "":
		return reviewerOn, "configured"
	case cfg.askReviewerOptOut:
		return reviewerOff, "--subagent-ask-reviewer off"
	default:
		return reviewerOff, ""
	}
}

// subagentAskReviewerCapability is the reviewer state for the capabilities
// echo; the reason stays in diagnostics only.
func subagentAskReviewerCapability(cfg Config) *string {
	state, _ := reviewerStatus(cfg)
	return &state
}

// reviewerState reports the subagent ask reviewer for the startup line.
func reviewerState(cfg Config) string {
	state, reason := reviewerStatus(cfg)
	if reason == "" {
		return state
	}
	return state + " (" + reason + ")"
}

// narratePermissionMode is the ONE build-once line reporting the resolved token,
// both halves it set with their lifetimes, and the checker and reviewer states
// as separately scannable items. The reviewer and the checker
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
		"posture_scope", "process-wide, fixed at startup; sets TrustProject floor and checker/reviewer gating",
		"session_mode", string(mode),
		"session_mode_scope", "default for new sessions; a session may change it up to the server ceiling",
		"max_allowed_mode", string(cfg.MaxAllowedMode),
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
			"permission mode auto: subagents KEEP the command-substitution guard the main agent loses at auto mode, so a subagent's $(...), backtick, or subshell command that cannot be proved read-only is not auto-run. Headless, the subagent ask reviewer adjudicates it instead of denying it (--subagent-ask-reviewer)")
	}
}

// isAskReviewerOptOut reports the explicit --subagent-ask-reviewer off value,
// mirroring the --guardrails off kill-switch.
func isAskReviewerOptOut(s string) bool {
	return strings.EqualFold(strings.TrimSpace(s), "off")
}

// resolveAskReviewerDefault decides whether the headless subagent ask reviewer
// is on by default: headless (no human approver), an
// allow-all mode, no explicit reviewer model, and no explicit opt-out. It
// resolves its model through the existing ask-reviewer slot with the
// parent-model fallback; when neither resolves, behaviour stays today's
// denial and a WARN names the fix.
func resolveAskReviewerDefault(cfg Config) Config {
	isAllowAll := cfg.MaxAllowedMode == session.ModeAuto || cfg.MaxAllowedMode == session.ModeYolo
	if cfg.Interactive || !isAllowAll || cfg.SubagentAskReviewerModel != "" || cfg.askReviewerOptOut {
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
