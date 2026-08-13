package fstools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// errSimulatedRead is a sentinel read error used to prove the Edit/Write tools
// surface a ReadVersion failure as a model-visible "cannot read" error rather
// than the changed-since-read refusal.
var errSimulatedRead = errors.New("simulated read failure")

// call builds a ToolCall with JSON args marshalled from m.
func call(t *testing.T, name string, m map[string]any) session.ToolCall {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return session.NewToolCall(session.ToolCallID("id-"+name), name, raw)
}

// exec runs a tool and fails the test on a harness-level (Go) error.
func exec(t *testing.T, tl tool.Tool, in session.ToolCall, ws tool.Workspace) session.ToolResult {
	t.Helper()
	res, err := tl.Execute(context.Background(), in, mustEnv(ws))
	if err != nil {
		t.Fatalf("%s: unexpected harness error: %v", tl.Spec().Name, err)
	}
	return res
}

// execWithRunner runs a tool against an Environment that binds runner (the Bash
// tool reads it off the Environment, issue #462).
func execWithRunner(t *testing.T, tl tool.Tool, in session.ToolCall, ws tool.Workspace, runner tool.CommandRunner) session.ToolResult {
	t.Helper()
	env, err := tool.NewEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "test"}, ws, runner)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tl.Execute(context.Background(), in, env)
	if err != nil {
		t.Fatalf("%s: unexpected harness error: %v", tl.Spec().Name, err)
	}
	return res
}

// mustEnv wraps a Workspace into a shell-less tool.Environment for the fstools
// tests (the file tools never use a runner).
func mustEnv(ws tool.Workspace) tool.Environment {
	env, err := tool.NewEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "test"}, ws, nil)
	if err != nil {
		panic(err)
	}
	return env
}

// seed writes a file directly into a memfs workspace (no read recorded).
func seed(t *testing.T, ws *memfs.Workspace, path, content string) {
	t.Helper()
	if err := ws.Write(context.Background(), path, []byte(content)); err != nil {
		t.Fatalf("seed %q: %v", path, err)
	}
}

// TestReadOnlyFlags pins the ReadOnly() contract that drives the loop's
// read-parallel / mutate-serial dispatch (gauntlet #4) for the filesystem tools.
func TestReadOnlyFlags(t *testing.T) {
	want := map[string]bool{
		"Read":  true,
		"Edit":  false,
		"Write": false,
		"Grep":  true,
		"Glob":  true,
	}
	got := map[string]bool{}
	for _, tl := range All() {
		got[tl.Spec().Name] = tl.ReadOnly()
	}
	if len(got) != len(want) {
		t.Fatalf("All() returned %d tools, want %d", len(got), len(want))
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s.ReadOnly() = %v, want %v", name, got[name], w)
		}
	}
	// Bash is mutating.
	if NewBashTool().ReadOnly() {
		t.Error("Bash.ReadOnly() = true, want false")
	}
}

// TestAllAndRegister pins the fstools bundle: All() is exactly the five
// filesystem tools (NO Bash — it is opt-in via NewBashTool), Register adds them,
// and NewBashTool registers Bash separately.
func TestAllAndRegister(t *testing.T) {
	if len(All()) != 5 {
		t.Fatalf("All() = %d tools, want 5 (Read, Edit, Write, Grep, Glob)", len(All()))
	}
	for _, tl := range All() {
		if tl.Spec().Name == BashToolName {
			t.Fatal("All() must not include Bash (it requires a CommandRunner)")
		}
	}
	cat := tool.NewCatalog()
	if err := Register(cat); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, name := range []string{"Read", "Edit", "Write", "Grep", "Glob"} {
		if _, ok := cat.Lookup(name); !ok {
			t.Errorf("catalog missing %q after Register", name)
		}
	}
	if _, ok := cat.Lookup(BashToolName); ok {
		t.Error("Register added Bash; it must be opt-in via NewBashTool")
	}
	cat.MustRegister(NewBashTool())
	if _, ok := cat.Lookup(BashToolName); !ok {
		t.Error("catalog missing Bash after explicit NewBashTool registration")
	}
	// Re-registering must collide.
	if err := Register(cat); err == nil {
		t.Error("re-Register did not return a duplicate error")
	}
}

func TestSpecsHaveDocs(t *testing.T) {
	all := append(All(), NewBashTool())
	for _, tl := range all {
		s := tl.Spec()
		if s.Name == "" {
			t.Error("tool with empty name")
		}
		if len(s.Description) < 80 {
			t.Errorf("%s: description too short to be onboarding docs (%d chars)", s.Name, len(s.Description))
		}
		var js any
		if err := json.Unmarshal(s.Schema, &js); err != nil {
			t.Errorf("%s: schema is not valid JSON: %v", s.Name, err)
		}
	}
}

