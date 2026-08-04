// Package tool — cycle note: FileSystem and Workspace are defined here, in the
// Tooling context that owns them (ARCHITECTURE.md §2), rather than in
// engine/port. This breaks the port↔tool import cycle that would form because
// port already imports tool (LLMRequest.Tools is []tool.ToolSpec) while
// Tool.Execute takes a Workspace.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

// ErrNoShell is the sentinel a CommandRunner returns when it has no shell to
// execute against (e.g. the in-memory runner, or a shell-less remote pod). The
// Bash tool surfaces it to the model as a tool-level error rather than aborting
// the harness.
var ErrNoShell = errors.New("tool: no shell available")

// ToolSpec is what the model sees for a tool: its name, a documentation-quality
// description (when to use / when not / example / limits), and the JSON schema
// for its arguments. ToolSpecs are stable across turns so the LLM adapter can
// cache them.
type ToolSpec struct {
	// Name is the tool's catalog name.
	Name string
	// Description is the model-facing documentation for the tool.
	Description string
	// Schema is the JSON schema describing the tool's Args.
	Schema json.RawMessage
}

// Tool is the contract every tool implements. ReadOnly drives the loop's
// read-parallel / mutate-serial dispatch. Execute runs the tool against a
// session-scoped Workspace and returns a domain ToolResult.
type Tool interface {
	// Spec returns the model-facing specification of the tool.
	Spec() ToolSpec
	// ReadOnly reports whether the tool only reads state (so the dispatcher may
	// run it in parallel with other read-only tools) versus mutating state
	// (which must run serially).
	ReadOnly() bool
	// Execute runs the tool. ctx carries cancellation; in is the model's call;
	// ws is the session-scoped filesystem/command seam. It returns a ToolResult
	// (with IsError set on a tool-level failure that should be fed back to the
	// model) and a non-nil error only for harness-level failures.
	Execute(ctx context.Context, in session.ToolCall, ws Workspace) (session.ToolResult, error)
}

// Disclosable is the OPTIONAL capability a Tool MAY implement to participate in
// progressive tool disclosure (pattern 9). A disclosable tool advertises a
// lightweight, metadata-only ToolSpec (typically name + a one-line description,
// with no or an empty Schema) until the model hydrates the full Spec() on demand
// via the ToolSearch tool. A tool that does NOT implement Disclosable is always
// advertised with its full Spec(), so the default catalog view is unchanged.
type Disclosable interface {
	Tool
	// Advertised returns the cheap, metadata-only spec rendered into the per-turn
	// tool inventory under progressive disclosure. Spec() remains the full,
	// hydrate-on-demand specification.
	Advertised() ToolSpec
}

// PlanOnly is the OPTIONAL capability a Tool MAY implement to declare that it is a
// plan-mode signalling tool: registered everywhere (so the shared and per-session
// catalog name-sets stay equal — guarded by TestPerSessionCatalogMatchesSharedCatalog)
// but advertised/callable ONLY in ModePlan. The catalog's mode projection
// (Available / Specs / AdvertisedSpecs) EXCLUDES a PlanOnly tool from every
// non-plan mode, so it is never offered to the model in default/acceptEdits.
//
// This is the projection gate for PresentPlan (issue #206): the plan-approval
// signalling tool is registered into every catalog (name-set equality holds) but
// the projection hides it outside plan mode. The dispatcher's name+mode check
// (sess.Mode == ModePlan && c.Name == "PresentPlan") is defense-in-depth ON TOP of
// this gate, not the sole gate. A tool that does NOT implement PlanOnly is
// advertised in every mode it is otherwise eligible for (read-only tools in plan
// mode, all tools in default/acceptEdits), so the default catalog view is
// unchanged for every non-plan-signalling tool.
type PlanOnly interface {
	Tool
	// PlanOnlyTool is the marker method. It carries no behaviour; implementing it
	// (alongside Tool) opts the tool into plan-mode-only advertisement.
	PlanOnlyTool()
}

