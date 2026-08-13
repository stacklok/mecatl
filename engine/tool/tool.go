// Package tool — cycle note: FileSystem, Workspace, and Environment are defined here, in the
// Tooling context that owns them (ARCHITECTURE.md §2), rather than in
// engine/port. This breaks the port↔tool import cycle that would form because
// port already imports tool (LLMRequest.Tools is []tool.ToolSpec) while
// Tool.Execute takes an Environment.
package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
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
// session-scoped Environment (Workspace + optional bound CommandRunner + ref)
// and returns a domain ToolResult.
type Tool interface {
	// Spec returns the model-facing specification of the tool.
	Spec() ToolSpec
	// ReadOnly reports whether the tool only reads state (so the dispatcher may
	// run it in parallel with other read-only tools) versus mutating state
	// (which must run serially).
	ReadOnly() bool
	// Execute runs the tool. ctx carries cancellation; in is the model's call;
	// env is the session-scoped Environment (Workspace + optional bound
	// CommandRunner + backend ref). It returns a ToolResult (with IsError set
	// on a tool-level failure that should be fed back to the model) and a
	// non-nil error only for harness-level failures.
	Execute(ctx context.Context, in session.ToolCall, env Environment) (session.ToolResult, error)
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
//
// BOUND RUNNER (issue #462). A CommandRunner is bound to a single namespace at
// construction: it privately stores its root and Run executes against it. The
// per-call workdir parameter is GONE — a runner serves exactly one Environment
// (the main session, or a forked child whose runner is bound to the child
// namespace), so the command's cwd always matches the workspace the tool
// executes against. A runner that can no longer serve a shell returns
// ErrNoShell.
type CommandRunner interface {
	// Run executes command and returns its result. A non-zero exit is reported
	// via CommandResult.ExitCode (not error); error is for execution faults
	// (cancellation, timeout, or a missing shell — see ErrNoShell). The command
	// runs in the runner's bound namespace root.
	Run(ctx context.Context, command string) (CommandResult, error)
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
	// RunStreaming runs command under the same shell rules as CommandRunner.Run
	// — the runner's BOUND namespace root, no per-call workdir (issue #462) —
	// with stdout and stderr written INTERLEAVED into out in the order the OS
	// delivers them. The CALLER owns bounding (e.g. a tail ring): the runner
	// writes everything it receives and does NOT also buffer or cap the stream.
	//
	// Cancellation and timeout semantics match Run: governed by ctx, with the
	// runner's default timeout applied when ctx has no deadline, and a
	// cancel/timeout reported as a harness-level err (the partial output
	// written so far stands). A non-zero exit is NOT an error — it is reported
	// via exitCode, which replaces CommandResult for this path; err is
	// reserved for execution faults (cancellation, timeout, or a missing
	// shell — see ErrNoShell).
	RunStreaming(ctx context.Context, command string, out io.Writer) (exitCode int, err error)
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
	// Stat returns metadata for the file at path.
	Stat(ctx context.Context, path string) (FileInfo, error)
	// Glob returns the paths matching the shell-style pattern.
	Glob(ctx context.Context, pattern string) ([]string, error)
}

// FileVersion is the opaque, comparable content version a Workspace attaches to a
// version-bearing read (ADR 0103). It is an adapter-minted token (a content hash,
// an inode+mtime pair, a remote ETag, …) the caller compares for equality with
// another FileVersion from the SAME adapter and passes back to a conditional
// mutation. It carries NO meaning outside equality and is NEVER used as a
// sentinel: there is deliberately no "any"/"wildcard"/"unversioned" FileVersion
// that means "overwrite unconditionally" — an unconditional overwrite is a
// distinct adapter/bootstrap operation that is deliberately NOT part of the
// Workspace capability passed to Tool.Execute. The zero value is an unusable
// placeholder; RecordedVersion returns ok=false (not a zero FileVersion sentinel)
// for a path that was never recorded.
type FileVersion struct {
	// token is interpreted only by the adapter that minted it. valid separates
	// the zero value (never a usable version) from an adapter token that happens
	// to be the empty string; empty is therefore never an overwrite sentinel.
	token string
	valid bool
}

// Equal reports whether two valid FileVersions carry the same opaque token.
// The zero value is invalid and never equals any version, including another
// zero value.
func (f FileVersion) Equal(o FileVersion) bool {
	return f.valid && o.valid && f.token == o.token
}

// NewFileVersion mints a FileVersion from an adapter-private token. It is the
// constructor adapters use to wrap the opaque token they computed (a content
// hash, an ETag, …); the field stays unexported so callers cannot inspect it.
// The agent-facing Read/Edit/Write tools never call this — they only receive
// FileVersions from ReadVersion/CreateFile/ReplaceFile and pass them back. An
// empty token is still a valid opaque token, never an "any version" sentinel.
func NewFileVersion(token string) FileVersion {
	return FileVersion{token: token, valid: true}
}

// Token returns the adapter-private opaque token this FileVersion carries, and
// ok reports whether the version is valid (a non-zero FileVersion). The zero
// value returns ("", false) — it is never an "any version" sentinel — so a
// caller can distinguish "no version recorded" from "an adapter minted the
// empty-string token". It is the serializer hook for a planned remote backend
// that must round-trip an adapter-minted version over the wire: the backend
// stores the token verbatim and reconstructs the FileVersion with
// NewFileVersion(token) on the way back. Callers MUST treat the token as
// opaque (compare with Equal, never inspect its bytes); only a serializer
// owned by the SAME adapter that minted the version ever reads it.
func (f FileVersion) Token() (token string, ok bool) {
	return f.token, f.valid
}

// WorkspaceReader is the READ-ONLY subset of Workspace: a rooted, path-scoped
// reader that can fetch a file and stat it, without any mutate capability. It is
// the narrow seam non-tool consumers take when they only need to LOOK at the
// workspace — e.g. the permission-config resolver (issue #13), which reads
// `.mecatl/settings.yaml` and stats it to revalidate its cache, but must never
// write. Passing a WorkspaceReader (not a full Workspace) to those consumers
// makes the read-only contract a compile-time guarantee.
//
// It carries the PLAIN (non-versioned) Read/Stat: non-agent consumers that only
// inspect the tree (permission config, prompt discovery, the agent-def/skill
// sources) never participate in the read-ledger / conditional-mutation protocol
// (ADR 0103) and do not need a FileVersion. The agent-facing built-in
// Read/Edit/Write tools use the version-bearing ReadVersion + CreateFile/
// ReplaceFile on the full Workspace, NOT this plain Read.
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
// the read/search operations the 7 core tools need, and carries the per-session
// read-ledger + the explicit, unambiguous mutation operations the built-in
// Edit/Write tools enforce their invariants through (ADR 0103).
//
// All paths are relative to the session root unless documented otherwise;
// adapters must reject any path that resolves outside the root. The ONE
// sanctioned exception is read-only: an adapter may carry explicit allowed
// roots (osfs.WithReadRoots — the per-skill directories of discovered skills)
// that Read/Stat/ReadVersion serve by absolute path. CreateFile, ReplaceFile,
// Glob, and Grep are workspace-only always.
//
// VERSION PROTOCOL (ADR 0103). The Workspace capability exposes only the
// explicit create-only / conditional-replace-by-version pair, so a tool mutation
// can never silently clobber a concurrent change:
//
//   - ReadVersion returns the content AND the authoritative FileVersion the
//     adapter currently holds for path. The built-in Read tool records that
//     version via RecordRead (a pure in-memory store, NO I/O) so a later
//     Edit/Write can assert read-before-mutate-and-unchanged.
//   - Existing-file Write and Edit: require a recorded version, ReadVersion
//     again to get the CURRENT version, compare the recorded version with the
//     current version (unchanged-since), and finish with ReplaceFile against
//     the CURRENT version — the conditional CAS that survives a change that
//     lands between the Edit's own ReadVersion and its ReplaceFile.
//   - New-file Write: CreateFile (atomic create-only; fails if the path already
//     exists, so it never silently clobbers).
//
// ADAPTER ATOMICITY CONTRACT. CreateFile and the compare+mutation in
// ReplaceFile are atomic with respect to concurrent calls through the same live
// Workspace/backend handle: a conditional replace sees either the pre- or the
// post-mutation version, never a torn middle. osfs provides the stronger guarantee
// across Workspace instances in this process using fixed canonical-path lock
// stripes. Other adapters need not globally serialize independent Workspace
// instances unless their backend contract says so. A NON-COOPERATING POSIX writer
// (a shell command, an external editor) that bypasses the Workspace seam can still
// race a conditional replace — this is honest best-effort same-process CAS, NOT
// kernel-level locking; a future remote transport will provide true backend CAS
// (ADR 0103, remote transport deferred).
type Workspace interface {
	// WorkspaceReader is the read-only subset (Root + plain Read + Stat);
	// embedding it keeps the read methods defined once and lets a *Workspace
	// satisfy a read-only consumer. Non-agent consumers use the plain Read;
	// agent-facing tools use ReadVersion below.
	WorkspaceReader

	// ReadVersion returns the contents of the file at path AND the authoritative
	// FileVersion the adapter currently holds for it. It is the version-bearing
	// read the built-in Read tool uses (recording the returned version via
	// RecordRead). It reads the SAME backing store as the plain Read; the only
	// difference is it also mints and returns a FileVersion.
	ReadVersion(ctx context.Context, path string) ([]byte, FileVersion, error)

	// CreateFile creates a NEW file at path with the given content, atomically.
	// It fails (wrapping fs.ErrExist) if a file already exists at path — it is
	// create-only, NEVER an overwrite, so it can never silently clobber an
	// existing file. Parent directories are created as needed. It returns the
	// new file's FileVersion. The agent-facing Write tool uses it for a
	// not-yet-existing path.
	CreateFile(ctx context.Context, path string, data []byte) (FileVersion, error)

	// ReplaceFile conditionally replaces the contents of the file at path with
	// data, ONLY if the file's CURRENT authoritative FileVersion equals old.
	// On success it returns the new FileVersion. On a version mismatch (the
	// file changed between the caller's ReadVersion and this call — including a
	// concurrent mutation through the same backend) it returns a
	// *VersionMismatchError; the caller re-reads and retries. If the file does
	// not exist it returns an error wrapping fs.ErrNotExist. It is the
	// conditional CAS the agent-facing Edit and existing-file Write tools finish
	// with, against the CURRENT version their own ReadVersion just returned. old must be a FileVersion the SAME adapter
	// minted (from ReadVersion or a prior CreateFile/ReplaceFile); a zero
	// FileVersion is never a valid "any version" sentinel — it always mismatches.
	ReplaceFile(ctx context.Context, path string, old FileVersion, data []byte) (FileVersion, error)

	// Glob returns session-relative paths matching the shell-style pattern.
	Glob(ctx context.Context, pattern string) ([]string, error)
	// Grep returns the matches of a regular expression across files selected by
	// an optional path glob. Results are capped/shaped by the adapter.
	Grep(ctx context.Context, pattern, pathGlob string) ([]GrepMatch, error)

	// RecordRead records that path was read at the authoritative version. It is
	// a PURE IN-MEMORY store: it performs NO I/O and stores the EXACT version
	// passed (the caller supplies the FileVersion its ReadVersion returned). A
	// later RecordedVersion lookup compares against this stored token. The
	// built-in Read tool calls it with the version ReadVersion minted; the
	// built-in Edit/Write tools call it after a successful CreateFile/ReplaceFile
	// so a subsequent same-turn Edit stays valid. Ledger-key normalization must
	// also perform NO I/O: ordinary absolute <root>/<rel> and relative <rel>
	// forms should converge lexically, while physical symlink aliases may
	// conservatively miss and force another Read.
	RecordRead(path string, version FileVersion)

	// RecordedVersion returns the version previously recorded for path via
	// RecordRead, performing NO I/O. ok is false if path was never recorded or
	// the live Workspace/ledger was rebuilt. It is the I/O-free
	// read-before-mutate lookup: the agent-facing Edit/Write tools call it to
	// assert the file was read this session; they then separately ReadVersion
	// for the CURRENT version and compare, so a file that changed since the
	// recorded read is caught by the version comparison, not by this lookup.
	RecordedVersion(path string) (version FileVersion, ok bool)
}

// VersionMismatchError is the error ReplaceFile returns when the file's current
// authoritative version does not equal the old version the caller supplied — a
// concurrent mutation landed between the caller's read and its conditional
// replace. Callers classify it with errors.As. It is the model-visible "changed
// since you read it" condition surfaced by the Edit/Write tools.
type VersionMismatchError struct {
	// Path is the session-relative (or canonical) path that mismatched.
	Path string
}

// Error implements the error interface without exposing either opaque version.
func (e *VersionMismatchError) Error() string {
	return fmt.Sprintf("tool: version mismatch for %q: file changed since read", e.Path)
}

// LedgerKey is the I/O-FREE lexical normalization a Workspace read-ledger uses to
// converge ordinary forms of the same in-root file onto one key, so a file read
// by relative path and then mutated by absolute in-root path (or vice versa)
// matches without any filesystem inspection. It performs NO Lstat/EvalSymlinks/
// RPC: physical symlink aliases may conservatively produce distinct entries
// (a safe false-negative that forces another Read).
//
//   - A session-RELATIVE path keys by its cleaned slash form (filepath.Clean,
//     ToSlash). filepath.IsLocal reports not-relative for absolute/slash-prefixed
//     operands; a relative path that climbs above the root ("../x") still keys by
//     its cleaned form (the ledger is a lookup, not a confinement gate —
//     confinement is the Workspace's business at use time).
//   - An ordinary ABSOLUTE <root>/<rel> path is reduced with filepath.Rel so it
//     converges with the relative <rel> form.
//   - An absolute path that does NOT lie under root (an out-of-root relaxed-read
//     path, or any path whose cleaned form climbs above root) keys by its cleaned
//     absolute form, so a `..`-carrying lexical alias matches in both directions.
//
// root MUST be the already-canonicalized absolute session root a Workspace
// reports via Root() (osfs canonicalizes it at construction; memfs passes its
// logical root). A relative path with an absolute root, or an absolute path with
// a relative root, is handled defensively: the former keys by the cleaned
// relative form, the latter by the cleaned absolute form.
func LedgerKey(root, path string) string {
	if path == "" {
		return "."
	}
	if !filepath.IsAbs(path) && !strings.HasPrefix(path, "/") {
		return filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	}
	cleaned := filepath.Clean(path)
	if root != "" {
		if rel, err := filepath.Rel(root, cleaned); err == nil &&
			rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(filepath.Clean(rel))
		}
	}
	return filepath.ToSlash(cleaned)
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
