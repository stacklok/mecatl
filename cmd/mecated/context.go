package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	yaml "github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"

	"github.com/stacklok/mecatl/engine/adapter/rulesfs"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/internal/adapter/tokenizer"
)

const contextVersion = "mecatl.context/v0alpha1"

const (
	contextKindReport        = "report"
	contextKindScan          = "scan"
	contextMarkdownExtension = ".md"
	contextRequestSource     = "request"
	contextUnknown           = "unknown"
	contextMCP               = "mcp"
	contextMCPInferred       = "mcp-inferred"
	contextObservedRequest   = "observed request"
	contextMaxInput          = 4 << 20
	contextMaxFile           = 1 << 20
	contextMaxEntries        = 256
)

var contextLabel = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

type contextOccurrence struct {
	Source                string `json:"source"`
	Name                  string `json:"name"`
	FileBytes             *int   `json:"file_bytes,omitempty"`
	Bytes                 *int   `json:"bytes,omitempty"`
	DocBytes              *int   `json:"doc_bytes,omitempty"`
	SchemaBytes           *int   `json:"schema_bytes,omitempty"`
	EstimatedDocTokens    *int   `json:"estimated_doc_tokens,omitempty"`
	EstimatedSchemaTokens *int   `json:"estimated_schema_tokens,omitempty"`
	EstimatedTokens       *int   `json:"estimated_tokens,omitempty"`
	Count                 *int   `json:"count,omitempty"`
	Provenance            string `json:"provenance,omitempty"`
	MCPServer             string `json:"mcp_server,omitempty"`
	Status                string `json:"status"`
}
type contextDocument struct {
	Version           string              `json:"version"`
	Kind              string              `json:"kind"`
	Method            string              `json:"method"`
	Coverage          string              `json:"coverage"`
	Model             string              `json:"model,omitempty"`
	ContextWindow     *int                `json:"context_window,omitempty"`
	MessageCount      *int                `json:"message_count,omitempty"`
	Headroom          *int                `json:"estimated_headroom,omitempty"`
	Scope             string              `json:"scope,omitempty"`
	RowIndex          *int                `json:"row_index,omitempty"`
	Occurrences       []contextOccurrence `json:"occurrences"`
	Candidates        []contextOccurrence `json:"candidates,omitempty"`
	InventoryMethod   string              `json:"inventory_method,omitempty"`
	InventoryScope    string              `json:"inventory_scope,omitempty"`
	InventoryCoverage string              `json:"inventory_coverage,omitempty"`
}
type contextChange struct {
	Source string             `json:"source"`
	Name   string             `json:"name"`
	Before *contextOccurrence `json:"before,omitempty"`
	After  *contextOccurrence `json:"after,omitempty"`
}
type contextDiff struct {
	Version            string          `json:"version"`
	Kind               string          `json:"kind"`
	Method             string          `json:"method"`
	Coverage           string          `json:"coverage"`
	Model              string          `json:"model,omitempty"`
	ContextWindow      *int            `json:"context_window,omitempty"`
	BeforeMessageCount *int            `json:"before_message_count,omitempty"`
	AfterMessageCount  *int            `json:"after_message_count,omitempty"`
	Changes            []contextChange `json:"changes"`
	CandidateChanges   []contextChange `json:"candidate_changes,omitempty"`
	InventoryMethod    string          `json:"inventory_method,omitempty"`
	InventoryScope     string          `json:"inventory_scope,omitempty"`
	InventoryCoverage  string          `json:"inventory_coverage,omitempty"`
}