func TestReadLineNumbersAndRange(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "a.txt", "alpha\nbeta\ngamma\n")

	res := exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), ws)
	if res.IsError {
		t.Fatalf("Read errored: %s", res.Content)
	}
	wantPrefix := "     1\talpha\n     2\tbeta\n     3\tgamma\n"
	if res.Content != wantPrefix {
		t.Errorf("Read content mismatch.\n got: %q\nwant: %q", res.Content, wantPrefix)
	}

	// offset/limit: start at line 2, one line.
	res = exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt", "offset": 2, "limit": 1}), ws)
	if res.Content != "     2\tbeta\n" {
		t.Errorf("Read offset/limit = %q, want %q", res.Content, "     2\tbeta\n")
	}
}

func TestReadRecordsReadForEdit(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "a.txt", "hello\n")
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), ws)
	ver, ok := ws.RecordedVersion("a.txt")
	if !ok {
		t.Fatal("Read did not record the read in the ledger")
	}
	// The recorded version must match the file's current version (it did not change).
	if _, cur, err := ws.ReadVersion(context.Background(), "a.txt"); err != nil {
		t.Fatalf("ReadVersion: %v", err)
	} else if !cur.Equal(ver) {
		t.Error("recorded version differed from current version after an unchanged Read")
	}
}

func TestReadTruncatesLineCap(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	var sb strings.Builder
	for i := 0; i < maxReadLines+50; i++ {
		fmt.Fprintf(&sb, "line%d\n", i)
	}
	seed(t, ws, "big.txt", sb.String())
	res := exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "big.txt"}), ws)
	if !strings.Contains(res.Content, "output truncated") {
		t.Error("expected truncation marker for over-cap file")
	}
	if len(res.Content) > MaxOutputBytes+200 {
		t.Errorf("output not byte-capped: %d bytes", len(res.Content))
	}
	// First line is always present and correctly numbered.
	if !strings.HasPrefix(res.Content, "     1\tline0\n") {
		t.Errorf("expected first line numbered, got start: %.20q", res.Content)
	}
}

// TestReadLineCapWithoutByteCap exercises the 2000-line cap using short lines so
// the byte cap does not fire first.
func TestReadLineCapWithoutByteCap(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	var sb strings.Builder
	for i := 0; i < maxReadLines+50; i++ {
		sb.WriteString("a\n") // tiny lines: byte cap won't trigger before line cap
	}
	seed(t, ws, "big.txt", sb.String())
	res := exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "big.txt"}), ws)
	if !strings.Contains(res.Content, "showed 2000 lines") {
		t.Errorf("expected line-cap marker, got tail: %.80q", res.Content[len(res.Content)-80:])
	}
}

func TestReadMissingFile(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	res := exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "nope.txt"}), ws)
	if !res.IsError {
		t.Error("Read of missing file should be a tool error")
	}
}

// Gauntlet #2: Edit must fail if the file was not read this session.
func TestEditFailsWhenNotRead(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "a.txt", "hello world\n")
	res := exec(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": "a.txt", "old_string": "hello", "new_string": "hi",
	}), ws)
	if !res.IsError {
		t.Fatal("Edit on un-read file must error (gauntlet #2)")
	}
	// Green: after reading, the same edit succeeds.
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), ws)
	res = exec(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": "a.txt", "old_string": "hello", "new_string": "hi",
	}), ws)
	if res.IsError {
		t.Fatalf("Edit after Read should succeed, got error: %s", res.Content)
	}
	data, _ := ws.Read(context.Background(), "a.txt")
	if string(data) != "hi world\n" {
		t.Errorf("file = %q, want %q", data, "hi world\n")
	}
}

func TestEditNonMatching(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "a.txt", "hello\n")
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), ws)
	res := exec(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": "a.txt", "old_string": "absent", "new_string": "x",
	}), ws)
	if !res.IsError {
		t.Error("Edit with non-matching old_string must error")
	}
}

func TestEditNonUniqueRequiresReplaceAll(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "a.txt", "x\nx\nx\n")
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), ws)

	// Red: non-unique without replace_all.
	res := exec(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": "a.txt", "old_string": "x", "new_string": "y",
	}), ws)
	if !res.IsError {
		t.Fatal("non-unique old_string without replace_all must error")
	}

	// Green: replace_all succeeds.
	res = exec(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": "a.txt", "old_string": "x", "new_string": "y", "replace_all": true,
	}), ws)
	if res.IsError {
		t.Fatalf("replace_all should succeed, got: %s", res.Content)
	}
	data, _ := ws.Read(context.Background(), "a.txt")
	if string(data) != "y\ny\ny\n" {
		t.Errorf("file = %q, want %q", data, "y\ny\ny\n")
	}
}

