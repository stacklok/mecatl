package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/permstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
)

// pathescape_scenario3_test.go pins the path-escape-posture Scenario 3
// acceptance criteria (docs/acceptance/path-escape-posture.md): at yolo an
// out-of-root Write/Edit succeeds; at auto (no guardrail knob) a write escape
// surfaces an EvPermissionAsk and executes only on an allow verdict; the Edit
// read-ledger keys out-of-root paths canonically; write escapes stay
// mutate-serial; the write flows through an *os.Root on the target's parent
// (a symlinked escaping parent component is refused); and a configured Deny
// (or configured Ask) on the tool still wins over the posture-relaxed escape
// Allow (deny-dominance). All offline (mockllm + a real osfs workspace under
// t.TempDir).

// writeFixture is the on-disk layout the Scenario-3 e2e tests share: a
// workspace root plus an out-of-root target path (not yet created for Write;
// pre-created for Edit).
type writeFixture struct {
	workspace string
	outside   string
	target    string // out-of-root absolute path the model writes/edits
}

// setupWriteFS builds workspace + outside dirs. The target itself is left to
// the caller (Write creates it; Edit pre-creates it).
func setupWriteFS(t *testing.T) writeFixture {
	t.Helper()
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{workspace, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("MkdirAll(%q): %v", d, err)
		}
	}
	return writeFixture{
		workspace: workspace,
		outside:   outside,
		target:    filepath.Join(outside, "written.txt"),
	}
}

// writeEscapeCfg mirrors escapeCfg but scripts Write turns (posture set by
// the caller).
func writeEscapeCfg(t *testing.T, f writeFixture, posture Posture, turns ...mockllm.Turn) Config {
	t.Helper()
	return Config{
		Workspace:           f.workspace,
		NoSoul:              true,
		Posture:             posture,
		PostureFlagSet:      true,
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient: offlineHTTPClient(),
		providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider {
			return mockllm.New(turns...)
		},
	}
}

// scenario3Call marshals one tool call with the given arg map.
func scenario3Call(id, name string, argMap map[string]string) session.ToolCall {
	args, _ := json.Marshal(argMap)
	return session.NewToolCall(session.ToolCallID(id), name, json.RawMessage(args))
}

// TestPathEscapePosture_Scenario3_YoloWriteEscapeAllowed pins AC3.1: at
// posture yolo, Write to an out-of-root absolute path creates the file (and a
// second Write replaces it) — the relax flows through the ordinary FS tool,
// never a Shell workaround, and never surfaces an ask at yolo.
func TestPathEscapePosture_Scenario3_YoloWriteEscapeAllowed(t *testing.T) {
	t.Parallel()
	f := setupWriteFS(t)
	create := scenario3Call("w1", "Write", map[string]string{"path": f.target, "content": "yolo-escape-content-9b2c"})
	replace := scenario3Call("w2", "Write", map[string]string{"path": f.target, "content": "yolo-escape-content-REPLACED"})
	built, err := buildIsolated(t, context.Background(), writeEscapeCfg(t, f, PostureYolo,
		mockllm.ToolCallTurn(create),
		mockllm.ToolCallTurn(replace),
		mockllm.TextTurn("done"),
	))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var results []*session.ToolResult
	var askSeen bool
	run, err := built.Service.StartRun(context.Background(), sess.ID, "write outside the workspace")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for ev := range run.Events() {
		if ev.Type == session.EvPermissionAsk {
			askSeen = true
		}
		if ev.Type == session.EvToolResult && ev.ToolResult != nil {
			results = append(results, ev.ToolResult)
		}
	}
	built.Service.FinishRun(sess.ID, run)
	if askSeen {
		t.Fatal("yolo Write escape surfaced an EvPermissionAsk — yolo must ALLOW a write escape, not ask")
	}
	if len(results) != 2 {
		t.Fatalf("got %d EvToolResult events, want 2 (create + replace)", len(results))
	}
	for _, res := range results {
		if res.IsError {
			t.Fatalf("yolo Write escape errored: %q (want the file written)", res.Content)
		}
	}
	data, err := os.ReadFile(f.target)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v — the yolo write escape must create the file", f.target, err)
	}
	if string(data) != "yolo-escape-content-REPLACED" {
		t.Fatalf("file content = %q, want the REPLACED content (the second Write must replace)", data)
	}
}

