package agentdef

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	yaml "github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/token"
)

// The parser below is the engine's agentfs parser (engine/adapter/agentfs,
// discover.go and carry.go) moved here. agentfs keeps its copy until it is
// switched to this package; change both together until then.

// Parse reads one definition file's bytes. It returns the definition and a
// note for each adjustment it made to a definition it kept, such as a
// truncated description, a skipped mcpServers entry, or an ignored memory
// tier. It returns an error when the file is not a definition: it has no
// frontmatter, the frontmatter is not valid YAML, or the name or description
// is missing. Error text never quotes the file, which can hold secrets.
//
// Tool names are not checked: an unknown tool name is the caller's concern,
// against a concrete tool catalog.
func Parse(raw []byte) (Definition, []string, error) {
	fmText, body, ok := splitFrontmatter(string(raw))
	if !ok {
		return Definition{}, nil, errors.New("missing YAML frontmatter (expected a leading '---' delimited block)")
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
		return Definition{}, nil, errors.New(frontmatterParseError(err))
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		return Definition{}, nil, errors.New("frontmatter is missing a non-empty \"name\"")
	}
	desc := strings.TrimSpace(fm.Description)
	if desc == "" {
		return Definition{}, nil, errors.New("frontmatter is missing a non-empty \"description\"")
	}

	var notes []string
	if len(desc) > MaxDescriptionBytes {
		notes = append(notes, fmt.Sprintf(
			"description is %d bytes; truncated to the always-in-context cap of %d bytes",
			len(desc), MaxDescriptionBytes))
		desc = truncateRunes(desc, MaxDescriptionBytes)
	}

	trimmedBody := strings.TrimSpace(body)
	if len(trimmedBody) > MaxBodyBytes {
		notes = append(notes, fmt.Sprintf(
			"body is %d bytes; truncated to the prompt-body cap of %d bytes (it is in-context every turn)",
			len(trimmedBody), MaxBodyBytes))
		trimmedBody = truncateRunes(trimmedBody, MaxBodyBytes)
	}

	// A skipped mcpServers entry is an adjustment: the definition is kept,
	// without the entry.
	notes = append(notes, fm.MCPServers.notes...)

	// Only "", "user", and "project" are memory tiers. Any other value, the
	// unimplemented "local" included, is ignored with a note rather than
	// failing the whole definition.
	mem := strings.ToLower(strings.TrimSpace(fm.Memory))
	switch mem {
	case "", "user", "project":
	default:
		notes = append(notes, fmt.Sprintf(
			"memory: %q is not supported (use \"user\" or \"project\"); ignored", fm.Memory))
		mem = ""
	}

	return Definition{
		Name:            name,
		Description:     desc,
		Tools:           canonicalToolNames(fm.Tools),
		DisallowedTools: canonicalToolNames(fm.DisallowedTools),
		Model:           strings.TrimSpace(fm.Model),
		Provider:        strings.TrimSpace(fm.Provider),
		PermissionMode:  strings.TrimSpace(fm.PermissionMode),
		MaxTurns:        fm.MaxTurns,
		MaxToolCalls:    fm.MaxToolCalls,
		Color:           strings.TrimSpace(fm.Color),
		Skills:          []string(fm.Skills),
		MCPServers:      fm.MCPServers.servers,
		Hooks:           normalizeHooks(fm.Hooks),
		Memory:          mem,
		Body:            trimmedBody,
	}, notes, nil
}

// frontmatter is the YAML header of a definition file. Only name and
// description are required. Unknown keys are ignored, so a newer file still
// parses. tools, disallowedTools, and skills accept a list or a single string
// of names separated by commas or spaces.
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

func (f *frontmatter) UnmarshalYAML(node ast.Node) error {
	type decoded frontmatter
	var value decoded
	if err := yaml.NodeToValue(node, &value); err != nil {
		return err
	}
	*f = frontmatter(value)
	for _, field := range []struct {
		name string
		dst  *stringOrSlice
	}{
		{"tools", &f.Tools},
		{"disallowedTools", &f.DisallowedTools},
		{"skills", &f.Skills},
	} {
		if value := frontmatterMappingValue(node, field.name); value != nil {
			if err := field.dst.UnmarshalYAML(value); err != nil {
				return err
			}
		}
	}
	return nil
}

