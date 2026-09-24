package prompt

import (
	"context"
	"errors"
	"io/fs"
	gopath "path"
	"sort"
	"strings"

	"github.com/stacklok/mecatl/engine/tool"
)

// Command is a discovered slash command's listing metadata: its invocation name
// (the "<name>" of "/<name>") and a short, human-facing description for a palette
// or help surface. It carries NO body — discovery is intentionally cheap and
// metadata-only; expansion (which reads the body) is a separate concern.
type Command struct {
	// Name is the command's invocation name (without the leading "/").
	Name string
	// Description is a short one-line summary derived from the command file: its
	// frontmatter `description:` field when present, else the first non-blank body
	// line, trimmed and length-capped. May be empty when neither yields text.
	Description string
}

// CommandLister is the discovery counterpart to CommandExpander: it enumerates
// the available commands (name + short description) WITHOUT expanding any. It is
// the seam a palette/help UI consumes to offer completion, kept separate from
// CommandExpander so an expander that cannot enumerate (e.g. a pure prompt
// source) need not implement it. List is read-only and cheap.
type CommandLister interface {
	// List returns the available commands from the already-bound source,
	// de-duplicated by name (first occurrence wins, matching expansion precedence) and sorted by
	// name. It returns a nil/empty slice when no commands are available. A non-nil
	// error is reserved for a genuine read fault enumerating the command dirs; a
	// dir that simply does not exist is not an error (it yields no commands).
	List(ctx context.Context) ([]Command, error)
}

// CommandExpander rewrites a raw user input into the prompt the model sees. If
// the input is a command invocation (e.g. "/review foo.go"), it expands the
// matching template; otherwise it returns the input unchanged (expanded=false).
//
// It is the seam that makes slash commands / templated prompts pluggable: the
// agent loop consumes this interface in recordPrompt instead of using the raw
// user text directly, so a richer expander can be wired at the composition root
// without touching the loop. The default implementation, NoopExpander, returns
// the input unchanged so behaviour is identical when no commands are configured.
type CommandExpander interface {
	// Expand inspects input. When input is a command invocation it loads the
	// matching template from the already-bound source, substitutes its placeholders, and returns the
	// rendered body with expanded=true. When input is not a command, or the named
	// command does not exist, it returns input unchanged with expanded=false.
	//
	// A non-nil error is returned only for a genuine read fault discovering or
	// reading a command file (not for "not a command" or "unknown command", which
	// are normal, non-error outcomes that must not abort the run).
	Expand(ctx context.Context, input string) (string, bool, error)
}

// NoopExpander is the default CommandExpander. It performs no expansion and
// returns every input unchanged (expanded=false). The zero value is ready to
// use; it is the default in agent.Deps so command expansion is OFF unless a
// DirCommandExpander (or another adapter) is explicitly wired in.
type NoopExpander struct{}

// Expand implements CommandExpander by returning input unchanged.
func (NoopExpander) Expand(_ context.Context, input string) (string, bool, error) {
	return input, false, nil
}

// List implements CommandLister by listing nothing: the NoopExpander has no
// command source, so a palette over it is empty.
func (NoopExpander) List(_ context.Context) ([]Command, error) {
	return nil, nil
}

// Compile-time assertions that NoopExpander satisfies both interfaces.
var (
	_ CommandExpander = NoopExpander{}
	_ CommandLister   = NoopExpander{}
)

// MultiExpander composes an ORDERED list of CommandExpanders into one with a
// first-that-expands-wins rule. It is the seam that lets several expansion
// sources (e.g. file-backed slash commands plus MCP prompts) be layered without
// the agent loop knowing about any of them. It is pure domain composition: it
// imports no infrastructure and only consumes the CommandExpander interface,
// mirroring skills.MultiSource's earlier-wins semantics.
//
// PRECEDENCE: the expanders are tried in slice order; the FIRST one that returns
// expanded=true wins and its rendered output is returned immediately. So an
// earlier expander SHADOWS a later one on a name collision (order callers
// highest-precedence-first). When no expander matches, the original input is
// returned unchanged with expanded=false. A non-nil error from any expander is a
// genuine read fault and is returned immediately (it stops the chain), matching
// the single-expander contract that errors are reserved for real I/O faults, not
// "not a command".
type MultiExpander struct {
	expanders []CommandExpander
}