func resolveContextCommand(args []string) commandResolution {
	if len(args) == 0 {
		return commandResolution{err: errors.New("context: expected scan, report, or diff (try 'mecated context --help')")}
	}
	if args[0] == "--help" || args[0] == "-h" {
		return commandResolution{handled: true, run: func(_ io.Reader, out, _ io.Writer) error { return contextHelp(out) }}
	}
	switch args[0] {
	case "scan", "report", "diff":
		return commandResolution{handled: true, run: func(in io.Reader, out, _ io.Writer) error { return runContext(args[0], args[1:], in, out) }}
	default:
		return commandResolution{err: errors.New("context: unknown subcommand (try 'mecated context --help')")}
	}
}
func contextHelp(out io.Writer) error {
	_, err := fmt.Fprintln(out, "Usage: mecated context scan --project PATH [--user-root PATH] [--mcp-snapshot FILE] [--format json|text]\n       mecated context report --input FILE|- [--row N] [--inventory FILE] [--format json|text]\n       mecated context diff --before FILE [--before-row N] --after FILE [--after-row N] [--format json|text]\n\nInputs: bare request-manifest JSON, a single {\"Type\":\"request.manifest\",\"RequestManifest\":{...}} event, a manifest InspectSession {\"view\":\"manifest\",\"rows\":[...]} projection, or a versioned mecatl.context/v0alpha1 report (scan is accepted by diff only). Projection requires an explicit zero-based row index. Imported projection flags are unverified and never imply a complete session. Example: mecated context report --input evidence.json --row 0 --inventory prior-scan.json --format json. --inventory accepts only a prior bounded regular-file v0alpha1 scan (not stdin); its user/project/MCP candidates remain separate from observed request occurrences. No candidate is thereby proven loaded or admitted, and candidate estimates must not be added to request totals. Scan only reads explicit roots and an optional offline MCP tools/list snapshot; it never inspects HOME, environment, network, providers, hooks, or executes tools. Explicit regular non-symlink files or bounded stdin only. Scan estimates candidate project instructions and per-rule eager blocks (shared header/footer/framing excluded), plus frontmatter-only skill/agent metadata; bodies are not eager. User-root candidates and MCP snapshot tools are not loaded: trust, runtime admission, and activation remain unknown. Outputs contain potentially sensitive metadata and names (unsafe names become opaque IDs). Token estimates are local, not provider usage; components overlap and must not be added. Reported per-rule blocks are observed parts of the rules fragment, not extra request tokens; omitted rules have unknown cost, and older manifests lacking rules metadata do not imply zero rules. Filtered tool decisions are candidates of unknown cost. Measured disclosure-hidden specs may still be advertised lightweight tools. MCP names are inferred from the mcp__ prefix (including when the request decision labels one mcp); this is not verified server-registration provenance. Ordinal labels for unsafe or duplicate names are per-request, not cross-request identities.\n\nJSON example: {\"version\":\"mecatl.context/v0alpha1\",\"kind\":\"report\",\"method\":\"unknown\",\"coverage\":\"request manifest\",\"occurrences\":[{\"source\":\"total\",\"name\":\"request\",\"status\":\"observed request\"}]}. Optional token and byte metrics are omitted when unknown; scan occurrences use file_bytes separately from rendered bytes.")
	return err
}

type contextFlags struct {
	format                          string
	row, beforeRow, afterRow        int
	project, userRoot, snapshot     string
	input, before, after, inventory string
}

func runContext(cmd string, args []string, in io.Reader, out io.Writer) error {
	flags, err := parseContextFlags(cmd, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return contextHelp(out)
		}
		return err
	}
	result, err := contextResult(cmd, flags, in)
	if err != nil {
		return err
	}
	return writeContextResult(out, flags.format, result)
}