func frontmatterMappingValue(node ast.Node, name string) ast.Node {
	mapping, ok := node.(*ast.MappingNode)
	if !ok {
		return nil
	}
	for _, value := range mapping.Values {
		if value.Key.GetToken().Value == name {
			return value.Value
		}
	}
	return nil
}

// mcpServerList is the mcpServers field. It accepts a string of names ("a,
// b"), a list of names, or a list of mappings ({name, url, headers}), mixed
// freely. A name, or a mapping without a URL, is a reference. An entry that
// cannot be used, nameless or declaring a non-HTTP transport, is dropped with
// a note, so one bad entry never fails the whole definition.
type mcpServerList struct {
	servers []MCPServer
	notes   []string
}

// rawMCPMapping is one mapping entry. type, transport, and command are read
// only to reject a non-HTTP (for example stdio) server.
type rawMCPMapping struct {
	Name      string            `yaml:"name"`
	URL       string            `yaml:"url"`
	Headers   map[string]string `yaml:"headers"`
	Type      string            `yaml:"type"`
	Transport string            `yaml:"transport"`
	Command   string            `yaml:"command"`
}

func (l *mcpServerList) UnmarshalYAML(node ast.Node) error {
	switch node.Type() {
	case ast.StringType:
		var name string
		if err := yaml.NodeToValue(node, &name); err != nil {
			return err
		}
		for _, name := range splitList(name) {
			l.servers = append(l.servers, MCPServer{Name: name})
		}
		return nil
	case ast.SequenceType:
		sequence, ok := node.(*ast.SequenceNode)
		if !ok {
			return fmt.Errorf("mcpServers: invalid sequence")
		}
		for _, item := range sequence.Values {
			switch item.Type() {
			case ast.StringType:
				var name string
				if err := yaml.NodeToValue(item, &name); err != nil {
					return err
				}
				for _, name := range splitList(name) {
					l.servers = append(l.servers, MCPServer{Name: name})
				}
			case ast.MappingType:
				var mapping rawMCPMapping
				if err := yaml.NodeToValue(item, &mapping); err != nil {
					return err
				}
				l.appendMapping(mapping)
			default:
				return fmt.Errorf("mcpServers entry: expected a string or a mapping")
			}
		}
		return nil
	default:
		return fmt.Errorf("mcpServers: expected a string, a list, or a list of mappings")
	}
}

// appendMapping adds one mapping entry, or drops it with a note when it has
// no name or declares a non-HTTP transport. Only streamable HTTP is supported:
// Mecatl never starts a stdio MCP server.
func (l *mcpServerList) appendMapping(m rawMCPMapping) {
	name := strings.TrimSpace(m.Name)
	url := strings.TrimSpace(m.URL)
	transport := strings.ToLower(strings.TrimSpace(m.Transport))
	typ := strings.ToLower(strings.TrimSpace(m.Type))
	if name == "" {
		l.notes = append(l.notes, "mcpServers entry skipped: missing a non-empty \"name\"")
		return
	}
	// A command (the stdio shape) or a non-HTTP transport rejects the entry,
	// even when it also has a URL.
	if strings.TrimSpace(m.Command) != "" || isNonHTTPTransport(transport) || isNonHTTPTransport(typ) {
		l.notes = append(l.notes, fmt.Sprintf(
			"mcpServers entry %q skipped: only the streamable-HTTP transport is supported (stdio/command/non-HTTP transports are rejected)", name))
		return
	}
	l.servers = append(l.servers, MCPServer{Name: name, URL: url, Headers: normalizeHeaders(m.Headers)})
}

// isNonHTTPTransport reports whether a type or transport value names a
// transport Mecatl refuses. Empty and the HTTP family are accepted.
func isNonHTTPTransport(v string) bool {
	switch v {
	case "", "http", "streamable-http", "streamable_http", "streamablehttp", "sse":
		return false
	default:
		return true
	}
}

