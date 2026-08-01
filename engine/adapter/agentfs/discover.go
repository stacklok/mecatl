package agentfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	"github.com/stacklok/mecatl/engine/tool"
)

// AgentFileExt is the conventional extension of an agent-definition file. A def
// lives at <dir>/<name>.md (a FLAT file, not a <name>/AGENT.md subdir), matching
// Claude Code's .claude/agents/ layout.
const AgentFileExt = ".md"

// maxDescriptionBytes is the package-internal alias of the CANONICAL
// description cap, which lives next to the port
// (tool.MaxAgentDescriptionBytes) so every source — this frontmatter parser
// AND the remote-driver client — enforces the same number (the
// maxDescriptionLen = MaxCommandDescriptionRunes pattern). parseAgentDef
// truncates (rune-safe, with an ellipsis) and records a non-fatal warning
// when it trims.
const maxDescriptionBytes = tool.MaxAgentDescriptionBytes

// maxPromptBodyBytes is the package-internal alias of tool.MaxAgentBodyBytes
// (see maxDescriptionBytes for the canonical-cap rationale). parseAgentDef
// truncates and warns.
const maxPromptBodyBytes = tool.MaxAgentBodyBytes

// frontmatter is the parsed YAML header of an agent-def file. Only name and
// description are required; the rest are optional. Unknown/extra keys are ignored
// (forward-compat). The `tools`/`disallowedTools` fields use stringOrSlice so a
// real Claude-Code `.claude/agents` file ("tools: Read, Edit") parses the same as
// the YAML-array form.
type frontmatter struct {
	Name            string            `yaml:"name"`
	Description     string            `yaml:"description"`
	Tools           stringOrSlice     `yaml:"tools"`
	DisallowedTools stringOrSlice     `yaml:"disallowedTools"`
	Model           string            `yaml:"model"`
	Provider        string            `yaml:"provider"`
	PermissionMode  string            `yaml:"permissionMode"`
	MaxTurns        int               `yaml:"maxTurns"`
	MaxToolCalls    int               `yaml:"maxToolCalls"`
	Color           string            `yaml:"color"`
	Skills          stringOrSlice     `yaml:"skills"`
	MCPServers      mcpServerList     `yaml:"mcpServers"`
	Hooks           map[string]string `yaml:"hooks"`
	Memory          string            `yaml:"memory"`
}

// mcpServerList is the parsed `mcpServers` frontmatter. It accepts THREE forms
// interchangeably (Claude-Code tolerance, extended for inline servers):
//
//   - a scalar string ("a, b") → references to configured main servers;
//   - a YAML array of scalars (["a", "b"]) → references;
//   - a YAML array of mappings ({name, url, headers, ...}) → inline servers (or a
//     reference when the mapping carries only a name / no url).
//
// A mapping may also be MIXED with scalars in the same array. UnmarshalYAML
// normalises every entry into an AgentMCPServer (empty URL = reference) and records
// per-entry SKIP notes for entries it cannot use (an inline entry that is plainly a
// non-HTTP/stdio transport, or a nameless entry). The notes ride alongside the
// parsed servers so parseAgentDef can surface them as non-fatal diagnostics — a bad
// MCP entry never fails the whole def.
type mcpServerList struct {
	servers []AgentMCPServer
	notes   []string
}

// rawMCPMapping is one inline `mcpServers` mapping entry. Only name/url/headers are
// used; type/transport/command are inspected solely to REJECT a non-HTTP (e.g.
// stdio) entry with a diagnostic, per the streamable-HTTP-only constraint.
type rawMCPMapping struct {
	Name      string            `yaml:"name"`
	URL       string            `yaml:"url"`
	Headers   map[string]string `yaml:"headers"`
	Type      string            `yaml:"type"`
	Transport string            `yaml:"transport"`
	Command   string            `yaml:"command"`
}

