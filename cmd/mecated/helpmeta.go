package main

import (
	"flag"
	"fmt"
	"io"
	"sort"

	"github.com/stacklok/mecatl/internal/cliconfig"
)

// flagMeta annotates a registered flag for progressive mode-specific help.
// A flag NOT listed here is treated as advanced (excluded from common help,
// included in --help-all).
type flagMeta struct {
	// group is the stable heading under which the flag appears in common help.
	group string
	// common is true when the flag appears in the mode-appropriate common help
	// (the ~15-20 operator-facing flags).
	common bool
	// acp is the ACP-mode policy: "include" (present in both) or "exclude"
	// (server-boundary, absent from ACP help).
	acp string
}

const (
	acpInclude = "include"
	acpExclude = "exclude"
)

// Group headings for the progressive common-help renderer. Named constants (not
// repeated string literals) so goconst stays green and the headings have one
// definition each; groupOrder references the same constants.
const (
	groupWorkspaceSession = "Workspace & Session" //nolint:gosec // G101 false-positive on the word "Session"; not a credential
	groupProvider         = "Provider"
	groupPermissions      = "Permissions"
	groupSecurity         = "Security"
	groupTools            = "Tools"
	groupStorage          = "Storage"
	groupMemoryKnowledge  = "Memory & Knowledge"
	groupSkillsAgents     = "Skills & Agents"
	groupAgentTeams       = "Agent Teams & Delegation"
	groupMCP              = "MCP"
	groupServer           = "Server"
	groupObservability    = "Observability"
	groupDriver           = "Driver"
	groupScheduling       = "Scheduling"
	groupLLMResilience    = "LLM Resilience"
	groupContext          = "Context"
	groupGuardrails       = "Guardrails"
	groupCommands         = "Commands"
	groupWebSearch        = "Web Search"
	groupInfo             = "Info"
)

