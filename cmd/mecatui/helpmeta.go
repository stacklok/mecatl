package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/internal/cliconfig"
)

// flagApplicability annotates a registered mecatui flag for mode-specific
// applicability (ADR 0083 Phase 1) and progressive help. The applicability axis
// is BY NAME: a flag explicitly passed in a mode where it is not applicable is
// REJECTED (connect rejects embedded-only flags; local rejects remote-only
// flags). A flag NOT listed here is treated as advanced (excluded from common
// help, included in --help-all) and applicable to ALL modes — EXCEPT that an
// unknown applicability defaults to fail-closed REJECT in explicit modes via the
// metadata-completeness invariant (validateFlagApplicability), so a newly-added
// flag without a metadata entry cannot silently slip through applicability
// checks.
type flagApplicability struct {
	// group is the stable heading under which the flag appears in common help.
	group string
	// common is true when the flag appears in the mode-appropriate common help.
	common bool
	// local is true when the flag is applicable in `mecatui local`.
	local bool
	// connect is true when the flag is applicable in `mecatui connect ADDRESS`.
	connect bool
}

// Group headings for the progressive common-help renderer.
const (
	groupTransport      = "Transport"
	groupSession        = "Session"
	groupUI             = "UI"
	groupProvider       = "Provider"
	groupPermissions    = "Permissions"
	groupStorage        = "Storage"
	groupKnowledge      = "Memory & Knowledge"
	groupSkillsCommands = "Skills & Commands"
	groupDelegation     = "Agent Teams & Delegation"
	groupMCP            = "MCP"
	groupLLMResilience  = "LLM Resilience"
	groupObservability  = "Observability"
	groupInfo           = "Info"
)

// hiddenMecatuiFlags is the set of registered flags excluded from ALL rendered
// help. They are legacy/compatibility surfaces whose public entry points are the
// canonical commands; listing them in --help would be misleading. The metadata
// completeness invariant (validateFlagApplicability) explicitly allows exactly
// this set to have no flagApplicability entry.
var hiddenMecatuiFlags = map[string]bool{
	// --output-economy is the deprecated no-op flag (ADR 0041, superseded); it
	// stays parseable for legacy invocations but is never advertised.
	"output-economy": true,
}