func (l *mcpServerList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		for _, name := range splitList(node.Value) {
			l.servers = append(l.servers, AgentMCPServer{Name: name})
		}
		return nil
	case yaml.SequenceNode:
		for _, item := range node.Content {
			switch item.Kind {
			case yaml.ScalarNode:
				for _, name := range splitList(item.Value) {
					l.servers = append(l.servers, AgentMCPServer{Name: name})
				}
			case yaml.MappingNode:
				var m rawMCPMapping
				if err := item.Decode(&m); err != nil {
					return err
				}
				l.appendMapping(m)
			default:
				return fmt.Errorf("mcpServers entry: expected a string or a mapping, got YAML kind %d", item.Kind)
			}
		}
		return nil
	default:
		return fmt.Errorf("mcpServers: expected a string, a list, or a list of mappings, got YAML kind %d", node.Kind)
	}
}

// appendMapping normalises one inline mapping into an AgentMCPServer, recording a
// skip note (and dropping the entry) when it is unusable: nameless, or an inline
// server declaring a non-HTTP transport (stdio/command) — only streamable-HTTP is
// supported (CLAUDE.md: no stdio MCP, ever).
func (l *mcpServerList) appendMapping(m rawMCPMapping) {
	name := strings.TrimSpace(m.Name)
	url := strings.TrimSpace(m.URL)
	transport := strings.ToLower(strings.TrimSpace(m.Transport))
	typ := strings.ToLower(strings.TrimSpace(m.Type))
	if name == "" {
		l.notes = append(l.notes, "mcpServers entry skipped: missing a non-empty \"name\"")
		return
	}
	// Reject any inline entry that names a non-HTTP transport, or supplies a
	// command (the stdio shape), even if a url is also present — streamable-HTTP is
	// the only supported transport.
	if strings.TrimSpace(m.Command) != "" || isNonHTTPTransport(transport) || isNonHTTPTransport(typ) {
		l.notes = append(l.notes, fmt.Sprintf(
			"mcpServers entry %q skipped: only the streamable-HTTP transport is supported (stdio/command/non-HTTP transports are rejected)", name))
		return
	}
	l.servers = append(l.servers, AgentMCPServer{Name: name, URL: url, Headers: NormalizeHeaders(m.Headers)})
}

// isNonHTTPTransport reports whether a `type`/`transport` value names a transport
// this harness refuses. Empty, "http", "streamable-http", "streamable_http", and
// "sse" are accepted (all HTTP-family); anything else (notably "stdio") is rejected.
func isNonHTTPTransport(v string) bool {
	switch v {
	case "", "http", "streamable-http", "streamable_http", "streamablehttp", "sse":
		return false
	default:
		return true
	}
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

// stringOrSlice is a YAML field that accepts BOTH a sequence (["Read","Grep"])
// AND a single comma/space-separated scalar ("Read, Grep" or "Read Grep") for
// Claude-Code compatibility — CC's agent frontmatter writes `tools` as a string
// in some versions and an array in others. UnmarshalYAML normalises either form
// into a trimmed, empty-free []string.
type stringOrSlice []string

func (s *stringOrSlice) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		var arr []string
		if err := node.Decode(&arr); err != nil {
			return err
		}
		*s = splitList(arr...)
		return nil
	case yaml.ScalarNode:
		*s = splitList(node.Value)
		return nil
	default:
		return fmt.Errorf("expected a string or a list, got YAML kind %d", node.Kind)
	}
}

