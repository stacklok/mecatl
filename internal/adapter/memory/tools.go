package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

// Tool names of the memory tools. They are the single authority for the names
// the tools register under (used in each tool's Spec().Name) so a consumer can
// probe the catalog for memory enablement by referencing the constant rather
// than a local literal that could drift on a rename (see
// internal/adapter/server.Service.capabilities).
const (
	// RememberToolName is the catalog name of the Remember tool.
	RememberToolName = "Remember"
	// RecallToolName is the catalog name of the Recall tool.
	RecallToolName = "Recall"
	// SearchMemoryToolName is the catalog name of the SearchMemory tool.
	SearchMemoryToolName = "SearchMemory"
)

// --- Descriptions ---------------------------------------------------------
//
// The descriptions below are the model's onboarding manual for memory. They
// STEER the model to be conservative: memory is for durable user preferences and
// cross-cutting project facts that the model cannot cheaply rediscover, NOT for
// anything the filesystem already knows. This directly targets the "over-eager
// memory" anti-pattern (docs/harnesses/08-design-considerations.md): "Saving
// facts the file system already knows is the dominant memory failure mode."

// rememberDescription is the model-facing documentation for the Remember tool.
const rememberDescription = `Save a small, durable fact to cross-session project memory so it survives into future sessions.

Your saved memory is summarised for you as an INDEX (every key + a one-line
description) shown automatically at the start of each session, so a fact you save
here is something future sessions can see at a glance and load with Recall.

When to use (be conservative):
- Durable USER PREFERENCES the user stated explicitly, e.g. "always run tests
  with gotestsum", "I prefer table-driven tests", "use conventional commits".
- CROSS-CUTTING PROJECT FACTS that are not obvious from any single file and that
  you cannot rediscover in a few tool calls, e.g. "the staging deploy is gated by
  a manual approval in CI", "issue tracker lives in Linear, not GitHub".

When NOT to use (the over-eager-memory anti-pattern):
- Do NOT save anything the filesystem already knows or that is rediscoverable in
  a few Read/Grep/Glob calls: file locations, function signatures, the build
  command in the Makefile, the module path, dependency versions. Let the code be
  the memory of the code. Saving such facts is the dominant memory failure mode:
  it goes stale and pollutes future context.
- Do NOT save transient task state, secrets, or large blobs.
- Do NOT save task progress or temporary working state — that belongs in the
  conversation, not durable memory.
- NEVER save a negative capability claim ("tool X is broken", "Y doesn't work",
  "can't do Z"). Transient failures get frozen into durable refusals the agent
  later cites against itself. Record durable facts, not momentary failures.

Phrasing (declarative facts, not instructions to yourself):
- Record what is TRUE, not what to DO. "Project uses pytest with xdist" — not
  "Run tests with pytest -n 4"; "user prefers conventional commits" — not
  "Always use conventional commits". A fact lets each future session decide what
  to do with it; an imperative note drifts out of date and over-steers.
- Do NOT record point-in-time artifacts that go stale within days: PR or issue
  numbers, commit SHAs, "fixed bug X", "shipped Y", "Phase N done", file counts.
  Those live in git history and the issue tracker, not in durable memory.

Behavior:
- Memory is scoped to THIS project. Writing a key overwrites any prior value.
- Use short, namespaced keys, e.g. "pref/test-runner", "project/deploy-gate".

Arguments:
- key         (required): a short, stable, namespaced identifier.
- value       (required): the concise fact to remember.
- description (optional): a one-line summary shown in your memory index. If
  omitted, the first line of value is used. Keep it short and specific — this is
  what future sessions see at a glance before deciding to Recall the full value.

Example:
  {"key": "pref/test-runner", "value": "Run tests with: gotestsum --format dots", "description": "preferred test runner"}`