// TestPathEscapePosture_Scenario3_AutoWriteEscapeAsks pins AC3.2: at posture
// auto (no guardrail knob), a Write escape surfaces an EvPermissionAsk and
// executes ONLY on an allow verdict — a deny verdict leaves nothing written
// (never a silent un-asked mutation below yolo).
func TestPathEscapePosture_Scenario3_AutoWriteEscapeAsks(t *testing.T) {
	t.Parallel()

	t.Run("deny leaves nothing written", func(t *testing.T) {
		t.Parallel()
		f := setupWriteFS(t)
		call := scenario3Call("w1", "Write", map[string]string{"path": f.target, "content": "must-never-land"})
		built, err := buildIsolated(t, context.Background(), writeEscapeCfg(t, f, PostureAuto,
			mockllm.ToolCallTurn(call),
			mockllm.TextTurn("done"),
		))
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		run, err := built.Service.StartRun(context.Background(), sess.ID, "write outside the workspace")
		if err != nil {
			t.Fatalf("StartRun: %v", err)
		}
		var askSeen, denied bool
		for ev := range run.Events() {
			if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
				askSeen = true
				run.Approve(ev.Ask.AskID, session.VerdictDeny)
			}
			if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.IsError {
				denied = true
			}
		}
		built.Service.FinishRun(sess.ID, run)
		if !askSeen {
			t.Fatal("auto Write escape never surfaced an EvPermissionAsk — a write escape must ASK below yolo")
		}
		if !denied {
			t.Fatal("no error tool result after the deny verdict — the denied write must surface a deny result")
		}
		if _, err := os.Stat(f.target); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Stat(%q) = %v — the denied write escape must leave NOTHING written", f.target, err)
		}
	})

	t.Run("allow executes", func(t *testing.T) {
		t.Parallel()
		f := setupWriteFS(t)
		call := scenario3Call("w1", "Write", map[string]string{"path": f.target, "content": "auto-escape-content-allowed"})
		built, err := buildIsolated(t, context.Background(), writeEscapeCfg(t, f, PostureAuto,
			mockllm.ToolCallTurn(call),
			mockllm.TextTurn("done"),
		))
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		defer built.Close()
		sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		run, err := built.Service.StartRun(context.Background(), sess.ID, "write outside the workspace")
		if err != nil {
			t.Fatalf("StartRun: %v", err)
		}
		var askSeen bool
		var result *session.ToolResult
		for ev := range run.Events() {
			if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
				askSeen = true
				run.Approve(ev.Ask.AskID, session.VerdictAllowOnce)
			}
			if ev.Type == session.EvToolResult && ev.ToolResult != nil {
				result = ev.ToolResult
			}
		}
		built.Service.FinishRun(sess.ID, run)
		if !askSeen {
			t.Fatal("auto Write escape never surfaced an EvPermissionAsk")
		}
		if result == nil || result.IsError {
			t.Fatalf("tool result after allow = %+v, want a non-error write result", result)
		}
		data, err := os.ReadFile(f.target)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v — the allowed write escape must create the file", f.target, err)
		}
		if string(data) != "auto-escape-content-allowed" {
			t.Fatalf("file content = %q, want the allowed content", data)
		}
	})
}

// readGateWorkspace wraps the relaxed escape workspace and PARKS a ReplaceFile
// of the gate path inside the workspace until release is closed. The
// EditLedgerOutOfRoot test uses it to plant the behind-the-back file change
// at a deterministic point: the Edit body re-reads the file (ReadVersion) and
// records the pre-change version, then calls ReplaceFile as the conditional
// CAS against that version. The gate fires on e3's ReplaceFile (the
// changed-since edit) so the test can plant a behind-the-back change BEFORE the
// CAS compares, proving the final ReplaceFile is load-bearing and
// model-visible — no reliance on event-loop delivery timing (which can lag
// behind dispatch under parallel load).
type readGateWorkspace struct {
	tool.Workspace
	gatePath string
	entered  chan struct{} // closed when the SECOND gated ReplaceFile parks (e3's)
	release  chan struct{} // closed by the test to let the parked replace through
	checks   atomic.Int32
}