func TestEditFailsWhenChangedSinceRead(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "a.txt", "hello\n")
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), ws)
	// Mutate on disk after the read.
	seed(t, ws, "a.txt", "changed\n")
	res := exec(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": "a.txt", "old_string": "changed", "new_string": "x",
	}), ws)
	if !res.IsError {
		t.Error("Edit must fail when the file changed since it was read")
	}
}

func TestWriteNewFileNoReadNeeded(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	res := exec(t, WriteTool{}, call(t, "Write", map[string]any{
		"path": "new.txt", "content": "fresh\n",
	}), ws)
	if res.IsError {
		t.Fatalf("Write of new file should succeed, got: %s", res.Content)
	}
	data, _ := ws.Read(context.Background(), "new.txt")
	if string(data) != "fresh\n" {
		t.Errorf("file = %q, want %q", data, "fresh\n")
	}
}

// createConflictWorkspace injects a create after Write's not-exist Stat but
// before its create-only operation, deterministically exercising the
// model-visible create-conflict path.
type createConflictWorkspace struct {
	tool.Workspace
	injected bool
}

func (w *createConflictWorkspace) CreateFile(ctx context.Context, path string, data []byte) (tool.FileVersion, error) {
	if !w.injected {
		w.injected = true
		if _, err := w.Workspace.CreateFile(ctx, path, []byte("concurrent\n")); err != nil {
			return tool.FileVersion{}, err
		}
	}
	return w.Workspace.CreateFile(ctx, path, data)
}

func TestWriteCreateOnlyRejectsConcurrentCreate(t *testing.T) {
	base := memfs.NewWorkspace("/")
	ws := &createConflictWorkspace{Workspace: base}
	res := exec(t, WriteTool{}, call(t, "Write", map[string]any{
		"path": "new.txt", "content": "agent\n",
	}), ws)
	if !res.IsError || !strings.Contains(res.Content, "already exists") {
		t.Fatalf("Write create conflict result = (error=%v, content=%q), want model-visible create refusal", res.IsError, res.Content)
	}
	data, err := base.Read(context.Background(), "new.txt")
	if err != nil {
		t.Fatalf("Read final file: %v", err)
	}
	if string(data) != "concurrent\n" {
		t.Fatalf("final file = %q, want concurrent create preserved", data)
	}
}

func TestWriteOverwriteUnreadRejected(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "a.txt", "old\n")
	res := exec(t, WriteTool{}, call(t, "Write", map[string]any{
		"path": "a.txt", "content": "new\n",
	}), ws)
	if !res.IsError {
		t.Fatal("overwriting an existing un-read file must be rejected")
	}
	// Green: after Read, overwrite is allowed.
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), ws)
	res = exec(t, WriteTool{}, call(t, "Write", map[string]any{
		"path": "a.txt", "content": "new\n",
	}), ws)
	if res.IsError {
		t.Fatalf("overwrite after Read should succeed, got: %s", res.Content)
	}
}

// replaceConflictWorkspace injects one cooperating mutation at the final
// ReplaceFile call. The built-in tool has already completed its current
// ReadVersion by then, so this deterministically proves the final conditional
// replace is load-bearing rather than relying on scheduler timing.
type replaceConflictWorkspace struct {
	tool.Workspace
	base     *memfs.Workspace
	injected bool
}

func (w *replaceConflictWorkspace) ReplaceFile(ctx context.Context, path string, old tool.FileVersion, data []byte) (tool.FileVersion, error) {
	if !w.injected {
		w.injected = true
		if err := w.base.Write(ctx, path, []byte("concurrent\n")); err != nil {
			return tool.FileVersion{}, err
		}
	}
	return w.Workspace.ReplaceFile(ctx, path, old, data)
}

func TestEditConditionalReplaceRejectsConcurrentChange(t *testing.T) {
	base := memfs.NewWorkspace("/")
	seed(t, base, "a.txt", "old\n")
	ws := &replaceConflictWorkspace{Workspace: base, base: base}
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), ws)

	res := exec(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": "a.txt", "old_string": "old", "new_string": "edited",
	}), ws)
	if !res.IsError || !strings.Contains(res.Content, "changed since you read it") {
		t.Fatalf("Edit CAS conflict result = (error=%v, content=%q), want model-visible changed-since refusal", res.IsError, res.Content)
	}
	data, err := base.Read(context.Background(), "a.txt")
	if err != nil {
		t.Fatalf("Read final file: %v", err)
	}
	if string(data) != "concurrent\n" {
		t.Fatalf("final file = %q, want concurrent mutation preserved", data)
	}
}

