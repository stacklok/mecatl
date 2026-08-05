package app

import (
	"context"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

// slots.go is the COMPOSITION-LAYER per-slot model resolver (ADR 0030, Phase 1+2):
// the "aliases as the spine" layer plus the `models.slots` map that binds named
// pipeline functions to aliases. It is a pure composition concern — engine/agent
// never sees a slot or an alias, only the already-resolved concrete model id.
//
// A SLOT is a named internal LLM call ("compaction", "ask-reviewer", "guardrail")
// or a semantic TIER ("cheap", "fast", "reasoning"). resolveSlotModel maps a slot
// name to a concrete model id THROUGH the existing alias machinery (lookupModelAlias),
// so the two never drift: a slot value is itself an alias or a literal id, resolved
// by the SAME grammar the agent-def `model:` path uses.
//
// THE BYTE-IDENTICAL GUARANTEE: when no slot is configured (cfg.ModelSlots empty/
// absent) resolveSlotModel returns ("", false) for every slot, and each of the three
// routed call sites keeps its EXACT pre-feature behaviour (the session model). The
// posture is FAIL-SOFT throughout: a typo'd slot key, an unknown/inherit alias, or
// any other miss WARNs and degrades to today's behaviour — a broken housekeeping
// slot must never wedge a compaction / ask-review / guardrail call.
//
// This slice routes exactly THREE internal lightweight calls to a slot: compaction
// (the tier-4 summary LLM call), ask-reviewer, and guardrail. Team synthesis is
// DEFERRED (it lacks a clean seam — the lead synthesis runs on the lead member's
// whole engine), and the project-tier override / the subagent router (ADR 0030
// Layer 3b and the project-merge-within-cap) are out of this slice.
//
// Phase 3 (ADR 0030 Layer 3) adds the `plan` slot — wired on the MODE axis, NOT the
// internal-call axis. Unlike the three call-slots above, `plan` does NOT route a
// lightweight housekeeping call: it re-resolves the SESSION model when the session's
// PermissionMode is ModePlan, re-resolved BETWEEN turns at the run-entry seam (the
// opusplan pattern). It reuses resolveSlotModel UNCHANGED — the resolution grammar is
// identical; only the consumer differs (the per-session engine factory in build.go,
// keyed on the session's mode, instead of a per-call deps builder). And it defaults to
// the `reasoning` tier, NOT `cheap`: a plan model is a STRONG-reasoning model, the one
// place a slot's default tier diverges from cheap.

// Slot names — the three routed internal lightweight calls (Layer 2). Each is the
// stable key an operator writes under `models.slots:` (or --model-slot).
const (
	// slotCompaction routes the CascadeCompactor's tier-4 summary LLM call.
	slotCompaction = "compaction"
	// slotAskReviewer routes the OPT-IN headless child-ask reviewer (issue #31).
	slotAskReviewer = "ask-reviewer"
	// slotGuardrail routes the LLM-backed guardrail content checker (issue #27).
	slotGuardrail = "guardrail"
	// slotSynthesis is DEFINED for completeness (team synthesis is the cheap tier's
	// natural fourth consumer) but is deliberately NOT wired this slice — the lead
	// synthesis runs on the lead member's whole engine and lacks a clean seam.
	slotSynthesis = "synthesis"
	// slotPlan routes the SESSION model when the session's PermissionMode is ModePlan
	// (ADR 0030 Layer 3, the opusplan pattern). It is the ONE slot wired on the MODE
	// axis rather than the internal-call axis: it is consumed by the per-session engine
	// factory (sessionEngineFactory in build.go), re-resolved between turns at the
	// run-entry seam when Session.Mode changes — NOT by a per-call deps builder. Its
	// default tier is `reasoning`, not `cheap` (slotDefaultTier) — a plan model is a
	// strong-reasoning model.
	slotPlan = "plan"
	// slotRouter routes the CLASSIFIER call of the OPT-IN semantic Subagent model router
	// (ADR 0031, Phase 5). Like the three internal call-slots it routes a lightweight
	// housekeeping call (one tiny classification turn), defaulting to the `cheap` tier —
	// the classifier is housekeeping, NOT the routed work. The router's per-CATEGORY
	// target models are a SEPARATE operator taxonomy (cfg.RouterCategories), not slots.
	slotRouter = "router"
)

// Semantic TIER names — the alias spine (Layer 1). A slot with no explicit binding
// falls through to its default tier (slotDefaultTier), so binding `cheap` once routes
// every cheap-defaulted slot.
const (
	// slotCheap is the cheapest/fastest tier — the default for every housekeeping slot.
	slotCheap = "cheap"
	// slotFast is the low-latency mid tier.
	slotFast = "fast"
	// slotReasoning is the strong-reasoning tier.
	slotReasoning = "reasoning"
)

// knownSlotNames is the set of recognised keys (call-slots + tiers) accepted under
// `models.slots:` / --model-slot. A key outside this set is a typo (e.g. `compacton`
// or `chaep`): foldOperatorModelSlots WARNs and ignores it rather than silently
// binding a slot nobody reads. It is fail-soft validation, NOT a hard parse error —
// the strict-parse guard at the YAML layer (ModelsSection.UnmarshalYAML) rejects an
// unknown TOP key (`slotz:`), while a free-form INNER map key is validated here.
var knownSlotNames = map[string]struct{}{
	slotCompaction:  {},
	slotAskReviewer: {},
	slotGuardrail:   {},
	slotSynthesis:   {},
	slotPlan:        {},
	slotRouter:      {},
	slotCheap:       {},
	slotFast:        {},
	slotReasoning:   {},
}

// slotDefaultTier maps a slot to the semantic tier it falls through to when it has no
// explicit binding. The three internal-call slots default to `cheap` (housekeeping
// runs on the cheapest model unless the operator says otherwise — the ADR's immediate
// token-savings win). The `plan` slot is the DELIBERATE divergence: it defaults to the
// `reasoning` tier, because a plan-mode model is a STRONG-reasoning model, not a cheap
// one (ADR 0030 Layer 3). A slot absent here has no default tier (an explicit binding
// is the only way to route it).
var slotDefaultTier = map[string]string{
	slotCompaction:  slotCheap,
	slotAskReviewer: slotCheap,
	slotGuardrail:   slotCheap,
	slotSynthesis:   slotCheap,
	slotPlan:        slotReasoning,
	slotRouter:      slotCheap,
}

// resolveSlotModel resolves a slot name to a concrete provider model id, in the
// composition layer only (the domain/agent never sees a slot or an alias). The
// precedence, fail-soft at every miss:
//
//  1. an explicit cfg.ModelSlots[slotName] binding;
//  2. else the slot's default tier (slotDefaultTier[slotName]) when THAT tier is
//     bound in cfg.ModelSlots;
//  3. else ("", false) — NO slot configured, so the caller keeps today's EXACT
//     behaviour (the byte-identical guarantee).
//
// The chosen selector (an alias or a literal id) is resolved THROUGH the existing
// lookupModelAlias grammar — the SAME path the agent-def `model:` resolution uses,
// so the two cannot drift. A selector that is known AND resolves to a concrete id
// returns (id, true); a selector that is unknown OR resolves to inherit/"" returns
// ("", false) — FAIL-SOFT, never a wedged housekeeping call. parentModel is the
// session model the caller falls back to on a ("", false); it is accepted for
// symmetry with the other resolvers and to make the call sites read uniformly (the
// function itself never returns it).
//
// resolveSlotModel is SILENT by design (no diagnostics): it is called from the
// PER-ENGINE deps builders (engineDepsForProvider, the child factory,
// askAdjudicatorDeps, buildGuardrailsChecker — once per session AND per child), so a
// WARN here would re-fire N times for one misconfigured slot (the documented
// per-derivation-duplication trap). The one-time misconfig WARN lives in the
// build-once path instead (slotMisconfig, narrated by logSlotConfigFacts from Build).
func resolveSlotModel(cfg Config, slotName, parentModel string) (model string, configured bool) {
	_ = parentModel // the caller owns the fallback; named for call-site symmetry.
	sel := selectorForSlot(cfg, slotName)
	if sel == "" {
		return "", false // no slot configured: byte-identical default.
	}
	id, known := lookupModelAlias(cfg, sel)
	if known && id != "" {
		return id, true
	}
	return "", false // configured but unresolvable: fail-soft, degrade to inherit.
}

// selectorForSlot returns the trimmed selector a slot resolves through: the explicit
// cfg.ModelSlots[slotName] binding, else the slot's default tier binding, else "".
// It is the shared lookup behind resolveSlotModel and the build-once misconfig narration.
func selectorForSlot(cfg Config, slotName string) string {
	sel := strings.TrimSpace(cfg.ModelSlots[slotName])
	if sel == "" {
		if tier := slotDefaultTier[slotName]; tier != "" {
			sel = strings.TrimSpace(cfg.ModelSlots[tier])
		}
	}
	return sel
}

// foldOperatorModelSlots merges the OPERATOR-TIER `models:` YAML subtree (read by the
// permconfig resolver from the user-global + CLI tiers ONLY — never the project file,
// which is IGNORED with a WARN, mirroring guardrails/posture) onto cfg. It merges
// `models.slots` onto cfg.ModelSlots and `models.aliases` onto cfg.ModelAliases, with
// the CLI flags (--model-slot / --model-alias, already on cfg) WINNING over YAML
// (per-key: a CLI binding for a key is never overwritten by a YAML binding for the
// same key). A typo'd inner slot key (not in knownSlotNames) is WARNed and dropped.
// It is a no-op when no operator-tier models: block was configured. cfg is taken and
// returned by value (Build holds a local cfg).
func foldOperatorModelSlots(cfg Config) Config {
	res, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok || res == nil {
		return cfg
	}
	m := res.OperatorModelSlots()
	if m == nil {
		return cfg
	}
	// Aliases: YAML entries fill in any key the CLI did not set (CLI wins per key).
	if len(m.Aliases) > 0 {
		if cfg.ModelAliases == nil {
			cfg.ModelAliases = make(map[string]string, len(m.Aliases))
		}
		for k, v := range m.Aliases {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if _, cliSet := cfg.ModelAliases[k]; cliSet {
				continue // CLI --model-alias wins.
			}
			cfg.ModelAliases[k] = strings.TrimSpace(v)
		}
	}
	// Slots: same per-key CLI-wins fold, with fail-soft validation of the slot key.
	if len(m.Slots) > 0 {
		if cfg.ModelSlots == nil {
			cfg.ModelSlots = make(map[string]string, len(m.Slots))
		}
		for k, v := range m.Slots {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if _, known := knownSlotNames[k]; !known {
				cfg.diag().Log(context.Background(), port.LevelWarn,
					"models.slots: unknown slot key IGNORED (not a known call-slot or tier)",
					"slot", k, "known", knownSlotNamesList())
				continue
			}
			if _, cliSet := cfg.ModelSlots[k]; cliSet {
				continue // CLI --model-slot wins.
			}
			cfg.ModelSlots[k] = strings.TrimSpace(v)
		}
	}
	return cfg
}

// cliModelKeys is the snapshot of which model bindings the OPERATOR set on the CLI
// (--model-slot / --model-alias / --model), taken BEFORE foldOperatorModelSlots merges
// the operator-YAML in (ADR 0030 Phase 4). It is the mechanism by which a CLI flag
// survives a project-tier override: foldProjectModelBindings overrides operator-YAML-set
// keys but SKIPS any key recorded here, realising the precedence
//
//	CLI (operator flags) > project-YAML (capped) > operator-YAML (settings.yaml) > built-in
//
// At the capture point cfg.ModelSlots/cfg.ModelAliases hold ONLY the CLI-set keys (the
// operator-YAML fold has not run yet), and cfg.Model is the CLI --model value (the
// reg.ResolvedDefaultModel() default has not been applied yet — that runs LATER in
// Build), so a non-empty cfg.Model here means --model was set on the CLI.
type cliModelKeys struct {
	slots    map[string]struct{}
	aliases  map[string]struct{}
	modelSet bool
	// subagentModelSet records whether --subagent-model was set on the CLI (issue #288).
	// It gates foldOperatorSubagentModel so a CLI flag wins over an operator-YAML
	// models.subagent. Like modelSet it is a faithful CLI-vs-YAML discriminator ONLY at
	// the capture point, where cfg.SubagentModel holds the bare CLI --subagent-model
	// value (the operator-YAML fold has not run yet).
	subagentModelSet bool
}

// captureCLIModelKeys snapshots the CLI-set model-binding keys from cfg. ORDERING
// INVARIANT: it MUST be called in Build IMMEDIATELY BEFORE foldOperatorModelSlots — the
// snapshot is a faithful CLI-vs-YAML discriminator ONLY because at that single point cfg
// holds nothing but the CLI bindings and cfg.Model is the bare CLI --model (no operator-
// YAML merged, no registry/operator default applied). Capturing later would misclassify
// YAML keys as CLI-set and invert the precedence. cfg.SubagentModel is likewise the bare
// CLI --subagent-model here (foldOperatorSubagentModel has not run), so a non-empty value
// means the flag was set. See cliModelKeys for the full rationale.
func captureCLIModelKeys(cfg Config) cliModelKeys {
	keys := cliModelKeys{
		slots:            make(map[string]struct{}, len(cfg.ModelSlots)),
		aliases:          make(map[string]struct{}, len(cfg.ModelAliases)),
		modelSet:         strings.TrimSpace(cfg.Model) != "",
		subagentModelSet: strings.TrimSpace(cfg.SubagentModel) != "",
	}
	for k := range cfg.ModelSlots {
		keys.slots[k] = struct{}{}
	}
	for k := range cfg.ModelAliases {
		keys.aliases[k] = struct{}{}
	}
	return keys
}

// foldOperatorModelDefault applies an operator-YAML `models.default:` to cfg.Model (ADR
// 0030 Phase 4), the operator-YAML rung of the default precedence
//
//	CLI --model > project-YAML default (capped) > operator-YAML default > registry default
//
// It is UNCAPPED (the operator's own binding — the allowlist caps PROJECT bindings only,
// the operator is authoritative) and resolved through lookupModelAlias (the operator-merged
// alias map; foldOperatorModelSlots has already folded operator-YAML aliases by the time
// this runs in Build). cliKeys.modelSet (the pre-foldOperatorModelSlots snapshot) gates it:
// a CLI --model wins, so the operator-YAML default is SKIPPED when --model was set. It runs
// AFTER the registry default is assigned (so it overrides it) and BEFORE
// foldProjectModelBindings (so a capped project default can override it in turn). No-op
// (byte-identical) when there is no permResolver, no operator models: block, or no
// operator models.default (or it resolves to inherit/unknown — fail-soft, keep the
// registry default).
func foldOperatorModelDefault(cfg Config, cliKeys cliModelKeys) Config {
	if cliKeys.modelSet {
		return cfg // CLI --model wins over the operator-YAML default.
	}
	res, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok || res == nil {
		return cfg
	}
	policy := res.OperatorModelPolicy()
	if policy == nil {
		return cfg
	}
	sel := strings.TrimSpace(policy.Default)
	if sel == "" {
		return cfg
	}
	// UNCAPPED: the operator's own default resolves through the alias grammar with no
	// allowlist membership test. An unresolvable selector is fail-soft (keep the registry
	// default) so a typo never wedges startup.
	id, known := lookupModelAlias(cfg, sel)
	if !known || id == "" {
		cfg.diag().Log(context.Background(), port.LevelWarn,
			"models.default: operator binding is unknown or means inherit; keeping the registry default model",
			"selector", sel, "model", cfg.Model)
		return cfg
	}
	cfg.Model = id
	cfg.diag().Log(context.Background(), port.LevelInfo,
		"models.default: operator binding ACTIVE (session default re-bound over the registry default)", "selector", sel, "model", id)
	return cfg
}

// foldOperatorSubagentModel applies an operator-YAML `models.subagent:` to
// cfg.SubagentModel (issue #288), the settings.yaml twin of the --subagent-model flag.
// The value is the def-less child-default model selector for every Subagent /
// Parallel-branch / team-member child that does not pin its own model.
//
// It is OPERATOR-TIER ONLY (read from OperatorModelPolicy(), the user-global + CLI tiers;
// a project-tier subagent: was already stripped with a WARN in captureProjectModels). It
// is a no-op (byte-identical) when --subagent-model was set on the CLI (cliKeys.subagentModelSet
// — the flag WINS), when there is no permResolver, no operator models: block, or an empty
// models.subagent. The value is set VERBATIM (NOT pre-resolved): the downstream
// normalizeSubagentModel is the ONE validator and keeps aliases verbatim by design, so
// this fold must not resolve the selector or it would double-resolve an alias whose target
// is itself a bare token. It runs in Build immediately BEFORE normalizeSubagentModel (after
// foldOperatorModelRouter, so the operator-merged alias map is final) — DELIBERATELY, so a
// YAML value goes through the SAME fail-fast normalizeSubagentModel path as the flag (a dead
// YAML selector fails startup, unlike the fail-soft models.default). cfg is taken/returned
// by value.
func foldOperatorSubagentModel(cfg Config, cliKeys cliModelKeys) Config {
	if cliKeys.subagentModelSet {
		return cfg // CLI --subagent-model wins over the operator-YAML value.
	}
	res, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok || res == nil {
		return cfg
	}
	policy := res.OperatorModelPolicy()
	if policy == nil {
		return cfg
	}
	sel := strings.TrimSpace(policy.Subagent)
	if sel == "" {
		return cfg
	}
	cfg.SubagentModel = sel // verbatim — normalizeSubagentModel validates + narrates.
	return cfg
}

// foldOperatorDefaultProvider merges the OPERATOR-TIER `models.default_provider:`
// YAML scalar (read by the permconfig resolver from the user-global + CLI tiers ONLY —
// never the project file, which is IGNORED with a WARN by the existing operator-only
// captureModels discipline) onto cfg.DefaultProvider. A CLI --default-provider
// (cfg.DefaultProviderFlagSet) OUT-RANKS the YAML value. It is a no-op when no
// operator-tier models.default_provider: key was configured. Mirrors
// foldOperatorModelDefault / foldOperatorPosture. cfg is
// taken and returned by value.
//
// The value feeds the UNCHANGED preferredDefaultProvider ladder as an explicit
// operator override — it does NOT lower the precedence of key-driven providers. The
// ladder is unchanged; this is the operator saying "I want toolhive (or any provider)
// to be the default despite my key." The existing validateDefaultModel fail-fast gate
// still fires if the named provider is unavailable. It MUST run in Build BEFORE
// buildProviderRegistry/resolveDefaultModel (so the registry sees the YAML value) and
// BEFORE validateDefaultModel (so the fail-fast gate catches an unknown provider).
func foldOperatorDefaultProvider(cfg Config) Config {
	if cfg.DefaultProviderFlagSet {
		return cfg // CLI wins; YAML cannot override an explicit flag.
	}
	res, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok || res == nil {
		return cfg
	}
	policy := res.OperatorModelPolicy()
	if policy == nil {
		return cfg
	}
	yamlProvider := strings.TrimSpace(policy.DefaultProvider)
	if yamlProvider == "" {
		return cfg
	}
	cfg.DefaultProvider = yamlProvider
	return cfg
}

// foldProjectModelBindings merges a TRUSTED project's `.mecatl/settings.yaml` models:
// bindings (slots/aliases/default) onto cfg, CAPPED by the operator allowlist (ADR 0030
// Phase 4). It runs in Build ONCE, AFTER foldOperatorModelSlots (so it overrides the
// operator-YAML layer) and AFTER cfg.Model has been resolved to the registry default (so
// a project `default` can re-bind cfg.Model and the cap resolves through the
// operator-merged alias map), and BEFORE modeNeedsEngine/logSlotConfigFacts (so the plan
// slot, the predicate, and the narration see the final merged maps).
//
// PRECEDENCE: CLI (operator flags) > project-YAML (capped) > operator-YAML > built-in.
// A project binding overrides an operator-YAML-set key but SKIPS any key the operator set
// on the CLI (cliKeys) — the operator's explicit per-run flag is a deliberate override
// that still wins. This rests on the cliKeys SNAPSHOT having been taken BEFORE
// foldOperatorModelSlots (see captureCLIModelKeys' ordering invariant): cliKeys must name
// ONLY the CLI-set keys, never the operator-YAML ones, or a project binding would wrongly
// skip an operator-YAML key (precedence inverts). The allowlist + its canonicalization are
// ALWAYS operator-only.
//
// CAP — resolve-then-check: each project binding VALUE is resolved through lookupModelAlias
// to a concrete id, and that id is tested for membership in the canonical allowlist set
// (each allowlist ENTRY likewise resolved to a concrete id via the operator-merged alias
// map). Accept ⇒ merge; reject ⇒ DROP and keep the operator/default value, with ONE
// build-once WARN per dropped binding. Slots are ALSO validated against knownSlotNames.
//
// No-op fast paths (each keeps cfg byte-identical): nil/absent permResolver; empty
// operator allowlist (the opt-in); project ingestion not admitted; no project
// models block. Under any of these foldProjectModelBindings returns cfg unchanged
// and logs nothing.
func foldProjectModelBindings(cfg Config, cliKeys cliModelKeys) Config {
	res, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok || res == nil {
		return cfg
	}
	policy := res.OperatorModelPolicy()
	if policy == nil || len(policy.Allowlist) == 0 {
		return cfg // opt-in: no operator allowlist ⇒ project models WARN-ignored upstream.
	}
	if !projectIngestionAdmitted(cfg) {
		return cfg // untrusted, or ingestion grant withheld: the project block was already WARN-ignored at capture.
	}
	proj := res.ProjectModelBindings(projectModelWorkspace(cfg))
	if proj == nil {
		return cfg // no honoured project models block.
	}

	// Canonicalize the operator allowlist to a concrete-id SET (each entry resolved
	// through the operator-merged alias map — at this point cfg.ModelAliases already
	// holds the operator-YAML aliases, folded by foldOperatorModelSlots).
	allowed := canonicalAllowlist(cfg, policy.Allowlist)

	// default: re-bind cfg.Model within the cap (CLI --model still wins).
	if sel := strings.TrimSpace(proj.Default); sel != "" && !cliKeys.modelSet {
		if id, okCap := capResolve(cfg, sel, allowed); okCap {
			cfg.Model = id
			cfg.diag().Log(context.Background(), port.LevelInfo,
				"models.default: project binding ACCEPTED (within the operator allowlist)", "selector", sel, "model", id)
		} else {
			cfg.diag().Log(context.Background(), port.LevelWarn,
				"models.default: project binding DROPPED (outside the operator allowlist); keeping the operator/default model",
				"selector", sel, "model", cfg.Model)
		}
	}

	// aliases: re-bind each alias key within the cap (CLI --model-alias key wins).
	cfg.ModelAliases = capMergeProjectBindings(cfg, "aliases", proj.Aliases, cfg.ModelAliases, cliKeys.aliases, allowed, false)
	// slots: re-bind each slot key within the cap (CLI --model-slot key wins; the slot
	// NAME is validated against knownSlotNames, the slot VALUE is capped resolve-then-check).
	cfg.ModelSlots = capMergeProjectBindings(cfg, "slots", proj.Slots, cfg.ModelSlots, cliKeys.slots, allowed, true)
	return cfg
}

// capMergeProjectBindings merges one project-binding MAP (slots or aliases) onto dst,
// capped by the operator allowlist (ADR 0030 Phase 4). For each project entry: SKIP a
// CLI-set key (cliKeys — the CLI flag wins); for slots, drop an unknown slot NAME
// fail-soft (knownSlotNames, validateName==true); resolve-then-check the VALUE against
// the allowlist (accept→merge the concrete id, drop→keep dst's existing value); emit one
// build-once INFO per accept and one build-once WARN per drop, tagged with kind
// ("slots"/"aliases"). It is the shared loop body for the two project-binding maps so
// foldProjectModelBindings stays under the gocyclo budget. dst is allocated lazily on the
// first accept and returned (cfg's map is reassigned to it by the caller).
func capMergeProjectBindings(cfg Config, kind string, src, dst map[string]string, cliKeys map[string]struct{}, allowed map[string]struct{}, validateName bool) map[string]string {
	for k, v := range src {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if validateName {
			if _, known := knownSlotNames[k]; !known {
				cfg.diag().Log(context.Background(), port.LevelWarn,
					"models.slots: unknown project slot key IGNORED (not a known call-slot or tier)",
					"slot", k, "known", knownSlotNamesList())
				continue
			}
		}
		if _, cli := cliKeys[k]; cli {
			continue // a CLI --model-slot/--model-alias for this key wins.
		}
		if id, okCap := capResolve(cfg, strings.TrimSpace(v), allowed); okCap {
			if dst == nil {
				dst = map[string]string{}
			}
			dst[k] = id
			cfg.diag().Log(context.Background(), port.LevelInfo,
				"models."+kind+": project binding ACCEPTED (within the operator allowlist)", "key", k, "model", id)
		} else {
			cfg.diag().Log(context.Background(), port.LevelWarn,
				"models."+kind+": project binding DROPPED (outside the operator allowlist); keeping the operator/default value",
				"key", k, "selector", v)
		}
	}
	return dst
}

// projectModelWorkspace builds the read-only workspace the resolver reads the project
// models: block through (ProjectModelBindings). It mirrors the build-time workspace the
// permission resolver/agent registry are resolved against (cfg.Workspace); a "" workspace
// yields nil (no project root ⇒ no project models, the fast path in
// foldProjectModelBindings handles the nil). It uses the read-only OS workspace so the
// resolver's Stat/Read seam works against the host filesystem.
func projectModelWorkspace(cfg Config) tool.WorkspaceReader {
	if strings.TrimSpace(cfg.Workspace) == "" {
		return nil
	}
	ws, err := osfs.NewWorkspace(cfg.Workspace)
	if err != nil {
		return nil
	}
	return ws
}

// canonicalAllowlist resolves every operator-allowlist ENTRY (an alias name or a concrete
// id) through lookupModelAlias against the operator-merged alias map, returning the SET of
// concrete ids a project binding must resolve INTO to be honoured. An entry that does not
// resolve to a concrete id (unknown bare token, or an alias meaning inherit) contributes
// nothing — fail-closed: an unresolvable allowlist entry never widens the cap.
func canonicalAllowlist(cfg Config, entries []string) map[string]struct{} {
	set := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if id, known := lookupModelAlias(cfg, e); known && id != "" {
			set[id] = struct{}{}
		}
	}
	return set
}