// NewMultiExpander builds a MultiExpander over the given ordered expanders
// (highest precedence first). nil entries are dropped so callers can assemble the
// slice conditionally without nil checks.
func NewMultiExpander(expanders ...CommandExpander) *MultiExpander {
	filtered := make([]CommandExpander, 0, len(expanders))
	for _, e := range expanders {
		if e != nil {
			filtered = append(filtered, e)
		}
	}
	return &MultiExpander{expanders: filtered}
}

// Expand tries each composed expander in order and returns the first expansion
// (expanded=true). If none expands, it returns the original input unchanged with
// expanded=false. An error from any expander stops the chain and is returned.
func (m *MultiExpander) Expand(ctx context.Context, input string) (string, bool, error) {
	for _, e := range m.expanders {
		out, expanded, err := e.Expand(ctx, input)
		if err != nil {
			return input, false, err
		}
		if expanded {
			return out, true, nil
		}
	}
	return input, false, nil
}

// List aggregates the lists of every composed expander that ALSO implements
// CommandLister, applying the SAME first-wins precedence as Expand: the
// expanders are walked in slice order and the first occurrence of a name wins,
// so an earlier (higher-precedence) source shadows a later one on a name
// collision. An expander that does not implement CommandLister contributes
// nothing (it cannot enumerate). The merged result is de-duplicated by name and
// sorted. A read fault from any child stops the walk and is returned.
func (m *MultiExpander) List(ctx context.Context) ([]Command, error) {
	seen := make(map[string]struct{})
	var out []Command
	for _, e := range m.expanders {
		lister, ok := e.(CommandLister)
		if !ok {
			continue
		}
		cmds, err := lister.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, c := range cmds {
			if _, dup := seen[c.Name]; dup {
				continue
			}
			seen[c.Name] = struct{}{}
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Compile-time assertions that *MultiExpander satisfies both interfaces.
var (
	_ CommandExpander = (*MultiExpander)(nil)
	_ CommandLister   = (*MultiExpander)(nil)
)

// DefaultCommandDirs are the workspace-relative, PROJECT-TIER directories
// DirCommandExpander searches, in order, for a command's <name>.md file.
// ".mecatl/commands/" is the native location; ".claude/commands/" is accepted for
// familiarity. The first directory that contains a matching file wins.
//
// This is the CANONICAL project-tier command dir set: it is the single source of
// truth for (a) the expander's default search path here, (b) composition's
// untrusted-workspace command gate (internal/app/build.go), and (c) the
// workspace-trust identity anchor (internal/adapter/workspacetrust/anchor.go), so
// the gate's admission surface and the anchor's drift surface can never silently
// diverge. Treat it as read-only; copy before mutating.
var DefaultCommandDirs = []string{".mecatl/commands", ".claude/commands"}

// DirCommandExpander discovers command templates as <name>.md files under one or
// more workspace-relative directories (default ".mecatl/commands/" and
// ".claude/commands/"), read through the tool.Workspace FS port (never os, so
// the type stays infra-free / domain-pure).
//
// Invocation grammar: an input is a command iff, after trimming leading spaces,
// it begins with "/" followed by a non-empty command name made of letters,
// digits, '-', '_', or '.'. The remainder (after the name) is split on
// whitespace into positional arguments.
//
// Template substitution, applied to the command body:
//   - "$ARGUMENTS" → all arguments joined by a single space (empty if none).
//   - "$1", "$2", … → the corresponding positional argument (1-based); a
//     reference past the end of the argument list expands to the empty string.
//   - Any other "$"-prefixed token is left intact, so bodies may contain literal
//     shell-style variables the model is meant to see.
//
// Frontmatter: an optional leading YAML frontmatter block (delimited by a "---"
// line at the very start and a closing "---" line) is stripped before
// substitution, so only the body template is returned. The frontmatter is
// metadata (e.g. a description) and never reaches the model.
//
// Unknown command (no matching file in any directory) or non-command input
// returns the original input unchanged with expanded=false and no error, so a
// mistyped or unconfigured command never aborts the run.
type DirCommandExpander struct {
	source tool.Workspace
	dirs   []string
}

// NewDirCommandExpander constructs a DirCommandExpander. Each dir is a
// workspace-relative directory searched in order for "<name>.md"; a blank dir is
// ignored. When no non-blank dir is given it falls back to the defaults
// (".mecatl/commands/" then ".claude/commands/").
func NewDirCommandExpander(source tool.Workspace, dirs ...string) *DirCommandExpander {
	cleaned := make([]string, 0, len(dirs))
	for _, d := range dirs {
		d = strings.TrimRight(strings.TrimSpace(d), "/")
		if d != "" {
			cleaned = append(cleaned, d)
		}
	}
	if len(cleaned) == 0 {
		cleaned = append(cleaned, DefaultCommandDirs...)
	}
	return &DirCommandExpander{source: source, dirs: cleaned}
}

// Expand implements CommandExpander. See the type doc for the grammar and
// substitution rules.
func (e *DirCommandExpander) Expand(ctx context.Context, input string) (string, bool, error) {
	if e.source == nil {
		return input, false, nil
	}
	name, args, ok := parseCommand(input)
	if !ok {
		return input, false, nil
	}

	for _, dir := range e.dirs {
		path := dir + "/" + name + ".md"
		data, err := e.source.Read(ctx, path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return input, false, err
		}
		body := stripFrontmatter(string(data))
		return substitute(body, args), true, nil
	}

	// No matching command file in any directory: not an error, leave unchanged.
	return input, false, nil
}

// List implements CommandLister. It scans each configured directory in order for
// "<name>.md" files (via the workspace Glob port, so it stays infra-free), reads
// each to derive a short description, and returns the commands de-duplicated by
// name (first directory wins, matching Expand's precedence) and sorted by name.
//
// Description source, in order of preference: a frontmatter `description:` field
// when the file opens with a YAML frontmatter block; otherwise the first
// non-blank body line. Either way the result is trimmed and capped to
// maxDescriptionLen runes. A file that yields neither gets an empty description.
//
// It is read-only and fail-soft on a missing directory (Glob over a dir with no
// files yields nothing). A file that cannot be read is skipped (logged via no
// channel here — it simply contributes nothing) rather than aborting the scan; a
// Glob fault on a dir IS returned, since that is a genuine enumeration failure.
func (e *DirCommandExpander) List(ctx context.Context) ([]Command, error) {
	if e.source == nil {
		return nil, nil
	}
	seen := make(map[string]struct{})
	var out []Command
	for _, dir := range e.dirs {
		matches, err := e.source.Glob(ctx, dir+"/*.md")
		if err != nil {
			return nil, err
		}
		sort.Strings(matches)
		for _, p := range matches {
			name := commandNameFromPath(p)
			if name == "" {
				continue
			}
			if _, dup := seen[name]; dup {
				continue // earlier dir wins, matching Expand precedence
			}
			data, rerr := e.source.Read(ctx, p)
			if rerr != nil {
				// A file that vanished or is unreadable between Glob and Read is not
				// fatal to enumeration: skip it (it also won't be in seen, so a
				// later dir's same-named file can still surface).
				continue
			}
			seen[name] = struct{}{}
			out = append(out, Command{Name: name, Description: describeCommand(string(data))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Compile-time assertions that DirCommandExpander satisfies both interfaces.
var (
	_ CommandExpander = (*DirCommandExpander)(nil)
	_ CommandLister   = (*DirCommandExpander)(nil)
)

// frontmatterDelim is the YAML frontmatter fence line (a line that is exactly
// "---"). A leading fence opens the block; the next fence closes it.
const frontmatterDelim = "---"

// maxDescriptionLen caps a derived command description (in runes) so a long
// frontmatter line or body sentence cannot blow out a palette row. The "…"
// ellipsis (when truncating) counts toward the cap. It aliases the exported
// MaxCommandDescriptionRunes (commandsource.go) — the ONE cap every command
// surface shares.
const maxDescriptionLen = MaxCommandDescriptionRunes

// commandNameFromPath derives the command name from a "<dir>/<name>.md" path: the
// base file name with the ".md" suffix stripped. A path whose base is not a
// "<name>.md" (or is empty) yields "".
func commandNameFromPath(p string) string {
	base := gopath.Base(p)
	if !strings.HasSuffix(base, ".md") {
		return ""
	}
	return strings.TrimSuffix(base, ".md")
}

// describeCommand derives a short, capped description from a command file body.
// It prefers a frontmatter `description:` field (when the file opens with a YAML
// frontmatter block) and otherwise falls back to the first non-blank line of the
// body (after stripping any frontmatter). The result is trimmed and rune-capped.
func describeCommand(body string) string {
	if desc, ok := frontmatterDescription(body); ok && strings.TrimSpace(desc) != "" {
		return capRunes(strings.TrimSpace(desc), maxDescriptionLen)
	}
	for _, line := range strings.Split(stripFrontmatter(body), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return capRunes(line, maxDescriptionLen)
		}
	}
	return ""
}

// frontmatterDescription extracts the value of a top-level `description:` key
// from a leading YAML frontmatter block, if present. It does NOT parse full YAML
// — it scans the frontmatter lines for a `description:` prefix and returns the
// remainder with surrounding quotes stripped. ok is false when there is no
// frontmatter block or no description key.
func frontmatterDescription(body string) (string, bool) {
	if !strings.HasPrefix(body, "---\n") && !strings.HasPrefix(body, "---\r\n") {
		return "", false
	}
	lines := strings.Split(body, "\n")
	if strings.TrimRight(lines[0], "\r") != frontmatterDelim {
		return "", false
	}
	for i := 1; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		if strings.TrimSpace(line) == frontmatterDelim {
			return "", false // end of frontmatter, no description found
		}
		trimmed := strings.TrimSpace(line)
		const key = "description:"
		if strings.HasPrefix(trimmed, key) {
			val := strings.TrimSpace(trimmed[len(key):])
			val = strings.Trim(val, `"'`)
			return val, true
		}
	}
	return "", false
}

// capRunes truncates s to at most limit runes, appending "…" (which counts
// toward the limit) when it overflows. It is rune-safe so a multibyte
// description is never split mid-character.
func capRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	if limit <= 1 {
		return "…"
	}
	return string(r[:limit-1]) + "…"
}

// parseCommand reports whether input is a command invocation and, if so, returns
// the command name and the positional arguments. An input is a command iff it
// begins (after leading whitespace) with "/" immediately followed by a non-empty
// run of name characters (letters, digits, '-', '_', '.'). The remainder is
// split on whitespace into args.
func parseCommand(input string) (name string, args []string, ok bool) {
	s := strings.TrimLeft(input, " \t")
	if !strings.HasPrefix(s, "/") {
		return "", nil, false
	}
	s = s[1:]
	// Find the end of the command name.
	end := len(s)
	for i, r := range s {
		if !isNameRune(r) {
			end = i
			break
		}
	}
	name = s[:end]
	if name == "" {
		return "", nil, false
	}
	args = strings.Fields(s[end:])
	return name, args, true
}

// isNameRune reports whether r is allowed in a command name.
func isNameRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '-' || r == '_' || r == '.':
		return true
	default:
		return false
	}
}

// stripFrontmatter removes a leading YAML frontmatter block from body. A block
// is present only when the very first line is exactly "---"; it ends at the next
// line that is exactly "---". The content between (the metadata) and the
// delimiters are removed and the remaining body is returned with a single
// leading newline trimmed. If there is no opening delimiter, or no closing
// delimiter, body is returned unchanged.
func stripFrontmatter(body string) string {
	if !strings.HasPrefix(body, "---\n") && body != "---" && !strings.HasPrefix(body, "---\r\n") {
		return body
	}
	// Normalise the search to line-by-line so we tolerate CRLF.
	lines := strings.Split(body, "\n")
	if strings.TrimRight(lines[0], "\r") != frontmatterDelim {
		return body
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == frontmatterDelim {
			rest := strings.Join(lines[i+1:], "\n")
			return strings.TrimPrefix(rest, "\n")
		}
	}
	// No closing delimiter: not valid frontmatter, leave body untouched.
	return body
}

// substitute applies the placeholder rules ($ARGUMENTS, $1, $2, …) to body,
// leaving any other "$"-prefixed token intact.
func substitute(body string, args []string) string {
	var b strings.Builder
	b.Grow(len(body))
	for i := 0; i < len(body); {
		if body[i] != '$' {
			b.WriteByte(body[i])
			i++
			continue
		}
		// At a '$': try to match a known placeholder.
		rest := body[i+1:]
		if strings.HasPrefix(rest, "ARGUMENTS") {
			b.WriteString(strings.Join(args, " "))
			i += 1 + len("ARGUMENTS")
			continue
		}
		// Positional: $ followed by one or more digits (1-based).
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			j++
		}
		if j > 0 {
			n := atoi(rest[:j])
			if n >= 1 && n <= len(args) {
				b.WriteString(args[n-1])
			}
			// Out-of-range positional expands to empty (placeholder consumed).
			i += 1 + j
			continue
		}
		// Not a recognised placeholder: emit the '$' literally and move on.
		b.WriteByte('$')
		i++
	}
	return b.String()
}

// atoi parses a run of ASCII digits into an int. The input is guaranteed by the
// caller to contain only '0'–'9' and to be non-empty.
func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}