func (w *readGateWorkspace) AuthorityResourcePath(path string) (target, workspace string, err error) {
	return wrappedAuthorityResourcePath(w.Workspace, path)
}

func wrappedAuthorityResourcePath(ws tool.Workspace, path string) (target, workspace string, err error) {
	resolver, ok := ws.(tool.AuthorityResourceResolver)
	if !ok {
		return "", "", errors.New("wrapped workspace cannot derive an authority resource identity")
	}
	return resolver.AuthorityResourcePath(path)
}

// ReplaceFile is the Edit tool's conditional CAS — the call that fires AFTER the
// Edit body re-read the file (ReadVersion) and recorded the pre-change version.
// The FIRST gated replace is e1's (the cross-form edit — it must flow
// unimpeded); the SECOND is e3's (the changed-since edit — the one the test
// plants the behind-the-back change behind). Parking e3's replace while the
// test plants the change makes the ordering deterministic: the replace resumes
// and compares its recorded version (e3 just read it) against the now-CHANGED
// on-disk content, so the CAS provably mismatches and the edit is rejected.
func (w *readGateWorkspace) ReplaceFile(ctx context.Context, path string, old tool.FileVersion, data []byte) (tool.FileVersion, error) {
	if filepath.Clean(path) == filepath.Clean(w.gatePath) {
		if w.checks.Add(1) == 2 {
			close(w.entered)
			select {
			case <-w.release:
			case <-ctx.Done():
				return tool.FileVersion{}, ctx.Err()
			}
		}
	}
	return w.Workspace.ReplaceFile(ctx, path, old, data)
}

