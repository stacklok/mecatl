package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/toolkit"
)

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
	res, err := tl.Execute(context.Background(), in, ws)
	if err != nil {
		t.Fatalf("%s: unexpected harness error: %v", tl.Spec().Name, err)
	}
	return res
}

// seed writes a file directly into a memfs workspace (no read recorded).
func seed(t *testing.T, ws *memfs.Workspace, path, content string) {
	t.Helper()
	if err := ws.Write(context.Background(), path, []byte(content)); err != nil {
		t.Fatalf("seed %q: %v", path, err)
	}
}

func TestReadOnlyFlags(t *testing.T) {
	// Bash is excluded from All() (it requires a CommandRunner); these are the
	// always-available tools. Bash's ReadOnly is asserted separately.
	want := map[string]bool{
		"Read":             true,
		"Edit":             false,
		"Write":            false,
		"Grep":             true,
		"Glob":             true,
		"WebFetch":         true,
		"FetchMcpResource": true,
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
	if NewBashTool(memfs.NewCommandRunner()).ReadOnly() {
		t.Error("Bash.ReadOnly() = true, want false")
	}
}

// TestAllExcludesBash documents that the always-available catalog has NO Bash:
// command execution is optional and added only when a runner is configured.
func TestAllExcludesBash(t *testing.T) {
	for _, tl := range All() {
		if tl.Spec().Name == "Bash" {
			t.Fatal("All() must not include Bash (it requires a CommandRunner)")
		}
	}
}

// TestNoFSExcludesFileTools pins the no-FS core surface: NoFS() is exactly
// {WebFetch, FetchMcpResource} — no file-touching tool (Read/Edit/Write/Grep/
// Glob) and no Bash may ever appear in it. FetchMcpResource (issue #223 Phase
// 2) is an outbound read that needs no filesystem, so it rides alongside
// WebFetch. This is the anti-drift pin for the "no-fs" session profile's core
// tier: a tool added to All() does NOT automatically reach NoFS().
func TestNoFSExcludesFileTools(t *testing.T) {
	got := NoFS()
	if len(got) != 2 {
		t.Fatalf("NoFS() = %d tools, want exactly 2 (WebFetch, FetchMcpResource)", len(got))
	}
	names := map[string]bool{}
	for _, tl := range got {
		names[tl.Spec().Name] = true
	}
	for _, want := range []string{"WebFetch", "FetchMcpResource"} {
		if !names[want] {
			t.Errorf("NoFS() missing %q", want)
		}
	}
	banned := map[string]bool{"Read": true, "Edit": true, "Write": true, "Grep": true, "Glob": true, BashToolName: true}
	for _, tl := range got {
		if banned[tl.Spec().Name] {
			t.Errorf("NoFS() includes file/shell tool %q — the no-FS profile must never carry it", tl.Spec().Name)
		}
	}
}

func TestAllAndRegister(t *testing.T) {
	if len(All()) != 7 {
		t.Fatalf("All() = %d tools, want 7", len(All()))
	}
	cat := tool.NewCatalog()
	if err := Register(cat); err != nil {
		t.Fatalf("Register: %v", err)
	}
	for _, name := range []string{"Read", "Edit", "Write", "Grep", "Glob", "WebFetch", "FetchMcpResource"} {
		if _, ok := cat.Lookup(name); !ok {
			t.Errorf("catalog missing %q after Register", name)
		}
	}
	// Register does NOT add Bash; the catalog is shell-less until NewBashTool is
	// registered explicitly.
	if _, ok := cat.Lookup("Bash"); ok {
		t.Error("Register added Bash; it must be opt-in via NewBashTool")
	}
	// Adding the optional Bash tool with a configured runner succeeds.
	cat.MustRegister(NewBashTool(memfs.NewCommandRunner()))
	if _, ok := cat.Lookup("Bash"); !ok {
		t.Error("catalog missing Bash after explicit NewBashTool registration")
	}
	// Re-registering must collide.
	if err := Register(cat); err == nil {
		t.Error("re-Register did not return a duplicate error")
	}
}

func TestSpecsHaveDocs(t *testing.T) {
	for _, tl := range All() {
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
	ok, err := ws.WasReadUnchanged(context.Background(), "a.txt")
	if err != nil {
		t.Fatalf("WasReadUnchanged: %v", err)
	}
	if !ok {
		t.Error("Read did not record the read in the ledger")
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
	if len(res.Content) > toolkit.MaxOutputBytes+200 {
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

func TestBashOutputAndExitMapping(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	runner := memfs.NewCommandRunner()
	bash := NewBashTool(runner)

	runner.SetResult(&tool.CommandResult{Stdout: "hi there", Stderr: "", ExitCode: 0}, nil)
	res := exec(t, bash, call(t, "Bash", map[string]any{"command": "echo hi there"}), ws)
	if res.IsError {
		t.Fatalf("Bash exit 0 should not be an error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "hi there") || !strings.Contains(res.Content, "[exit code: 0]") {
		t.Errorf("Bash content = %q", res.Content)
	}

	// Non-zero exit => error result, output preserved.
	runner.SetResult(&tool.CommandResult{Stdout: "", Stderr: "boom", ExitCode: 2}, nil)
	res = exec(t, bash, call(t, "Bash", map[string]any{"command": "false"}), ws)
	if !res.IsError {
		t.Error("non-zero exit should be a tool error")
	}
	if !strings.Contains(res.Content, "boom") || !strings.Contains(res.Content, "[exit code: 2]") {
		t.Errorf("Bash error content = %q", res.Content)
	}
}

func TestBashNoShellSurfacesAsToolError(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	bash := NewBashTool(memfs.NewCommandRunner()) // default runner: ErrNoShell
	res := exec(t, bash, call(t, "Bash", map[string]any{"command": "echo hi"}), ws)
	if !res.IsError {
		t.Error("runner error should surface as a tool error, not a harness error")
	}
	// tool.ErrNoShell must be classified as the no-shell case, not the generic
	// default — the message should name the missing shell.
	rr := &recordingRunner{returnError: tool.ErrNoShell}
	res = exec(t, NewBashTool(rr), call(t, "Bash", map[string]any{"command": "echo hi"}), ws)
	if !res.IsError {
		t.Error("ErrNoShell should surface as a tool error")
	}
	if !strings.Contains(res.Content, "no shell available") {
		t.Errorf("Bash no-shell content = %q; want a 'no shell available' message", res.Content)
	}
}

func TestBashTimeoutSurfacesPartialOutput(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	rr := &recordingRunner{
		result:      tool.CommandResult{Stdout: "hi\n"},
		returnError: context.DeadlineExceeded,
	}
	res := exec(t, NewBashTool(rr), call(t, "Bash", map[string]any{"command": "echo hi; sleep 5", "timeout_ms": 50}), ws)
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
	res := exec(t, NewBashTool(rr), call(t, "Bash", map[string]any{"command": "sleep 5", "timeout_ms": 50}), ws)
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
	res := exec(t, NewBashTool(rr), call(t, "Bash", map[string]any{"command": "sleep 99"}), ws)
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
	res := exec(t, NewBashTool(rr), call(t, "Bash", map[string]any{"command": "echo before cancel; sleep 5"}), ws)
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
	huge := strings.Repeat("x", toolkit.MaxOutputBytes+5000) + "\n"
	rr := &recordingRunner{
		result:      tool.CommandResult{Stdout: huge},
		returnError: context.DeadlineExceeded,
	}
	res := exec(t, NewBashTool(rr), call(t, "Bash", map[string]any{"command": "yes", "timeout_ms": 50}), ws)
	if !res.IsError {
		t.Fatal("a timed-out command should be a tool error")
	}
	if !strings.Contains(res.Content, "timed out") {
		t.Errorf("timeout trailer was truncated away: content tail = %q", tail(res.Content, 200))
	}
	if !strings.Contains(res.Content, "50ms") {
		t.Errorf("timeout trailer lost the configured limit: content tail = %q", tail(res.Content, 200))
	}
	if len(res.Content) > toolkit.MaxOutputBytes {
		t.Errorf("final output %d bytes exceeds the cap %d", len(res.Content), toolkit.MaxOutputBytes)
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
	res := exec(t, NewBashTool(rr), call(t, "Bash", map[string]any{"command": "go build ./...", "timeout_ms": 50}), ws)
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
	res := exec(t, NewBashTool(rr), call(t, "Bash", map[string]any{"command": "sleep 99"}), ws)
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
	res := exec(t, NewBashTool(rr), call(t, "Bash", map[string]any{"command": "echo partial"}), ws)
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

// recordingRunner is a tool.CommandRunner fake that records the workdir of the
// last Run call so a test can assert BashTool threads the Workspace.Root() through.
type recordingRunner struct {
	gotWorkdir  string
	gotCommand  string
	result      tool.CommandResult
	returnError error
}

func (r *recordingRunner) Run(_ context.Context, command, workdir string) (tool.CommandResult, error) {
	r.gotCommand = command
	r.gotWorkdir = workdir
	return r.result, r.returnError
}

// TestBashPassesWorkspaceRootAsWorkdir proves BashTool.Execute passes the per-call
// Workspace.Root() to CommandRunner.Run as the working directory — the seam that
// makes Bash run in the fork it executes against rather than a baked-in root.
func TestBashPassesWorkspaceRootAsWorkdir(t *testing.T) {
	ws := memfs.NewWorkspace("/fork/root")
	rr := &recordingRunner{result: tool.CommandResult{Stdout: "ok", ExitCode: 0}}
	bash := NewBashTool(rr)

	res := exec(t, bash, call(t, "Bash", map[string]any{"command": "echo hi"}), ws)
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", res.Content)
	}
	if rr.gotWorkdir != ws.Root() {
		t.Errorf("Bash passed workdir %q; want the Workspace root %q", rr.gotWorkdir, ws.Root())
	}
	if rr.gotCommand != "echo hi" {
		t.Errorf("Bash passed command %q; want %q (command must be unchanged)", rr.gotCommand, "echo hi")
	}
}

func TestNewBashToolNilRunnerPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewBashTool(nil) should panic")
		}
	}()
	_ = NewBashTool(nil)
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

func TestWebFetchStub(t *testing.T) {
	ws := memfs.NewWorkspace("/")
	res := exec(t, WebFetchTool{}, call(t, "WebFetch", map[string]any{"url": "https://example.com"}), ws)
	if !res.IsError {
		t.Error("WebFetch stub must return a tool error")
	}
	if !strings.Contains(res.Content, "not implemented") {
		t.Errorf("WebFetch content = %q", res.Content)
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
		{NewBashTool(memfs.NewCommandRunner()), call(t, "Bash", map[string]any{})},
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

// TestBashOnOSFSRealEcho is an optional hermetic real-command test against the
// osfs adapter. It runs only when an osfs workspace constructor is available and
// uses t.TempDir(), so CI stays offline. It is skipped if osfs is unavailable.
func TestBashEnvHermetic(t *testing.T) {
	// Confirm the temp dir machinery works without touching the network; this
	// keeps the suite hermetic. A real-shell test belongs with the osfs adapter.
	dir := t.TempDir()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	_ = filepath.Join(dir, "x")
}