// capResolve resolves a project selector to a concrete id and tests it against the
// canonical allowlist set (resolve-then-check). It returns (id, true) when the selector
// resolves to a concrete id that IS in the set, else ("", false). A selector that does not
// resolve to a concrete id (unknown bare token / inherit) is rejected — a project binding
// must name something the operator vetted.
func capResolve(cfg Config, sel string, allowed map[string]struct{}) (string, bool) {
	if sel == "" {
		return "", false
	}
	id, known := lookupModelAlias(cfg, sel)
	if !known || id == "" {
		return "", false
	}
	if _, ok := allowed[id]; !ok {
		return "", false
	}
	return id, true
}

// foldOperatorModelRouter folds the OPERATOR-TIER `models.router:` taxonomy (ADR 0031,
// Phase 5) onto cfg: the routing categories, the default category, and the classifier
// slot. It is OPERATOR-TIER ONLY (read from OperatorModelPolicy(), which is the
// user-global + CLI tiers; a project-tier router: was already stripped with a WARN in
// captureProjectModels). It is FAIL-SOFT: a category with an empty name OR an empty
// description OR an empty model selector is WARN-dropped (a category the classifier
// cannot name/describe, or that maps to nothing, is useless) — the rest still load.
// ADR 0042 (superseding 0031's enable model): the TAXONOMY enables the router, so this
// fold ALSO ORs the YAML `disabled:` kill-switch onto cfg.RouterDisabled (mirroring
// foldOperatorGuardrails' Disabled handling) — the CLI kill-switch sets the same field,
// the two combine. A no-taxonomy operator (no router: block) leaves cfg byte-identical.
// cfg is taken/returned by value.
func foldOperatorModelRouter(cfg Config) Config {
	res, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok || res == nil {
		return cfg
	}
	policy := res.OperatorModelPolicy()
	if policy == nil || policy.Router == nil {
		return cfg
	}
	router := policy.Router
	// Disabled: OR the YAML kill-switch with the CLI one (either disables) — ADR 0042,
	// mirroring foldOperatorGuardrails.
	if router.Disabled {
		cfg.RouterDisabled = true
	}
	cfg.RouterClassifierSlot = strings.TrimSpace(router.ClassifierSlot)
	cfg.RouterDefaultCategory = strings.TrimSpace(router.DefaultCategory)
	cfg.RouterCategories = nil
	for _, c := range router.Categories {
		name := strings.TrimSpace(c.Name)
		desc := strings.TrimSpace(c.Description)
		model := strings.TrimSpace(c.Model)
		if name == "" || desc == "" || model == "" {
			cfg.diag().Log(context.Background(), port.LevelWarn,
				"models.router: DROPPING a malformed category (name, description, and model are all required)",
				"name", name)
			continue
		}
		cfg.RouterCategories = append(cfg.RouterCategories, permconfig.RouterCategory{
			Name: name, Description: desc, Model: model,
		})
	}
	return cfg
}