func parseContextFlags(cmd string, args []string) (contextFlags, error) {
	fs := flag.NewFlagSet("context "+cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	format := fs.String("format", "text", "json or text")
	row := fs.Int("row", -1, "explicit zero-based projection row (report only)")
	beforeRow := fs.Int("before-row", -1, "zero-based before projection row (diff only)")
	afterRow := fs.Int("after-row", -1, "zero-based after projection row (diff only)")
	var project, userRoot, snapshot, input, before, after, inventory *string
	switch cmd {
	case contextKindScan:
		project = fs.String("project", "", "explicit project root")
		userRoot = fs.String("user-root", "", "explicit user home directory")
		snapshot = fs.String("mcp-snapshot", "", "offline MCP tools/list JSON snapshot")
	case contextKindReport:
		input = fs.String("input", "", "manifest JSON file or -")
		inventory = fs.String("inventory", "", "prior versioned scan JSON regular file")
	case "diff":
		before = fs.String("before", "", "before report")
		after = fs.String("after", "", "after report")
	}
	if err := fs.Parse(args); err != nil {
		return contextFlags{}, err
	}
	flags := contextFlags{format: *format, row: *row, beforeRow: *beforeRow, afterRow: *afterRow}
	if project != nil {
		flags.project, flags.userRoot, flags.snapshot = *project, *userRoot, *snapshot
	}
	if input != nil {
		flags.input = *input
		flags.inventory = *inventory
	}
	if before != nil {
		flags.before, flags.after = *before, *after
	}
	if fs.NArg() != 0 || !validContextFlags(cmd, flags) {
		return contextFlags{}, errors.New("context: unexpected arguments, format, or row selection")
	}
	return flags, nil
}

func validContextFlags(cmd string, flags contextFlags) bool {
	if flags.format != "json" && flags.format != "text" || flags.row < -1 || flags.beforeRow < -1 || flags.afterRow < -1 {
		return false
	}
	switch cmd {
	case contextKindScan:
		return flags.row == -1 && flags.beforeRow == -1 && flags.afterRow == -1
	case contextKindReport:
		return flags.beforeRow == -1 && flags.afterRow == -1
	case "diff":
		return flags.row == -1
	default:
		return false
	}
}

func contextResult(cmd string, flags contextFlags, in io.Reader) (any, error) {
	switch cmd {
	case contextKindScan:
		if flags.project == "" {
			return nil, errors.New("context scan: --project is required")
		}
		return scanContextInputs(flags.project, flags.userRoot, flags.snapshot)
	case contextKindReport:
		if flags.input == "" {
			return nil, errors.New("context report: --input is required")
		}
		if flags.inventory == "-" {
			return nil, errors.New("context report: --inventory requires an explicit regular file")
		}
		doc, err := readContextRow(flags.input, in, flags.row)
		if err != nil {
			return nil, err
		}
		if doc.Kind != contextKindReport {
			return nil, errors.New("context report: expected request manifest, not scan")
		}
		if flags.inventory != "" {
			inventory, err := readContextRow(flags.inventory, in, -1)
			if err != nil {
				return nil, err
			}
			if inventory.Kind != contextKindScan || inventory.Scope == "" {
				return nil, errors.New("context report: --inventory must be a versioned scan with explicit scope")
			}
			doc.Candidates = append([]contextOccurrence(nil), inventory.Occurrences...)
			doc.InventoryMethod, doc.InventoryScope, doc.InventoryCoverage = inventory.Method, inventory.Scope, inventory.Coverage
		}
		return doc, nil
	case "diff":
		if flags.before == "" || flags.after == "" {
			return nil, errors.New("context diff: --before and --after are required")
		}
		before, err := readContextRow(flags.before, in, flags.beforeRow)
		if err != nil {
			return nil, err
		}
		after, err := readContextRow(flags.after, in, flags.afterRow)
		if err != nil {
			return nil, err
		}
		return diffContext(before, after)
	default:
		return nil, errors.New("context: unknown subcommand (try 'mecated context --help')")
	}
}

func writeContextResult(out io.Writer, format string, result any) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	switch value := result.(type) {
	case contextDocument:
		return writeContextDocument(out, value)
	case contextDiff:
		return writeContextDiff(out, value)
	default:
		return errors.New("context: unsupported result")
	}
}