// flagApplicabilityByFlag is the SINGLE source of grouping + common/advanced +
// local/connect applicability for every public flag registered in
// parseTransportFlags. A flag not listed here:
//   - is ADVANCED (excluded from common help),
//   - appears in --help-all,
//   - is rejected in BOTH explicit modes by default (fail-closed), UNLESS it is
//     in hiddenMecatuiFlags (the deprecated no-op, accepted everywhere as today).
//
// applicability is BY NAME: the explicit-mode applicability check (flagsVisit)
// reads this map, so a flag marked local-only rejects an explicit --flag in
// connect mode and vice versa. The legacy mode is NOT subject to by-name
// rejection (it retains today's acceptance behaviour).
var flagApplicabilityByFlag = map[string]flagApplicability{
	// ── Transport (remote-only) ───────────────────────────────────────────
	"server":     {group: groupTransport, common: true, local: false, connect: false}, // legacy --server; not applicable in either explicit mode (local embeds; connect takes ADDRESS)
	"auth-token": {group: groupTransport, common: true, local: false, connect: true},
	"tls":        {group: groupTransport, common: true, local: false, connect: true},
	"tls-ca":     {group: groupTransport, common: false, local: false, connect: true},
	"insecure":   {group: groupTransport, common: false, local: false, connect: true},

	// ── Session (shared) ──────────────────────────────────────────────────
	"workspace": {group: groupSession, common: true, local: true, connect: true},
	"mode":      {group: groupSession, common: true, local: true, connect: true},

	// ── UI (shared) ───────────────────────────────────────────────────────
	"theme":          {group: groupUI, common: true, local: true, connect: true},
	"theme-dir":      {group: groupUI, common: false, local: true, connect: true},
	"list-themes":    {group: groupUI, common: false, local: true, connect: true},
	"no-alt-screen":  {group: groupUI, common: true, local: true, connect: true},
	"inline":         {group: groupUI, common: false, local: true, connect: true},
	"no-mouse":       {group: groupUI, common: false, local: true, connect: true},
	"no-banner":      {group: groupUI, common: false, local: true, connect: true},
	"terminal-title": {group: groupUI, common: false, local: true, connect: true},
	"keymap":         {group: groupUI, common: false, local: true, connect: true},
	"quiet":          {group: groupUI, common: false, local: true, connect: false},

	// ── Provider (embedded-only) ──────────────────────────────────────────
	"model":                 {group: groupProvider, common: true, local: true, connect: false},
	"default-provider":      {group: groupProvider, common: false, local: true, connect: false},
	"default-model":         {group: groupProvider, common: false, local: true, connect: false},
	"openai-base-url":       {group: groupProvider, common: false, local: true, connect: false},
	"openrouter-base-url":   {group: groupProvider, common: false, local: true, connect: false},
	"anthropic-base-url":    {group: groupProvider, common: false, local: true, connect: false},
	"opencode-base-url":     {group: groupProvider, common: false, local: true, connect: false},
	"auth-file":             {group: groupProvider, common: false, local: true, connect: false},
	"mock":                  {group: groupProvider, common: true, local: true, connect: false},
	"no-bash":               {group: groupProvider, common: false, local: true, connect: false},
	"toolhive-llm":          {group: groupProvider, common: false, local: true, connect: false},
	"toolhive-llm-base-url": {group: groupProvider, common: false, local: true, connect: false},
	"reasoning-effort":      {group: groupProvider, common: false, local: true, connect: false},

	// ── LLM resilience (embedded-only) ────────────────────────────────────
	"llm-per-attempt-timeout": {group: groupLLMResilience, common: false, local: true, connect: false},
	"llm-stream-idle-timeout": {group: groupLLMResilience, common: false, local: true, connect: false},

	// ── Permissions (embedded-only) ───────────────────────────────────────
	"posture":       {group: groupPermissions, common: true, local: true, connect: false},
	"yolo":          {group: groupPermissions, common: true, local: true, connect: false},
	"trust-project": {group: groupPermissions, common: true, local: true, connect: false},

	// ── Storage (embedded-only) ───────────────────────────────────────────
	"store-dir":  {group: groupStorage, common: true, local: true, connect: false},
	"no-store":   {group: groupStorage, common: false, local: true, connect: false},
	"memory-dir": {group: groupStorage, common: false, local: true, connect: false},
	"no-memory":  {group: groupStorage, common: false, local: true, connect: false},

	// ── Memory & knowledge (embedded-only) ────────────────────────────────
	"soul-file":                  {group: groupKnowledge, common: false, local: true, connect: false},
	"no-soul":                    {group: groupKnowledge, common: false, local: true, connect: false},
	"approve-soul":               {group: groupKnowledge, common: false, local: true, connect: false},
	"soul-strict":                {group: groupKnowledge, common: false, local: true, connect: false},
	"user-model-dir":             {group: groupKnowledge, common: false, local: true, connect: false},
	"no-user-model":              {group: groupKnowledge, common: false, local: true, connect: false},
	"user-model-review":          {group: groupKnowledge, common: false, local: true, connect: false},
	"user-model-review-interval": {group: groupKnowledge, common: false, local: true, connect: false},

	// ── Skills & commands (embedded-only) ─────────────────────────────────
	"skills-dir":   {group: groupSkillsCommands, common: false, local: true, connect: false},
	"no-skills":    {group: groupSkillsCommands, common: false, local: true, connect: false},
	"commands-dir": {group: groupSkillsCommands, common: false, local: true, connect: false},
	"no-commands":  {group: groupSkillsCommands, common: false, local: true, connect: false},

	// ── Agent teams & delegation (embedded-only) ──────────────────────────
	"subagent-model":        {group: groupDelegation, common: false, local: true, connect: false},
	"model-alias":           {group: groupDelegation, common: false, local: true, connect: false},
	"model-slot":            {group: groupDelegation, common: false, local: true, connect: false},
	"subagent-model-router": {group: groupDelegation, common: false, local: true, connect: false},

	// ── MCP (embedded-only) ───────────────────────────────────────────────
	// (mecatui has no --mcp-server/--toolhive-group flags; the embedded server
	// enables ToolHive discovery implicitly. No MCP flags are registered.)

	// ── Observability (embedded-only) ─────────────────────────────────────
	"perf":                          {group: groupObservability, common: false, local: true, connect: false},
	"perf-addr":                     {group: groupObservability, common: false, local: true, connect: false},
	"perf-goroutine-warn-threshold": {group: groupObservability, common: false, local: true, connect: false},
	"perf-mcp":                      {group: groupObservability, common: false, local: true, connect: false},

	// ── Info (meta-flags, both modes) ─────────────────────────────────────
	"help-all": {group: groupInfo, common: false, local: true, connect: true},
}