// recallDescription is the model-facing documentation for the Recall tool.
const recallDescription = `Load the full value of a saved memory entry by exact key (or list entries by key prefix).

Behavior:
- Your current memory INDEX (every saved key + a one-line description, value
  omitted) is shown to you automatically at the start of each session. Use Recall
  to load the FULL value of a key you see in that index.
- The index is capped, so older entries may be omitted from it. If the key you
  need is not in the index, use SearchMemory with a topic query to find candidate
  keys, then Recall the key it returns.
- A key that exactly matches an entry returns that entry's full value.
- A key that matches no exact entry is treated as a PREFIX and returns every
  entry whose key starts with it (sorted by key).
- A lookup that finds nothing returns a clear "not found" result, NOT an error.

When NOT to use:
- To discover facts about the current code: use Read, Grep, and Glob instead.
  Memory holds only what was deliberately saved with Remember; use the code as the
  source of truth for the code.

Arguments:
- key (required): the exact key (from your index), or a prefix such as "pref/".

Example:
  {"key": "pref/"}   lists every saved preference.`

// searchMemoryDescription is the model-facing documentation for the SearchMemory
// tool.
const searchMemoryDescription = `Search your cross-session project memory by topic and get back the best-matching keys, ranked by lexical relevance. Use this to FIND saved facts not shown in your tier-0 memory index (the index is capped, so older entries may be omitted).

The memory recall loop is: (1) check the tier-0 memory index shown at session start; (2) if what you need is not there, SearchMemory with a topic query to find candidate keys; (3) Recall the key to load its full value.

Results are "key — one-line description" lines, ranked best-first; values are omitted (Recall a key to load its full value). Ranking is local and lexical (term overlap), so phrase your query with the words you expect in the key or description, e.g. "preferred test runner".

When NOT to use:
- To discover facts about the current code: use Read, Grep, and Glob. Memory holds only what was deliberately saved with Remember.

Arguments:
- query (required): topic words to rank against.
- limit (optional): max results (default 10).`

// --- Remember tool --------------------------------------------------------

// RememberTool writes a fact to the injected tool.MemoryStore. It is mutating
// (ReadOnly() == false). The store is constructor-injected, mirroring how the
// Bash tool takes a CommandRunner, so command/memory side effects stay optional
// and testable with a fake.
//
// The tool is PARAMETERIZED so the user-model family (RememberUser, see
// usermodeltools.go) reuses the exact same write logic with only a different
// catalog name, a specialized description, an enforced key prefix, and an
// injection scan — rather than a near-duplicate type. The zero value of those
// fields reproduces the original project-memory Remember behaviour.
type RememberTool struct {
	store tool.MemoryStore
	// name overrides the catalog name; "" means RememberToolName.
	name string
	// desc overrides the model-facing description; "" means rememberDescription.
	desc string
	// keyPrefix, when non-empty, is ENFORCED on every write: a key lacking it is
	// rejected (the caller must namespace explicitly, so a stray key cannot escape
	// the user-model namespace). Empty means no prefix constraint.
	keyPrefix string
	// scanInjection, when true, runs an injection scan over the VALUE at write time
	// and rejects (does not store) a flagged value. It guards transcript-sourced
	// poisoning for the user-model write paths (the agent tool AND the 2b fork).
	scanInjection bool
}

// NewRememberTool constructs the Remember tool bound to store. store must be
// non-nil; the composition root registers this tool only when a store is wired.
func NewRememberTool(store tool.MemoryStore) tool.Tool {
	if store == nil {
		panic("memory: NewRememberTool requires a non-nil MemoryStore")
	}
	return RememberTool{store: store}
}

// Compile-time assertion that RememberTool implements tool.Tool.
var _ tool.Tool = RememberTool{}

// rememberArgs is the JSON argument shape for the Remember tool.
type rememberArgs struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

