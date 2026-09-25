package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/modelhook"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const (
	editToolName    = "Edit"
	listDirToolName = "ListDir"
	readToolName    = "Read"
	writeToolName   = "Write"
)

// foldOperatorGuardrails merges the OPERATOR-TIER `guardrails:` YAML subtree (read by
// the permconfig resolver from the user-global + CLI tiers ONLY — never the project
// file) onto cfg. CLI flags out-rank YAML for the scalar knobs that have a flag
// (GuardrailsModel from --guardrails-model, GuardrailsDisabled from --guardrails=off);
// the rule list comes from YAML (a flag cannot express it). It is a no-op when no
// operator-tier guardrails block was configured. cfg is taken and returned by value
// (Build holds a local cfg).
func foldOperatorGuardrails(cfg Config) Config {
	res, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok || res == nil {
		return cfg
	}
	g := res.OperatorGuardrails()
	if g == nil {
		return cfg
	}
	// Model: a CLI --guardrails-model wins; else adopt the YAML model.
	if strings.TrimSpace(cfg.GuardrailsModel) == "" {
		cfg.GuardrailsModel = strings.TrimSpace(g.Model)
	}
	// Disabled: OR the YAML kill-switch with the CLI one (either disables).
	if g.Disabled {
		cfg.GuardrailsDisabled = true
	}
	// OnCheckerDown: YAML supplies it (no flag); empty = fail (the safe default).
	if cfg.GuardrailsOnCheckerDown == "" {
		cfg.GuardrailsOnCheckerDown = strings.TrimSpace(g.OnCheckerDown)
	}
	if cfg.GuardrailsDefaultMode == "" {
		cfg.GuardrailsDefaultMode = strings.TrimSpace(g.DefaultMode)
	}
	if cfg.GuardrailsTaskWindow == 0 {
		cfg.GuardrailsTaskWindow = g.TaskWindow
	}
	cfg.GuardrailsTaskWindow = clampReviewTaskWindow(cfg.GuardrailsTaskWindow)
	// Escape knob (ADR 0080): YAML-only (no flag); OR-folded like Disabled.
	if g.Escape {
		cfg.GuardrailsEscape = true
	}
	// Rules: YAML is the sole source. Map the on-disk specs to app.GuardrailRule.
	if len(cfg.GuardrailsRules) == 0 && len(g.Rules) > 0 {
		rules := make([]GuardrailRule, 0, len(g.Rules))
		for _, r := range g.Rules {
			rules = append(rules, GuardrailRule{
				Match:         r.Match,
				Phases:        r.Phases,
				Mode:          r.Mode,
				Prompt:        r.Prompt,
				FailClosed:    r.FailClosed,
				FailClosedSet: r.FailClosedPresent,
			})
		}
		cfg.GuardrailsRules = rules
	}
	return cfg
}

func clampReviewTaskWindow(value int) int {
	if value < 1 {
		return 1
	}
	if value > 3 {
		return 3
	}
	return value
}

// guardrails.go is the composition wiring for contextual action/inbound review.
// It builds one engine-backed ToolReviewer, compiles operator-tier rules, and
// injects the reviewer directly at the engine's exact call/result choke points.
// Ordinary HookRunner behavior stays generic and unwrapped.
//
// The checker engine is built through childEngineDepsForProvider with inert hooks,
// no nested reviewer, and only the two run-scoped review tools.

// engineGuardrailsChecker adapts the contextual ToolReviewer only for the narrow
// permission escape route, whose existing adapter-local seam consumes a
// VerdictChecker.
type engineGuardrailsChecker struct {
	reviewer agent.ToolReviewer
}