// TestPathEscapePosture_Scenario3_EditLedgerOutOfRoot pins AC3.3: an
// out-of-root Edit enforces read-before-edit-and-unchanged identically to an
// in-root edit — editing a path not first read (or changed since) is
// rejected — and the ledger keys the path by its CANONICAL form, so a Read
// through a path with `..` components that normalizes to the same canonical
// absolute path satisfies the edit's ledger check (and the reverse).
//
// DETERMINISM: the changed-since-read half plants the behind-the-back change
// while e3's ReplaceFile CAS is PARKED inside the workspace
// (readGateWorkspace): r2's re-read already re-recorded the pre-change
// version, and e3's own ReadVersion re-read it too, so the gate fires with
// the recorded version in that state; the test plants the change, releases
// the gate, and the resumed CAS compares its recorded version against the
// CHANGED on-disk content and mismatches. (The earlier shape planted the
// change on the r2 EVENT, but event delivery can lag behind dispatch under
// parallel load — the edit then checked the ledger before the plant landed,
// a test-ordering flake, not a product race.)
func TestPathEscapePosture_Scenario3_EditLedgerOutOfRoot(t *testing.T) {
	t.Parallel()
	f := setupWriteFS(t)
	if err := os.WriteFile(f.target, []byte("edit-ledger-original"), 0o644); err != nil {
		t.Fatalf("WriteFile(target): %v", err)
	}
	// The canonical form and a `..`-carrying alias that normalizes to it.
	canonical := f.target
	dotdot := filepath.Join(f.outside, "sub", "..", filepath.Base(f.target))

	read := scenario3Call("r1", "Read", map[string]string{"path": canonical})
	editViaAlias := scenario3Call("e1", "Edit", map[string]string{
		"path": dotdot, "old_string": "original", "new_string": "edited-via-alias",
	})
	editUnread := scenario3Call("e2", "Edit", map[string]string{
		"path": filepath.Join(f.outside, "unread.txt"), "old_string": "x", "new_string": "y",
	})
	readChanged := scenario3Call("r2", "Read", map[string]string{"path": canonical})
	editChanged := scenario3Call("e3", "Edit", map[string]string{
		"path": canonical, "old_string": "edited-via-alias", "new_string": "edited-after-change",
	})

	built, err := buildIsolated(t, context.Background(), writeEscapeCfg(t, f, PostureYolo,
		// 1: Read canonical → Edit via the `..` alias — the cross-form ledger
		//    key must match, so this edit SUCCEEDS.
		mockllm.ToolCallTurn(read, editViaAlias),
		// 2: Edit a never-read out-of-root path — read-before-edit rejects it.
		mockllm.ToolCallTurn(editUnread),
		// 3: Re-read the target (records the pre-change version); the next turn's
		//    Edit re-reads it again (ReadVersion), then its ReplaceFile CAS PARKS
		//    at the gate. The test plants the behind-the-back change, releases
		//    the gate, and the resumed CAS mismatches the changed file.
		mockllm.ToolCallTurn(readChanged),
		mockllm.ToolCallTurn(editChanged),
		mockllm.TextTurn("done"),
	))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Install the replace-gate workspace for the whole run: r1's read flows
	// through BEFORE the e1 edit (ungated — the gate fires once, on e3's
	// replace), so the cross-form half runs unimpeded; e3's ReplaceFile parks
	// until the test plants the behind-the-back change, making the
	// changed-since-read ordering deterministic.
	clf, err := newEscapeClassifier(f.workspace)
	if err != nil {
		t.Fatalf("newEscapeClassifier: %v", err)
	}
	base, err := osfs.NewWorkspace(f.workspace, osfs.WithRelaxedReads(), osfs.WithRelaxedWrites())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	gate := &readGateWorkspace{
		Workspace: newEscapeWorkspace(base, clf),
		gatePath:  canonical,
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	env, err := tool.NewEnvironment(sess.EnvironmentRef, gate, memledger.New(), nil)
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	built.Service.SetSessionEnvironment(sess.ID, env)

	run, err := built.Service.StartRun(context.Background(), sess.ID, "edit outside the workspace")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	results := map[session.ToolCallID]*session.ToolResult{}
	// Drive the event loop in the BACKGROUND so the test goroutine can plant
	// the change at the gate. The r1/e1 results may be consumed by EITHER
	// goroutine; only e3's result matters for the collect (the r1/e1/e2
	// assertions below re-derive from the run's own recorded conversation).
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for ev := range run.Events() {
			if ev.Type == session.EvToolResult && ev.ToolResult != nil {
				results[ev.ToolResult.CallID] = ev.ToolResult
			}
		}
	}()
	// Wait for e3's ReplaceFile CAS to park inside the gate (e3 already
	// re-read the file and recorded the pre-change version), plant the
	// behind-the-back change, then release. r1/e1/e2/r2 all completed before
	// e3 starts (turn order), so this single rendezvous bounds the whole
	// run's progress.
	deadline := time.After(30 * time.Second)
	select {
	case <-gate.entered:
	case <-deadline:
		t.Fatal("the changed-since edit (e3) never reached the replace gate")
	}
	if err := os.WriteFile(f.target, []byte("changed-behind-the-back"), 0o644); err != nil {
		t.Fatalf("planting the changed file: %v", err)
	}
	close(gate.release)

	<-eventsDone
	built.Service.FinishRun(sess.ID, run)

	// Snapshot the disk state the cross-form edit left: the conversation's
	// e1 edit already ran (its result is in `results`), and the only later
	// write to the file is the plant — so derive the e1 state from the
	// conversation instead: the e1 result's success plus the e3 rejection
	// below jointly pin the sequence. The on-disk state at this point is the
	// PLANT (e3 was rejected), which itself proves e3 did not write.
	data, err := os.ReadFile(f.target)
	if err != nil {
		t.Fatalf("ReadFile(target): %v", err)
	}
	if string(data) != "changed-behind-the-back" {
		t.Fatalf("final disk content = %q, want the planted change (a rejected e3 must not have written)", data)
	}

	// Cross-form edit: read canonical, edit via the `..` alias → succeeds.
	res := results["e1"]
	if res == nil {
		t.Fatal("no tool result for the cross-form edit (e1)")
	}
	if res.IsError {
		t.Fatalf("cross-form edit errored: %q — a Read of the canonical path must satisfy an Edit keyed through a `..`-normalizing alias", res.Content)
	}
	// The cross-form edit's content provably landed on disk: e3's old_string
	// ("edited-via-alias") is what e1 wrote — e3 being REJECTED with the
	// changed-since refusal (below) means its old_string was FOUND (a miss
	// would fail with the exact-match error instead), so e1's content was on
	// disk when e3 ran.

	// Unread edit: rejected with the read-before-edit refusal.
	res = results["e2"]
	if res == nil {
		t.Fatal("no tool result for the unread edit (e2)")
	}
	if !res.IsError {
		t.Fatalf("unread out-of-root edit SUCCEEDED: %q — read-before-edit must reject it", res.Content)
	}
	if !strings.Contains(res.Content, "not read this session") {
		t.Fatalf("unread edit error = %q, want the read-before-edit refusal", res.Content)
	}

	// Changed-since-read edit: rejected with the changed-since refusal.
	res = results["e3"]
	if res == nil {
		t.Fatal("no tool result for the changed-since-read edit (e3)")
	}
	if !res.IsError {
		t.Fatalf("changed-since-read out-of-root edit SUCCEEDED: %q — unchanged-since-read must reject it", res.Content)
	}
	if !strings.Contains(res.Content, "changed since") {
		t.Fatalf("changed-since-read edit error = %q, want the changed-since-read refusal", res.Content)
	}
}