// Spec returns the model-facing specification of the Remember tool. The name and
// description fall back to the project-memory defaults unless overridden (the
// user-model family overrides both).
func (rt RememberTool) Spec() tool.ToolSpec {
	name := rt.name
	if name == "" {
		name = RememberToolName
	}
	desc := rt.desc
	if desc == "" {
		desc = rememberDescription
	}
	return tool.ToolSpec{
		Name:        name,
		Description: desc,
		Schema: schema(`{
  "type": "object",
  "properties": {
    "key": {"type": "string", "description": "Short, stable, namespaced key, e.g. \"pref/test-runner\"."},
    "value": {"type": "string", "description": "Concise, durable fact to remember."},
    "description": {"type": "string", "description": "Optional one-line summary shown in your memory index; defaults to the first line of value."}
  },
  "required": ["key", "value"]
}`),
	}
}

// ReadOnly reports that Remember mutates persistent state.
func (RememberTool) ReadOnly() bool { return false }

// Execute saves the key/value to the store.
func (rt RememberTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	var args rememberArgs
	if msg, ok := parseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	if strings.TrimSpace(args.Key) == "" {
		return session.NewToolError(in.ID, "the \"key\" argument is required"), nil
	}
	if args.Value == "" {
		return session.NewToolError(in.ID, "the \"value\" argument is required"), nil
	}
	// Enforce the namespace prefix (user-model family): prepend it when absent so a
	// stray key cannot escape the namespace, but never double-prefix an already
	// well-formed key.
	key := args.Key
	if rt.keyPrefix != "" && !strings.HasPrefix(key, rt.keyPrefix) {
		key = rt.keyPrefix + key
	}
	// Injection-scan at write time (user-model family): a transcript-sourced fact
	// must not be able to launder role-override / instruction-injection text into a
	// block that re-enters context every session. The <user-model> block renders
	// KEY + DESCRIPTION (the value is dropped from the tier-0 index), and the
	// description is either the explicit args.Description or the value's first line —
	// so BOTH the value AND the EFFECTIVE description must be scanned, or a payload
	// hidden in `description` would bypass the value-only scan and still be injected.
	// A hit in EITHER field REJECTS the write (not stored) with a model-addressable
	// error. The same RememberTool{scanInjection:true} backs both the agent tool and
	// the 2b reviewer's child engine (buildUserModelReviewEngine), so this one gate
	// covers both write paths.
	if rt.scanInjection {
		effectiveDesc := descriptionOrFirstLine(args.Description, args.Value)
		for _, field := range []string{args.Value, args.Description, effectiveDesc} {
			if marker, found := scanForInjection(field); found {
				return session.NewToolError(in.ID, fmt.Sprintf("refusing to store %q: it contains a disallowed instruction-injection / role-override pattern (%q). Store plain FACTS about the operator, not instructions.", key, marker)), nil
			}
		}
		// Fence-integrity guard (mirrors soul's reject-on-close-tag): a value or
		// description containing the literal data-fence close-tag could close the
		// <user-model> fence early and smuggle trailing text out of the data zone.
		// Reject it. Case-insensitive so "</USER-MODEL>" cannot slip through.
		for _, field := range []string{args.Value, args.Description} {
			if strings.Contains(strings.ToLower(field), userModelCloseTag) {
				return session.NewToolError(in.ID, fmt.Sprintf("refusing to store %q: it contains the data-fence close-tag %q, which could break the <user-model> data fence.", key, userModelCloseTag)), nil
			}
		}
	}
	if err := rt.store.RememberEntry(ctx, tool.MemoryEntry{
		Key:         key,
		Value:       args.Value,
		Description: args.Description,
	}); err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf("could not remember %q: %v", args.Key, err)), nil
	}
	// Echo the exact index line the write just produced (explicit description, else
	// the value's first line) via the SAME derivation the store's Index uses, so the
	// echo can never drift from what the next session's index will show. This is the
	// in-run feedback that lets the model see its own write immediately, even though
	// the tier-0 index itself is computed once at run start and does not refresh
	// mid-run.
	return session.NewToolResult(in.ID, fmt.Sprintf("Remembered %q — %s", key, descriptionOrFirstLine(args.Description, args.Value))), nil
}

// --- Recall tool ----------------------------------------------------------