func (c engineGuardrailsChecker) Check(ctx context.Context, req modelhook.CheckRequest) (modelhook.CheckResult, error) {
	if c.reviewer == nil {
		return modelhook.CheckResult{}, errGuardrailVerdictUnparseable
	}
	job := agent.ReviewJobInbound
	phase := governance.PhasePostToolUse
	if req.Phase == modelhook.PhasePre {
		job = agent.ReviewJobAction
		phase = governance.PhasePreToolUse
	}
	event := governance.HookEvent{Phase: phase, Tool: req.Tool, Input: []byte(req.Content), SessionID: "escape-review", CallID: "escape-call"}
	result, usage, err := c.reviewer.Review(ctx, agent.ToolReviewRequest{
		ReviewID:               "escape-review",
		Job:                    job,
		Event:                  event,
		EffectiveCall:          session.NewToolCall("escape-call", req.Tool, []byte(req.Content)),
		PrincipalFactsComplete: true,
		Caller:                 agent.ReviewCaller{Role: "main", Capabilities: []string{req.Tool}},
		EvidenceComplete:       true,
		TrajectoryComplete:     true,
	}, nil)
	if err != nil {
		return modelhook.CheckResult{Usage: usage}, err
	}
	safe := result.Assessment == agent.ReviewAcceptable
	if result.Assessment == agent.ReviewUnresolved {
		return modelhook.CheckResult{Usage: usage}, errGuardrailVerdictUnparseable
	}
	reason := ""
	if !safe {
		reason = "contextual guardrail finding"
	}
	return modelhook.CheckResult{Verdict: modelhook.Verdict{Safe: &safe, Reason: reason}, Usage: usage}, nil
}

// errGuardrailVerdictUnparseable is the sentinel used by the narrow escape
// checker when contextual review cannot produce a complete decision.
var errGuardrailVerdictUnparseable = guardrailError("guardrail checker verdict unparseable or ambiguous")

func guardrailFailClosed(value string) bool {
	return !strings.EqualFold(strings.TrimSpace(value), "warn")
}

type guardrailError string

func (e guardrailError) Error() string { return string(e) }

type guardrailRouteHealth struct {
	mu         sync.RWMutex
	seen       bool
	inspection string
	assessment string
	reasonCode agent.ReviewFailureCode
}

func (h *guardrailRouteHealth) record(result agent.ToolReviewResult, err error) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen = true
	if err != nil {
		h.inspection, h.assessment = "operational_failure", ""
		h.reasonCode = agent.ReviewFailureProviderFailure
		var classified agent.GuardrailReviewFailure
		if errors.As(err, &classified) {
			switch code := classified.GuardrailReviewFailureCode(); code {
			case agent.ReviewFailureProviderFailure, agent.ReviewFailureTimeout, agent.ReviewFailureBlankAssessment,
				agent.ReviewFailureMalformedAssessment, agent.ReviewFailureInvalidAssessment,
				agent.ReviewFailureMissingSubmit, agent.ReviewFailureEvidenceFailure:
				h.reasonCode = code
			}
		}
		return
	}
	h.inspection, h.assessment, h.reasonCode = "complete", string(result.Assessment), ""
}