// serialProbeWorkspace wraps the REAL relaxed escape workspace (the same
// relaxed wrapper the composition factory builds) and records how many
// CreateFile executions are in flight concurrently. A pause is injected on
// entry so an overlapping pair cannot hide behind instantaneous creates.
type serialProbeWorkspace struct {
	tool.Workspace
	inflight *atomic.Int32
	maxSeen  *atomic.Int32
	pause    time.Duration
}

func (w *serialProbeWorkspace) AuthorityResourcePath(path string) (target, workspace string, err error) {
	return wrappedAuthorityResourcePath(w.Workspace, path)
}

func newSerialProbeWorkspace(t *testing.T, root string, inflight, maxSeen *atomic.Int32, pause time.Duration) *serialProbeWorkspace {
	t.Helper()
	clf, err := newEscapeClassifier(root)
	if err != nil {
		t.Fatalf("newEscapeClassifier(%q): %v", root, err)
	}
	ws, err := osfs.NewWorkspace(root, osfs.WithRelaxedReads(), osfs.WithRelaxedWrites())
	if err != nil {
		t.Fatalf("NewWorkspace(%q): %v", root, err)
	}
	return &serialProbeWorkspace{
		Workspace: newEscapeWorkspace(ws, clf),
		inflight:  inflight,
		maxSeen:   maxSeen,
		pause:     pause,
	}
}

// CreateFile records the concurrency window around the delegated safe create.
func (w *serialProbeWorkspace) CreateFile(ctx context.Context, path string, data []byte) (tool.FileVersion, error) {
	n := w.inflight.Add(1)
	for {
		old := w.maxSeen.Load()
		if n <= old || w.maxSeen.CompareAndSwap(old, n) {
			break
		}
	}
	time.Sleep(w.pause)
	defer w.inflight.Add(-1)
	return w.Workspace.CreateFile(ctx, path, data)
}