// RecallTool reads facts from the injected tool.MemoryStore. It is read-only
// (ReadOnly() == true) so the loop may dispatch it in parallel with other reads.
type RecallTool struct {
	store tool.MemoryStore
	// name/desc override the catalog name and description for the user-model family
	// (RecallUser). Empty falls back to the project-memory defaults.
	name string
	desc string
}

// NewRecallTool constructs the Recall tool bound to store. store must be non-nil.
func NewRecallTool(store tool.MemoryStore) tool.Tool {
	if store == nil {
		panic("memory: NewRecallTool requires a non-nil MemoryStore")
	}
	return RecallTool{store: store}
}

// Compile-time assertion that RecallTool implements tool.Tool.
var _ tool.Tool = RecallTool{}

// recallArgs is the JSON argument shape for the Recall tool.
type recallArgs struct {
	Key string `json:"key"`
}

// Spec returns the model-facing specification of the Recall tool.
func (rt RecallTool) Spec() tool.ToolSpec {
	name := rt.name
	if name == "" {
		name = RecallToolName
	}
	desc := rt.desc
	if desc == "" {
		desc = recallDescription
	}
	return tool.ToolSpec{
		Name:        name,
		Description: desc,
		Schema: schema(`{
  "type": "object",
  "properties": {
    "key": {"type": "string", "description": "Exact key, or a prefix such as \"pref/\"."}
  },
  "required": ["key"]
}`),
	}
}

// ReadOnly reports that Recall does not mutate state.
func (RecallTool) ReadOnly() bool { return true }