// flagMetaByFlag is the SINGLE source of grouping + common/advanced + ACP
// membership for every public flag registered in parseFlagsMode. A flag not
// listed here:
//   - is ADVANCED (excluded from common help),
//   - appears in --help-all,
//   - is acpInclude by default.
var flagMetaByFlag = map[string]flagMeta{
	// ── Server (serve-only) ───────────────────────────────────────────────
	"config":    {group: groupServer, common: false, acp: acpExclude},
	"grpc-addr": {group: groupServer, common: true, acp: acpExclude},
	"http-addr": {group: groupServer, common: true, acp: acpExclude},
	// Daemon hosting (issue #821 Scenario 8): what a SPAWNED local daemon needs.
	// Advanced — an operator running mecated by hand never sets them — and
	// server-boundary, so absent from ACP help (a stdio ACP client already has
	// its parent's lifetime and needs no socket or readiness barrier).
	"grpc-unix-socket": {group: groupServer, common: false, acp: acpExclude},
	"ready-file":       {group: groupServer, common: false, acp: acpExclude},
	"lifetime-pipe-fd": {group: groupServer, common: false, acp: acpExclude},
	"metrics-addr":     {group: groupServer, common: false, acp: acpExclude},

	// ── Security (serve-only) ─────────────────────────────────────────────
	"auth-token": {group: groupSecurity, common: true, acp: acpExclude},
	"tls-cert":   {group: groupSecurity, common: false, acp: acpExclude},
	"tls-key":    {group: groupSecurity, common: false, acp: acpExclude},
	"client-ca":  {group: groupSecurity, common: false, acp: acpExclude},
	"rate-limit": {group: groupSecurity, common: false, acp: acpExclude},
	"rate-burst": {group: groupSecurity, common: false, acp: acpExclude},
	// Caller identity (ADR 0204): advanced, server-boundary — an ACP client
	// speaks over stdio and has no authenticated edge.
	"oidc-issuer":             {group: groupSecurity, common: false, acp: acpExclude},
	"oidc-jwks-uri":           {group: groupSecurity, common: false, acp: acpExclude},
	"oidc-audience":           {group: groupSecurity, common: false, acp: acpExclude},
	"oidc-max-jwks-staleness": {group: groupSecurity, common: false, acp: acpExclude},
	// TEST-ONLY SSRF relaxation (see cliconfig.OIDCConfig): not common, and
	// acpExclude like its siblings — an ACP client has no business setting it.
	"oidc-insecure-allow-private-issuer": {group: groupSecurity, common: false, acp: acpExclude},
	"oidc-allow-private-https-issuer":    {group: groupSecurity, common: false, acp: acpExclude},
	"oidc-ca-cert-file":                  {group: groupSecurity, common: false, acp: acpExclude},

	// ── Observability (both) ────────────────────────────────────────────────
	"log-level":                {group: groupObservability, common: true, acp: acpInclude},
	"otlp-endpoint":            {group: groupObservability, common: false, acp: acpExclude},
	"otlp-protocol":            {group: groupObservability, common: false, acp: acpExclude},
	"otlp-insecure":            {group: groupObservability, common: false, acp: acpExclude},
	"flight-recorder":          {group: groupObservability, common: false, acp: acpExclude},
	"mutex-profile-fraction":   {group: groupObservability, common: false, acp: acpExclude},
	"block-profile-rate":       {group: groupObservability, common: false, acp: acpExclude},
	"perf-mcp":                 {group: groupObservability, common: false, acp: acpExclude},
	"goroutine-warn-threshold": {group: groupObservability, common: false, acp: acpExclude},
	"goroutine-warn-interval":  {group: groupObservability, common: false, acp: acpExclude},

	// ── Driver connectivity (serve-only) ──────────────────────────────────
	"driver-auth-token":            {group: groupDriver, common: false, acp: acpExclude},
	"driver-tls":                   {group: groupDriver, common: false, acp: acpExclude},
	"driver-tls-ca":                {group: groupDriver, common: false, acp: acpExclude},
	"driver-tls-cert":              {group: groupDriver, common: false, acp: acpExclude},
	"driver-tls-key":               {group: groupDriver, common: false, acp: acpExclude},
	"session-store-url":            {group: groupDriver, common: false, acp: acpExclude},
	"memory-store-url":             {group: groupDriver, common: false, acp: acpExclude},
	"event-log-url":                {group: groupDriver, common: false, acp: acpExclude},
	"schedule-store-url":           {group: groupDriver, common: false, acp: acpExclude},
	"skill-source-url":             {group: groupDriver, common: false, acp: acpExclude},
	"soul-source-url":              {group: groupDriver, common: false, acp: acpExclude},
	"agent-source-url":             {group: groupDriver, common: false, acp: acpExclude},
	"command-source-url":           {group: groupDriver, common: false, acp: acpExclude},
	"session-lease-url":            {group: groupDriver, common: false, acp: acpExclude},
	"session-lease-dir":            {group: groupDriver, common: false, acp: acpExclude},
	"session-lease-k8s-namespace":  {group: groupDriver, common: false, acp: acpExclude},
	"session-lease-ttl":            {group: groupDriver, common: false, acp: acpExclude},
	"session-lease-renew-interval": {group: groupDriver, common: false, acp: acpExclude},

	// ── Scheduling (serve-only; scheduler is in-process only) ───────────
	"no-scheduler":                      {group: groupScheduling, common: false, acp: acpExclude},
	"scheduler-tick-interval":           {group: groupScheduling, common: false, acp: acpExclude},
	"scheduler-min-interval":            {group: groupScheduling, common: false, acp: acpExclude},
	"scheduler-max-concurrent-fires":    {group: groupScheduling, common: false, acp: acpExclude},
	"schedule-fire-retention":           {group: groupScheduling, common: false, acp: acpExclude},
	"schedule-fire-retention-max-total": {group: groupScheduling, common: false, acp: acpExclude},

	// ── Workspace & Session (both; headless is serve-only) ───────────────
	"workspace": {group: groupWorkspaceSession, common: true, acp: acpInclude},
	"headless":  {group: groupWorkspaceSession, common: true, acp: acpExclude},

	// ── Provider (both) ───────────────────────────────────────────────────
	"model":                 {group: groupProvider, common: true, acp: acpInclude},
	"default-provider":      {group: groupProvider, common: true, acp: acpInclude},
	"default-model":         {group: groupProvider, common: true, acp: acpInclude},
	"openai":                {group: groupProvider, common: true, acp: acpInclude},
	"openai-base-url":       {group: groupProvider, common: false, acp: acpInclude},
	"openrouter-base-url":   {group: groupProvider, common: false, acp: acpInclude},
	"anthropic-base-url":    {group: groupProvider, common: false, acp: acpInclude},
	"opencode-base-url":     {group: groupProvider, common: false, acp: acpInclude},
	"auth-file":             {group: groupProvider, common: false, acp: acpInclude},
	"mock":                  {group: groupProvider, common: false, acp: acpInclude},
	"mock-script":           {group: groupProvider, common: false, acp: acpInclude},
	"toolhive-llm":          {group: groupProvider, common: false, acp: acpInclude},
	"toolhive-llm-base-url": {group: groupProvider, common: false, acp: acpInclude},
	"toolhive-llm-mode":     {group: groupProvider, common: false, acp: acpInclude},
	"reasoning-effort":      {group: groupProvider, common: false, acp: acpInclude},
	"no-prompt-cache":       {group: groupProvider, common: false, acp: acpInclude},
	"anthropic-cache-ttl":   {group: groupProvider, common: false, acp: acpInclude},

	// ── LLM resilience (both) ────────────────────────────────────────────
	"llm-max-attempts":        {group: groupLLMResilience, common: false, acp: acpInclude},
	"llm-per-attempt-timeout": {group: groupLLMResilience, common: false, acp: acpInclude},
	"llm-stream-idle-timeout": {group: groupLLMResilience, common: false, acp: acpInclude},
	"llm-breaker-threshold":   {group: groupLLMResilience, common: false, acp: acpInclude},
	"llm-breaker-cooldown":    {group: groupLLMResilience, common: false, acp: acpInclude},
	"max-run-tokens":          {group: groupLLMResilience, common: false, acp: acpInclude},
	"max-team-tokens":         {group: groupLLMResilience, common: false, acp: acpInclude},

	// ── Context management (both) ────────────────────────────────────────
	"compaction":              {group: groupContext, common: false, acp: acpInclude},
	"tokenizer":               {group: groupContext, common: false, acp: acpInclude},
	"context-window-override": {group: groupContext, common: false, acp: acpInclude},

	// ── Tools (both) ─────────────────────────────────────────────────────
	"shell":   {group: groupTools, common: true, acp: acpInclude},
	"no-bash": {group: groupTools, common: true, acp: acpInclude},

	// ── Posture & permissions (both) ─────────────────────────────────────
	"posture":                   {group: groupPermissions, common: true, acp: acpInclude},
	"deployment-id":             {group: groupServer, common: false, acp: acpInclude},
	"cors-origins":              {group: groupSecurity, common: false, acp: acpExclude},
	"yolo":                      {group: groupPermissions, common: true, acp: acpInclude},
	"trust-project":             {group: groupPermissions, common: true, acp: acpInclude},
	"permissions-conventional":  {group: groupPermissions, common: true, acp: acpInclude},
	"import-claude-permissions": {group: groupPermissions, common: false, acp: acpInclude},
	"permission-config":         {group: groupPermissions, common: false, acp: acpInclude},
	"plan-mode-auto-approve":    {group: groupPermissions, common: false, acp: acpInclude},
	"no-steer":                  {group: groupPermissions, common: false, acp: acpInclude},
	"authority-evaluator":       {group: groupPermissions, common: false, acp: acpExclude},
	"cedar-authority-policy":    {group: groupPermissions, common: false, acp: acpExclude},

	// ── Guardrails (both) ────────────────────────────────────────────────
	"guardrails-model": {group: groupGuardrails, common: false, acp: acpInclude},
	"guardrails":       {group: groupGuardrails, common: false, acp: acpInclude},

	// ── Storage (both) ───────────────────────────────────────────────────
	"store-dir":                      {group: groupStorage, common: true, acp: acpInclude},
	"memory-dir":                     {group: groupStorage, common: true, acp: acpInclude},
	"memory-consolidate-interval":    {group: groupStorage, common: false, acp: acpInclude},
	"child-retention":                {group: groupStorage, common: false, acp: acpInclude},
	"child-retention-max-per-family": {group: groupStorage, common: false, acp: acpInclude},
	"child-gc-interval":              {group: groupStorage, common: false, acp: acpInclude},
	"main-retention":                 {group: groupStorage, common: false, acp: acpInclude},
	"main-retention-max-total":       {group: groupStorage, common: false, acp: acpInclude},
	"acknowledge-main-retention":     {group: groupStorage, common: false, acp: acpInclude},

	// ── Soul & user model (both) ─────────────────────────────────────────
	"soul-file":                       {group: groupMemoryKnowledge, common: false, acp: acpInclude},
	"no-soul":                         {group: groupMemoryKnowledge, common: false, acp: acpInclude},
	"approve-soul":                    {group: groupMemoryKnowledge, common: false, acp: acpInclude},
	"soul-strict":                     {group: groupMemoryKnowledge, common: false, acp: acpInclude},
	"user-model-dir":                  {group: groupMemoryKnowledge, common: false, acp: acpInclude},
	"no-user-model":                   {group: groupMemoryKnowledge, common: false, acp: acpInclude},
	"user-model-review":               {group: groupMemoryKnowledge, common: false, acp: acpInclude},
	"user-model-review-interval":      {group: groupMemoryKnowledge, common: false, acp: acpInclude},
	"user-model-consolidate-interval": {group: groupMemoryKnowledge, common: false, acp: acpInclude},

	// ── Skills & agents (both) ───────────────────────────────────────────
	"skills-dir":                        {group: groupSkillsAgents, common: false, acp: acpInclude},
	"skills-conventional":               {group: groupSkillsAgents, common: false, acp: acpInclude},
	"skills-draft-dir":                  {group: groupSkillsAgents, common: false, acp: acpInclude},
	"skills-draft-similarity-threshold": {group: groupSkillsAgents, common: false, acp: acpInclude},
	"agents-dir":                        {group: groupSkillsAgents, common: false, acp: acpInclude},
	"agents-conventional":               {group: groupSkillsAgents, common: false, acp: acpInclude},

	// ── Agent teams & delegation (both) ──────────────────────────────────
	"subagent-model":                   {group: groupAgentTeams, common: false, acp: acpInclude},
	"model-alias":                      {group: groupAgentTeams, common: false, acp: acpInclude},
	"model-slot":                       {group: groupAgentTeams, common: false, acp: acpInclude},
	"subagent-ask-reviewer":            {group: groupAgentTeams, common: false, acp: acpInclude},
	"subagent-ask-reviewer-max-denies": {group: groupAgentTeams, common: false, acp: acpInclude},
	"subagent-ask-reviewer-policy":     {group: groupAgentTeams, common: false, acp: acpInclude},
	"subagent-model-router":            {group: groupAgentTeams, common: false, acp: acpInclude},
	"enable-parallel":                  {group: groupAgentTeams, common: true, acp: acpInclude},
	"fork-preserved-cap":               {group: groupAgentTeams, common: false, acp: acpInclude},
	"enable-teams":                     {group: groupAgentTeams, common: true, acp: acpInclude},

	// ── MCP (both) ───────────────────────────────────────────────────────
	"mcp-server":               {group: groupMCP, common: true, acp: acpInclude},
	"mcp-server-insecure-http": {group: groupMCP, common: false, acp: acpInclude},
	"mcp-resource-tools":       {group: groupMCP, common: false, acp: acpInclude},
	"mcp-prompts":              {group: groupMCP, common: false, acp: acpInclude},
	"toolhive":                 {group: groupMCP, common: false, acp: acpInclude},
	"toolhive-group":           {group: groupMCP, common: false, acp: acpInclude},

	// ── Slash commands (both) ─────────────────────────────────────────────
	"commands-dir":    {group: groupCommands, common: false, acp: acpInclude},
	"enable-commands": {group: groupCommands, common: false, acp: acpInclude},

	// ── Web search (both) ─────────────────────────────────────────────────
	"websearch":             {group: groupWebSearch, common: false, acp: acpInclude},
	"websearch-url":         {group: groupWebSearch, common: false, acp: acpInclude},
	"websearch-auth-header": {group: groupWebSearch, common: false, acp: acpInclude},
	"websearch-query-param": {group: groupWebSearch, common: false, acp: acpInclude},

	// ── Info (meta-flags, both modes) ────────────────────────────────────
	"help-all": {group: groupInfo, common: false, acp: acpInclude},
}