// FileInfo is the minimal, provider-neutral file metadata the tools need. It is
// a subset of io/fs.FileInfo carried as plain fields so adapters (osfs, memfs)
// can populate it without leaking os types into the domain.
type FileInfo struct {
	// Name is the base name of the file.
	Name string
	// Size is the length in bytes.
	Size int64
	// Mode is the file mode bits.
	Mode fs.FileMode
	// ModTime is the last-modification time.
	ModTime time.Time
	// IsDir reports whether the entry is a directory.
	IsDir bool
}

// BashToolName is the catalog name of the Bash tool. It is the single authority
// for the name the Bash tool registers under (used in its Spec().Name) so a
// consumer can probe the catalog for bash enablement by referencing the constant
// rather than a local literal that could drift on a rename (see
// internal/adapter/server.Service.capabilities). It lives in the PORT package
// (not an adapter) because the permission evaluator special-cases the literal
// name — a tool named anything else would silently bypass the bash gate — so
// every Bash implementation must register under exactly this name, and
// engine/agent's own BashTool cannot import the fstools adapter to get it.
const BashToolName = "Bash"

// CommandRunner executes a shell command. Implementations may run it locally
// (/bin/sh), in a remote environment, or refuse it (no shell available). The
// agent loop never references this type — only the Bash tool depends on it,
// which is what makes the Bash tool (and therefore any command execution)
// optional in the catalog.
type CommandRunner interface {
	// Run executes command and returns its result. A non-zero exit is reported
	// via CommandResult.ExitCode (not error); error is for execution faults
	// (cancellation, timeout, or a missing shell — see ErrNoShell).
	//
	// workdir is the absolute working directory the command runs in — the
	// session/fork Workspace root the tool executes against, so a SINGLE runner
	// can serve both the main session (rooted at the configured workspace) and a
	// forked child (rooted at an isolated temp base OUTSIDE that configured root).
	// Implementations MUST honor a workdir outside their configured root and MUST
	// NOT confine/reject it — fork isolation depends on this. An EMPTY workdir
	// falls back to the runner's own configured root, so a runner can still be
	// used standalone.
	Run(ctx context.Context, command, workdir string) (CommandResult, error)
}

// CommandStreamer is an OPTIONAL CommandRunner capability for callers that need
// the command's output streamed to a caller-owned sink instead of captured into
// the runner's internal (head-capped, first-bytes-win) buffers — e.g. a
// background command whose RECENT output the caller wants in a bounded tail
// ring, which a first-bytes capture cannot provide. Discover it with a type
// assertion on a CommandRunner; a runner that does not implement it simply
// declines, and the caller must fail soft (an honest "not supported by this
// runner"), never fall back to Run and silently lose the tail.
type CommandStreamer interface {
	// RunStreaming runs command under the same shell and workdir rules as
	// CommandRunner.Run — an EMPTY workdir falls back to the runner's
	// configured root, and a workdir OUTSIDE that root MUST be honored, never
	// confined/rejected — with stdout and stderr written INTERLEAVED into out
	// in the order the OS delivers them. The CALLER owns bounding (e.g. a tail
	// ring): the runner writes everything it receives and does NOT also buffer
	// or cap the stream.
	//
	// Cancellation and timeout semantics match Run: governed by ctx, with the
	// runner's default timeout applied when ctx has no deadline, and a
	// cancel/timeout reported as a harness-level err (the partial output
	// written so far stands). A non-zero exit is NOT an error — it is reported
	// via exitCode, which replaces CommandResult for this path; err is
	// reserved for execution faults (cancellation, timeout, or a missing
	// shell — see ErrNoShell).
	RunStreaming(ctx context.Context, command, workdir string, out io.Writer) (exitCode int, err error)
}

// CommandResult is the outcome of a CommandRunner.Run invocation.
type CommandResult struct {
	// Stdout is the captured standard output (already truncated by the adapter).
	Stdout string
	// Stderr is the captured standard error (already truncated by the adapter).
	Stderr string
	// ExitCode is the process exit status.
	ExitCode int
}

