package app

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/skills"
)

// HarnessSourceID is a trusted composition registration identity.
type HarnessSourceID string

// HarnessSourceScope is the authoritative owner/profile supplied to a binding.
type HarnessSourceScope struct {
	Principal *session.Principal
	Profile   string
}

// HarnessSourceScopeKind controls whether a registration is shared by the
// process or bound independently for each session principal.
type HarnessSourceScopeKind uint8

// Harness source scope kinds.
const (
	// HarnessSourceScopeProcess shares one caller-neutral, concurrency-safe binding.
	HarnessSourceScopeProcess HarnessSourceScopeKind = iota
	// HarnessSourceScopePrincipal creates an isolated binding for each session owner.
	HarnessSourceScopePrincipal
)

// HarnessProvenancePolicy constrains the admission tiers a source may produce.
type HarnessProvenancePolicy struct {
	Fixed           string
	PreserveAllowed []string
}

// HarnessSourceRegistration describes one trusted kind-specific source.
type HarnessSourceRegistration[T any] struct {
	ID         HarnessSourceID
	Scope      HarnessSourceScopeKind
	Provenance HarnessProvenancePolicy
	Bind       func(context.Context, HarnessSourceScope) (T, func() error, error)
}

const (
	harnessProjectTier  = prompt.InstructionProvenanceProject
	harnessUserTier     = "user"
	harnessExplicitTier = "explicit"
	harnessDriverTier   = "driver"
	harnessLocalSource  = "local"
	harnessModeCombine  = "combine"
	harnessModeReplace  = "replace"
)

var harnessSourceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

var harnessTiers = map[string]struct{}{
	harnessProjectTier: {}, harnessUserTier: {}, harnessExplicitTier: {}, harnessDriverTier: {},
}

func validateHarnessRegistration[T any](kind string, regs []HarnessSourceRegistration[T]) error {
	seen := make(map[HarnessSourceID]struct{}, len(regs))
	for _, reg := range regs {
		if !harnessSourceIDPattern.MatchString(string(reg.ID)) {
			return fmt.Errorf("harness context %s source has invalid id %q", kind, reg.ID)
		}
		if _, ok := seen[reg.ID]; ok {
			return fmt.Errorf("harness context %s source id %q is duplicated", kind, reg.ID)
		}
		seen[reg.ID] = struct{}{}
		if reg.Scope != HarnessSourceScopeProcess && reg.Scope != HarnessSourceScopePrincipal {
			return fmt.Errorf("harness context %s source %q has invalid scope", kind, reg.ID)
		}
		if reg.Bind == nil {
			return fmt.Errorf("harness context %s source %q has no binder", kind, reg.ID)
		}
		if err := validateHarnessProvenance(reg.Provenance); err != nil {
			return fmt.Errorf("harness context %s source %q: %w", kind, reg.ID, err)
		}
		if kind == "rules" && (reg.Provenance.Fixed == "explicit" || tierAllowed("explicit", reg.Provenance.PreserveAllowed)) {
			return fmt.Errorf("harness context rules do not have an explicit provenance tier")
		}
		if reg.ID == "driver" && reg.Provenance.Fixed != "driver" {
			return fmt.Errorf("harness context %s driver source must use fixed driver provenance", kind)
		}
	}
	return nil
}