func writeContextDocument(out io.Writer, document contextDocument) error {
	if _, err := fmt.Fprintf(out, "%s %s (%s; %s) scope=%s row=%s model=%s window=%s messages=%s estimated headroom=%s\n", document.Version, document.Kind, document.Method, document.Coverage, document.Scope, contextNumber(document.RowIndex), document.Model, contextNumber(document.ContextWindow), contextNumber(document.MessageCount), contextNumber(document.Headroom)); err != nil {
		return err
	}
	if document.Kind == contextKindReport {
		if _, err := fmt.Fprintln(out, "Request totals, components, and details overlap; do not add their token estimates."); err != nil {
			return err
		}
	}
	if document.InventoryMethod != "" {
		if _, err := fmt.Fprintln(out, "Observed request occurrences (measured in this request; overlapping components):"); err != nil {
			return err
		}
	}
	if err := writeContextOccurrences(out, document.Occurrences, document.Kind); err != nil {
		return err
	}
	if document.InventoryMethod != "" {
		if _, err := fmt.Fprintf(out, "Unverified candidate inventory (method=%s; scope=%s; coverage=%s): NOT observed, admitted, or loaded; candidate estimates are not additive. Deferred bodies have unknown cost.\n", document.InventoryMethod, document.InventoryScope, document.InventoryCoverage); err != nil {
			return err
		}
		return writeContextOccurrences(out, document.Candidates, contextKindScan)
	}
	return nil
}
func writeContextOccurrences(out io.Writer, entries []contextOccurrence, kind string) error {
	occurrences := append([]contextOccurrence(nil), entries...)
	sort.SliceStable(occurrences, func(i, j int) bool {
		a, b := occurrences[i].EstimatedTokens, occurrences[j].EstimatedTokens
		if a == nil {
			return false
		}
		return b == nil || *a > *b
	})
	for _, occurrence := range occurrences {
		size := occurrence.Bytes
		sizeKind := "request bytes"
		if kind == contextKindScan {
			sizeKind = "candidate rendered/metadata bytes"
		}
		if size == nil {
			size = occurrence.FileBytes
			sizeKind = "file bytes (not loaded)"
			if size == nil {
				sizeKind = "size"
			}
		}
		if _, err := fmt.Fprintf(out, "%s tokens  %s %s  %s/%s (%s)", contextNumber(occurrence.EstimatedTokens), contextNumber(size), sizeKind, occurrence.Source, occurrence.Name, occurrence.Status); err != nil {
			return err
		}
		if occurrence.DocBytes != nil && occurrence.SchemaBytes != nil {
			if _, err := fmt.Fprintf(out, " [tool description/schema: %d/%d bytes]", *occurrence.DocBytes, *occurrence.SchemaBytes); err != nil {
				return err
			}
		}
		if occurrence.Count != nil {
			if _, err := fmt.Fprintf(out, " [omitted rules: %d; cost unknown]", *occurrence.Count); err != nil {
				return err
			}
		}
		if occurrence.Provenance != "" {
			if _, err := fmt.Fprintf(out, " [source: %s]", occurrence.Provenance); err != nil {
				return err
			}
		}
		if occurrence.MCPServer != "" {
			if _, err := fmt.Fprintf(out, " [MCP server: %s]", occurrence.MCPServer); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}
	return nil
}

func writeContextDiff(out io.Writer, diff contextDiff) error {
	if _, err := fmt.Fprintf(out, "%s diff %s (%s) messages %s -> %s\n", diff.Version, diff.Kind, diff.Method, contextNumber(diff.BeforeMessageCount), contextNumber(diff.AfterMessageCount)); err != nil {
		return err
	}
	for _, change := range diff.Changes {
		if _, err := fmt.Fprintf(out, "%s/%s: %s -> %s\n", change.Source, change.Name, contextValue(change.Before), contextValue(change.After)); err != nil {
			return err
		}
	}
	if diff.CandidateChanges != nil {
		if _, err := fmt.Fprintf(out, "Unverified candidate inventory changes (method=%s; scope=%s; coverage=%s; not evidence of loading):\n", diff.InventoryMethod, diff.InventoryScope, diff.InventoryCoverage); err != nil {
			return err
		}
		for _, change := range diff.CandidateChanges {
			if _, err := fmt.Fprintf(out, "%s/%s: %s -> %s\n", change.Source, change.Name, contextValue(change.Before), contextValue(change.After)); err != nil {
				return err
			}
		}
	}
	return nil
}
func contextNumber(n *int) string {
	if n == nil {
		return contextUnknown
	}
	return fmt.Sprint(*n)
}
func contextValue(o *contextOccurrence) string {
	if o == nil {
		return "absent"
	}
	if o.Bytes == nil {
		return "file bytes=" + contextNumber(o.FileBytes) + ", tokens=" + contextNumber(o.EstimatedTokens) + ", status=" + o.Status
	}
	return "rendered/request bytes=" + contextNumber(o.Bytes) + ", tokens=" + contextNumber(o.EstimatedTokens) + ", status=" + o.Status
}
func contextPtr(n int) *int { return &n }

// scanContext retains the project-only API used by focused callers.
func scanContext(root string) (contextDocument, error) {
	return scanContextInputs(root, "", "")
}

// scanContextInputs only reads roots and snapshots supplied by the caller.
func scanContextInputs(root, userRoot, mcpSnapshot string) (contextDocument, error) {
	d, counter, fsroot, userFS, remaining, err := prepareContextScan(root, userRoot, mcpSnapshot)
	if err != nil {
		return d, err
	}
	defer func() { _ = fsroot.Close() }()
	if userFS != nil {
		defer func() { _ = userFS.Close() }()
	}
	if err := scanProjectInstructions(&d, fsroot, &remaining, counter); err != nil {
		return d, err
	}
	if err := scanContextRules(&d, fsroot, userFS, &remaining, counter); err != nil {
		return d, err
	}
	if err := scanContextAssets(&d, fsroot, userFS, &remaining, counter); err != nil {
		return d, err
	}
	if err := scanContextExtras(&d, userFS, mcpSnapshot, &remaining, counter); err != nil {
		return d, err
	}
	sortContextScanOccurrences(d.Occurrences)
	return d, nil
}

type contextAssetGroup struct {
	root              *os.Root
	dir, file, source string
}

func scanContextAssets(d *contextDocument, project, user *os.Root, remaining *int, counter *tokenizer.Counter) error {
	groups := []contextAssetGroup{{project, ".mecatl/agents", contextMarkdownExtension, ".mecatl/agents"}, {project, ".claude/agents", contextMarkdownExtension, ".claude/agents"}, {project, ".mecatl/skills", "SKILL.md", ".mecatl/skills"}, {project, ".claude/skills", "SKILL.md", ".claude/skills"}, {user, ".config/mecatl/agents", contextMarkdownExtension, "user:.config/mecatl/agents"}, {user, ".claude/agents", contextMarkdownExtension, "user:.claude/agents"}, {user, ".config/mecatl/skills", "SKILL.md", "user:.config/mecatl/skills"}, {user, ".claude/skills", "SKILL.md", "user:.claude/skills"}}
	for _, group := range groups {
		if group.root == nil {
			continue
		}
		entries, err := scanDir(group.root, group.dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := scanContextAsset(d, group, entry, remaining, counter); err != nil {
				return err
			}
		}
	}
	return nil
}

func scanContextAsset(d *contextDocument, group contextAssetGroup, entry os.DirEntry, remaining *int, counter *tokenizer.Counter) error {
	name := entry.Name()
	path, label := filepath.Join(group.dir, name), strings.TrimSuffix(name, contextMarkdownExtension)
	if group.file == contextMarkdownExtension {
		if !strings.HasSuffix(name, contextMarkdownExtension) {
			return nil
		}
	} else {
		if !entry.IsDir() {
			return nil
		}
		path, label = filepath.Join(group.dir, name, "SKILL.md"), name
		info, err := group.root.Lstat(filepath.Join(group.dir, name))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("context scan: unsafe skill directory")
		}
	}
	if !contextLabel.MatchString(label) {
		return nil
	}
	data, found, err := scanContent(group.root, path, remaining)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	occurrence := contextOccurrence{Source: group.source, Name: label, FileBytes: contextPtr(len(data)), Status: "snapshot candidate; runtime admission unknown"}
	front, _, ok := rulesfs.SplitFrontmatter(string(data))
	if ok && front != "" {
		var fields map[string]any
		if yaml.Unmarshal([]byte(front), &fields) == nil && fields != nil {
			occurrence.Bytes, occurrence.EstimatedTokens, occurrence.Status = contextPtr(len(front)), contextPtr(counter.Count(front)), "metadata-only estimate; runtime admission unknown"
		} else {
			occurrence.Status = "invalid metadata frontmatter"
		}
	}
	d.Occurrences = append(d.Occurrences, occurrence)
	if len(d.Occurrences) > contextMaxEntries {
		return errors.New("context scan: too many entries")
	}
	return nil
}