func TestWriteConditionalReplaceRejectsConcurrentChange(t *testing.T) {
	base := memfs.NewWorkspace("/")
	seed(t, base, "a.txt", "old\n")
	ws := &replaceConflictWorkspace{Workspace: base, base: base}
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), ws)

	res := exec(t, WriteTool{}, call(t, "Write", map[string]any{
		"path": "a.txt", "content": "overwritten\n",
	}), ws)
	if !res.IsError || !strings.Contains(res.Content, "changed since you read it") {
		t.Fatalf("Write CAS conflict result = (error=%v, content=%q), want model-visible changed-since refusal", res.IsError, res.Content)
	}
	data, err := base.Read(context.Background(), "a.txt")
	if err != nil {
		t.Fatalf("Read final file: %v", err)
	}
	if string(data) != "concurrent\n" {
		t.Fatalf("final file = %q, want concurrent mutation preserved", data)
	}
}

// --- cause-first read errors & deleted-after-read ---

// readErrorWorkspace is a fake that returns a fixed error from ReadVersion (for a
// recorded path) so the Edit/Write tools hit the re-read branch and must surface
// a model-visible "cannot read" error rather than the changed-since-read refusal.
type readErrorWorkspace struct {
	tool.Workspace
	readErr error
}

func (w *readErrorWorkspace) ReadVersion(context.Context, string) ([]byte, tool.FileVersion, error) {
	return nil, tool.FileVersion{}, w.readErr
}

// TestEditReadVersionErrorIsCauseFirst pins that a non-nil ReadVersion error in
// Edit surfaces a model-visible "cannot read %q: %v" error, NOT the
// changed-since-read retry message. A read failure (deletion, unreadable path,
// escape rejection) is not "changed since you read it"; the model needs the real
// cause to act on it.
func TestEditReadVersionErrorIsCauseFirst(t *testing.T) {
	base := memfs.NewWorkspace("/")
	seed(t, base, "a.txt", "hello\n")
	// Satisfy the read-before-edit precondition via the REAL workspace first, so
	// the only failing step in the Edit under test is the re-ReadVersion.
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), base)
	ws := &readErrorWorkspace{Workspace: base, readErr: errSimulatedRead}

	res := exec(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": "a.txt", "old_string": "hello", "new_string": "hi",
	}), ws)
	if !res.IsError {
		t.Fatal("Edit with a ReadVersion error must be a tool error")
	}
	if strings.Contains(res.Content, "changed since you read it") {
		t.Fatalf("Edit ReadVersion error must not be the changed-since refusal: %q", res.Content)
	}
	if !strings.Contains(res.Content, "cannot read") || !strings.Contains(res.Content, "a.txt") {
		t.Fatalf("Edit ReadVersion error must be model-visible 'cannot read %q': %q", "a.txt", res.Content)
	}
}

// TestWriteReadVersionErrorIsCauseFirst pins the same cause-first behavior for
// the existing-file Write path.
func TestWriteReadVersionErrorIsCauseFirst(t *testing.T) {
	base := memfs.NewWorkspace("/")
	seed(t, base, "a.txt", "old\n")
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), base)
	ws := &readErrorWorkspace{Workspace: base, readErr: errSimulatedRead}

	res := exec(t, WriteTool{}, call(t, "Write", map[string]any{
		"path": "a.txt", "content": "new\n",
	}), ws)
	if !res.IsError {
		t.Fatal("Write with a ReadVersion error must be a tool error")
	}
	if strings.Contains(res.Content, "changed since you read it") {
		t.Fatalf("Write ReadVersion error must not be the changed-since refusal: %q", res.Content)
	}
	if !strings.Contains(res.Content, "cannot read") || !strings.Contains(res.Content, "a.txt") {
		t.Fatalf("Write ReadVersion error must be model-visible 'cannot read %q': %q", "a.txt", res.Content)
	}
}

// deletedAfterReadWorkspace injects a deletion at the final ReplaceFile call:
// the tool has already done its current ReadVersion (succeeding), so this
// deterministically proves a concurrent delete-after-read surfaces as a
// model-visible "deleted since you read it" refusal, not a harness-level error
// and not the changed-since-read message.
type deletedAfterReadWorkspace struct {
	tool.Workspace
	base     *memfs.Workspace
	injected bool
}