func validateHarnessProvenance(policy HarnessProvenancePolicy) error {
	fixed := policy.Fixed != ""
	preserve := len(policy.PreserveAllowed) > 0
	if fixed == preserve {
		return fmt.Errorf("provenance must set exactly one of fixed or preserve_allowed")
	}
	values := policy.PreserveAllowed
	if fixed {
		values = []string{policy.Fixed}
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := harnessTiers[value]; !ok {
			return fmt.Errorf("unknown provenance tier %q", value)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("duplicate provenance tier %q", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

type harnessKindPolicy struct {
	sources   []HarnessSourceID
	mode      string
	exclude   map[string]map[HarnessSourceID]struct{}
	overrides map[string]harnessOverride
}

type harnessOverride struct {
	winner   HarnessSourceID
	replaces map[HarnessSourceID]struct{}
}

// compileHarnessKind validates the closed per-kind policy as one fail-fast unit.
//
//nolint:gocyclo // Validation intentionally reports each malformed declaration precisely.
func compileHarnessKind(name string, cfg permconfig.HarnessContextKind, enabled map[HarnessSourceID]struct{}, registered map[HarnessSourceID]struct{}, named bool) (harnessKindPolicy, error) {
	p := harnessKindPolicy{mode: cfg.Mode, exclude: make(map[string]map[HarnessSourceID]struct{}), overrides: make(map[string]harnessOverride)}
	if p.mode != harnessModeCombine && p.mode != harnessModeReplace {
		return p, fmt.Errorf("harness_context.kinds.%s.mode must be combine or replace", name)
	}
	seen := make(map[HarnessSourceID]struct{}, len(cfg.Sources))
	for _, raw := range cfg.Sources {
		id := HarnessSourceID(raw)
		if _, ok := seen[id]; ok {
			return p, fmt.Errorf("harness_context.kinds.%s has duplicate source %q", name, id)
		}
		if _, ok := enabled[id]; !ok {
			return p, fmt.Errorf("harness_context.kinds.%s source %q is not enabled", name, id)
		}
		if _, ok := registered[id]; !ok {
			return p, fmt.Errorf("harness_context.kinds.%s source %q is not registered", name, id)
		}
		seen[id] = struct{}{}
		p.sources = append(p.sources, id)
	}
	for _, ex := range cfg.Exclude {
		id := HarnessSourceID(ex.Source)
		if _, ok := seen[id]; !ok {
			return p, fmt.Errorf("harness_context.kinds.%s exclusion source %q is not ordered", name, id)
		}
		if named == (ex.Name == "") {
			return p, fmt.Errorf("harness_context.kinds.%s exclusion has invalid name", name)
		}
		key := ex.Name
		if p.exclude[key] == nil {
			p.exclude[key] = make(map[HarnessSourceID]struct{})
		}
		if _, duplicate := p.exclude[key][id]; duplicate {
			return p, fmt.Errorf("harness_context.kinds.%s has duplicate exclusion", name)
		}
		p.exclude[key][id] = struct{}{}
	}
	if !named && len(cfg.Overrides) > 0 || p.mode == harnessModeReplace && len(cfg.Overrides) > 0 {
		return p, fmt.Errorf("harness_context.kinds.%s does not permit overrides", name)
	}
	for _, configured := range cfg.Overrides {
		if configured.Name == "" {
			return p, fmt.Errorf("harness_context.kinds.%s override name is empty", name)
		}
		if _, duplicate := p.overrides[configured.Name]; duplicate {
			return p, fmt.Errorf("harness_context.kinds.%s has duplicate override %q", name, configured.Name)
		}
		winner := HarnessSourceID(configured.Winner)
		winnerPos, ok := harnessSourcePosition(p.sources, winner)
		if !ok {
			return p, fmt.Errorf("harness_context.kinds.%s override winner %q is not ordered", name, winner)
		}
		if len(configured.Replaces) == 0 {
			return p, fmt.Errorf("harness_context.kinds.%s override %q replaces nothing", name, configured.Name)
		}
		replaces := make(map[HarnessSourceID]struct{}, len(configured.Replaces))
		allAfterWinner := true
		for _, raw := range configured.Replaces {
			id := HarnessSourceID(raw)
			pos, exists := harnessSourcePosition(p.sources, id)
			if !exists || id == winner {
				return p, fmt.Errorf("harness_context.kinds.%s override %q has invalid replacement %q", name, configured.Name, id)
			}
			if _, duplicate := replaces[id]; duplicate {
				return p, fmt.Errorf("harness_context.kinds.%s override %q duplicates replacement %q", name, configured.Name, id)
			}
			if pos < winnerPos {
				allAfterWinner = false
			}
			replaces[id] = struct{}{}
		}
		if allAfterWinner {
			return p, fmt.Errorf("harness_context.kinds.%s override %q is a no-op", name, configured.Name)
		}
		if _, excluded := p.exclude[configured.Name][winner]; excluded {
			return p, fmt.Errorf("harness_context.kinds.%s override %q excludes its winner", name, configured.Name)
		}
		allExcluded := true
		for id := range replaces {
			if _, excluded := p.exclude[configured.Name][id]; !excluded {
				allExcluded = false
			}
		}
		if allExcluded {
			return p, fmt.Errorf("harness_context.kinds.%s override %q excludes every replacement", name, configured.Name)
		}
		p.overrides[configured.Name] = harnessOverride{winner: winner, replaces: replaces}
	}
	return p, nil
}

func harnessSourcePosition(order []HarnessSourceID, target HarnessSourceID) (int, bool) {
	for i, id := range order {
		if id == target {
			return i, true
		}
	}
	return 0, false
}

func chooseHarnessWinner(name string, present map[HarnessSourceID]bool, policy harnessKindPolicy) (HarnessSourceID, bool) {
	override, hasOverride := policy.overrides[name]
	winnerPresent := present[override.winner]
	for _, id := range policy.sources {
		if !present[id] {
			continue
		}
		if excluded := policy.exclude[name]; excluded != nil {
			if _, ok := excluded[id]; ok {
				continue
			}
		}
		if hasOverride && winnerPresent {
			if _, replaced := override.replaces[id]; replaced {
				continue
			}
		}
		return id, true
	}
	return "", false
}

type boundRulesSource struct {
	id         HarnessSourceID
	source     prompt.RulesSource
	provenance HarnessProvenancePolicy
}

type resolvedRulesSource struct {
	policy          harnessKindPolicy
	sources         []boundRulesSource
	projectAdmitted bool
}

func (s *resolvedRulesSource) ListRules(ctx context.Context) ([]prompt.Rule, error) {
	candidates := make(map[HarnessSourceID]map[string]prompt.Rule)
	names := make(map[string]struct{})
	for _, source := range s.sources {
		rules, err := source.source.ListRules(ctx)
		if err != nil {
			return nil, err
		}
		entries := make(map[string]prompt.Rule, len(rules))
		for _, rule := range rules {
			if source.provenance.Fixed != "" {
				rule.Origin = prompt.RuleOrigin(source.provenance.Fixed)
			} else if !tierAllowed(string(rule.Origin), source.provenance.PreserveAllowed) {
				return nil, fmt.Errorf("rule %q has disallowed provenance %q", rule.Name, rule.Origin)
			}
			if rule.Origin == prompt.RuleOriginProject && !s.projectAdmitted {
				continue
			}
			if s.policy.excludes(source.id, rule.Name) {
				continue
			}
			entries[rule.Name] = rule
			names[rule.Name] = struct{}{}
		}
		candidates[source.id] = entries
		if s.policy.mode == harnessModeReplace && len(entries) > 0 {
			break
		}
	}
	out := make([]prompt.Rule, 0, len(names))
	for name := range names {
		present := make(map[HarnessSourceID]bool)
		for id, entries := range candidates {
			_, present[id] = entries[name]
		}
		if winner, ok := chooseHarnessWinner(name, present, s.policy); ok {
			out = append(out, candidates[winner][name])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

type resolvedSkillSource struct {
	metas   []tool.SkillMeta
	winners map[string]tool.SkillSource
}

func (s *resolvedSkillSource) ListSkills(context.Context) ([]tool.SkillMeta, error) {
	return append([]tool.SkillMeta(nil), s.metas...), nil
}
func (s *resolvedSkillSource) SkillBody(ctx context.Context, name string) (string, error) {
	if s.winners[name] == nil {
		return "", tool.ErrSkillNotFound
	}
	return s.winners[name].SkillBody(ctx, name)
}
func (s *resolvedSkillSource) ListSkillAssets(ctx context.Context, name string) ([]tool.SkillAsset, error) {
	if s.winners[name] == nil {
		return nil, tool.ErrSkillNotFound
	}
	return s.winners[name].ListSkillAssets(ctx, name)
}
func (s *resolvedSkillSource) ReadSkillAsset(ctx context.Context, skill, asset string) ([]byte, error) {
	if s.winners[skill] == nil {
		return nil, tool.ErrSkillAssetNotFound
	}
	return s.winners[skill].ReadSkillAsset(ctx, skill, asset)
}

type resolvedAgentSource struct{ defs []tool.AgentDef }

func (s *resolvedAgentSource) ListAgentDefs(context.Context) ([]tool.AgentDef, error) {
	return append([]tool.AgentDef(nil), s.defs...), nil
}

func tierAllowed(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if candidate == value {
			return true
		}
	}
	return false
}

//nolint:gocyclo // Binding five distinct typed source families keeps their contracts explicit.
func resolveProcessHarnessSnapshots(ctx context.Context, cfg *Config) error {
	resolver, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok || resolver.OperatorHarnessContext() == nil {
		return nil
	}
	section := resolver.OperatorHarnessContext()
	enabled := make(map[HarnessSourceID]struct{}, len(section.EnabledSources))
	for _, raw := range section.EnabledSources {
		enabled[HarnessSourceID(raw)] = struct{}{}
	}
	var cleanups []func() error
	addCleanup := func(cleanup func() error) {
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
	}

	rulesPolicy, err := compileHarnessKind("rules", section.Kinds.Rules, enabled, harnessRegistrationIDs(cfg.HarnessRulesSources), true)
	if err != nil {
		return err
	}
	rulesRegs := make(map[HarnessSourceID]HarnessSourceRegistration[prompt.RulesSource])
	for _, reg := range cfg.HarnessRulesSources {
		rulesRegs[reg.ID] = reg
	}
	var boundRules []boundRulesSource
	for _, id := range rulesPolicy.sources {
		reg := rulesRegs[id]
		if reg.Provenance.Fixed == harnessProjectTier && !projectIngestionAdmitted(*cfg) {
			continue
		}
		if reg.Scope == HarnessSourceScopePrincipal && cfg.harnessScope == nil {
			continue
		}
		source, cleanup, bindErr := reg.Bind(ctx, harnessBindingScope(*cfg))
		if bindErr != nil || source == nil {
			if cleanup != nil {
				_ = cleanup()
			}
			closeHarnessCleanups(cleanups)
			if bindErr == nil {
				bindErr = fmt.Errorf("source returned nil")
			}
			return fmt.Errorf("bind rules source %q: %w", id, bindErr)
		}
		addCleanup(cleanup)
		boundRules = append(boundRules, boundRulesSource{id: id, source: source, provenance: reg.Provenance})
	}
	rulesSnapshot, rulesErr := (&resolvedRulesSource{policy: rulesPolicy, sources: boundRules, projectAdmitted: projectIngestionAdmitted(*cfg)}).ListRules(ctx)
	cfg.harnessRules = frozenHarnessRules{rules: rulesSnapshot, err: rulesErr}

	skillsPolicy, err := compileHarnessKind("skills", section.Kinds.Skills, enabled, harnessRegistrationIDs(cfg.HarnessSkillSources), true)
	if err != nil {
		closeHarnessCleanups(cleanups)
		return err
	}
	skillRegs := make(map[HarnessSourceID]HarnessSourceRegistration[tool.SkillSource])
	for _, reg := range cfg.HarnessSkillSources {
		skillRegs[reg.ID] = reg
	}
	skillEntries := make(map[HarnessSourceID]map[string]tool.SkillMeta)
	skillSources := make(map[HarnessSourceID]tool.SkillSource)
	allSkills := make(map[string]struct{})
	for _, id := range skillsPolicy.sources {
		reg := skillRegs[id]
		if reg.Provenance.Fixed == harnessProjectTier && !projectIngestionAdmitted(*cfg) {
			continue
		}
		if reg.Scope == HarnessSourceScopePrincipal && cfg.harnessScope == nil {
			continue
		}
		source, cleanup, bindErr := reg.Bind(ctx, harnessBindingScope(*cfg))
		if bindErr != nil || source == nil {
			if cleanup != nil {
				_ = cleanup()
			}
			closeHarnessCleanups(cleanups)
			if bindErr == nil {
				bindErr = fmt.Errorf("source returned nil")
			}
			return fmt.Errorf("bind skill source %q: %w", id, bindErr)
		}
		metas, listErr := source.ListSkills(ctx)
		if listErr != nil {
			if cleanup != nil {
				_ = cleanup()
			}
			closeHarnessCleanups(cleanups)
			return listErr
		}
		addCleanup(cleanup)
		entries := make(map[string]tool.SkillMeta, len(metas))
		for _, meta := range metas {
			if reg.Provenance.Fixed != "" {
				meta.Origin = tool.SkillOrigin(reg.Provenance.Fixed)
			} else if !tierAllowed(string(meta.Origin), reg.Provenance.PreserveAllowed) {
				closeHarnessCleanups(cleanups)
				return fmt.Errorf("skill %q has disallowed provenance %q", meta.Name, meta.Origin)
			}
			if meta.Origin == tool.SkillOriginProject && !projectIngestionAdmitted(*cfg) {
				continue
			}
			if skillsPolicy.excludes(id, meta.Name) {
				continue
			}
			entries[meta.Name] = meta
			allSkills[meta.Name] = struct{}{}
		}
		skillEntries[id] = entries
		skillSources[id] = source
		if skillsPolicy.mode == harnessModeReplace && len(entries) > 0 {
			break
		}
	}
	resolvedSkills := &resolvedSkillSource{winners: make(map[string]tool.SkillSource)}
	for name := range allSkills {
		present := make(map[HarnessSourceID]bool)
		for id, entries := range skillEntries {
			_, present[id] = entries[name]
		}
		if winner, found := chooseHarnessWinner(name, present, skillsPolicy); found {
			resolvedSkills.metas = append(resolvedSkills.metas, skillEntries[winner][name])
			resolvedSkills.winners[name] = skillSources[winner]
		}
	}
	sort.Slice(resolvedSkills.metas, func(i, j int) bool { return resolvedSkills.metas[i].Name < resolvedSkills.metas[j].Name })
	cfg.harnessSkills = resolvedSkills

	agentPolicy, err := compileHarnessKind("agent_defs", section.Kinds.AgentDefs, enabled, harnessRegistrationIDs(cfg.HarnessAgentDefSources), true)
	if err != nil {
		closeHarnessCleanups(cleanups)
		return err
	}
	agentRegs := make(map[HarnessSourceID]HarnessSourceRegistration[tool.AgentDefSource])
	for _, reg := range cfg.HarnessAgentDefSources {
		agentRegs[reg.ID] = reg
	}
	agentEntries := make(map[HarnessSourceID]map[string]tool.AgentDef)
	allAgents := make(map[string]struct{})
	for _, id := range agentPolicy.sources {
		reg := agentRegs[id]
		if reg.Provenance.Fixed == harnessProjectTier && !projectIngestionAdmitted(*cfg) {
			continue
		}
		if reg.Scope == HarnessSourceScopePrincipal && cfg.harnessScope == nil {
			continue
		}
		source, cleanup, bindErr := reg.Bind(ctx, harnessBindingScope(*cfg))
		if bindErr != nil || source == nil {
			if cleanup != nil {
				_ = cleanup()
			}
			closeHarnessCleanups(cleanups)
			if bindErr == nil {
				bindErr = fmt.Errorf("source returned nil")
			}
			return fmt.Errorf("bind agent source %q: %w", id, bindErr)
		}
		defs, listErr := source.ListAgentDefs(ctx)
		if listErr != nil {
			if cleanup != nil {
				_ = cleanup()
			}
			closeHarnessCleanups(cleanups)
			return listErr
		}
		addCleanup(cleanup)
		entries := make(map[string]tool.AgentDef, len(defs))
		for _, def := range defs {
			if reg.Provenance.Fixed != "" {
				def.Origin = tool.AgentOrigin(reg.Provenance.Fixed)
			} else if !tierAllowed(string(def.Origin), reg.Provenance.PreserveAllowed) {
				closeHarnessCleanups(cleanups)
				return fmt.Errorf("agent %q has disallowed provenance %q", def.Name, def.Origin)
			}
			if def.Origin == tool.AgentOriginProject && !projectIngestionAdmitted(*cfg) {
				continue
			}
			if agentPolicy.excludes(id, def.Name) {
				continue
			}
			entries[def.Name] = def
			allAgents[def.Name] = struct{}{}
		}
		agentEntries[id] = entries
		if agentPolicy.mode == harnessModeReplace && len(entries) > 0 {
			break
		}
	}
	resolvedAgents := &resolvedAgentSource{}
	for name := range allAgents {
		present := make(map[HarnessSourceID]bool)
		for id, entries := range agentEntries {
			_, present[id] = entries[name]
		}
		if winner, found := chooseHarnessWinner(name, present, agentPolicy); found {
			resolvedAgents.defs = append(resolvedAgents.defs, agentEntries[winner][name])
		}
	}
	sort.Slice(resolvedAgents.defs, func(i, j int) bool { return resolvedAgents.defs[i].Name < resolvedAgents.defs[j].Name })
	cfg.harnessAgentDefs = resolvedAgents
	previousClose := cfg.harnessContextClose
	cfg.harnessContextClose = func() {
		closeHarnessCleanups(cleanups)
		if previousClose != nil {
			previousClose()
		}
	}
	return nil
}

type boundCommandSource struct {
	id      HarnessSourceID
	binding server.CommandSourceBinding
}

type resolvedCommandBinding struct {
	generation harnessGeneration
	context    *Config
	policy     harnessKindPolicy
	sources    []boundCommandSource
}

type harnessCommandChoice struct {
	command prompt.Command
	source  server.CommandSourceBinding
}

func (b *resolvedCommandBinding) observe(ctx context.Context) (map[string]harnessCommandChoice, error) {
	listed := make(map[HarnessSourceID]map[string]prompt.Command, len(b.sources))
	bindings := make(map[HarnessSourceID]server.CommandSourceBinding, len(b.sources))
	allNames := make(map[string]struct{})
	for _, source := range b.sources {
		commands, err := source.binding.List(ctx)
		if err != nil {
			return nil, err
		}
		entries := make(map[string]prompt.Command, len(commands))
		for _, command := range commands {
			if !prompt.ValidCommandName(command.Name) || b.policy.excludes(source.id, command.Name) {
				continue
			}
			entries[command.Name] = command
			allNames[command.Name] = struct{}{}
		}
		listed[source.id], bindings[source.id] = entries, source.binding
		if b.policy.mode == harnessModeReplace && len(entries) > 0 {
			break
		}
	}
	out := make(map[string]harnessCommandChoice, len(allNames))
	for name := range allNames {
		present := make(map[HarnessSourceID]bool, len(listed))
		for id, entries := range listed {
			_, present[id] = entries[name]
		}
		if winner, ok := chooseHarnessWinner(name, present, b.policy); ok {
			out[name] = harnessCommandChoice{command: listed[winner][name], source: bindings[winner]}
		}
	}
	return out, nil
}

func (b *resolvedCommandBinding) List(ctx context.Context) ([]prompt.Command, error) {
	observed, err := b.observe(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]prompt.Command, 0, len(observed))
	for _, choice := range observed {
		out = append(out, choice.command)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (b *resolvedCommandBinding) Expand(ctx context.Context, input string) (string, bool, error) {
	name := commandInvocationName(input)
	if name == "" {
		return input, false, nil
	}
	observed, err := b.observe(ctx)
	if err != nil {
		return input, false, err
	}
	choice, ok := observed[name]
	if !ok {
		return input, false, nil
	}
	return choice.source.Expand(ctx, input)
}

func commandInvocationName(input string) string {
	s := strings.TrimLeft(input, " \t")
	if !strings.HasPrefix(s, "/") {
		return ""
	}
	s = s[1:]
	for i, r := range s {
		if !prompt.ValidCommandName(string(r)) {
			return s[:i]
		}
	}
	return s
}

func (p harnessKindPolicy) excludes(source HarnessSourceID, name string) bool {
	_, excluded := p.exclude[name][source]
	return excluded
}

type commandBindingEntry struct {
	principal *session.Principal
	profile   string
	binding   server.CommandSourceBinding
	cleanup   []func() error
	refs      int
	retired   bool
}

type harnessCommandResolver struct {
	mu           sync.Mutex
	sourceConfig *Config
	policy       harnessKindPolicy
	regs         map[HarnessSourceID]HarnessSourceRegistration[server.CommandSourceBinding]
	process      map[HarnessSourceID]boundCommandSource
	close        []func() error
	entries      map[session.SessionID]*commandBindingEntry
	generations  map[*commandBindingEntry]struct{}
	retired      map[session.SessionID]struct{}
	closed       bool
}

func newHarnessCommandResolver(ctx context.Context, policy harnessKindPolicy, regs []HarnessSourceRegistration[server.CommandSourceBinding]) (*harnessCommandResolver, error) {
	return initializeHarnessCommandResolver(ctx, policy, regs, &harnessCommandResolver{})
}

func initializeHarnessCommandResolver(ctx context.Context, policy harnessKindPolicy, regs []HarnessSourceRegistration[server.CommandSourceBinding], r *harnessCommandResolver) (*harnessCommandResolver, error) {
	r.policy = policy
	r.regs = make(map[HarnessSourceID]HarnessSourceRegistration[server.CommandSourceBinding])
	r.process = make(map[HarnessSourceID]boundCommandSource)
	r.entries = make(map[session.SessionID]*commandBindingEntry)
	r.retired = make(map[session.SessionID]struct{})
	r.generations = make(map[*commandBindingEntry]struct{})
	for _, reg := range regs {
		if _, selected := harnessSourcePosition(policy.sources, reg.ID); !selected {
			continue
		}
		r.regs[reg.ID] = reg
		if reg.Scope != HarnessSourceScopeProcess {
			continue
		}
		binding, cleanup, err := reg.Bind(ctx, HarnessSourceScope{})
		if cleanup != nil {
			r.close = append(r.close, cleanup)
		}
		if err != nil {
			r.closeProcess()
			return nil, fmt.Errorf("bind process command source %q: %w", reg.ID, err)
		}
		if binding == nil {
			r.closeProcess()
			return nil, fmt.Errorf("bind process command source %q returned nil", reg.ID)
		}
		r.process[reg.ID] = boundCommandSource{id: reg.ID, binding: binding}
	}
	return r, nil
}

func (r *harnessCommandResolver) Activate(ctx context.Context, id session.SessionID, principal *session.Principal, profile string) error {
	_, release, err := r.borrow(ctx, id, principal, profile, true)
	if err == nil {
		release()
	}
	return err
}

func (r *harnessCommandResolver) Borrow(ctx context.Context, id session.SessionID, principal *session.Principal, profile string) (server.CommandSourceBinding, func(), error) {
	return r.borrow(ctx, id, principal, profile, false)
}

func (r *harnessCommandResolver) borrow(ctx context.Context, id session.SessionID, principal *session.Principal, profile string, activate bool) (server.CommandSourceBinding, func(), error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, nil, fmt.Errorf("harness command resolver is closed")
	}
	if _, retired := r.retired[id]; retired && !activate {
		r.mu.Unlock()
		return nil, nil, fmt.Errorf("harness command binding is retired")
	}
	if entry := r.entries[id]; entry != nil {
		if !sameHarnessPrincipal(entry.principal, principal) || entry.profile != profile {
			r.mu.Unlock()
			return nil, nil, fmt.Errorf("harness command binding identity mismatch")
		}
		if !entry.retired {
			entry.refs++
			release := r.release(entry)
			binding := entry.binding
			r.mu.Unlock()
			return binding, release, nil
		}
	}
	sources := make([]boundCommandSource, 0, len(r.policy.sources))
	cleanups := make([]func() error, 0)
	scopeConfig, scopeCleanup, scopeErr := r.bindSessionSources(ctx, HarnessSourceScope{Principal: principal.Clone(), Profile: profile})
	if scopeErr != nil {
		r.mu.Unlock()
		return nil, nil, scopeErr
	}
	if scopeCleanup != nil {
		cleanups = append(cleanups, scopeCleanup)
	}
	for _, sourceID := range r.policy.sources {
		if sourceID == "skills" && scopeConfig != nil {
			metas, err := scopeConfig.harnessSkills.ListSkills(ctx)
			if err != nil {
				r.mu.Unlock()
				closeHarnessCleanups(cleanups)
				return nil, nil, err
			}
			sources = append(sources, boundCommandSource{id: sourceID, binding: prompt.NewSourceExpander(skills.NewSkillCommandSource(metas, scopeConfig.harnessSkills))})
			continue
		}
		if process, ok := r.process[sourceID]; ok {
			sources = append(sources, process)
			continue
		}
		reg := r.regs[sourceID]
		binding, cleanup, err := reg.Bind(ctx, HarnessSourceScope{Principal: principal.Clone(), Profile: profile})
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
		if err != nil || binding == nil {
			r.mu.Unlock()
			for i := len(cleanups) - 1; i >= 0; i-- {
				_ = cleanups[i]()
			}
			if err == nil {
				err = fmt.Errorf("source returned nil binding")
			}
			return nil, nil, fmt.Errorf("bind command source %q: %w", sourceID, err)
		}
		sources = append(sources, boundCommandSource{id: sourceID, binding: binding})
	}
	entry := &commandBindingEntry{principal: principal.Clone(), profile: profile, binding: &resolvedCommandBinding{context: scopeConfig, policy: r.policy, sources: sources}, cleanup: cleanups, refs: 1}
	entry.binding.(*resolvedCommandBinding).generation = harnessGeneration{resolver: r, entry: entry}
	r.entries[id] = entry
	r.generations[entry] = struct{}{}
	delete(r.retired, id)
	release := r.release(entry)
	binding := entry.binding
	r.mu.Unlock()
	return binding, release, nil
}

func (r *harnessCommandResolver) release(target *commandBindingEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			if target.refs > 0 {
				target.refs--
			}
			cleanup := target.retired && target.refs == 0
			if cleanup {
				delete(r.generations, target)
			}
			processCleanup := r.takeProcessCleanupLocked()
			r.mu.Unlock()
			if cleanup {
				closeHarnessCleanups(target.cleanup)
			}
			closeHarnessCleanups(processCleanup)
		})
	}
}

func (r *harnessCommandResolver) Retire(id session.SessionID) {
	r.mu.Lock()
	r.retired[id] = struct{}{}
	entry := r.entries[id]
	if entry == nil || entry.retired {
		r.mu.Unlock()
		return
	}
	entry.retired = true
	cleanup := entry.refs == 0
	if cleanup {
		delete(r.generations, entry)
	}
	r.mu.Unlock()
	if cleanup {
		closeHarnessCleanups(entry.cleanup)
	}
}

func (r *harnessCommandResolver) Close() {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	var cleanup [][]func() error
	for entry := range r.generations {
		entry.retired = true
		if entry.refs == 0 {
			delete(r.generations, entry)
			cleanup = append(cleanup, entry.cleanup)
		}
	}
	processCleanup := r.takeProcessCleanupLocked()
	r.mu.Unlock()
	for _, funcs := range cleanup {
		closeHarnessCleanups(funcs)
	}
	closeHarnessCleanups(processCleanup)
}

func (r *harnessCommandResolver) takeProcessCleanupLocked() []func() error {
	if !r.closed || len(r.generations) != 0 {
		return nil
	}
	cleanup := r.close
	r.close = nil
	return cleanup
}

func (r *harnessCommandResolver) closeProcess() {
	closeHarnessCleanups(r.close)
	r.close = nil
}

type staticCommandResolver struct {
	binding server.CommandSourceBinding
}

func (r staticCommandResolver) Borrow(context.Context, session.SessionID, *session.Principal, string) (server.CommandSourceBinding, func(), error) {
	return r.binding, func() {}, nil
}
func (staticCommandResolver) Activate(context.Context, session.SessionID, *session.Principal, string) error {
	return nil
}
func (staticCommandResolver) Retire(session.SessionID) {}

type fixedInstructionAssembler struct {
	inner           prompt.InstructionAssembler
	provenance      HarnessProvenancePolicy
	projectAdmitted bool
}

func (a fixedInstructionAssembler) Assemble(ctx context.Context) ([]session.Message, error) {
	messages, _, err := a.AssembleWithManifest(ctx)
	return messages, err
}
func (a fixedInstructionAssembler) AssembleWithManifest(ctx context.Context) ([]session.Message, []prompt.InstructionManifest, error) {
	messages, manifest, err := prompt.AssembleWithManifest(ctx, a.inner)
	if err != nil {
		return nil, nil, err
	}
	if len(messages) != len(manifest) {
		return nil, nil, fmt.Errorf("instruction manifest count mismatch")
	}
	var out []session.Message
	var metadata []prompt.InstructionManifest
	for i, row := range manifest {
		if row.Provenance == prompt.InstructionProvenanceProject && !a.projectAdmitted {
			continue
		}
		if a.provenance.Fixed != "" {
			if row.Provenance == prompt.InstructionProvenanceProject && a.provenance.Fixed != harnessProjectTier {
				return nil, nil, fmt.Errorf("root instruction provenance must remain project")
			}
			row.Provenance = a.provenance.Fixed
		} else if !tierAllowed(row.Provenance, a.provenance.PreserveAllowed) {
			return nil, nil, fmt.Errorf("instruction source emitted disallowed provenance")
		}
		if row.Provenance == harnessProjectTier && !a.projectAdmitted {
			continue
		}
		out = append(out, messages[i])
		metadata = append(metadata, row)
	}
	return out, metadata, nil
}

type policyInstructionAssembler struct {
	mode    string
	sources []prompt.InstructionAssembler
}

func (a policyInstructionAssembler) Assemble(ctx context.Context) ([]session.Message, error) {
	messages, _, err := a.AssembleWithManifest(ctx)
	return messages, err
}
func (a policyInstructionAssembler) AssembleWithManifest(ctx context.Context) ([]session.Message, []prompt.InstructionManifest, error) {
	var out []session.Message
	var metadata []prompt.InstructionManifest
	for _, source := range a.sources {
		messages, manifest, err := prompt.AssembleWithManifest(ctx, source)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, messages...)
		metadata = append(metadata, manifest...)
		if a.mode == harnessModeReplace && len(messages) != 0 {
			break
		}
	}
	return out, metadata, nil
}

func resolveProcessHarnessInstructions(ctx context.Context, cfg *Config) error {
	resolver, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok || resolver.OperatorHarnessContext() == nil {
		return nil
	}
	section := resolver.OperatorHarnessContext()
	enabled := make(map[HarnessSourceID]struct{}, len(section.EnabledSources))
	for _, raw := range section.EnabledSources {
		enabled[HarnessSourceID(raw)] = struct{}{}
	}
	policy, err := compileHarnessKind("instructions", section.Kinds.Instructions, enabled, harnessRegistrationIDs(cfg.HarnessInstructionSources), false)
	if err != nil {
		return err
	}
	regs := make(map[HarnessSourceID]HarnessSourceRegistration[prompt.InstructionAssembler], len(cfg.HarnessInstructionSources))
	for _, reg := range cfg.HarnessInstructionSources {
		regs[reg.ID] = reg
	}
	var sources []prompt.InstructionAssembler
	var cleanups []func() error
	for _, id := range policy.sources {
		if _, excluded := policy.exclude[""][id]; excluded {
			continue
		}
		reg := regs[id]
		if reg.Provenance.Fixed == harnessProjectTier && !projectIngestionAdmitted(*cfg) {
			continue
		}
		if reg.Scope == HarnessSourceScopePrincipal && cfg.harnessScope == nil {
			continue
		}
		assembler, cleanup, bindErr := reg.Bind(ctx, harnessBindingScope(*cfg))
		if bindErr != nil || assembler == nil {
			if cleanup != nil {
				_ = cleanup()
			}
			closeHarnessCleanups(cleanups)
			if bindErr == nil {
				bindErr = fmt.Errorf("source returned nil assembler")
			}
			return fmt.Errorf("bind instruction source %q: %w", id, bindErr)
		}
		if cleanup != nil {
			cleanups = append(cleanups, cleanup)
		}
		assembler = fixedInstructionAssembler{inner: assembler, provenance: reg.Provenance, projectAdmitted: projectIngestionAdmitted(*cfg)}
		sources = append(sources, assembler)
	}
	cfg.harnessInstructions = policyInstructionAssembler{mode: policy.mode, sources: sources}
	cfg.harnessContextClose = func() { closeHarnessCleanups(cleanups) }
	return nil
}

func buildConfiguredCommandResolver(ctx context.Context, cfg Config, mcpProvider mcp.Provider) (server.CommandSourceResolver, error) {
	section, err := validateHarnessPolicy(cfg)
	if err != nil {
		return nil, err
	}
	if section == nil {
		return buildCommandLister(cfg, mcpProvider), nil
	}
	enabled := make(map[HarnessSourceID]struct{}, len(section.EnabledSources))
	for _, id := range section.EnabledSources {
		enabled[HarnessSourceID(id)] = struct{}{}
	}
	commands, err := compileHarnessKind("commands", section.Kinds.Commands, enabled, harnessRegistrationIDs(cfg.HarnessCommandSources), true)
	if err != nil {
		return nil, err
	}
	regs := append([]HarnessSourceRegistration[server.CommandSourceBinding](nil), cfg.HarnessCommandSources...)
	if !projectIngestionAdmitted(cfg) {
		for i := range regs {
			if regs[i].Provenance.Fixed != harnessProjectTier {
				continue
			}
			regs[i].Bind = func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
				return prompt.NoopExpander{}, nil, nil
			}
		}
	}
	target := cfg.harnessResolver
	if target == nil {
		target = &harnessCommandResolver{}
	}
	resolver, err := initializeHarnessCommandResolver(ctx, commands, regs, target)
	if err != nil {
		return nil, err
	}
	resolver.sourceConfig = &cfg
	if cfg.harnessContextClose != nil {
		resolver.close = append(resolver.close, func() error { cfg.harnessContextClose(); return nil })
	}
	return resolver, nil
}

func validateHarnessPolicy(cfg Config) (*permconfig.HarnessContextSection, error) {
	if err := validateHarnessRegistrations(cfg); err != nil {
		return nil, err
	}
	resolver, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok {
		return nil, nil
	}
	if err := resolver.HarnessContextError(); err != nil {
		return nil, err
	}
	if resolver.OperatorHarnessContext() == nil {
		return nil, nil
	}
	section := resolver.OperatorHarnessContext()
	enabled := make(map[HarnessSourceID]struct{}, len(section.EnabledSources))
	for _, raw := range section.EnabledSources {
		id := HarnessSourceID(raw)
		if !harnessSourceIDPattern.MatchString(raw) {
			return nil, fmt.Errorf("harness_context enabled source has invalid id %q", raw)
		}
		if _, duplicate := enabled[id]; duplicate {
			return nil, fmt.Errorf("harness_context enabled source %q is duplicated", id)
		}
		enabled[id] = struct{}{}
	}
	instructions, err := compileHarnessKind("instructions", section.Kinds.Instructions, enabled, harnessRegistrationIDs(cfg.HarnessInstructionSources), false)
	if err != nil {
		return nil, err
	}
	commands, err := compileHarnessKind("commands", section.Kinds.Commands, enabled, harnessRegistrationIDs(cfg.HarnessCommandSources), true)
	if err != nil {
		return nil, err
	}
	rules, err := compileHarnessKind("rules", section.Kinds.Rules, enabled, harnessRegistrationIDs(cfg.HarnessRulesSources), true)
	if err != nil {
		return nil, err
	}
	skillPolicy, err := compileHarnessKind("skills", section.Kinds.Skills, enabled, harnessRegistrationIDs(cfg.HarnessSkillSources), true)
	if err != nil {
		return nil, err
	}
	agents, err := compileHarnessKind("agent_defs", section.Kinds.AgentDefs, enabled, harnessRegistrationIDs(cfg.HarnessAgentDefSources), true)
	if err != nil {
		return nil, err
	}
	used := make(map[HarnessSourceID]struct{})
	for _, policy := range []harnessKindPolicy{instructions, commands, rules, skillPolicy, agents} {
		for _, id := range policy.sources {
			used[id] = struct{}{}
		}
	}
	for id := range enabled {
		if _, ok := used[id]; !ok {
			return nil, fmt.Errorf("harness_context enabled source %q is unused", id)
		}
	}
	return section, nil
}

func validateHarnessRegistrations(cfg Config) error {
	if err := validateHarnessRegistration("instructions", cfg.HarnessInstructionSources); err != nil {
		return err
	}
	if err := validateHarnessRegistration("commands", cfg.HarnessCommandSources); err != nil {
		return err
	}
	if err := validateHarnessRegistration("rules", cfg.HarnessRulesSources); err != nil {
		return err
	}
	if err := validateHarnessRegistration("skills", cfg.HarnessSkillSources); err != nil {
		return err
	}
	return validateHarnessRegistration("agent_defs", cfg.HarnessAgentDefSources)
}

func harnessRegistrationIDs[T any](regs []HarnessSourceRegistration[T]) map[HarnessSourceID]struct{} {
	ids := make(map[HarnessSourceID]struct{}, len(regs))
	for _, reg := range regs {
		ids[reg.ID] = struct{}{}
	}
	return ids
}

func sameHarnessPrincipal(a, b *session.Principal) bool {
	return a == nil && b == nil || a != nil && a.SameIdentity(b)
}

func closeHarnessCleanups(cleanups []func() error) {
	for i := len(cleanups) - 1; i >= 0; i-- {
		_ = cleanups[i]()
	}
}