func scanContextExtras(d *contextDocument, user *os.Root, snapshot string, remaining *int, counter *tokenizer.Counter) error {
	if user != nil {
		data, found, err := scanContent(user, ".config/mecatl/soul.md", remaining)
		if err != nil {
			return err
		}
		if found {
			d.Occurrences = append(d.Occurrences, contextOccurrence{Source: "user:.config/mecatl", Name: "soul", FileBytes: contextPtr(len(data)), Status: "candidate; selection unknown"})
		}
	}
	if snapshot == "" {
		return nil
	}
	tools, err := scanMCPSnapshot(snapshot, remaining, counter)
	if err != nil {
		return err
	}
	if len(d.Occurrences)+len(tools) > contextMaxEntries {
		return errors.New("context scan: too many entries")
	}
	d.Occurrences = append(d.Occurrences, tools...)
	return nil
}

func sortContextScanOccurrences(occurrences []contextOccurrence) {
	sort.Slice(occurrences, func(i, j int) bool {
		return occurrences[i].Source < occurrences[j].Source || occurrences[i].Source == occurrences[j].Source && occurrences[i].Name < occurrences[j].Name
	})
}

type contextRuleCandidate struct {
	occurrence contextOccurrence
	block      string
}

func scanContextRules(d *contextDocument, project, user *os.Root, remaining *int, counter *tokenizer.Counter) error {
	seen := map[string]bool{}
	var candidates []contextRuleCandidate
	for _, location := range []struct {
		root        *os.Root
		dir, source string
	}{{project, ".mecatl/rules", ".mecatl/rules"}, {project, ".claude/rules", ".claude/rules"}, {user, ".config/mecatl/rules", "user:.config/mecatl/rules"}, {user, ".claude/rules", "user:.claude/rules"}} {
		if location.root == nil {
			continue
		}
		entries, err := scanDir(location.root, location.dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, contextMarkdownExtension) || !contextLabel.MatchString(strings.TrimSuffix(name, contextMarkdownExtension)) {
				continue
			}
			data, found, err := scanContent(location.root, filepath.Join(location.dir, name), remaining)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			stem, block, status := strings.TrimSuffix(name, contextMarkdownExtension), "", "eligible eager rule; trust unknown"
			block, valid := contextRuleBlock(stem, data)
			if !valid {
				status = "invalid rule frontmatter"
			} else if seen[stem] {
				status = "shadowed by higher-precedence project rule"
			} else {
				seen[stem] = true
			}
			candidates = append(candidates, contextRuleCandidate{contextOccurrence{Source: location.source, Name: stem, FileBytes: contextPtr(len(data)), Status: status}, block})
			if len(candidates)+len(d.Occurrences) > contextMaxEntries {
				return errors.New("context scan: too many entries")
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i].occurrence, candidates[j].occurrence
		return a.Name < b.Name || a.Name == b.Name && a.Source > b.Source
	})
	used, shown, dropping := len(prompt.RulesHeader()), 0, false
	for _, candidate := range candidates {
		if candidate.occurrence.Status == "eligible eager rule; trust unknown" {
			if dropping || shown >= 32 || used+len(candidate.block) > 40*1024 {
				dropping, candidate.occurrence.Status = true, "omitted by eager rules cap"
			} else {
				candidate.occurrence.Bytes, candidate.occurrence.EstimatedTokens = contextPtr(len(candidate.block)), contextPtr(counter.Count(candidate.block))
				used += len(candidate.block)
				shown++
			}
		}
		d.Occurrences = append(d.Occurrences, candidate.occurrence)
	}
	return nil
}