// Execute looks up key: an exact hit returns its value; otherwise key is treated
// as a prefix and matching entries are listed. A miss is a non-error result.
func (rt RecallTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	var args recallArgs
	if msg, ok := parseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	if strings.TrimSpace(args.Key) == "" {
		return session.NewToolError(in.ID, "the \"key\" argument is required"), nil
	}

	// Exact key first.
	entry, ok, err := rt.store.Recall(ctx, args.Key)
	if err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf("could not recall %q: %v", args.Key, err)), nil
	}
	if ok {
		return session.NewToolResult(in.ID, truncateMemory(fmt.Sprintf("%s = %s", entry.Key, entry.Value))), nil
	}

	// Fall back to prefix listing.
	entries, err := rt.store.List(ctx, args.Key)
	if err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf("could not recall %q: %v", args.Key, err)), nil
	}
	if len(entries) == 0 {
		// A miss is a clear, non-error result so the model can proceed.
		return session.NewToolResult(in.ID, fmt.Sprintf("No memory found for %q.", args.Key)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d entr%s matching prefix %q:\n", len(entries), plural(len(entries)), args.Key)
	for _, e := range entries {
		fmt.Fprintf(&b, "%s = %s\n", e.Key, e.Value)
	}
	return session.NewToolResult(in.ID, truncateMemory(b.String())), nil
}

// --- SearchMemory tool ----------------------------------------------------

// SearchMemoryTool ranks memory entries by lexical relevance to a query and
// returns the best-matching keys (value omitted). It is read-only
// (ReadOnly() == true) so the loop may dispatch it in parallel with other reads.
type SearchMemoryTool struct {
	store tool.MemoryStore
	// name/desc override the catalog name and description for the user-model family
	// (SearchUserModel). Empty falls back to the project-memory defaults.
	name string
	desc string
}

// NewSearchMemoryTool constructs the SearchMemory tool bound to store. store
// must be non-nil.
func NewSearchMemoryTool(store tool.MemoryStore) tool.Tool {
	if store == nil {
		panic("memory: NewSearchMemoryTool requires a non-nil MemoryStore")
	}
	return SearchMemoryTool{store: store}
}

// Compile-time assertion that SearchMemoryTool implements tool.Tool.
var _ tool.Tool = SearchMemoryTool{}

// searchMemoryArgs is the JSON argument shape for the SearchMemory tool.
type searchMemoryArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// Spec returns the model-facing specification of the SearchMemory tool.
func (st SearchMemoryTool) Spec() tool.ToolSpec {
	name := st.name
	if name == "" {
		name = SearchMemoryToolName
	}
	desc := st.desc
	if desc == "" {
		desc = searchMemoryDescription
	}
	return tool.ToolSpec{
		Name:        name,
		Description: desc,
		Schema: schema(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "Topic words to rank memory entries against, e.g. \"preferred test runner\"."},
    "limit": {"type": "integer", "description": "Optional maximum number of results to return (default 10)."}
  },
  "required": ["query"]
}`),
	}
}

// ReadOnly reports that SearchMemory does not mutate state.
func (SearchMemoryTool) ReadOnly() bool { return true }

// Execute ranks entries against the query and renders the best matches as
// "key — description" lines (values omitted; Recall a key to load its value).
func (st SearchMemoryTool) Execute(ctx context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	var args searchMemoryArgs
	if msg, ok := parseArgs(in, &args); !ok {
		return session.NewToolError(in.ID, msg), nil
	}
	if strings.TrimSpace(args.Query) == "" {
		return session.NewToolError(in.ID, "the \"query\" argument is required"), nil
	}

	entries, err := st.store.Search(ctx, args.Query, args.Limit)
	if err != nil {
		return session.NewToolError(in.ID, fmt.Sprintf("could not search memory for %q: %v", args.Query, err)), nil
	}
	if len(entries) == 0 {
		// A no-hit search is a clear, non-error result so the model can proceed.
		return session.NewToolResult(in.ID, fmt.Sprintf("No memory entries match %q. (Try different words, or check the tier-0 index for exact keys.)", args.Query)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d match%s for %q (best first; Recall a key to load its full value):\n", len(entries), matchPlural(len(entries)), args.Query)
	for _, e := range entries {
		fmt.Fprintf(&b, "- %s — %s\n", e.Key, e.Description)
	}
	return session.NewToolResult(in.ID, truncateMemory(b.String())), nil
}

// --- Registration helpers -------------------------------------------------

// Tools returns the memory tools (Recall + Remember) bound to store, ready for
// registration. Memory is OPT-IN: the composition root calls this only when it
// has constructed a store, so a deployment without memory simply never registers
// these tools. store must be non-nil.
func Tools(store tool.MemoryStore) []tool.Tool {
	return []tool.Tool{
		NewRecallTool(store),
		NewRememberTool(store),
		NewSearchMemoryTool(store),
	}
}

// Register adds the memory tools (bound to store) to cat, returning the first
// registration error (e.g. a name collision) or nil. It is the opt-in companion
// to tools.Register; call it only when a memory store is configured.
func Register(cat *tool.Catalog, store tool.MemoryStore) error {
	for _, t := range Tools(store) {
		if err := cat.Register(t); err != nil {
			return err
		}
	}
	return nil
}

// --- helpers --------------------------------------------------------------

// parseArgs unmarshals a tool call's JSON arguments into dst, delegating to the
// shared toolkit helper. It returns a model-facing error string (not a Go
// error) on malformed JSON.
func parseArgs(in session.ToolCall, dst any) (string, bool) {
	return toolkit.ParseArgs(in, dst)
}

// schema wraps a static JSON-schema literal as json.RawMessage for a ToolSpec,
// delegating to the shared toolkit helper.
func schema(s string) json.RawMessage { return toolkit.Schema(s) }

// truncateMemory trims s to at most toolkit.MaxOutputBytes on a rune boundary,
// appending a marker when it trims. It delegates to the shared toolkit helper so
// the cap stays in lockstep with the rest of the adapter layer.
func truncateMemory(s string) string {
	return toolkit.Truncate(s, toolkit.MaxOutputBytes)
}

// plural returns "y" for one entry and "ies" otherwise, for "entr{y,ies}".
func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// matchPlural returns "" for one match and "es" otherwise, for "match{,es}".
func matchPlural(n int) string {
	if n == 1 {
		return ""
	}
	return "es"
}