// groupOrder is the stable presentation order for groups in common help.
var groupOrder = []string{
	groupTransport,
	groupSession,
	groupUI,
	groupProvider,
	groupPermissions,
	groupStorage,
	groupKnowledge,
	groupSkillsCommands,
	groupDelegation,
	groupLLMResilience,
	groupObservability,
}

// applicableIn reports whether a flag named `name` is applicable in mode. The
// legacy mode is NEVER subject to by-name rejection (it retains today's
// acceptance behaviour). For explicit modes, a flag with no metadata entry is
// REJECTED (fail-closed) unless it is in hiddenMecatuiFlags (the deprecated
// no-op, accepted everywhere as today) — this is the metadata-completeness
// invariant's runtime twin.
func applicableIn(name string, mode transportMode) bool {
	if isLegacyMode(mode) {
		return true
	}
	if hiddenMecatuiFlags[name] {
		return true
	}
	meta, ok := flagApplicabilityByFlag[name]
	if !ok {
		// Unknown metadata: fail closed (reject), never silently include.
		return false
	}
	switch mode {
	case modeLocal:
		return meta.local
	case modeConnect:
		return meta.connect
	}
	return false
}

// rejectInapplicableFlags returns a non-nil error naming the first explicitly-set
// flag that is not applicable in mode. It is PURE: given the parsed FlagSet's
// visited flags and the mode, it returns the rejection or nil. The legacy mode is
// a no-op (retains today's acceptance behaviour).
func rejectInapplicableFlags(fs *flag.FlagSet, mode transportMode) error {
	if isLegacyMode(mode) {
		return nil
	}
	var offenders []string
	fs.Visit(func(f *flag.Flag) {
		if !applicableIn(f.Name, mode) {
			offenders = append(offenders, f.Name)
		}
	})
	if len(offenders) == 0 {
		return nil
	}
	sort.Strings(offenders)
	return fmt.Errorf("flag(s) not applicable in %q mode: --%s (run 'mecatui %s --help' for the applicable flags)",
		mode, strings.Join(offenders, ", --"), mode)
}

// commonFlagNames returns the set of flag names marked common and applicable for
// the given mode.
func commonFlagNames(mode transportMode) map[string]bool {
	names := make(map[string]bool)
	for name, m := range flagApplicabilityByFlag {
		if !m.common {
			continue
		}
		switch mode {
		case modeLocal:
			if !m.local {
				continue
			}
		case modeConnect:
			if !m.connect {
				continue
			}
		}
		names[name] = true
	}
	return names
}