func prepareContextScan(root, userRoot, snapshot string) (contextDocument, *tokenizer.Counter, *os.Root, *os.Root, int, error) {
	d := contextDocument{Version: contextVersion, Kind: contextKindScan, Method: "o200k_base-local-estimate", Coverage: "explicit scan candidates; trust, runtime admission, and activation unknown", Scope: "explicit project", Occurrences: []contextOccurrence{}}
	counter, err := tokenizer.New(tokenizer.O200kBase)
	if err != nil {
		return d, nil, nil, nil, 0, errors.New("context scan: offline tokenizer unavailable")
	}
	project, err := openContextRoot(root, "project")
	if err != nil {
		return d, nil, nil, nil, 0, err
	}
	var user *os.Root
	if userRoot != "" {
		user, err = openContextRoot(userRoot, "user root")
		if err != nil {
			_ = project.Close()
			return d, nil, nil, nil, 0, err
		}
		d.Scope = "explicit project + user root"
	}
	if snapshot != "" {
		d.Scope += " + MCP snapshot"
	}
	return d, counter, project, user, 8 << 20, nil
}

func openContextRoot(path, label string) (*os.Root, error) {
	info, err := os.Lstat(path) // #nosec G703 -- explicit user-specified root is opened with os.OpenRoot and verified with os.SameFile.
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("context scan: %s must be a directory, not a symlink", label)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("context scan: cannot open %s", label)
	}
	opened, err := root.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(info, opened) {
		_ = root.Close()
		return nil, fmt.Errorf("context scan: %s changed while opening", label)
	}
	return root, nil
}