// TestPathEscapePosture_Scenario3_WriteEscapeMutateSerial pins AC3.4: two
// Write escapes issued in the SAME turn never run in parallel — the
// read-parallel / mutate-serial dispatch invariant holds on the escape path
// (mutating tools execute one at a time, strictly sequentially).
func TestPathEscapePosture_Scenario3_WriteEscapeMutateSerial(t *testing.T) {
	t.Parallel()
	f := setupWriteFS(t)
	w1 := scenario3Call("w1", "Write", map[string]string{"path": filepath.Join(f.outside, "a.txt"), "content": "A"})
	w2 := scenario3Call("w2", "Write", map[string]string{"path": filepath.Join(f.outside, "b.txt"), "content": "B"})
	built, err := buildIsolated(t, context.Background(), writeEscapeCfg(t, f, PostureYolo,
		mockllm.ToolCallTurn(w1, w2),
		mockllm.TextTurn("done"),
	))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	// Swap the run's workspace for the probe BEFORE the run starts (a run
	// reads its workspace at start). The 50ms entry pause makes a genuine
	// overlap unmissable.
	var inflight, maxSeen atomic.Int32
	env, err := tool.NewEnvironment(sess.EnvironmentRef,
		newSerialProbeWorkspace(t, f.workspace, &inflight, &maxSeen, 50*time.Millisecond), memledger.New(), nil)
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	built.Service.SetSessionEnvironment(sess.ID, env)
	run, err := built.Service.StartRun(context.Background(), sess.ID, "two writes outside the workspace")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for range run.Events() {
	}
	built.Service.FinishRun(sess.ID, run)

	// Positive control: both writes DID run — the serial assertion below is
	// not vacuous (a run that never executed either write would also show
	// zero overlap).
	for _, name := range []string{"a.txt", "b.txt"} {
		data, err := os.ReadFile(filepath.Join(f.outside, name))
		if err != nil {
			t.Fatalf("ReadFile(%q): %v — both write escapes must execute (serially)", name, err)
		}
		if len(data) == 0 {
			t.Fatalf("%q is empty — the write escape must land", name)
		}
	}
	if got := maxSeen.Load(); got > 1 {
		t.Fatalf("two write escapes ran in PARALLEL (inflight reached %d) — mutate-serial violated", got)
	}
}

// TestPathEscapePosture_Scenario3_WriteEscapeServedThroughOsRoot pins AC3.5:
// an out-of-root Write flows through an *os.Root on the target's parent —
// never a direct os.WriteFile — so a SYMLINKED TARGET COMPONENT that escapes
// further is refused exactly as an in-root write through the workspace root
// is refused. The discriminating fixture: an existing LEAF SYMLINK inside the
// serving parent whose target escapes to a third dir — os.Root REFUSES to
// open it (O_NOFOLLOW semantics), while a bare os.WriteFile would silently
// follow it and replace the third dir's file.
func TestPathEscapePosture_Scenario3_WriteEscapeServedThroughOsRoot(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixtures require a POSIX filesystem")
	}
	f := setupWriteFS(t)
	// A THIRD out-of-root file; a leaf symlink INSIDE the (vetted) write
	// parent pointing at it. Writing outside/link through a fresh os.Root on
	// outside/ must REFUSE (link escapes the serving root); a bare
	// os.WriteFile would follow it and REPLACE third/planted.txt.
	third := filepath.Join(filepath.Dir(f.outside), "third")
	if err := os.MkdirAll(third, 0o755); err != nil {
		t.Fatalf("MkdirAll(third): %v", err)
	}
	thirdTarget := filepath.Join(third, "planted.txt")
	if err := os.WriteFile(thirdTarget, []byte("third-original-content"), 0o644); err != nil {
		t.Fatalf("WriteFile(third): %v", err)
	}
	linkTarget := filepath.Join(f.outside, "link")
	if err := os.Symlink(thirdTarget, linkTarget); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	call := scenario3Call("w1", "Write", map[string]string{"path": linkTarget, "content": "must-never-land-through-symlink"})
	built, err := buildIsolated(t, context.Background(), writeEscapeCfg(t, f, PostureYolo,
		mockllm.ToolCallTurn(call),
		mockllm.TextTurn("done"),
	))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(context.Background(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	result, _ := runOneTurn(t, built, sess.ID)
	if result == nil {
		t.Fatal("no EvToolResult emitted for the Write call")
	}
	if !result.IsError {
		t.Fatalf("yolo Write through an escaping symlinked parent SUCCEEDED: %q — the serving *os.Root must refuse it", result.Content)
	}
	data, err := os.ReadFile(thirdTarget)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", thirdTarget, err)
	}
	if string(data) != "third-original-content" {
		t.Fatalf("third dir content = %q — a direct os.WriteFile would have FOLLOWED the leaf symlink and replaced it; the serving *os.Root must refuse", data)
	}
}