// groupOrder is the stable presentation order for groups in common help.
// A group NOT listed here appears after the last listed group, sorted
// alphabetically.
var groupOrder = []string{
	groupWorkspaceSession,
	groupProvider,
	groupPermissions,
	groupSecurity,
	groupTools,
	groupStorage,
	groupMemoryKnowledge,
	groupSkillsAgents,
	groupAgentTeams,
	groupMCP,
	groupServer,
	groupObservability,
}

// commonFlagNames returns the set of flag names marked common and appropriate
// for the given mode.
func commonFlagNames(mode commandMode) map[string]bool {
	names := make(map[string]bool)
	for name, m := range flagMetaByFlag {
		if !m.common {
			continue
		}
		if mode == modeACP && m.acp == acpExclude {
			continue
		}
		names[name] = true
	}
	return names
}

// acpExcludedNames returns the set of flag names to exclude from ACP exhaustive
// help (server-boundary flags). It is the per-flag ACP policy (acpExclude).
func acpExcludedNames() map[string]bool {
	names := make(map[string]bool)
	for name, m := range flagMetaByFlag {
		if m.acp == acpExclude {
			names[name] = true
		}
	}
	return names
}

// validateFlagMeta is the REAL completeness invariant over the full production
// FlagSet. It asserts that every registered public flag has a flagMetaByFlag
// entry, AND every flagMetaByFlag key names a real registered flag (no
// orphans). It is the
// validation seam called by a real parser/test (TestFlagMetaCompletenessOverRealFlagSet)
// over the FULL parseFlagsMode FlagSet — not a synthetic subset.
//
// A failure here means a flag was added/removed from parseFlagsMode without
// updating the progressive-help metadata, so --help / --help-all / ACP exclusion
// would silently drift.
func validateFlagMeta(fs *flag.FlagSet) error {
	// Every metadata key must name a real registered flag (no orphans).
	for name := range flagMetaByFlag {
		if fs.Lookup(name) == nil {
			return fmt.Errorf("flagMetaByFlag has orphan key %q: no such flag is registered in parseFlagsMode", name)
		}
	}
	// Every registered public flag must have metadata.
	var missing []string
	fs.VisitAll(func(f *flag.Flag) {
		if _, ok := flagMetaByFlag[f.Name]; !ok {
			missing = append(missing, f.Name)
		}
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("public flags registered in parseFlagsMode have no flagMetaByFlag entry (add them for correct common/help-all/ACP rendering): %v", missing)
	}
	return nil
}

// ── Help renderers ────────────────────────────────────────────────────────

// writeServeCommonHelp renders the task-oriented common flag list for
// `mecated serve --help`.
func writeServeCommonHelp(out io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(out, "Usage: mecated serve [flags]\n\n")
	_, _ = fmt.Fprintf(out, "Common flags grouped by task.  Run 'mecated serve --help-all' for the\n")
	_, _ = fmt.Fprintf(out, "full exhaustive reference including every advanced tuning knob.\n\n")

	renderGroupedCommon(out, fs, commonFlagNames(modeServe))
}

// writeAcpCommonHelp renders the ACP-applicable common flags for
// `mecated acp --help`.
func writeAcpCommonHelp(out io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(out, "Usage: mecated acp [flags]\n\n")
	_, _ = fmt.Fprintf(out, "Common flags for ACP (Agent Client Protocol over stdio) sessions.\n")
	_, _ = fmt.Fprintf(out, "Server-boundary flags (listener, TLS, metrics, drivers, scheduling)\n")
	_, _ = fmt.Fprintf(out, "are not shown here.  Run 'mecated acp --help-all' for the full ACP flag\n")
	_, _ = fmt.Fprintf(out, "reference.\n\n")

	renderGroupedCommon(out, fs, commonFlagNames(modeACP))
}

// renderGroupedCommon writes flags grouped by their assigned group heading,
// preserving groupOrder and skipping empty groups. Flags WITHIN each group are
// sorted by name so the common-help ordering is DETERMINISTIC (independent of
// the flag-registration order in parseFlagsMode, which changes as flags are
// added/moved).
func renderGroupedCommon(out io.Writer, fs *flag.FlagSet, common map[string]bool) {
	// Collect flags by group, iterating ALL metadata once and bucketing every
	// common flag into its group.
	type entry struct {
		name string
		f    *flag.Flag
	}
	groups := make(map[string][]entry)

	for name, m := range flagMetaByFlag {
		if !common[name] {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		groups[m.group] = append(groups[m.group], entry{name, f})
	}

	// Stable rendering order: groups listed in groupOrder first, then any
	// remaining group alphabetically.
	orderedGroups := make([]string, 0, len(groups))
	seen := make(map[string]bool)

	for _, grp := range groupOrder {
		if _, ok := groups[grp]; ok && !seen[grp] {
			orderedGroups = append(orderedGroups, grp)
			seen[grp] = true
		}
	}

	// Collect remaining groups and sort.
	var remaining []string
	for grp := range groups {
		if !seen[grp] {
			remaining = append(remaining, grp)
		}
	}
	sort.Strings(remaining)
	orderedGroups = append(orderedGroups, remaining...)

	for _, grp := range orderedGroups {
		entries := groups[grp]
		if len(entries) == 0 {
			continue
		}
		// Sort flags within the group by name for deterministic ordering.
		sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
		_, _ = fmt.Fprintf(out, "%s:\n", grp)
		for _, e := range entries {
			cliconfig.PrintFlagDefault(out, e.f)
		}
		_, _ = fmt.Fprintf(out, "\n")
	}
}

// writeServeHelpAll renders the exhaustive flag list for `mecated serve --help-all`.
// It uses the single cliconfig formatter (byte-identical to flag.PrintDefaults).
func writeServeHelpAll(out io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(out, "Usage: mecated serve [flags]\n\nFlags:\n")
	cliconfig.PrintDefaultsExcluding(out, fs, nil)
}

// writeAcpHelpAll renders the exhaustive ACP-applicable flag list for
// `mecated acp --help-all`. It excludes server-boundary flags (acpExcludedNames)
// via the single cliconfig formatter.
func writeAcpHelpAll(out io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(out, "Usage: mecated acp [flags]\n\nFlags:\n")
	cliconfig.PrintDefaultsExcluding(out, fs, acpExcludedNames())
}

// writeTopLevelHelpAll renders the exhaustive reference for the top-level
// command page: the command list plus every public flag a `mecated serve`
// invocation accepts, and a pointer to `mecated acp --help-all` for the
// ACP-scoped subset.
func writeTopLevelHelpAll(out io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(out, "Usage: mecated <command> [flags]\n\n")
	_, _ = fmt.Fprintf(out, "Commands:\n")
	_, _ = fmt.Fprintf(out, "  serve                   start the network daemon (gRPC + HTTP/SSE)\n")
	_, _ = fmt.Fprintf(out, "  acp                     serve the Agent Client Protocol over stdio\n")
	_, _ = fmt.Fprintf(out, "  import                  import a Codex or Claude Code session, skills, and workspace files\n")
	_, _ = fmt.Fprintf(out, "  config init             write/print the operator settings.yaml skeleton\n")
	_, _ = fmt.Fprintf(out, "  config daemon init      write/print the daemon.yaml listener-topology skeleton\n")
	_, _ = fmt.Fprintf(out, "  config daemon validate  strictly validate a daemon.yaml\n")
	_, _ = fmt.Fprintf(out, "  skills promote          promote a model-authored candidate skill\n")
	_, _ = fmt.Fprintf(out, "  perf-mcp print-config   print a paste-ready client .mcp.json\n")
	_, _ = fmt.Fprintf(out, "\nGlobal: mecated --version prints the build version and exits.\n")
	_, _ = fmt.Fprintf(out, "\nExhaustive serve-compatible flag reference (every public flag a\n")
	_, _ = fmt.Fprintf(out, "`mecated serve` invocation accepts):\n\n")
	_, _ = fmt.Fprintf(out, "Flags:\n")
	cliconfig.PrintDefaultsExcluding(out, fs, nil)
	_, _ = fmt.Fprintf(out, "\nNote: `mecated acp --help-all` lists the ACP-scoped subset\n")
	_, _ = fmt.Fprintf(out, "(server-boundary flags such as --grpc-addr/--tls-*/--metrics-addr\n")
	_, _ = fmt.Fprintf(out, "and the scheduler/driver knobs are omitted there).\n")
}