// FileSystem is the low-level, path-oriented filesystem seam. Adapters implement
// it over the real OS (osfs) and over memory (memfs). Paths are interpreted by
// the adapter; the session-scoping and the Edit read-ledger live in Workspace,
// which composes a FileSystem.
type FileSystem interface {
	// Read returns the entire contents of the file at path.
	Read(ctx context.Context, path string) ([]byte, error)
	// Write replaces the contents of the file at path, creating it if needed.
	Write(ctx context.Context, path string, data []byte) error
	// Stat returns metadata for the file at path.
	Stat(ctx context.Context, path string) (FileInfo, error)
	// Glob returns the paths matching the shell-style pattern.
	Glob(ctx context.Context, pattern string) ([]string, error)
}

// WorkspaceReader is the READ-ONLY subset of Workspace: a rooted, path-scoped
// reader that can fetch a file and stat it, without any mutate capability. It is
// the narrow seam non-tool consumers take when they only need to LOOK at the
// workspace — e.g. the permission-config resolver (issue #13), which reads
// `.mecatl/settings.yaml` and stats it to revalidate its cache, but must never
// write. Passing a WorkspaceReader (not a full Workspace) to those consumers
// makes the read-only contract a compile-time guarantee.
//
// Workspace embeds it, so any *Workspace is usable where a WorkspaceReader is
// expected. All paths are session-relative; the adapter rejects escapes, EXCEPT
// for any explicit READ-ONLY allowed roots the adapter was constructed with
// (the activated-skill base-directory carve-out — see osfs.WithReadRoots),
// which Read/Stat may serve by absolute path.
type WorkspaceReader interface {
	// Root returns the absolute session root all paths are scoped to.
	Root() string
	// Read returns the contents of the file at the session-relative path.
	Read(ctx context.Context, path string) ([]byte, error)
	// Stat returns metadata for the file at the session-relative path.
	Stat(ctx context.Context, path string) (FileInfo, error)
}

// Workspace is the session-scoped seam every Tool executes against. It scopes
// all paths to a single session root (rejecting escapes such as "../"), exposes
// the read/search/run operations the 7 core tools need, and carries the
// per-session Edit read-ledger that lets the Edit tool enforce its invariants.
//
// All paths are relative to the session root unless documented otherwise;
// adapters must reject any path that resolves outside the root. The ONE
// sanctioned exception is read-only: an adapter may carry explicit allowed
// roots (osfs.WithReadRoots — the per-skill directories of discovered skills)
// that Read and Stat, and only Read and Stat, serve by absolute path. Write,
// Glob, and Grep are workspace-only always.
type Workspace interface {
	// WorkspaceReader is the read-only subset (Root + Read + Stat); embedding it
	// keeps the read methods defined once and lets a *Workspace satisfy a
	// read-only consumer.
	WorkspaceReader

	// Write replaces the contents of the file at the session-relative path,
	// creating it (and parent directories) if needed.
	Write(ctx context.Context, path string, data []byte) error
	// Glob returns session-relative paths matching the shell-style pattern.
	Glob(ctx context.Context, pattern string) ([]string, error)
	// Grep returns the matches of a regular expression across files selected by
	// an optional path glob. Results are capped/shaped by the adapter.
	Grep(ctx context.Context, pattern, pathGlob string) ([]GrepMatch, error)

	// RecordRead marks path as having been read at the given content version so
	// the Edit tool can later assert read-before-edit. version is an opaque
	// fingerprint (e.g. a content hash or mtime) the adapter chooses; the Edit
	// tool treats it as a comparable token, not a meaning-bearing value.
	RecordRead(path string, version string)
	// WasReadUnchanged reports whether path was previously recorded via
	// RecordRead AND its current on-disk version still equals the recorded one.
	// This is the read-before-edit-and-unchanged check the Edit tool's first
	// invariant depends on. It returns false if path was never read or if the
	// file changed since it was read.
	WasReadUnchanged(ctx context.Context, path string) (bool, error)
}