func scanProjectInstructions(d *contextDocument, root *os.Root, remaining *int, counter *tokenizer.Counter) error {
	selected := false
	for _, candidate := range []string{"AGENTS.md", "CLAUDE.md"} {
		if selected && candidate == "CLAUDE.md" {
			if info, err := root.Lstat(candidate); err == nil && info.Mode()&os.ModeSymlink != 0 {
				continue
			}
		}
		data, found, err := scanContent(root, candidate, remaining)
		if err != nil {
			return err
		}
		if !found || strings.TrimSpace(string(data)) == "" {
			continue
		}
		occurrence := contextOccurrence{Source: "project-instructions", Name: candidate, FileBytes: contextPtr(len(data)), Status: "shadowed by AGENTS.md"}
		if !selected {
			marker := "Project instructions (" + candidate + "):\n\n" + strings.TrimSpace(string(data))
			occurrence.Bytes, occurrence.EstimatedTokens, occurrence.Status = contextPtr(len(marker)), contextPtr(counter.Count(marker)), "selected candidate; trust unknown"
			selected = true
		}
		d.Occurrences = append(d.Occurrences, occurrence)
	}
	return nil
}

func scanDir(root *os.Root, relative string) ([]os.DirEntry, error) {
	for _, part := range []string{filepath.Dir(relative), relative} {
		info, err := root.Lstat(part)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("context scan: unsafe or unreadable project directory")
		}
	}
	f, err := root.OpenFile(relative, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("context scan: cannot list directory")
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !opened.IsDir() {
		return nil, errors.New("context scan: directory changed while opening")
	}
	entries, err := f.ReadDir(contextMaxEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New("context scan: cannot list rules")
	}
	if len(entries) > contextMaxEntries {
		return nil, errors.New("context scan: too many entries")
	}
	return entries, nil
}
func scanContent(root *os.Root, relative string, remaining *int) ([]byte, bool, error) {
	info, err := root.Lstat(relative)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > contextMaxFile || info.Size() > int64(*remaining) {
		return nil, false, errors.New("context scan: unsafe file or read budget exceeded")
	}
	f, err := root.OpenFile(relative, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false, errors.New("context scan: cannot read file")
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() > contextMaxFile || opened.Size() > int64(*remaining) {
		return nil, false, errors.New("context scan: file changed or read budget exceeded")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(min(contextMaxFile, *remaining)+1)))
	if err != nil || len(data) > contextMaxFile || len(data) > *remaining {
		return nil, false, errors.New("context scan: file read budget exceeded")
	}
	*remaining -= len(data)
	return data, true, nil
}
func contextRuleBlock(name string, data []byte) (string, bool) {
	front, body, ok := rulesfs.SplitFrontmatter(string(data))
	if !ok {
		body = string(data)
	}
	var paths contextRulePaths
	if front != "" {
		var fm contextRuleFront
		if yaml.Unmarshal([]byte(front), &fm) != nil {
			return "", false
		}
		paths = fm.Paths
	}
	body = strings.TrimSpace(body)
	if len(body) > prompt.MaxRuleBytes {
		body = rulesfs.TruncateRunes(body, prompt.MaxRuleBytes)
	}
	condition := "(always)"
	if len(paths) > 0 {
		condition = strings.Join(paths, ", ")
	}
	block := "\n<rule name=\"" + name + "\">\nApplies when: " + condition + "\n" + body
	if !strings.HasSuffix(body, "\n") {
		block += "\n"
	}
	return block + "</rule>", true
}