// TestPathEscapePosture_Scenario3_ConfiguredDenyWinsOverEscapeAllow pins
// AC3.6: deny-dominance holds against the posture relax — a CONFIGURED Deny
// on Write wins over the yolo escape Allow (the wrapper never relaxes an
// inner deny), and a CONFIGURED Ask is never suppressed into an escape Allow
// (the wrapper leaves an inner ask standing).
func TestPathEscapePosture_Scenario3_ConfiguredDenyWinsOverEscapeAllow(t *testing.T) {
	t.Parallel()
	f := setupWriteFS(t)
	ws, err := osfs.NewWorkspace(f.workspace)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	call := scenario3Call("w1", "Write", map[string]string{"path": f.target, "content": "x"})

	t.Run("configured deny wins over the yolo escape allow", func(t *testing.T) {
		t.Parallel()
		inner := permpolicy.NewPolicy([]governance.Rule{
			{Scope: governance.ScopeUser, Tool: "Write", Effect: governance.Deny},
		}, nil)
		p := newEscapePolicy(inner, PostureYolo)
		d := p.Evaluate(context.Background(), session.SessionID("s1"), session.ModeDefault, call, ws).Decision
		if d.Effect != governance.Deny {
			t.Fatalf("effect = %v, want Deny — a configured Deny must win over the posture-relaxed escape Allow", d.Effect)
		}
	})

	t.Run("configured ask is never suppressed by the relax", func(t *testing.T) {
		t.Parallel()
		inner := permpolicy.NewPolicy([]governance.Rule{
			{Scope: governance.ScopeUser, Tool: "Write", Effect: governance.Ask},
		}, nil)
		p := newEscapePolicy(inner, PostureYolo)
		d := p.Evaluate(context.Background(), session.SessionID("s1"), session.ModeDefault, call, ws).Decision
		if d.Effect != governance.Ask {
			t.Fatalf("effect = %v, want Ask — the relax must NEVER suppress a configured Ask", d.Effect)
		}
		if !d.ConfiguredAsk {
			t.Fatal("ConfiguredAsk = false — the surviving Ask must stay marked configured (the child-ask model honours a configured Ask)")
		}
	})

	t.Run("an allow-always verdict on an escape learns nothing (allow-once v1)", func(t *testing.T) {
		t.Parallel()
		store := permstore.New()
		inner := permpolicy.NewPolicy(defaultRules(), store)
		p := newEscapePolicy(inner, PostureAuto)
		// Warm the per-root classifier (the Learn guard classifies against the
		// roots the policy has already seen — in the loop, Learn only ever
		// follows an Evaluate of the same call).
		d := p.Evaluate(context.Background(), session.SessionID("s1"), session.ModeDefault, call, ws).Decision
		if d.Effect != governance.Ask || d.ConfiguredAsk {
			t.Fatalf("escape at auto = %+v, want an unconfigured escape Ask", d)
		}
		// The loop calls Learn on an allow-always verdict; the wrapper must
		// NOT forward an out-of-root escape call — v1 escape asks are
		// allow-once only, never a learned out-of-root rule.
		p.Learn(session.SessionID("s1"), call)
		if got := store.Rules(session.SessionID("s1")); len(got) != 0 {
			t.Fatalf("Learn recorded %d rules for an out-of-root escape — the escape ask is allow-once only, nothing may be learned", len(got))
		}
		// Positive control: an IN-ROOT call on the same tool still learns (the
		// guard is escape-specific, not a Learn no-op).
		inRoot := scenario3Call("w2", "Write", map[string]string{"path": "note.txt", "content": "x"})
		p.Learn(session.SessionID("s1"), inRoot)
		if got := store.Rules(session.SessionID("s1")); len(got) != 1 {
			t.Fatalf("Learn recorded %d rules for an in-root Write, want 1 — the guard must not suppress ordinary learning", len(got))
		}
	})
}
