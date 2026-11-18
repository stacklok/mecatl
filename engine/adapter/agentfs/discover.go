package agentfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/agentdef"
	"github.com/stacklok/mecatl/engine/tool"
)

// AgentFileExt is the conventional extension of an agent-definition file. A def
// lives at <dir>/<name>.md (a FLAT file, not a <name>/AGENT.md subdir), matching
// Claude Code's .claude/agents/ layout.
const AgentFileExt = agentdef.FileExt

// DirSource is the local-OS-filesystem implementation of AgentSource: it produces
// the defs laid out as <Dir>/<name>.md (flat files) under a single directory.
type DirSource struct {
	// Dir is the directory to scan. An empty or absent Dir yields no defs (opt-in).
	Dir string
	// Label is an optional human-readable name for this source (e.g. "project",
	// "user", "explicit"), surfaced in diagnostics (it prefixes each Discovered
	// entry's Detail). It does not affect discovery.
	Label string
	// Tier is the admission tier stamped onto every def this source produces
	// (AgentDef.Origin on the port). ResolveSources sets it per conventional
	// location; a zero Tier defaults to tool.AgentOriginExplicit (a
	// hand-constructed source is an operator-configured location).
	Tier tool.AgentOrigin
}

// origin returns the admission tier stamped onto this source's defs: Tier when
// set, else tool.AgentOriginExplicit (the zero-value default).
func (s DirSource) origin() tool.AgentOrigin {
	if s.Tier != "" {
		return s.Tier
	}
	return tool.AgentOriginExplicit
}

// detail renders the adapter-private locator string for a def discovered at
// path: "<label>: <path>", or the bare path when the source carries no label.
// It is the NON-PORT diagnostics channel (Discovered.Detail / Registry.Detail)
// that replaced the old AgentDef.Path field.
func (s DirSource) detail(path string) string {
	if l := strings.TrimSpace(s.Label); l != "" {
		return l + ": " + path
	}
	return path
}

// Agents implements AgentSource for a single local directory. It scans Dir for
// flat <name>.md files, parses each one's YAML frontmatter and markdown body, and
// returns the valid defs sorted by name.
//
// It is forgiving by design: an empty or missing Dir yields no defs and no error
// (opt-in); a malformed or frontmatter-less file is SKIPPED and reported via the
// returned []SkipError rather than aborting the scan. It returns a non-nil error
// only for a genuine I/O fault reading the directory itself.
//
// A def's frontmatter `name` is the source of truth (NOT the filename); duplicate
// effective names WITHIN this directory are resolved keep-first in sorted-path
// order. Cross-source collisions are resolved one level up by MultiSource.
//
// Every kept def is stamped with this source's admission tier (Origin) and
// carried with its adapter-private locator (Detail = "<label>: <path>") —
// SkipError diagnostics keep the verbatim path as before.
func (s DirSource) Agents(_ context.Context) ([]Discovered, []SkipError, error) {
	dir := strings.TrimSpace(s.Dir)
	if dir == "" {
		return nil, nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("agents: read dir %q: %w", dir, err)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var (
		out   []Discovered
		skips []SkipError
		seen  = map[string]string{} // effective name -> path that claimed it
	)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), AgentFileExt) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			skips = append(skips, SkipError{Path: path, Reason: fmt.Sprintf("cannot read: %v", rerr), Fatal: true})
			continue
		}
		def, perr, notes := parseAgentDef(raw, path)
		if perr != "" {
			skips = append(skips, SkipError{Path: path, Reason: perr, Fatal: true})
			continue
		}
		// Notes are non-fatal: the def IS kept, just adjusted (truncated, an
		// mcpServers entry dropped, an unsupported memory tier ignored). Fatal
		// stays the zero value (false).
		for _, n := range notes {
			skips = append(skips, SkipError{Path: path, Reason: n})
		}
		if prev, dup := seen[def.Name]; dup {
			skips = append(skips, SkipError{
				Path:   path,
				Reason: fmt.Sprintf("duplicate agent name %q (already defined at %q)", def.Name, prev),
				Fatal:  true,
			})
			continue
		}
		seen[def.Name] = path
		def.Origin = s.origin()
		out = append(out, Discovered{Def: def, Detail: s.detail(path)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Def.Name < out[j].Def.Name })
	return out, skips, nil
}

// parseAgentDef parses raw with the shared agentdef format, the parser the
// identity broker's definition loader uses too, and maps the result to the
// port value object. It returns a fatal reason string (with a zero AgentDef)
// when raw is not a definition, so the caller records a SkipError and EXCLUDES
// the def (reason is "" on success), plus the non-fatal notes for a def that IS
// kept (e.g. truncation). The path parameter is retained for diagnostic
// messages; the parsed value object carries NO locator (the caller records the
// path on its Discovered.Detail / SkipError.Path channels).
//
// Tool names in Tools/DisallowedTools are NOT validated here — the parser is
// catalog-free. An unknown tool name becomes a resolve-time diagnostic in
// internal/app (against a concrete catalog), never a parse failure.
func parseAgentDef(raw []byte, _ string) (AgentDef, string, []string) {
	def, notes, err := agentdef.Parse(raw)
	if err != nil {
		return AgentDef{}, err.Error(), notes
	}
	var servers []AgentMCPServer
	for _, s := range def.MCPServers {
		servers = append(servers, AgentMCPServer{Name: s.Name, URL: s.URL, Headers: s.Headers})
	}
	return AgentDef{
		Name:            def.Name,
		Description:     def.Description,
		Tools:           def.Tools,
		DisallowedTools: def.DisallowedTools,
		Model:           def.Model,
		Provider:        def.Provider,
		PermissionMode:  def.PermissionMode,
		MaxTurns:        def.MaxTurns,
		MaxToolCalls:    def.MaxToolCalls,
		Color:           def.Color,
		Skills:          def.Skills,
		MCPServers:      servers,
		Hooks:           def.Hooks,
		Memory:          def.Memory,
		Body:            def.Body,
	}, "", notes
}

// NormalizeHeaders trims keys/values and drops empties, returning nil for an
// empty/absent map so a server with no headers carries a nil Headers. Exported
// so the remote-driver client applies the SAME normalization to wire headers
// (the values are SECRET-SHAPED and are never logged anywhere; see
// tool.AgentMCPServer.Headers).
func NormalizeHeaders(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if key == "" || val == "" {
			continue
		}
		out[key] = val
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NormalizeHooks trims each phase key and command value and drops any entry whose
// key or value is empty, returning nil for an empty/absent map so a def with no
// hooks carries a nil Hooks (not an empty non-nil map). It does NOT validate phase
// names against the governance taxonomy — that is a composition-time concern, kept
// out of this catalog-free parser (mirroring how tool names are not validated
// here). Exported so the remote-driver client applies the SAME normalization to
// wire hooks.
func NormalizeHooks(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		phase := strings.TrimSpace(k)
		cmd := strings.TrimSpace(v)
		if phase == "" || cmd == "" {
			continue
		}
		out[phase] = cmd
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