type contextRuleFront struct {
	Paths contextRulePaths `yaml:"paths"`
}

func (f *contextRuleFront) UnmarshalYAML(node ast.Node) error {
	type decoded contextRuleFront
	var value decoded
	if err := yaml.NodeToValue(node, &value); err != nil {
		return err
	}
	*f = contextRuleFront(value)
	mapping, ok := node.(*ast.MappingNode)
	if !ok {
		return nil
	}
	for _, item := range mapping.Values {
		if item.Key.GetToken().Value == "paths" {
			return f.Paths.UnmarshalYAML(item.Value)
		}
	}
	return nil
}

type contextRulePaths []string

func (p *contextRulePaths) UnmarshalYAML(node ast.Node) error {
	var values []string
	if node.Type() == ast.SequenceType {
		seq, ok := node.(*ast.SequenceNode)
		if !ok {
			return errors.New("invalid rule paths")
		}
		for _, item := range seq.Values {
			text, ok := contextRuleScalar(item)
			if !ok {
				return errors.New("invalid rule path")
			}
			values = append(values, text)
		}
	} else {
		text, ok := contextRuleScalar(node)
		if !ok {
			return errors.New("invalid rule path")
		}
		values = append(values, text)
	}
	*p = nil
	for _, value := range values {
		for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				*p = append(*p, trimmed)
			}
		}
	}
	return nil
}
func contextRuleScalar(node ast.Node) (string, bool) {
	switch node.Type() {
	case ast.StringType, ast.LiteralType:
		var text string
		if yaml.NodeToValue(node, &text) != nil {
			return "", false
		}
		return text, true
	case ast.BoolType, ast.IntegerType, ast.FloatType, ast.NullType, ast.InfinityType, ast.NanType:
		if token := node.GetToken(); token != nil {
			return token.Value, true
		}
	}
	return "", false
}