// normalizeHeaders trims keys and values and drops empty ones. It returns nil
// for a map left empty, so a server with no headers has nil Headers. The
// values can be secrets: never log them.
func normalizeHeaders(in map[string]string) map[string]string {
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

// normalizeHooks trims each phase and command and drops empty ones. It returns
// nil for a map left empty. It does not check phase names.
func normalizeHooks(in map[string]string) map[string]string {
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

// stringOrSlice is a field that accepts a list ([Read, Grep]) or one string of
// names separated by commas or spaces ("Read, Grep"), as Claude Code's agent
// files use both.
type stringOrSlice []string

func (s *stringOrSlice) UnmarshalYAML(node ast.Node) error {
	if text, ok := frontmatterScalarText(node); ok {
		*s = splitList(text)
		return nil
	}
	if node.Type() != ast.SequenceType {
		return fmt.Errorf("expected a string or a list")
	}
	sequence, ok := node.(*ast.SequenceNode)
	if !ok {
		return fmt.Errorf("expected a string or a list")
	}
	values := make([]string, 0, len(sequence.Values))
	for _, item := range sequence.Values {
		text, ok := frontmatterScalarText(item)
		if !ok {
			return fmt.Errorf("expected a string or a list of strings")
		}
		values = append(values, text)
	}
	*s = splitList(values...)
	return nil
}

// frontmatterScalarText reads any scalar as its text, so tools: true or tools:
// 42 is the name "true" or "42".
func frontmatterScalarText(node ast.Node) (string, bool) {
	switch node.Type() {
	case ast.StringType, ast.LiteralType:
		var text string
		if err := yaml.NodeToValue(node, &text); err != nil {
			return "", false
		}
		return text, true
	case ast.BoolType, ast.IntegerType, ast.FloatType, ast.NullType, ast.InfinityType, ast.NanType:
		if parserToken := node.GetToken(); parserToken != nil {
			return parserToken.Value, true
		}
	}
	return "", false
}

// splitList splits each input on commas and whitespace, trims each name, and
// drops empty ones. It returns nil when no name is left.
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

func canonicalToolNames(names []string) []string {
	out := append([]string(nil), names...)
	for i := range out {
		if out[i] == "Bash" {
			out[i] = shellToolName
		}
	}
	return out
}

// frontmatterParseError describes a YAML error by its position only, never by
// the parser's text, which can quote the file.
func frontmatterParseError(err error) string {
	var located interface{ GetToken() *token.Token }
	if errors.As(err, &located) {
		if parserToken := located.GetToken(); parserToken != nil && parserToken.Position != nil && parserToken.Position.Line > 0 && parserToken.Position.Column > 0 {
			return fmt.Sprintf("malformed YAML frontmatter at line %d, column %d", parserToken.Position.Line, parserToken.Position.Column)
		}
	}
	return "malformed YAML frontmatter"
}

// splitFrontmatter separates a leading frontmatter block from the body. The
// block starts with a "---" line at the very start and ends at the next line
// that is exactly "---". It returns the block without the delimiters, the
// body, and whether a well-formed block was found. A leading UTF-8 byte order
// mark is ignored and CRLF line endings are read as LF.
func splitFrontmatter(s string) (fm, body string, ok bool) {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") && s != "---" {
		return "", "", false
	}
	rest := strings.TrimPrefix(s, "---\n")
	idx := indexClosingDelim(rest)
	if idx < 0 {
		return "", "", false
	}
	fm = rest[:idx]
	after := rest[idx:]
	after = strings.TrimPrefix(after, "---")
	after = strings.TrimPrefix(after, "\n")
	return fm, after, true
}

// indexClosingDelim returns the byte offset in s of the first line that is
// exactly "---", or -1 if there is none.
func indexClosingDelim(s string) int {
	offset := 0
	for _, line := range strings.SplitAfter(s, "\n") {
		trimmed := strings.TrimSuffix(line, "\n")
		if trimmed == "---" {
			return offset
		}
		offset += len(line)
	}
	return -1
}

// truncateRunes trims s to at most maxBytes on a rune boundary, ending in a
// single-character ellipsis ("…"). A string that fits is returned unchanged.
func truncateRunes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	const ellipsis = "…"
	cut := maxBytes - len(ellipsis)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + ellipsis
}