// resolveRouterClassifierModel resolves the model the model-router CLASSIFIER runs on
// (ADR 0031), the SINGLE source both buildModelRouterTask (the live closure, keyed on the
// session's parentModel) and logModelRouterFacts (the build-once narration, keyed on
// cfg.Model) call — so the logged classifier model is exactly the one a session of that
// parentModel classifies on. Precedence: an operator `classifier-slot` wins; else the
// `router` slot (default cheap tier); else parentModel (fail-soft — the classifier is
// housekeeping and must never wedge a delegation by failing to resolve). It is silent
// (no diagnostics); the build-once narration is logModelRouterFacts' job.
func resolveRouterClassifierModel(cfg Config, parentModel string) string {
	if cfg.RouterClassifierSlot != "" {
		if m, ok := resolveSlotModel(cfg, cfg.RouterClassifierSlot, parentModel); ok {
			return m
		}
		return parentModel
	}
	if m, ok := resolveSlotModel(cfg, slotRouter, parentModel); ok {
		return m
	}
	return parentModel
}

// knownSlotNamesList renders the known slot/tier keys in a stable sorted order for
// the unknown-key WARN, so an operator who typo'd a slot sees exactly what is accepted.
func knownSlotNamesList() string {
	keys := make([]string, 0, len(knownSlotNames))
	for k := range knownSlotNames {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// logSlotConfigFacts emits the build-once per-slot narration EXACTLY ONCE through
// cfg.diag(): an INFO "model slot ACTIVE" for each ROUTED slot that resolves, and the
// one-time WARN for each ROUTED slot that is CONFIGURED but unresolvable (selector set,
// yet it points at an unknown alias or one meaning inherit — so the slot degrades to
// the session model). It is called ONLY from Build (after the normalize block) — never
// the per-engine deps builders (the no-per-derivation-duplication rule, the same
// discipline as logBuildConfigFacts / narratePosture). resolveSlotModel itself is
// silent precisely so this is the ONE place a misconfigured slot warns (once), not N
// times across per-session/per-child engine builds. A slot that resolves keeps the
// "loop emits exactly THREE lines" invariant intact: this is a Build-level fact, not a
// loop line. When nothing is configured it logs nothing (byte-identical to pre-feature).
func logSlotConfigFacts(cfg Config) {
	if len(cfg.ModelSlots) == 0 {
		return
	}
	for _, slot := range []string{slotCompaction, slotAskReviewer, slotGuardrail, slotPlan, slotRouter} {
		model, ok := resolveSlotModel(cfg, slot, cfg.Model)
		// The three internal call-slots route a lightweight housekeeping call; the
		// plan slot (ADR 0030 Layer 3) instead re-resolves the SESSION model in plan
		// mode (the opusplan pattern) — narrate it distinctly so the INFO is honest.
		active := "model slot ACTIVE: this internal lightweight call runs on the slot model instead of the session model"
		if slot == slotPlan {
			active = "model slot ACTIVE: plan-mode turns run on the plan model instead of the session model (opusplan), re-resolved between turns on a mode change"
		}
		switch {
		case ok:
			cfg.diag().Log(context.Background(), port.LevelInfo, active, "slot", slot, "model", model)
		case selectorForSlot(cfg, slot) != "":
			// Configured (a selector exists) but unresolvable: WARN ONCE here so the
			// operator sees the typo at Build, while the runtime stays fail-soft.
			cfg.diag().Log(context.Background(), port.LevelWarn,
				"model slot points at an unknown alias or one meaning inherit; the slot is IGNORED and this call keeps the session model",
				"slot", slot, "selector", selectorForSlot(cfg, slot))
		}
	}
}

// logModelRouterFacts emits the build-once Subagent-model-router fact (ADR 0031; enable
// model per ADR 0042) EXACTLY ONCE through cfg.diag(). Per ADR 0042 the TAXONOMY is the
// enable, so:
//   - no taxonomy (len(RouterCategories)==0)        → SILENT (byte-identical OFF; the
//     0031 "flag set but no taxonomy" WARN is GONE — there is no enable flag anymore).
//   - taxonomy present + cfg.RouterDisabled          → a one-time WARN: the taxonomy is
//     configured but the kill-switch (CLI --subagent-model-router=false or the YAML
//     models.router.disabled) forces it OFF, so no routing happens.
//   - taxonomy present + not disabled                → the "subagent model router ACTIVE"
//     INFO (category count + classifier model).
//
// Build-once ONLY (never per-engine — the no-per-derivation-duplication rule; the "loop
// emits exactly THREE lines" invariant holds, this is a Build fact not a loop line).
func logModelRouterFacts(cfg Config) {
	if len(cfg.RouterCategories) == 0 {
		return // No taxonomy: byte-identical, silent (ADR 0042 — taxonomy is the enable).
	}
	if cfg.RouterDisabled {
		cfg.diag().Log(context.Background(), port.LevelWarn,
			"models.router taxonomy is configured but the subagent model router is DISABLED (kill-switch: --subagent-model-router=false or models.router.disabled: true); no per-delegation routing happens")
		return
	}
	// Resolve the classifier model the SAME way buildModelRouterTask does (shared
	// resolveRouterClassifierModel), keyed on cfg.Model — at Build time cfg.Model IS the
	// shared-engine/parent model the build-once fact narrates, so the logged classifier
	// matches what a default session classifies on (a per-session engine on a non-default
	// model re-derives the closure on ITS parentModel; the build-once fact is the
	// shared-engine narration, like logSlotConfigFacts).
	classifier := resolveRouterClassifierModel(cfg, cfg.Model)
	cfg.diag().Log(context.Background(), port.LevelInfo,
		"subagent model router ACTIVE: a tiny classifier picks the child model per routable delegation from the operator taxonomy — plain, writable-explorer, and unpinned agent-def calls (issues #285/#286), adding one extra classifier LLM call each (bounded by --max-run-tokens; set models.router.disabled or --subagent-model-router=false to turn it off)",
		"categories", len(cfg.RouterCategories), "classifier", classifier, "default_category", cfg.RouterDefaultCategory)
}

// modeNeedsEngine returns the composition predicate wired into
// server.Config.ModeNeedsEngine (ADR 0030 Layer 3): for a given session
// PermissionMode, does that mode resolve a model DIFFERING from the shared engine's
// model (cfg.Model)? It is true ONLY for ModePlan when the `plan` slot resolves to a
// concrete id that differs from cfg.Model — the only case where promoting a DEFAULT-FS
// session to a per-session factory engine actually changes anything.
//
// It returns NIL when no plan slot is configured (or it resolves to cfg.Model), so the
// Service never promotes a default-FS session and a mode flip is BYTE-IDENTICAL to
// pre-Phase-3 (the regression guard). The predicate is evaluated against the BUILD-TIME
// plan resolution — operator-tier config is fixed at Build, so this is sound and
// cheap (no per-call re-resolution). ModeDefault/ModeAccept always answer false (they
// keep the session model). The predicate intentionally compares against cfg.Model (the
// shared-engine model), because promotion only matters for a session that would
// otherwise ride the shared engine.
func modeNeedsEngine(cfg Config) func(mode session.PermissionMode) bool {
	planModel, configured := resolveSlotModel(cfg, slotPlan, cfg.Model)
	if !configured || planModel == "" || planModel == cfg.Model {
		// No active plan slot (or it resolves to the shared-engine model): a mode flip
		// never changes the model, so never promote — nil keeps the default-FS path
		// byte-identical.
		return nil
	}
	return func(mode session.PermissionMode) bool {
		return mode == session.ModePlan
	}
}