func (w *deletedAfterReadWorkspace) ReplaceFile(ctx context.Context, path string, old tool.FileVersion, data []byte) (tool.FileVersion, error) {
	if !w.injected {
		w.injected = true
		// memfs has no delete; overwrite-then-read-error approximates a
		// concurrent delete by making the path unreadable for the CAS read the
		// underlying ReplaceFile performs. We instead model the delete by
		// returning fs.ErrNotExist directly, mirroring an adapter whose
		// ReplaceFile observes the file vanished between the current read and
		// the conditional write.
		return tool.FileVersion{}, fmt.Errorf("memfs: replace %q: %w", path, fs.ErrNotExist)
	}
	return w.Workspace.ReplaceFile(ctx, path, old, data)
}

// TestEditDeletedAfterReadIsModelVisible pins that a concurrent delete after the
// current read but before the conditional replace surfaces as a model-visible
// "deleted since you read it" refusal in Edit, distinct from a concurrent change
// (the file is GONE, not changed) and not a harness-level error.
func TestEditDeletedAfterReadIsModelVisible(t *testing.T) {
	base := memfs.NewWorkspace("/")
	seed(t, base, "a.txt", "hello\n")
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), base)
	ws := &deletedAfterReadWorkspace{Workspace: base, base: base}

	res := exec(t, EditTool{}, call(t, "Edit", map[string]any{
		"path": "a.txt", "old_string": "hello", "new_string": "hi",
	}), ws)
	if !res.IsError {
		t.Fatal("Edit with a concurrent delete must be a tool error")
	}
	if strings.Contains(res.Content, "changed since you read it") {
		t.Fatalf("Edit delete-after-read must not be the changed-since refusal: %q", res.Content)
	}
	if !strings.Contains(res.Content, "deleted since you read it") || !strings.Contains(res.Content, "a.txt") {
		t.Fatalf("Edit delete-after-read must be model-visible 'deleted since you read it': %q", res.Content)
	}
}

// TestWriteDeletedAfterReadIsModelVisible pins the same delete-after-read
// behavior for the existing-file Write path.
func TestWriteDeletedAfterReadIsModelVisible(t *testing.T) {
	base := memfs.NewWorkspace("/")
	seed(t, base, "a.txt", "old\n")
	exec(t, ReadTool{}, call(t, "Read", map[string]any{"path": "a.txt"}), base)
	ws := &deletedAfterReadWorkspace{Workspace: base, base: base}

	res := exec(t, WriteTool{}, call(t, "Write", map[string]any{
		"path": "a.txt", "content": "new\n",
	}), ws)
	if !res.IsError {
		t.Fatal("Write with a concurrent delete must be a tool error")
	}
	if strings.Contains(res.Content, "changed since you read it") {
		t.Fatalf("Write delete-after-read must not be the changed-since refusal: %q", res.Content)
	}
	if !strings.Contains(res.Content, "deleted since you read it") || !strings.Contains(res.Content, "a.txt") {
		t.Fatalf("Write delete-after-read must be model-visible 'deleted since you read it': %q", res.Content)
	}
}