// validateFlagApplicability is the REAL completeness invariant over the full
// production FlagSet. It asserts that every registered public flag except the
// explicitly hidden legacy flags has a flagApplicabilityByFlag entry, AND every
// flagApplicabilityByFlag key names a real registered flag (no orphans). It is
// the validation seam called by a real parser/test
// (TestFlagApplicabilityCompletenessOverRealFlagSet) over the FULL
// parseTransportFlags FlagSet — not a synthetic subset.
//
// A failure here means a flag was added/removed from parseTransportFlags without
// updating the applicability metadata, so --help / --help-all / mode-specific
// applicability would silently drift.
func validateFlagApplicability(fs *flag.FlagSet) error {
	// Every metadata key must name a real registered flag (no orphans).
	for name := range flagApplicabilityByFlag {
		if fs.Lookup(name) == nil {
			return fmt.Errorf("flagApplicabilityByFlag has orphan key %q: no such flag is registered in parseTransportFlags", name)
		}
	}
	// Every registered public flag (not hidden) must have metadata.
	var missing []string
	fs.VisitAll(func(f *flag.Flag) {
		if hiddenMecatuiFlags[f.Name] {
			return
		}
		if _, ok := flagApplicabilityByFlag[f.Name]; !ok {
			missing = append(missing, f.Name)
		}
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("public flags registered in parseTransportFlags have no flagApplicabilityByFlag entry (add them for correct applicability/help rendering): %v", missing)
	}
	return nil
}

// ── Help renderers ────────────────────────────────────────────────────────

// writeLocalCommonHelp renders the task-oriented common flag list for
// `mecatui local --help`.
func writeLocalCommonHelp(out io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(out, "Usage: mecatui local [flags]\n\n")
	_, _ = fmt.Fprintf(out, "Host an embedded mecated server in-process over a private UNIX socket. mecatui\n")
	_, _ = fmt.Fprintf(out, "NEVER probes loopback in this mode — it always embeds. Run 'mecatui local\n")
	_, _ = fmt.Fprintf(out, "--help-all' for the full exhaustive reference including every embedded-server\n")
	_, _ = fmt.Fprintf(out, "tuning knob.\n\n")
	renderGroupedCommon(out, fs, commonFlagNames(modeLocal))
}

// writeConnectCommonHelp renders the task-oriented common flag list for
// `mecatui connect --help`.
func writeConnectCommonHelp(out io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(out, "Usage: mecatui connect ADDRESS [flags]\n\n")
	_, _ = fmt.Fprintf(out, "Dial a running mecated at ADDRESS (host:port). mecatui NEVER probes loopback and\n")
	_, _ = fmt.Fprintf(out, "NEVER embeds a server in this mode — the target must already be serving. Embedded-\n")
	_, _ = fmt.Fprintf(out, "server flags (--mock, --trust-project, provider knobs, …) are rejected here. Run\n")
	_, _ = fmt.Fprintf(out, "'mecatui connect --help-all' for the full reference.\n\n")
	renderGroupedCommon(out, fs, commonFlagNames(modeConnect))
}

// renderGroupedCommon writes flags grouped by their assigned group heading,
// preserving groupOrder and skipping empty groups. Flags WITHIN each group are
// sorted by name for deterministic ordering.
func renderGroupedCommon(out io.Writer, fs *flag.FlagSet, common map[string]bool) {
	type entry struct {
		name string
		f    *flag.Flag
	}
	groups := make(map[string][]entry)
	for name, m := range flagApplicabilityByFlag {
		if !common[name] {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		groups[m.group] = append(groups[m.group], entry{name, f})
	}
	orderedGroups := make([]string, 0, len(groups))
	seen := make(map[string]bool)
	for _, grp := range groupOrder {
		if _, ok := groups[grp]; ok && !seen[grp] {
			orderedGroups = append(orderedGroups, grp)
			seen[grp] = true
		}
	}
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
		sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
		_, _ = fmt.Fprintf(out, "%s:\n", grp)
		for _, e := range entries {
			cliconfig.PrintFlagDefault(out, e.f)
		}
		_, _ = fmt.Fprintf(out, "\n")
	}
}

// writeLocalHelpAll renders the exhaustive flag list for `mecatui local --help-all`.
// It uses the single cliconfig formatter (byte-identical to flag.PrintDefaults),
// excluding only the hiddenMecatuiFlags legacy surfaces that must never appear in
// help.
func writeLocalHelpAll(out io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(out, "Usage: mecatui local [flags]\n\nFlags:\n")
	cliconfig.PrintDefaultsExcluding(out, fs, hiddenMecatuiFlags)
}

// writeConnectHelpAll renders the exhaustive connect-applicable flag list for
// `mecatui connect --help-all`. It excludes embedded-server flags (local-only)
// AND the hiddenMecatuiFlags, via the single cliconfig formatter.
func writeConnectHelpAll(out io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(out, "Usage: mecatui connect ADDRESS [flags]\n\nFlags:\n")
	exclude := make(map[string]bool)
	for k := range hiddenMecatuiFlags {
		exclude[k] = true
	}
	for name, m := range flagApplicabilityByFlag {
		if !m.connect {
			exclude[name] = true
		}
	}
	cliconfig.PrintDefaultsExcluding(out, fs, exclude)
}

// writeLegacyHelpAll renders the help text for bare `mecatui --help-all`. The
// --help-all flag promises an EXHAUSTIVE reference, so the bare form provides the
// exhaustive flag reference (every public flag a mecatui invocation accepts) plus
// a brief compatibility note pointing at the canonical commands.
func writeLegacyHelpAll(out io.Writer, fs *flag.FlagSet) {
	_, _ = fmt.Fprintf(out, "Usage: mecatui <command> [flags]\n\n")
	_, _ = fmt.Fprintf(out, "Commands:\n")
	_, _ = fmt.Fprintf(out, "  local             host an embedded mecated server in-process\n")
	_, _ = fmt.Fprintf(out, "  connect ADDRESS   dial a running mecated at ADDRESS\n")
	_, _ = fmt.Fprintf(out, "\nExhaustive flag reference (every public flag a mecatui invocation accepts):\n\n")
	_, _ = fmt.Fprintf(out, "Flags:\n")
	cliconfig.PrintDefaultsExcluding(out, fs, hiddenMecatuiFlags)
	_, _ = fmt.Fprintf(out, "\nCompatibility note: bare 'mecatui [flags]' and 'mecatui --server ADDRESS' still\n")
	_, _ = fmt.Fprintf(out, "work but are deprecated; prefer 'mecatui local' / 'mecatui connect ADDRESS'.\n")
}