// MemoryEntry is a single cross-session memory record: an opaque key, its stored
// value, an optional one-line description, and the wall-clock time it was last
// written. It is the unit returned by MemoryStore.Recall, MemoryStore.List and
// MemoryStore.Index.
type MemoryEntry struct {
	// Key is the opaque lookup key (e.g. "pref/test-runner").
	Key string
	// Value is the stored text. The store treats it as opaque bytes. It is
	// EMPTY in MemoryStore.Index results, which omit values by design (the
	// tier-0 index carries only the routing table, not the payload).
	Value string
	// Description is an optional one-line summary used as the tier-0 index hook.
	// When empty on write, the store derives one from the value's first line; so
	// Index results always carry a non-empty Description even for entries that
	// were stored without one.
	Description string
	// UpdatedAt is the wall-clock time the entry was last written.
	UpdatedAt time.Time
}

// MemoryStore is the seam for conservative, cross-session ("tiered") memory
// (harness pattern 3). It is defined here, alongside Workspace and CommandRunner,
// for the same layering reason: the memory tools depend on it the way the Bash
// tool depends on CommandRunner, and keeping the interface in engine/tool
// avoids the port↔tool import cycle a separate package would risk.
//
// SCOPING: a MemoryStore is scoped per STORE INSTANCE — the composition root
// constructs one instance per scope (a per-project store for project memory, a
// per-user store for the user model), so entries written in one session are
// visible to later sessions over the SAME scope and never across scopes.
// Implementations must be safe for concurrent use and durable across process
// restarts; HOW they achieve that (file locking, a remote service, ...) is
// adapter-internal and must not leak into this contract.
//
// Conformance: engine/adapter/memconformance is the shared behavioral suite
// every implementation must pass (the flock-file reference adapter runs it
// today; remote drivers run it over their client).
type MemoryStore interface {
	// RememberEntry stores e, overwriting any existing entry under e.Key and
	// bumping its UpdatedAt. e.Description is the optional one-line tier-0 hook;
	// an empty description means "derive from the value's first non-empty line
	// on Index". An empty (or whitespace-only) key is rejected with an error.
	RememberEntry(ctx context.Context, e MemoryEntry) error
	// Recall returns the entry for the exact key. The boolean reports whether an
	// entry was found; a miss is (zero, false, nil), not an error.
	Recall(ctx context.Context, key string) (MemoryEntry, bool, error)
	// List returns all entries whose key has the given prefix, sorted by key for
	// deterministic output. An empty prefix returns every entry. Unlike Index and
	// Search, List returns FULL entries — Value included — so consumers (e.g. a
	// consolidation planner, a prefix-fallback read) can load payloads from it.
	List(ctx context.Context, prefix string) ([]MemoryEntry, error)
	// Forget deletes the entry for key. Deleting a missing key is not an error.
	Forget(ctx context.Context, key string) error
	// Index returns the tier-0 routing table: every entry as (key, description,
	// updated-at) with the VALUE OMITTED, sorted by key for deterministic output.
	// The implementation fills Description (explicit, else derived from the
	// value's first non-empty line) but does NOT apply the tier-0 size cap —
	// capping/rendering is the consumer's concern.
	Index(ctx context.Context) ([]MemoryEntry, error)
	// Search returns up to k entries relevant to query, best-first. Like Index,
	// results carry (key, description, updated-at) with the VALUE OMITTED — the
	// value may participate in scoring but is never returned; callers Recall a
	// key to load it. Ordering must be deterministic for identical store state
	// and query; entries with no relevance to the query are dropped, not padded.
	// An empty or whitespace-only query yields an empty slice, NOT an error.
	// k <= 0 selects the implementation's default page size. Ranking is
	// implementation-defined (the reference adapter uses local lexical BM25).
	Search(ctx context.Context, query string, k int) ([]MemoryEntry, error)
}

// GrepMatch is a single Workspace.Grep hit.
type GrepMatch struct {
	// Path is the session-relative file the match occurred in.
	Path string
	// Line is the 1-based line number of the match.
	Line int
	// Text is the matching line's content.
	Text string
}