func (h *guardrailRouteHealth) snapshot() (bool, string, string, agent.ReviewFailureCode) {
	if h == nil {
		return false, "", "", ""
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.seen, h.inspection, h.assessment, h.reasonCode
}

type guardrailActionReviewer struct {
	base                agent.ToolReviewer
	rules               []modelhook.CompiledRule
	failClosed          bool
	grants              *modelhook.WaiverHolder
	providerID, modelID string
	ruleOrigin          string
	health              *guardrailRouteHealth
}

// GuardrailReviewPolicy implements agent.ReviewPolicyProvider so configured
// matching and enforcement remain explicit on the primary reviewer.
func (r *guardrailActionReviewer) GuardrailReviewPolicy(toolName string, job agent.ReviewJob, operationalFailure bool) (applies, enforce bool) {
	phase := modelhook.PhasePre
	if job == agent.ReviewJobInbound {
		phase = modelhook.PhasePost
	}
	rule, matched := modelhook.ResolveRule(r.rules, toolName, phase)
	if !matched {
		return false, false
	}
	if rule.Advisory() {
		return true, false
	}
	if operationalFailure {
		return true, rule.FailClosed(r.failClosed)
	}
	return true, true
}

func (r *guardrailActionReviewer) GuardrailPermissionReviewEligible(call session.ToolCall) bool {
	if call.Name != tool.ShellToolName {
		return false
	}
	rule, matched := modelhook.ResolveRule(r.rules, call.Name, modelhook.PhasePre)
	return matched && !rule.Advisory() && !rule.SkipAction(call.Name, string(call.Args))
}

func (r *guardrailActionReviewer) GuardrailReviewMetadata(toolName string, job agent.ReviewJob) (ruleID, ruleOrigin, providerID, modelID string) {
	phase := modelhook.PhasePre
	if job == agent.ReviewJobInbound {
		phase = modelhook.PhasePost
	}
	rule, matched := modelhook.ResolveRule(r.rules, toolName, phase)
	if matched {
		ruleID = rule.Match()
	}
	ruleOrigin = r.ruleOrigin
	return ruleID, ruleOrigin, r.providerID, r.modelID
}

func (r *guardrailActionReviewer) RecordGuardrailReviewFailure(result agent.ToolReviewResult, err error) {
	r.health.record(result, err)
}

func (r *guardrailActionReviewer) Review(ctx context.Context, req agent.ToolReviewRequest, source agent.ReviewEvidenceSource) (agent.ToolReviewResult, session.AuxiliaryUsage, error) {
	phase := modelhook.PhasePre
	if req.Job == agent.ReviewJobInbound {
		phase = modelhook.PhasePost
	}
	rule, matched := modelhook.ResolveRule(r.rules, req.EffectiveCall.Name, phase)
	if !matched || (phase == modelhook.PhasePre && rule.SkipAction(req.EffectiveCall.Name, string(req.EffectiveCall.Args))) {
		return agent.ToolReviewResult{Assessment: agent.ReviewAcceptable}, session.AuxiliaryUsage{}, nil
	}
	if prompt := strings.TrimSpace(rule.Prompt()); prompt != "" {
		req.PrincipalFacts = append(req.PrincipalFacts, agent.ReviewPrincipalFact{Kind: "operator_task_risk_policy", Ref: "operator-policy", Statement: prompt})
	}
	result, usage, err := r.base.Review(ctx, req, source)
	r.health.record(result, err)
	return result, usage, err
}

func (r *guardrailActionReviewer) GrantDigest(req agent.ToolReviewRequest) (string, bool) {
	if r.grants == nil || !req.TrajectoryComplete || req.Target.Kind != "workspace" || req.EffectiveCall.Name == tool.ShellToolName {
		return "", false
	}
	for _, fact := range req.PrincipalFacts {
		if fact.Kind == "repeat_dependency_incomplete" {
			return "", false
		}
	}
	isolation := byte(0)
	if req.Caller.Isolated {
		isolation = 1
	}
	complete := byte(0)
	if req.PrincipalFactsComplete {
		complete = 1
	}
	parts := [][]byte{
		[]byte(req.Event.SessionID), []byte(req.Environment.Kind), []byte(req.Environment.ID), []byte(req.Environment.Revision),
		[]byte(req.Caller.Role), {isolation}, {complete}, []byte(req.EffectiveCall.Name), req.EffectiveCall.Args,
		[]byte(req.Target.Kind), []byte(req.Target.Display), []byte(req.Target.DestinationID),
	}
	caps := append([]string(nil), req.Caller.Capabilities...)
	sort.Strings(caps)
	for _, capability := range caps {
		parts = append(parts, []byte(capability))
	}
	for _, fact := range req.PrincipalFacts {
		positive := byte(0)
		if fact.PositiveVerdict {
			positive = 1
		}
		parts = append(parts, []byte(fact.Kind), []byte(fact.Ref), []byte(fact.Statement), []byte{positive})
	}
	digest := r.grants.Digest(parts...)
	if !r.grants.AllowsDigest(digest) && !r.grants.CanArm(req.Event.SessionID) {
		return "", false
	}
	return digest, true
}

func (r *guardrailActionReviewer) AllowsGrant(digest string) bool {
	return r.grants.AllowsDigest(digest)
}
func (r *guardrailActionReviewer) ArmGrant(digest, sessionID string) {
	r.grants.ArmDigestForSession(sessionID, digest)
}

func buildGuardrailsActionReviewer(cfg Config, provReg *providerRegistry, provider port.LLMProvider, parentProviderID string, grants *modelhook.WaiverHolder) agent.ToolReviewer {
	base := buildGuardrailsReviewer(cfg, provReg, provider, parentProviderID)
	if base == nil {
		return nil
	}
	specs, _ := effectiveGuardrailSpecs(cfg)
	rules, ok := compileGuardrailRules(cfg, specs)
	if !ok {
		return nil
	}
	origin := "default"
	if len(cfg.GuardrailsRules) > 0 {
		origin = "operator"
	}
	providerID, modelID := cfg.guardrailProviderID, cfg.guardrailModel
	if routed, ok := base.(interface{ GuardrailCheckerRoute() (string, string) }); ok {
		providerID, modelID = routed.GuardrailCheckerRoute()
	}
	return &guardrailActionReviewer{base: base, rules: rules, failClosed: guardrailFailClosed(cfg.GuardrailsOnCheckerDown), grants: grants, providerID: providerID, modelID: modelID, ruleOrigin: origin, health: cfg.guardrailHealth}
}

func attachGuardrailReviewer(deps *agent.Deps, reviewer agent.ToolReviewer, details agent.ReviewDetailSink) {
	deps.ToolReviewer = reviewer
	if preparer, ok := reviewer.(agent.ReviewEvidencePreparer); ok {
		deps.ReviewEvidencePreparer = preparer
	}
	deps.ReviewDetails = details
}

func guardrailCoverageFor(cfg Config, sess *session.Session) server.GuardrailCoverage {
	coverage := server.GuardrailCoverage{
		Enabled:           cfg.guardrailConfigured && !cfg.GuardrailsDisabled,
		CheckerProviderID: cfg.guardrailProviderID,
		CheckerModelID:    cfg.guardrailModel,
	}
	if !coverage.Enabled || sess == nil {
		return coverage
	}
	specs, _ := effectiveGuardrailSpecs(cfg)
	rules, ok := compileGuardrailRules(cfg, specs)
	if !ok {
		coverage.Enabled = false
		return coverage
	}
	origin := "operator"
	if len(cfg.GuardrailsRules) == 0 {
		origin = "default"
	}
	authority, bound := sess.BoundAuthority()
	if !bound {
		return coverage
	}
	seen, inspection, assessment, reasonCode := cfg.guardrailHealth.snapshot()
	tools := append([]string(nil), authority.CapabilitySet.Tools...)
	sort.Strings(tools)
	for _, toolName := range tools {
		for _, phase := range []modelhook.Phase{modelhook.PhasePre, modelhook.PhasePost} {
			rule, matched := modelhook.ResolveRule(rules, toolName, phase)
			if !matched {
				continue
			}
			job := string(agent.ReviewJobAction)
			if phase == modelhook.PhasePost {
				job = string(agent.ReviewJobInbound)
			}
			statusReason := "checker route has not yet completed an inspection"
			if seen && inspection == "operational_failure" {
				statusReason = "the checker route's latest inspection attempt failed: " + string(reasonCode) + "; no unsafe finding was inferred"
			} else if seen {
				statusReason = "checker route operational; last completed assessment: " + assessment
			}
			coverage.Entries = append(coverage.Entries, server.GuardrailCoverageEntry{
				Tool: toolName, Phase: string(phase), Job: job, Mode: string(rule.Mode()),
				RuleID: rule.Match(), RuleOrigin: origin, Inspection: inspection,
				Reason: statusReason,
			})
		}
	}
	return coverage
}

// defaultGuardrailSpecs is the built-in BLOCK rule set applied when a guardrails
// model is configured but the operator authored no explicit rules. Enabling
// guardrails is the opt-in to spend — the default posture is enforcement (block),
// not observe-only. Advisory is available via the defaultMode key or an explicit
// rule list. The list covers action and inbound review across local, web, MCP,
// and delegation boundaries; SkipReadOnlyShell avoids action review only for a
// positively classified read-only command.
var defaultGuardrailSpecs = []modelhook.RuleSpec{
	{Match: "WebSearch", Phases: []string{string(modelhook.PhasePre), string(modelhook.PhasePost)}, Mode: string(modelhook.ModeBlock)},
	{Match: "WebFetch", Phases: []string{string(modelhook.PhasePre), string(modelhook.PhasePost)}, Mode: string(modelhook.ModeBlock)},
	{Match: "FetchMcpResource", Phases: []string{string(modelhook.PhasePost)}, Mode: string(modelhook.ModeBlock)},
	{Match: "CallMcpWithQuery", Phases: []string{string(modelhook.PhasePre), string(modelhook.PhasePost)}, Mode: string(modelhook.ModeBlock)},
	{Match: "mcp__*", Phases: []string{string(modelhook.PhasePre), string(modelhook.PhasePost)}, Mode: string(modelhook.ModeBlock)},
	{Match: "Shell", Phases: []string{string(modelhook.PhasePre), string(modelhook.PhasePost)}, Mode: string(modelhook.ModeBlock), SkipReadOnlyShell: true, Prompt: modelhook.DefaultShellPrePrompt},
	{Match: readToolName, Phases: []string{string(modelhook.PhasePost)}, Mode: string(modelhook.ModeBlock)},
	{Match: listDirToolName, Phases: []string{string(modelhook.PhasePost)}, Mode: string(modelhook.ModeBlock)},
	{Match: "Grep", Phases: []string{string(modelhook.PhasePost)}, Mode: string(modelhook.ModeBlock)},
	{Match: "Glob", Phases: []string{string(modelhook.PhasePost)}, Mode: string(modelhook.ModeBlock)},
	{Match: editToolName, Phases: []string{string(modelhook.PhasePre)}, Mode: string(modelhook.ModeBlock)},
	{Match: writeToolName, Phases: []string{string(modelhook.PhasePre)}, Mode: string(modelhook.ModeBlock)},
	{Match: "Copy", Phases: []string{string(modelhook.PhasePre)}, Mode: string(modelhook.ModeBlock)},
	{Match: "Move", Phases: []string{string(modelhook.PhasePre)}, Mode: string(modelhook.ModeBlock)},
	{Match: "Remove", Phases: []string{string(modelhook.PhasePre)}, Mode: string(modelhook.ModeBlock)},
	{Match: "Subagent", Phases: []string{string(modelhook.PhasePre)}, Mode: string(modelhook.ModeBlock)},
	{Match: "Parallel", Phases: []string{string(modelhook.PhasePre)}, Mode: string(modelhook.ModeBlock)},
	{Match: "Team", Phases: []string{string(modelhook.PhasePre)}, Mode: string(modelhook.ModeBlock)},
}

// effectiveGuardrailSpecs returns the rule specs to compile: the operator's explicit
// rules when any are configured, else the built-in default BLOCK set (ADR 0060).
// usedDefaults reports which, so the posture line (logGuardrailsPosture) can annotate
// "default set" only when the defaults are in force.
//
// Posture-coupling (ADR 0062, sub-decision B): under posture YOLO ONLY (the
// truly-off, gate-free tier that maps to Claude Code's bypassPermissions) ALL
// guardrail rule modes are DEMOTED to advisory (observe-only) by demoteForPosture —
// it never blocks or asks, it only logs + emits an EvHook. strict/trusted/AUTO keep
// ENFORCING: under auto the approve-once ask IS the auto-mode behaviour (the checker
// blocks, an interactive human allows once — CC auto-mode parity), so demoting auto
// would remove that very behaviour. The interactive approve-once path is gated on
// Deps.Interactive, NOT on posture. This is composition-only (no engine change) and
// posture is operator-tier, consistent with guardrails being operator-tier (no
// project-tier downgrade).
func effectiveGuardrailSpecs(cfg Config) (specs []modelhook.RuleSpec, usedDefaults bool) {
	if len(cfg.GuardrailsRules) > 0 {
		out := make([]modelhook.RuleSpec, 0, len(cfg.GuardrailsRules))
		for i, gr := range cfg.GuardrailsRules {
			out = append(out, modelhook.RuleSpec{
				Match:         gr.Match,
				Phases:        gr.Phases,
				Mode:          demoteForPosture(cfg, gr.Mode),
				Prompt:        gr.Prompt,
				FailClosed:    gr.FailClosed,
				FailClosedSet: gr.FailClosedSet,
				Order:         i,
			})
		}
		return out, false
	}
	// No explicit rules: ship the default rule set (the model being configured is
	// the opt-in). Apply the operator's defaultMode override if set; else the
	// built-in block default. Copy with Order stamped so the matcher tiebreak is
	// deterministic.
	out := make([]modelhook.RuleSpec, len(defaultGuardrailSpecs))
	for i, s := range defaultGuardrailSpecs {
		s.Order = i
		if dm := strings.TrimSpace(cfg.GuardrailsDefaultMode); dm != "" {
			s.Mode = dm
		}
		s.Mode = demoteForPosture(cfg, s.Mode)
		out[i] = s
	}
	return out, true
}

// demoteForPosture demotes the enforcing guardrail block mode to advisory
// under posture YOLO ONLY (ADR 0062, sub-decision B; CC bypassPermissions parity).
// strict/trusted/auto keep the configured mode — under auto the approve-once ask IS
// the enforcement behaviour. It is the SINGLE posture→mode coupling point so the
// default-set and operator-rule branches cannot drift.
func demoteForPosture(cfg Config, mode string) string {
	if cfg.Posture < PostureYolo {
		return mode
	}
	return string(modelhook.ModeAdvisory)
}

// guardrailsConfigured reports whether guardrails are switched on: a checker model
// is configured — via --guardrails-model OR a bound `guardrail` model slot (ADR 0046,
// configure = enable, the router-parity model of ADR 0042) — AND the master kill-switch
// is not set. A model with NO explicit rules is still ON — it takes the default BLOCK
// rule set (effectiveGuardrailSpecs, ADR 0060; the model being configured is the opt-in
// to spend). The kill-switch (--guardrails=off → GuardrailsDisabled) wins over any
// config. Resolution precedence is unchanged: a bound slot SUPERSEDES the gate value's
// model (see resolveGuardrailsCheckerModel).
func guardrailsConfigured(cfg Config) bool {
	if cfg.GuardrailsDisabled {
		return false
	}
	return cfg.GuardrailSlot != nil || cfg.GuardrailsModel != "" || selectorForSlot(cfg, slotGuardrail) != ""
}

// guardrailSource is the provenance of the resolved checker model — the single axis the
// build-once posture line (logGuardrailsPosture) narrates alongside the resolved id. It
// is the composition source of truth for "where did the checker model come from", shared
// by buildGuardrailsChecker (which only needs the resolved id) and the posture line.
type guardrailSource int

const (
	// srcNone: no checker model resolves (guardrails OFF — nothing configured, or the
	// configured slot/gate is unresolvable AND there is no usable literal).
	srcNone guardrailSource = iota
	// srcGate: the checker model came from the gate value (--guardrails-model / the
	// guardrails.model YAML), with no slot superseding it.
	srcGate
	// srcSlot: the checker model came from a bound `guardrail` model slot (incl. its
	// cheap-tier fallthrough), with no non-empty gate value differing from it.
	srcSlot
	// srcSlotSupersedingGate: a bound `guardrail` slot resolved AND a non-empty gate
	// value (--guardrails-model) is present but differs from the slot's resolved model —
	// the slot won, the gate value is inert for routing. The posture line names both so
	// an operator who set both sees the precedence honestly.
	srcSlotSupersedingGate
)

// resolveGuardrailsCheckerModel is the SINGLE source of truth for the resolved guardrail
// checker model + its provenance. It is PURE (no diagnostics, no provider) so the
// build-once posture line (logGuardrailsPosture) and the per-session checker builder
// (buildGuardrailsChecker) read the SAME resolution and cannot drift. Precedence mirrors
// the pre-#46 buildGuardrailsChecker exactly:
//
//  1. slot: resolveSlotModel(cfg, slotGuardrail, "") → if ok, model = that; src = srcSlot
//     (or srcSlotSupersedingGate when cfg.GuardrailsModel != "" AND differs from the
//     slot's resolved model — a same-id gate value stays srcSlot, no "superseding").
//  2. else gate: sel := cfg.GuardrailsModel; resolved, _ := lookupModelAlias(cfg, sel);
//     src = srcGate. Under UseMock an unresolved gate value passes through as the literal
//     sel verbatim (matching the old buildGuardrailsChecker:245-251 fail-soft).
//  3. else nothing: model = "", src = srcNone.
//
// configured = src != srcNone. A slot that is bound but unresolvable falls through to the
// gate value (today's fail-soft — a broken slot never wedges the checker).
func resolveGuardrailBinding(cfg Config, reg *providerRegistry) (providerID, model string, src guardrailSource, configured bool, err error) {
	if cfg.GuardrailsDisabled {
		return "", "", srcNone, false, nil
	}
	var selector string
	if cfg.GuardrailSlot != nil {
		providerID = strings.TrimSpace(cfg.GuardrailSlot.ProviderID)
		selector = strings.TrimSpace(cfg.GuardrailSlot.Model)
		src = srcSlot
	} else if selector = selectorForSlot(cfg, slotGuardrail); selector != "" {
		providerID = reg.Default()
		src = srcSlot
	} else if selector = strings.TrimSpace(cfg.GuardrailsModel); selector != "" {
		providerID = reg.Default()
		src = srcGate
	} else {
		return "", "", srcNone, false, nil
	}
	model, known := lookupModelAlias(cfg, selector)
	if (!known || model == "") && cfg.UseMock {
		model = selector
	}
	if !known && !cfg.UseMock {
		return "", "", srcNone, false, fmt.Errorf("guardrail model selector %q is not resolvable", selector)
	}
	if model == "" {
		return "", "", srcNone, false, fmt.Errorf("guardrail model selector %q resolves to inherit", selector)
	}
	if providerID == "" {
		return "", "", srcNone, false, fmt.Errorf("guardrail provider is empty")
	}
	if _, ok := reg.Lookup(providerID); !ok {
		return "", "", srcNone, false, fmt.Errorf("guardrail provider %q is not configured", providerID)
	}
	if src == srcSlot {
		gate := strings.TrimSpace(cfg.GuardrailsModel)
		if gate != "" {
			gateModel, _ := lookupModelAlias(cfg, gate)
			if gateModel == "" && cfg.UseMock {
				gateModel = gate
			}
			if gateModel != model {
				src = srcSlotSupersedingGate
			}
		}
	}
	return providerID, model, src, true, nil
}

// resolveGuardrailsCheckerModel is the legacy pure projection retained for helper tests.
func resolveGuardrailsCheckerModel(cfg Config) (model string, src guardrailSource, configured bool) {
	if gm, ok := resolveSlotModel(cfg, slotGuardrail, ""); ok && gm != "" {
		src = srcSlot
		if g := strings.TrimSpace(cfg.GuardrailsModel); g != "" && g != gm {
			src = srcSlotSupersedingGate
		}
		return gm, src, true
	}
	sel := strings.TrimSpace(cfg.GuardrailsModel)
	if sel == "" {
		return "", srcNone, false
	}
	resolved, _ := lookupModelAlias(cfg, sel)
	if resolved == "" {
		// UseMock (or a bare-token passthrough) takes the literal verbatim; an empty
		// resolve here is the defensive fail-soft path (Build normalized the value
		// fail-fast for non-mock).
		resolved = sel
	}
	if resolved == "" {
		return "", srcNone, false
	}
	return resolved, srcGate, true
}

// buildGuardrailsReviewer constructs the one contextual investigative reviewer
// bound to the Build-captured checker route. Its catalog is empty; only the two
// run-scoped evidence protocol tools are visible during Review.
func buildGuardrailsReviewer(cfg Config, provReg *providerRegistry, provider port.LLMProvider, parentProviderID string) agent.ToolReviewer {
	deps, ok := guardrailsCheckerDeps(cfg, provReg, provider, parentProviderID, "")
	if !ok {
		return nil
	}
	return newContextualToolReviewer(agent.NewEngine(deps), deps.ProviderModel.ProviderID, deps.ProviderModel.ModelID, cfg.diag())
}

func guardrailsCheckerDeps(cfg Config, provReg *providerRegistry, provider port.LLMProvider, parentProviderID, _ string) (agent.Deps, bool) {
	var providerID, resolved string
	var configured bool
	if cfg.guardrailConfigured {
		providerID, resolved, configured = cfg.guardrailProviderID, cfg.guardrailModel, true
	} else {
		resolved, _, configured = resolveGuardrailsCheckerModel(cfg)
		providerID = parentProviderID
	}
	if !configured {
		return agent.Deps{}, false
	}
	if provReg != nil && cfg.guardrailConfigured {
		entry, ok := provReg.Lookup(providerID)
		if !ok {
			return agent.Deps{}, false
		}
		provider = entry.provider
	}
	if provider == nil {
		return agent.Deps{}, false
	}
	windowFn := childWindowFor(cfg, provReg, providerID, resolved)
	pc := promptConfig(modelCfgFor(cfg, resolved), cfg.gitStatus)
	pc.Role = contextualReviewerSystemPrompt
	providerModel := session.ProviderModelID{ProviderID: providerID, ModelID: resolved}
	deps := childEngineDepsForProvider(cfg, "guardrail-reviewer", provider, providerModel, windowFn,
		tool.NewCatalog(), pc, nil)
	deps.MaxNoProgressNudges = -1
	deps.ToolReviewer = nil
	return deps, true
}

// buildGuardrailsChecker is the compatibility shape consumed by the narrow
// escape-policy seam; it delegates to the same contextual reviewer.
func buildGuardrailsChecker(cfg Config, provReg *providerRegistry, provider port.LLMProvider, parentProviderID string, _ string) modelhook.VerdictChecker {
	reviewer := buildGuardrailsReviewer(cfg, provReg, provider, parentProviderID)
	if reviewer == nil {
		return nil
	}
	return engineGuardrailsChecker{reviewer: reviewer}
}

// buildGuardrailsEscapeChecker builds the ADR-0080 escape route's checker, or
// nil when the knob is off or guardrails are unconfigured (the byte-identical
// no-route posture). It reuses the SAME engine-backed VerdictChecker as the
// hook-path Runner — the recursion guard (a tool-less one-turn engine that
// fires no hooks) and the operator-tier-only config carry over unchanged. The
// route is armed only on the MAIN escape policy, only at posture auto
// (withEscapeGuardrailRoute enforces the posture gate), so a nil here is the
// common case and costs nothing.
func buildGuardrailsEscapeChecker(cfg Config, provReg *providerRegistry, provider port.LLMProvider) modelhook.VerdictChecker {
	if !cfg.GuardrailsEscape || !guardrailsConfigured(cfg) {
		return nil
	}
	return buildGuardrailsChecker(cfg, provReg, provider, "", cfg.Model)
}

// compileGuardrailRules converts the supplied rule specs into the adapter's compiled
// rules. ok=false when NO rule compiled (every spec invalid). Invalid modes / empty
// matchers are logged and skipped (fail-soft per rule). A load-time WARN flags two
// equally-specific matchers (the resolve() tiebreak then favours the earlier).
func compileGuardrailRules(cfg Config, specs []modelhook.RuleSpec) ([]modelhook.CompiledRule, bool) {
	out := make([]modelhook.CompiledRule, 0, len(specs))
	seen := map[string]int{} // matcher → first order, for the equal-specificity WARN
	for i, gr := range specs {
		cr, ok := modelhook.CompileRule(gr)
		if !ok {
			cfg.diag().Log(context.Background(), port.LevelWarn,
				"guardrails: invalid rule skipped (bad match/mode/phases)",
				"match", gr.Match, "mode", gr.Mode)
			continue
		}
		if prev, dup := seen[gr.Match]; dup {
			cfg.diag().Log(context.Background(), port.LevelWarn,
				"guardrails: two rules share a matcher; the EARLIER configured rule wins on a tie",
				"match", gr.Match, "first_index", prev, "duplicate_index", i)
		} else {
			seen[gr.Match] = i
		}
		out = append(out, cr)
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}