func TestBashOutputAndExitMapping(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	runner := memfs.NewCommandRunner()
	bash := NewBashTool()

	runner.SetResult(&tool.CommandResult{Stdout: "hi there", Stderr: "", ExitCode: 0}, nil)
	res := execWithRunner(t, bash, call(t, "Bash", map[string]any{"command": "echo hi there"}), ws, runner)
	if res.IsError {
		t.Fatalf("Bash exit 0 should not be an error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "hi there") || !strings.Contains(res.Content, "[exit code: 0]") {
		t.Errorf("Bash content = %q", res.Content)
	}

	// Non-zero exit => error result, output preserved.
	runner.SetResult(&tool.CommandResult{Stdout: "", Stderr: "boom", ExitCode: 2}, nil)
	res = execWithRunner(t, bash, call(t, "Bash", map[string]any{"command": "false"}), ws, runner)
	if !res.IsError {
		t.Error("non-zero exit should be a tool error")
	}
	if !strings.Contains(res.Content, "boom") || !strings.Contains(res.Content, "[exit code: 2]") {
		t.Errorf("Bash error content = %q", res.Content)
	}
}

func TestBashNoShellSurfacesAsToolError(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	bash := NewBashTool()
	// A shell-less Environment (nil runner) surfaces the no-shell message. The
	// nil-runner path routes through bashErrorMessage (the SAME composer every
	// runner-error site uses), so the message is byte-identical to the
	// ErrNoShell trailer wording — pin the exact string so it cannot drift.
	const wantNoShell = "[command failed to run: no shell available]"
	res := exec(t, bash, call(t, "Bash", map[string]any{"command": "echo hi"}), ws)
	if !res.IsError {
		t.Error("no-shell should surface as a tool error, not a harness error")
	}
	if res.Content != wantNoShell {
		t.Errorf("Bash no-shell content = %q; want %q", res.Content, wantNoShell)
	}
	// tool.ErrNoShell must be classified as the no-shell case, not the generic
	// default — the message should name the missing shell.
	rr := &recordingRunner{returnError: tool.ErrNoShell}
	res = execWithRunner(t, NewBashTool(), call(t, "Bash", map[string]any{"command": "echo hi"}), ws, rr)
	if !res.IsError {
		t.Error("ErrNoShell should surface as a tool error")
	}
	if res.Content != wantNoShell {
		t.Errorf("Bash no-shell content = %q; want %q", res.Content, wantNoShell)
	}
}

func TestBashTimeoutSurfacesPartialOutput(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	rr := &recordingRunner{
		result:      tool.CommandResult{Stdout: "hi\n"},
		returnError: context.DeadlineExceeded,
	}
	res := execWithRunner(t, NewBashTool(), call(t, "Bash", map[string]any{"command": "echo hi; sleep 5", "timeout_ms": 50}), ws, rr)
	if !res.IsError {
		t.Fatal("a timed-out command should be a tool error")
	}
	if !strings.Contains(res.Content, "hi") {
		t.Errorf("partial output dropped: content = %q", res.Content)
	}
	if !strings.Contains(res.Content, "timed out") || !strings.Contains(res.Content, "50ms") {
		t.Errorf("expected a timeout trailer naming 50ms: content = %q", res.Content)
	}
	if strings.Contains(res.Content, "[exit code: 0]") {
		t.Errorf("ctx-error path must not print a placeholder exit code: content = %q", res.Content)
	}
}

func TestBashTimeoutNoOutput(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	rr := &recordingRunner{returnError: context.DeadlineExceeded}
	res := execWithRunner(t, NewBashTool(), call(t, "Bash", map[string]any{"command": "sleep 5", "timeout_ms": 50}), ws, rr)
	if !res.IsError {
		t.Fatal("a timed-out command should be a tool error")
	}
	if strings.TrimSpace(res.Content) == "" {
		t.Fatal("a no-output timeout should still report why it stopped")
	}
	if !strings.Contains(res.Content, "timed out") || !strings.Contains(res.Content, "50ms") {
		t.Errorf("expected a timeout reason naming 50ms: content = %q", res.Content)
	}
	if !strings.Contains(res.Content, "no output") {
		t.Errorf("expected a no-output phrase: content = %q", res.Content)
	}
}

func TestBashTimeoutNoTimeoutMsSet(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	rr := &recordingRunner{
		result:      tool.CommandResult{Stdout: "partial\n"},
		returnError: context.DeadlineExceeded,
	}
	// No timeout_ms: the runner's private default fired. We must not fabricate a
	// number we cannot see.
	res := execWithRunner(t, NewBashTool(), call(t, "Bash", map[string]any{"command": "sleep 99"}), ws, rr)
	if !res.IsError {
		t.Fatal("a timed-out command should be a tool error")
	}
	if !strings.Contains(res.Content, "partial") {
		t.Errorf("partial output dropped: content = %q", res.Content)
	}
	if !strings.Contains(res.Content, "timed out") {
		t.Errorf("expected a timeout trailer: content = %q", res.Content)
	}
	if strings.Contains(res.Content, "ms") {
		t.Errorf("must not fabricate a timeout number when timeout_ms is unset: content = %q", res.Content)
	}
}

func TestBashCancelSurfacesPartialOutput(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	rr := &recordingRunner{
		result:      tool.CommandResult{Stdout: "before cancel\n"},
		returnError: context.Canceled,
	}
	res := execWithRunner(t, NewBashTool(), call(t, "Bash", map[string]any{"command": "echo before cancel; sleep 5"}), ws, rr)
	if !res.IsError {
		t.Fatal("a canceled command should be a tool error")
	}
	if !strings.Contains(res.Content, "before cancel") {
		t.Errorf("partial output dropped: content = %q", res.Content)
	}
	if !strings.Contains(res.Content, "canceled") {
		t.Errorf("expected a cancellation trailer: content = %q", res.Content)
	}
}

// TestBashTimeoutTrailerSurvivesTruncation pins the worst case: a runaway/timed-out
// command produces output larger than the output cap, so a naive "truncate the
// joined string" would land the cut inside the body and drop the timeout signal.
// The trailer must survive.
func TestBashTimeoutTrailerSurvivesTruncation(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	huge := strings.Repeat("x", MaxOutputBytes+5000) + "\n"
	rr := &recordingRunner{
		result:      tool.CommandResult{Stdout: huge},
		returnError: context.DeadlineExceeded,
	}
	res := execWithRunner(t, NewBashTool(), call(t, "Bash", map[string]any{"command": "yes", "timeout_ms": 50}), ws, rr)
	if !res.IsError {
		t.Fatal("a timed-out command should be a tool error")
	}
	if !strings.Contains(res.Content, "timed out") {
		t.Errorf("timeout trailer was truncated away: content tail = %q", tail(res.Content, 200))
	}
	if !strings.Contains(res.Content, "50ms") {
		t.Errorf("timeout trailer lost the configured limit: content tail = %q", tail(res.Content, 200))
	}
	if len(res.Content) > MaxOutputBytes {
		t.Errorf("final output %d bytes exceeds the cap %d", len(res.Content), MaxOutputBytes)
	}
}

// TestBashTimeoutStderrSurvives covers the motivating case — a build failure whose
// partial output landed on STDERR, not stdout.
func TestBashTimeoutStderrSurvives(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	rr := &recordingRunner{
		result:      tool.CommandResult{Stderr: "compile error: undefined symbol\n"},
		returnError: context.DeadlineExceeded,
	}
	res := execWithRunner(t, NewBashTool(), call(t, "Bash", map[string]any{"command": "go build ./...", "timeout_ms": 50}), ws, rr)
	if !res.IsError {
		t.Fatal("a timed-out command should be a tool error")
	}
	if !strings.Contains(res.Content, "compile error") {
		t.Errorf("stderr partial output dropped: content = %q", res.Content)
	}
	if !strings.Contains(res.Content, "timed out") {
		t.Errorf("expected a timeout trailer: content = %q", res.Content)
	}
}

// TestBashTimeoutNoOutputNoTimeoutMs hits the no-output + unset-timeout_ms branch:
// no fabricated number, and a self-contained no-output phrasing.
func TestBashTimeoutNoOutputNoTimeoutMs(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	rr := &recordingRunner{returnError: context.DeadlineExceeded}
	res := execWithRunner(t, NewBashTool(), call(t, "Bash", map[string]any{"command": "sleep 99"}), ws, rr)
	if !res.IsError {
		t.Fatal("a timed-out command should be a tool error")
	}
	if !strings.Contains(res.Content, "timed out") || !strings.Contains(res.Content, "no output") {
		t.Errorf("expected a no-output timeout message: content = %q", res.Content)
	}
	if strings.Contains(res.Content, "ms") {
		t.Errorf("must not fabricate a timeout number when timeout_ms is unset: content = %q", res.Content)
	}
}

// TestBashGenericErrorKeepsPartialOutput covers the non-ctx default branch: a
// generic runner error must still preserve any captured partial output.
func TestBashGenericErrorKeepsPartialOutput(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	rr := &recordingRunner{
		result:      tool.CommandResult{Stdout: "partial\n"},
		returnError: errors.New("boom"),
	}
	res := execWithRunner(t, NewBashTool(), call(t, "Bash", map[string]any{"command": "echo partial"}), ws, rr)
	if !res.IsError {
		t.Fatal("a runner error should be a tool error")
	}
	if !strings.Contains(res.Content, "partial") {
		t.Errorf("partial output dropped on the default error path: content = %q", res.Content)
	}
	if !strings.Contains(res.Content, "command failed to run: boom") {
		t.Errorf("expected the generic failure reason: content = %q", res.Content)
	}
}

// tail returns the last n bytes of s (for failure messages on large outputs).
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// recordingRunner is a tool.CommandRunner fake that records the last Run call so
// a test can assert BashTool reads the bound runner off the Environment and
// passes the command through (issue #462: the runner is bound to a namespace
// root, so there is no per-call workdir).
type recordingRunner struct {
	gotCommand  string
	result      tool.CommandResult
	returnError error
}

func (r *recordingRunner) Run(_ context.Context, command string) (tool.CommandResult, error) {
	r.gotCommand = command
	return r.result, r.returnError
}

// TestBashUsesBoundRunner proves BashTool.Execute reads the CommandRunner off
// the tool.Environment (issue #462): the bound runner receives the command, and
// a shell-less Environment (nil runner) surfaces ErrNoShell honestly.
func TestBashUsesBoundRunner(t *testing.T) {
	ws := memfs.NewWorkspace("/fork/root")
	rr := &recordingRunner{result: tool.CommandResult{Stdout: "ok", ExitCode: 0}}
	bash := NewBashTool()

	res := execWithRunner(t, bash, call(t, "Bash", map[string]any{"command": "echo hi"}), ws, rr)
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content)
	}
	if rr.gotCommand != "echo hi" {
		t.Errorf("Bash passed command %q; want %q", rr.gotCommand, "echo hi")
	}
	if !strings.Contains(res.Content, "ok") {
		t.Errorf("Bash content = %q; want the runner's output", res.Content)
	}

	// A shell-less Environment (nil runner) surfaces ErrNoShell honestly.
	res = exec(t, bash, call(t, "Bash", map[string]any{"command": "echo hi"}), ws)
	if !res.IsError {
		t.Fatal("a shell-less Environment should surface a no-shell tool error")
	}
	if !strings.Contains(res.Content, "no shell available") {
		t.Errorf("Bash no-shell content = %q; want a 'no shell available' message", res.Content)
	}
}