// splitList flattens its inputs, splitting any entry on commas and whitespace,
// trimming each token and dropping empties. It is the single normaliser for both
// the array and scalar `tools`/`disallowedTools` forms.
func splitList(in ...string) []string {
	var out []string
	for _, raw := range in {
		for _, tok := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\n'
		}) {
			if t := strings.TrimSpace(tok); t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

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

// Discover scans dir for agent defs and returns them. It is a thin convenience
// wrapper over DirSource for callers (and tests) that want single-directory
// discovery without composing a Source.
func Discover(dir string) ([]Discovered, []SkipError, error) {
	return DirSource{Dir: dir}.Agents(context.Background())
}

// parseAgentDef splits raw into YAML frontmatter and a markdown body and
// validates the required header fields. It returns a fatal reason string (with a
// zero AgentDef) on any structural problem so the caller records a SkipError and
// EXCLUDES the def (reason is "" on success), plus a slice of non-fatal warning
// notes for a def that IS kept (e.g. truncation). It is filesystem-free so every
// Source implementation can reuse it. The path parameter is retained for
// diagnostic messages; the parsed value object carries NO locator (the caller
// records the path on its Discovered.Detail / SkipError.Path channels).
//
// Tool names in Tools/DisallowedTools are NOT validated here — the parser is
// catalog-free. An unknown tool name becomes a resolve-time diagnostic in
// internal/app (against a concrete catalog), never a parse failure.
func parseAgentDef(raw []byte, _ string) (AgentDef, string, []string) {
	fmText, body, ok := SplitFrontmatter(string(raw))
	if !ok {
		return AgentDef{}, "missing YAML frontmatter (expected a leading '---' delimited block)", nil
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
		return AgentDef{}, fmt.Sprintf("malformed YAML frontmatter: %v", err), nil
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		return AgentDef{}, "frontmatter is missing a non-empty \"name\"", nil
	}
	desc := strings.TrimSpace(fm.Description)
	if desc == "" {
		return AgentDef{}, "frontmatter is missing a non-empty \"description\"", nil
	}

	var notes []string
	if len(desc) > maxDescriptionBytes {
		notes = append(notes, fmt.Sprintf(
			"description is %d bytes; truncated to the always-in-context cap of %d bytes",
			len(desc), maxDescriptionBytes))
		desc = TruncateRunes(desc, maxDescriptionBytes)
	}

	trimmedBody := strings.TrimSpace(body)
	if len(trimmedBody) > maxPromptBodyBytes {
		notes = append(notes, fmt.Sprintf(
			"body is %d bytes; truncated to the prompt-body cap of %d bytes (it is in-context every turn)",
			len(trimmedBody), maxPromptBodyBytes))
		trimmedBody = TruncateRunes(trimmedBody, maxPromptBodyBytes)
	}

	// Per-entry mcpServers diagnostics (a skipped stdio/nameless entry) are
	// non-fatal: the def is still kept, minus the unusable entry.
	notes = append(notes, fm.MCPServers.notes...)

	// `memory:` is a forgiving tier selector (mirrors the mcpServers skip-note
	// posture): only "" / "user" / "project" are accepted. "local" is DELIBERATELY
	// rejected (a scoped local-only memory is deferred). Any other value is a
	// non-fatal note and falls back to "" (no memory), so a shared .claude/agents
	// file naming a tier this harness doesn't support never fails the whole def.
	mem := strings.ToLower(strings.TrimSpace(fm.Memory))
	switch mem {
	case "", "user", "project":
		// accepted
	default:
		notes = append(notes, fmt.Sprintf(
			"memory: %q is not supported (use \"user\" or \"project\"); ignored", fm.Memory))
		mem = ""
	}

	return AgentDef{
		Name:            name,
		Description:     desc,
		Tools:           []string(fm.Tools),
		DisallowedTools: []string(fm.DisallowedTools),
		Model:           strings.TrimSpace(fm.Model),
		Provider:        strings.TrimSpace(fm.Provider),
		PermissionMode:  strings.TrimSpace(fm.PermissionMode),
		MaxTurns:        fm.MaxTurns,
		MaxToolCalls:    fm.MaxToolCalls,
		Color:           strings.TrimSpace(fm.Color),
		Skills:          []string(fm.Skills),
		MCPServers:      fm.MCPServers.servers,
		Hooks:           NormalizeHooks(fm.Hooks),
		Memory:          mem,
		Body:            trimmedBody,
	}, "", notes
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