func TestGrepCapAndFormat(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	var sb strings.Builder
	for i := 0; i < maxGrepMatches+25; i++ {
		fmt.Fprintf(&sb, "needle %d\n", i)
	}
	seed(t, ws, "hay.txt", sb.String())
	res := exec(t, GrepTool{}, call(t, "Grep", map[string]any{"pattern": "needle"}), ws)
	if res.IsError {
		t.Fatalf("Grep errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "output truncated") {
		t.Error("expected Grep truncation marker over cap")
	}
	if !strings.Contains(res.Content, "hay.txt:1:needle 0") {
		t.Errorf("expected path:line:text format, got start: %.40q", res.Content)
	}
	lines := strings.Count(res.Content, "needle ")
	if lines > maxGrepMatches {
		t.Errorf("returned %d matches, cap is %d", lines, maxGrepMatches)
	}
}

func TestGrepNoMatches(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "a.txt", "hello\n")
	res := exec(t, GrepTool{}, call(t, "Grep", map[string]any{"pattern": "zzz"}), ws)
	if res.IsError || !strings.Contains(res.Content, "no matches") {
		t.Errorf("expected 'no matches', got error=%v content=%q", res.IsError, res.Content)
	}
}

func TestGlobCap(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	for i := 0; i < maxGlobResults+10; i++ {
		seed(t, ws, fmt.Sprintf("f%05d.txt", i), "x")
	}
	res := exec(t, GlobTool{}, call(t, "Glob", map[string]any{"pattern": "*.txt"}), ws)
	if res.IsError {
		t.Fatalf("Glob errored: %s", res.Content)
	}
	if !strings.Contains(res.Content, "output truncated") {
		t.Error("expected Glob truncation marker over cap")
	}
}

func TestGlobNoMatch(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	res := exec(t, GlobTool{}, call(t, "Glob", map[string]any{"pattern": "*.go"}), ws)
	if res.IsError || !strings.Contains(res.Content, "no files match") {
		t.Errorf("expected 'no files match', got error=%v content=%q", res.IsError, res.Content)
	}
}

// TestGlobGlobstar drives the model-facing "**" capability end-to-end through
// GlobTool.Execute on a memfs workspace. It is the e2e gate proving an agent can
// recurse across directories.
func TestGlobGlobstar(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	seed(t, ws, "top.go", "package main\n")
	seed(t, ws, "a/mid.go", "package a\n")
	seed(t, ws, "a/b/deep.go", "package b\n")
	seed(t, ws, "a/b/notes.md", "ignore me\n")

	res := exec(t, GlobTool{}, call(t, "Glob", map[string]any{"pattern": "**/*.go"}), ws)
	if res.IsError {
		t.Fatalf("Glob errored: %s", res.Content)
	}
	for _, want := range []string{"top.go", "a/b/deep.go"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("Glob(**/*.go) result = %q, missing %q", res.Content, want)
		}
	}
	if strings.Contains(res.Content, "notes.md") {
		t.Errorf("Glob(**/*.go) result = %q, surfaced non-Go file", res.Content)
	}
}

func TestMissingRequiredArgs(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	cases := []struct {
		tl tool.Tool
		in session.ToolCall
	}{
		{ReadTool{}, call(t, "Read", map[string]any{})},
		{EditTool{}, call(t, "Edit", map[string]any{"path": "a"})},
		{WriteTool{}, call(t, "Write", map[string]any{"content": "x"})},
		{NewBashTool(), call(t, "Bash", map[string]any{})},
		{GrepTool{}, call(t, "Grep", map[string]any{})},
		{GlobTool{}, call(t, "Glob", map[string]any{})},
	}
	for _, c := range cases {
		res := exec(t, c.tl, c.in, ws)
		if !res.IsError {
			t.Errorf("%s with missing args should be a tool error", c.tl.Spec().Name)
		}
	}
}

// TestBashEnvHermetic confirms the temp dir machinery works without touching the
// network; this keeps the suite hermetic. A real-shell test belongs with the osfs
// adapter (internal/adapter/tools' abspath_tools_test.go over a real workspace).
func TestBashEnvHermetic(t *testing.T) {
	dir := t.TempDir()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	_ = filepath.Join(dir, "x")
}
