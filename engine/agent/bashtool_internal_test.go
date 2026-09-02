package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// fakeBashRunner is a scriptable tool.CommandRunner for the FOREGROUND path.
type fakeBashRunner struct {
	res tool.CommandResult
	err error

	mu      sync.Mutex
	command string
}

func (f *fakeBashRunner) Run(_ context.Context, command string) (tool.CommandResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.command = command
	return f.res, f.err
}

func (f *fakeBashRunner) RunWithEnvironment(ctx context.Context, command string, _ tool.CommandEnvironmentOverlay) (tool.CommandResult, error) {
	return f.Run(ctx, command)
}

// fakeStreamingRunner is a scriptable tool.CommandRunner + tool.CommandStreamer
// for the BACKGROUND path. RunStreaming blocks in run (nil ⇒ return
// immediately) so a test controls when the job lands its terminal.
type fakeStreamingRunner struct {
	out       string // written to the stream before returning
	exitCode  int
	err       error
	run       chan struct{} // closed by the test to release RunStreaming
	started   chan struct{} // closed (at most once) when RunStreaming begins
	sawCancel chan struct{} // closed when ctx dies while blocked

	streamErr error // if non-nil, RunStreaming fails before writing
}

var _ tool.CommandStreamer = (*fakeStreamingRunner)(nil)

func (*fakeStreamingRunner) Run(context.Context, string) (tool.CommandResult, error) {
	return tool.CommandResult{}, errors.New("fakeStreamingRunner: foreground Run not expected")
}

func (f *fakeStreamingRunner) RunStreaming(ctx context.Context, _ string, out io.Writer) (int, error) {
	if f.started != nil {
		// Close at most once: a table row may reuse one runner for the call,
		// and a started channel shared across rows must never double-close.
		select {
		case <-f.started:
		default:
			close(f.started)
		}
	}
	if f.streamErr != nil {
		return 0, f.streamErr
	}
	if f.run != nil {
		select {
		case <-f.run:
		case <-ctx.Done():
			if f.sawCancel != nil {
				close(f.sawCancel)
			}
			return 0, ctx.Err()
		}
	}
	if f.out != "" {
		_, _ = io.WriteString(out, f.out)
	}
	return f.exitCode, f.err
}

func (f *fakeStreamingRunner) RunStreamingWithEnvironment(ctx context.Context, command string, _ tool.CommandEnvironmentOverlay, out io.Writer) (int, error) {
	return f.RunStreaming(ctx, command, out)
}

// bashWS is the workspace every fake runs against (only Root() is read).
var bashWS = memfs.NewWorkspace("/ws")

// bashEnv wraps bashWS into a shell-less Environment (the background-Bash tests
// inject a streaming runner via the Environment's runner, not the workspace).
var bashEnv = tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws"}, bashWS, testReadLedger(), nil)

// bashEnvRunner wraps bashWS into an Environment bound to runner (the
// background-Bash tests pass a streaming runner this way, issue #462).
func bashEnvRunner(runner tool.CommandRunner) tool.Environment {
	return tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws"}, bashWS, testReadLedger(), runner)
}

func bashCall(id, command string, timeoutMS int, background bool) session.ToolCall {
	args := map[string]any{"command": command}
	if timeoutMS != 0 {
		args["timeout_ms"] = timeoutMS
	}
	if background {
		args["background"] = true
	}
	raw, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return session.ToolCall{ID: session.ToolCallID(id), Name: tool.BashToolName, Args: raw}
}

// --- foreground path (byte-identical to the fstools Bash body) ---

func TestBashToolForegroundSuccess(t *testing.T) {
	r := &fakeBashRunner{res: tool.CommandResult{Stdout: "hello\n", ExitCode: 0}}
	res, err := NewBashTool().Execute(context.Background(), bashCall("c1", "echo hello", 0, false), bashEnvRunner(r))
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError = true, body %q", res.Content)
	}
	if got, want := res.Content, "hello\n[exit code: 0]"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if r.command != "echo hello" {
		t.Fatalf("runner got %q, want echo hello", r.command)
	}
}

func TestBashToolForegroundNonZeroExit(t *testing.T) {
	r := &fakeBashRunner{res: tool.CommandResult{Stdout: "boom\n", Stderr: "bad\n", ExitCode: 3}}
	res, _ := NewBashTool().Execute(context.Background(), bashCall("c1", "false", 0, false), bashEnvRunner(r))
	if !res.IsError {
		t.Fatal("IsError = false on non-zero exit")
	}
	if got, want := res.Content, "boom\nbad\n[exit code: 3]"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestBashToolForegroundArgValidation(t *testing.T) {
	bt := NewBashTool()
	for _, tc := range []struct {
		name string
		call session.ToolCall
		want string
	}{
		{"empty command", bashCall("c1", "  ", 0, false), `the "command" argument is required`},
		{"negative timeout", bashCall("c1", "true", -5, false), `"timeout_ms" must be non-negative`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, _ := bt.Execute(context.Background(), tc.call, bashEnv)
			if !res.IsError || !strings.Contains(res.Content, tc.want) {
				t.Fatalf("res = %+v, want error containing %q", res, tc.want)
			}
		})
	}
	// Malformed JSON is a tool error, not a Go error.
	bad := session.ToolCall{ID: "c1", Name: tool.BashToolName, Args: json.RawMessage(`{"command": 42}`)}
	res, _ := bt.Execute(context.Background(), bad, bashEnv)
	if !res.IsError {
		t.Fatalf("malformed args: IsError = false, body %q", res.Content)
	}
}

func TestBashToolForegroundTimeoutTrailer(t *testing.T) {
	r := &fakeBashRunner{res: tool.CommandResult{Stdout: "partial\n"}, err: context.DeadlineExceeded}
	res, _ := NewBashTool().Execute(context.Background(), bashCall("c1", "sleep 99", 50, false), bashEnvRunner(r))
	if !res.IsError {
		t.Fatal("IsError = false on timeout")
	}
	want := "partial\n\n[command timed out after 50ms; output above is partial]"
	if res.Content != want {
		t.Fatalf("content = %q, want %q", res.Content, want)
	}
	// The ctx-error path prints no "[exit code: 0]" line.
	if strings.Contains(res.Content, "exit code") {
		t.Fatalf("ctx-error path leaked an exit line: %q", res.Content)
	}
}

func TestBashToolForegroundCancelAndNoShellTrailers(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"canceled", context.Canceled, "[command was canceled before producing output]"},
		{"no shell", tool.ErrNoShell, "[command failed to run: no shell available]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeBashRunner{err: tc.err}
			res, _ := NewBashTool().Execute(context.Background(), bashCall("c1", "x", 0, false), bashEnvRunner(r))
			if !res.IsError || res.Content != tc.want {
				t.Fatalf("res = %+v, want error %q", res, tc.want)
			}
		})
	}
}

func TestBashToolForegroundTimeoutTrailerSurvivesHugeOutput(t *testing.T) {
	r := &fakeBashRunner{
		res: tool.CommandResult{Stdout: strings.Repeat("x", 3*bashToolMaxOutputBytes)},
		err: context.DeadlineExceeded,
	}
	res, _ := NewBashTool().Execute(context.Background(), bashCall("c1", "x", 10, false), bashEnvRunner(r))
	if !res.IsError {
		t.Fatal("IsError = false on timeout")
	}
	trailer := "\n[command timed out after 10ms; output above is partial]"
	if !strings.HasSuffix(res.Content, trailer) {
		t.Fatalf("trailer lost under the cap; tail = %q", res.Content[len(res.Content)-80:])
	}
	if len(res.Content) > bashToolMaxOutputBytes {
		t.Fatalf("len = %d exceeds the %d cap", len(res.Content), bashToolMaxOutputBytes)
	}
}

func TestBashToolForegroundOutputCap(t *testing.T) {
	r := &fakeBashRunner{res: tool.CommandResult{Stdout: strings.Repeat("y", bashToolMaxOutputBytes+100)}}
	res, _ := NewBashTool().Execute(context.Background(), bashCall("c1", "x", 0, false), bashEnvRunner(r))
	if !strings.HasSuffix(res.Content, bashToolTruncationMarker) {
		t.Fatal("truncation marker missing")
	}
	if len(res.Content) > bashToolMaxOutputBytes+len(bashToolTruncationMarker) {
		t.Fatalf("len = %d exceeds cap+marker", len(res.Content))
	}
}

// TestBashToolNilRunnerSurfacesNoShellFromEverySite pins that every nil-runner
// site (foreground Execute, foreground ExecuteWithParent, background
// ExecuteWithParent) routes through the ONE bashNoShellResult composer, so the
// no-shell message can never drift from bashErrorTrailer's ErrNoShell wording.
func TestBashToolNilRunnerSurfacesNoShellFromEverySite(t *testing.T) {
	const want = "[command failed to run: no shell available]"
	bt := NewBashTool()

	// Foreground via Execute.
	res, _ := bt.Execute(context.Background(), bashCall("c1", "echo hi", 0, false), bashEnv)
	if !res.IsError || res.Content != want {
		t.Fatalf("Execute nil-runner: res = %+v, want error %q", res, want)
	}

	// Foreground via ExecuteWithParent (background:false).
	res2, _ := bt.(childCapableTool).ExecuteWithParent(
		context.Background(), bashCall("c2", "echo hi", 0, false), bashEnv, nil, parentCaps{})
	if !res2.IsError || res2.Content != want {
		t.Fatalf("ExecuteWithParent fg nil-runner: res = %+v, want error %q", res2, want)
	}

	// Background via ExecuteWithParent (background:true, with a child registry so
	// the no-registry decline does not fire first).
	reg := newChildRunRegistry()
	res3, _ := bt.(childCapableTool).ExecuteWithParent(
		context.Background(), bashCall("c3", "echo hi", 0, true), bashEnv, nil, parentCaps{children: reg})
	if !res3.IsError || res3.Content != want {
		t.Fatalf("ExecuteWithParent bg nil-runner: res = %+v, want error %q", res3, want)
	}
	if got := reg.statusSnapshot(); len(got) != 0 {
		t.Fatalf("registry = %+v, want empty (no phantom entry for a nil-runner decline)", got)
	}
}

func TestBashToolSpec(t *testing.T) {
	spec := NewBashTool().Spec()
	if spec.Name != tool.BashToolName {
		t.Fatalf("name = %q, want %q", spec.Name, tool.BashToolName)
	}
	if NewBashTool().ReadOnly() {
		t.Fatal("ReadOnly = true, want false")
	}
	s := string(spec.Schema)
	if !strings.Contains(s, `"background"`) {
		t.Fatal("schema lacks the background property")
	}
	if strings.Contains(s, `"required": ["command", "background"]`) {
		t.Fatal("background must stay optional")
	}
	for _, phrase := range []string{
		"BashStatus", "background", "job id", "cancelled if it is still running when this run",
		"REAL workspace root", "does not necessarily run Bash", "shell reported by \"shell:\"",
		"shell is non-interactive", "git rebase -i",
	} {
		if !strings.Contains(spec.Description, phrase) {
			t.Fatalf("description lacks %q", phrase)
		}
	}
}

// --- background path ---

// awaitJobDone parks on the registry doneCh the drive closes at terminal.
func awaitJobDone(t *testing.T, reg *childRunRegistry, jobID string) {
	t.Helper()
	doneCh, ok := reg.doneChFor(jobID)
	if !ok {
		t.Fatalf("no registry entry for %q", jobID)
	}
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("job %q never reached its terminal", jobID)
	}
}

func TestBashToolBackgroundStartAndTerminalResult(t *testing.T) {
	r := &fakeStreamingRunner{out: "line one\nline two\n", exitCode: 0, started: make(chan struct{})}
	reg := newChildRunRegistry()
	caps := parentCaps{children: reg}

	res, err := NewBashTool().(childCapableTool).ExecuteWithParent(
		context.Background(), bashCall("bg1", "make serve", 0, true), bashEnvRunner(r), nil, caps)
	if err != nil {
		t.Fatalf("ExecuteWithParent err = %v", err)
	}
	if res.IsError {
		t.Fatalf("started-result is an error: %q", res.Content)
	}
	if !strings.Contains(res.Content, "job id: bashcmd-bg1") {
		t.Fatalf("started-result lacks the job id: %q", res.Content)
	}
	if !strings.Contains(res.Content, "BashStatus") {
		t.Fatalf("started-result lacks the BashStatus pointer: %q", res.Content)
	}

	jobID := "bashcmd-bg1"
	select {
	case <-r.started:
	case <-time.After(5 * time.Second):
		t.Fatal("streaming drive never started")
	}
	awaitJobDone(t, reg, jobID)

	// Registry holds a done background entry with the tail + terminal result.
	snap := reg.statusSnapshot()
	if len(snap) != 1 || snap[0].id != jobID {
		t.Fatalf("snapshot = %+v, want the one %q entry", snap, jobID)
	}
	if snap[0].family != childFamilyBashCmd || !snap[0].background || snap[0].state != childDone || snap[0].stop != session.StopEndTurn {
		t.Fatalf("entry = %+v, want done background bash-cmd at StopEndTurn", snap[0])
	}
	stored, st, outcome := reg.collect(jobID)
	if outcome != collectOK {
		t.Fatalf("collect outcome = %v (status %+v), want collectOK", outcome, st)
	}
	if stored.IsError {
		t.Fatalf("clean exit stored an error result: %q", stored.Content)
	}
	want := "line one\nline two\n[exit code: 0]"
	if stored.Content != want {
		t.Fatalf("stored result = %q, want %q", stored.Content, want)
	}
	if tail, truncated, ok := reg.outputTailSnapshot(jobID); !ok || truncated || tail != "line one\nline two\n" {
		t.Fatalf("tail snapshot = (%q, %v, %v)", tail, truncated, ok)
	}
}

func TestBashToolBackgroundNoRegistry(t *testing.T) {
	res, _ := NewBashTool().(childCapableTool).ExecuteWithParent(
		context.Background(), bashCall("bg1", "x", 0, true), bashEnv, nil, parentCaps{})
	if !res.IsError || !strings.Contains(res.Content, "not supported on this run") {
		t.Fatalf("res = %+v, want not-supported error", res)
	}
	// The plain Execute path declines identically.
	res, _ = NewBashTool().Execute(context.Background(), bashCall("bg2", "x", 0, true), bashEnv)
	if !res.IsError || !strings.Contains(res.Content, "not supported on this run") {
		t.Fatalf("Execute res = %+v, want not-supported error", res)
	}
}

func TestBashToolBackgroundRunnerNotStreamer(t *testing.T) {
	reg := newChildRunRegistry()
	r := &fakeBashRunner{}
	res, _ := NewBashTool().(childCapableTool).ExecuteWithParent(
		context.Background(), bashCall("bg1", "x", 0, true), bashEnvRunner(r), nil, parentCaps{children: reg})
	if !res.IsError || !strings.Contains(res.Content, "not supported by this command runner") {
		t.Fatalf("res = %+v, want runner-not-streamer error", res)
	}
	if got := reg.statusSnapshot(); len(got) != 0 {
		t.Fatalf("registry = %+v, want empty (no phantom entry)", got)
	}
}

func TestBashToolBackgroundJobCountGate(t *testing.T) {
	reg := newChildRunRegistry()
	caps := parentCaps{children: reg}
	streamer := &fakeStreamingRunner{}
	bt := NewBashTool().(childCapableTool)

	// Fill the gate with maxBackgroundBashJobs LIVE background entries.
	for i := 0; i < maxBackgroundBashJobs; i++ {
		id := session.SessionID(BashCmdJobPrefix + strings.Repeat("x", 1) + string(rune('a'+i)))
		reg.register(string(id), childFamilyBashCmd, "live", context.CancelFunc(func() {}), true)
	}
	res, _ := bt.ExecuteWithParent(context.Background(), bashCall("bg9", "ninth", 0, true), bashEnvRunner(streamer), nil, caps)
	if !res.IsError {
		t.Fatalf("9th background start succeeded: %q", res.Content)
	}
	if !strings.Contains(res.Content, "background job concurrency limit reached") {
		t.Fatalf("res = %q, want the gate-full error", res.Content)
	}
	if strings.Contains(res.Content, "bashcmd-bg9") {
		t.Fatalf("gate-full error lists the failing call's own id: %q", res.Content)
	}
	if !strings.Contains(res.Content, BashCmdJobPrefix+"xa") {
		t.Fatalf("gate-full error omits the live ids: %q", res.Content)
	}
	if got := reg.statusSnapshot(); len(got) != maxBackgroundBashJobs {
		t.Fatalf("registry = %d entries, want the original %d", len(got), maxBackgroundBashJobs)
	}

	// A DONE background entry does not count toward the gate.
	reg.markDone(BashCmdJobPrefix+"xa", session.StopEndTurn)
	res, _ = bt.ExecuteWithParent(context.Background(), bashCall("bg9", "ninth", 0, true), bashEnvRunner(streamer), nil, caps)
	if res.IsError {
		t.Fatalf("start after a terminal still gated: %q", res.Content)
	}
}

func TestBashToolBackgroundTerminalClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		runner    *fakeStreamingRunner
		cancel    bool // cancel the job ctx instead of releasing the runner
		client    bool // mark the job client-cancelled first (CancelChild path)
		wantStop  session.StopReason
		wantError bool
		wantIn    []string
	}{{
		name:      "exit 0",
		runner:    &fakeStreamingRunner{out: "ok\n", exitCode: 0},
		wantStop:  session.StopEndTurn,
		wantError: false,
		wantIn:    []string{"ok", "[exit code: 0]"},
	}, {
		name:      "exit 1",
		runner:    &fakeStreamingRunner{out: "failed\n", exitCode: 1},
		wantStop:  session.StopError,
		wantError: true,
		wantIn:    []string{"failed", "[exit code: 1]"},
	}, {
		name:      "client cancel",
		runner:    &fakeStreamingRunner{run: make(chan struct{}), started: make(chan struct{})},
		cancel:    true,
		client:    true,
		wantStop:  session.StopCancelled,
		wantError: false,
		wantIn:    []string{"[command was canceled; output above is partial]"},
	}, {
		name:      "parent cancel (not client)",
		runner:    &fakeStreamingRunner{run: make(chan struct{}), started: make(chan struct{})},
		cancel:    true,
		wantStop:  session.StopCancelled,
		wantError: false,
		wantIn:    []string{"[command was canceled; output above is partial]"},
	}, {
		name:      "timeout",
		runner:    &fakeStreamingRunner{run: make(chan struct{}), started: make(chan struct{})},
		wantStop:  session.StopError,
		wantError: true,
		wantIn:    []string{"[command timed out; output above is partial]"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newChildRunRegistry()
			caps := parentCaps{children: reg}
			runner := *tc.runner
			runner.started = make(chan struct{})
			bt := NewBashTool().(childCapableTool)

			ctx := context.Background()
			cancelCtx, cancelRun := context.WithCancel(ctx)
			defer cancelRun()
			timeoutMS := 0
			if tc.name == "timeout" {
				timeoutMS = 30
			} else if tc.cancel {
				ctx = cancelCtx
			}

			res, _ := bt.ExecuteWithParent(ctx, bashCall("bg1", "cmd", timeoutMS, true), bashEnvRunner(&runner), nil, caps)
			if res.IsError {
				t.Fatalf("start failed: %q", res.Content)
			}
			jobID := "bashcmd-bg1"
			select {
			case <-runner.started:
			case <-time.After(5 * time.Second):
				t.Fatal("drive never started")
			}
			if tc.client {
				if _, _, ok := reg.requestCancel(jobID); !ok {
					t.Fatal("requestCancel: entry not found")
				}
			}
			if tc.cancel {
				cancelRun()
			}
			awaitJobDone(t, reg, jobID)

			snap := reg.statusSnapshot()
			if len(snap) != 1 || snap[0].stop != tc.wantStop {
				t.Fatalf("stop = %+v, want %s", snap, tc.wantStop)
			}
			stored, _, outcome := reg.collect(jobID)
			if outcome != collectOK {
				t.Fatalf("collect outcome = %v", outcome)
			}
			if stored.IsError != tc.wantError {
				t.Fatalf("IsError = %v, want %v (body %q)", stored.IsError, tc.wantError, stored.Content)
			}
			for _, want := range tc.wantIn {
				if !strings.Contains(stored.Content, want) {
					t.Fatalf("stored result %q lacks %q", stored.Content, want)
				}
			}
		})
	}
}

func TestBashToolBackgroundTruncatedTailFlag(t *testing.T) {
	r := &fakeStreamingRunner{out: strings.Repeat("z", maxBashJobTailBytes+10), exitCode: 0, started: make(chan struct{})}
	reg := newChildRunRegistry()
	res, _ := NewBashTool().(childCapableTool).ExecuteWithParent(
		context.Background(), bashCall("bg1", "flood", 0, true), bashEnvRunner(r), nil, parentCaps{children: reg})
	if res.IsError {
		t.Fatalf("start failed: %q", res.Content)
	}
	awaitJobDone(t, reg, "bashcmd-bg1")
	stored, _, outcome := reg.collect("bashcmd-bg1")
	if outcome != collectOK {
		t.Fatalf("collect outcome = %v", outcome)
	}
	if !strings.Contains(stored.Content, "older output dropped") {
		t.Fatalf("stored result lacks the truncated-tail flag: %q", stored.Content[len(stored.Content)-120:])
	}
	if _, truncated, ok := reg.outputTailSnapshot("bashcmd-bg1"); !ok || !truncated {
		t.Fatalf("tail truncated = (%v, %v)", truncated, ok)
	}
}

func TestBashToolBackgroundFinishSafeWhenSealed(t *testing.T) {
	r := &fakeStreamingRunner{run: make(chan struct{}), started: make(chan struct{})}
	reg := newChildRunRegistry()
	caps := parentCaps{children: reg}
	bt := NewBashTool().(childCapableTool)

	res, _ := bt.ExecuteWithParent(context.Background(), bashCall("bg1", "cmd", 0, true), bashEnvRunner(r), nil, caps)
	if res.IsError {
		t.Fatalf("start failed: %q", res.Content)
	}
	select {
	case <-r.started:
	case <-time.After(5 * time.Second):
		t.Fatal("drive never started")
	}
	// Seal the registry mid-drive (the run-end abandon path), then let the
	// job finish: the terminal must land without a panic (safeEmit/markDone
	// are post-seal no-ops).
	reg.seal()
	close(r.run)
	awaitJobDone(t, reg, "bashcmd-bg1")
	snap := reg.statusSnapshot()
	if len(snap) != 1 || snap[0].state != childDone {
		t.Fatalf("post-seal terminal = %+v, want done", snap)
	}
}

func TestBashToolBackgroundRunEndDrainCancelsJob(t *testing.T) {
	sawCancel := make(chan struct{})
	r := &fakeStreamingRunner{run: make(chan struct{}), started: make(chan struct{}), sawCancel: sawCancel}
	reg := newChildRunRegistry()
	caps := parentCaps{children: reg}
	bt := NewBashTool().(childCapableTool)

	res, _ := bt.ExecuteWithParent(context.Background(), bashCall("bg1", "sleep 99", 0, true), bashEnvRunner(r), nil, caps)
	if res.IsError {
		t.Fatalf("start failed: %q", res.Content)
	}
	select {
	case <-r.started:
	case <-time.After(5 * time.Second):
		t.Fatal("drive never started")
	}
	// The run-end drain cancels the live job and joins on its doneCh.
	joins := reg.cancelLiveBackground()
	if len(joins) != 1 || joins[0].id != "bashcmd-bg1" {
		t.Fatalf("joins = %+v, want the one job", joins)
	}
	select {
	case <-sawCancel:
	case <-time.After(5 * time.Second):
		t.Fatal("job ctx never cancelled")
	}
	for _, j := range joins {
		select {
		case <-j.doneCh:
		case <-time.After(5 * time.Second):
			t.Fatalf("job %q never joined", j.id)
		}
	}
	snap := reg.statusSnapshot()
	if len(snap) != 1 || snap[0].stop != session.StopCancelled {
		t.Fatalf("drained job stop = %+v, want StopCancelled", snap)
	}
}

func TestBashToolBackgroundLabelClamped(t *testing.T) {
	long := strings.Repeat("a", 500) + "\nwith-a-newline"
	reg := newChildRunRegistry()
	caps := parentCaps{children: reg}
	r := &fakeStreamingRunner{out: "", exitCode: 0}
	res, _ := NewBashTool().(childCapableTool).ExecuteWithParent(
		context.Background(), bashCall("bg1", long, 0, true), bashEnvRunner(r), nil, caps)
	if res.IsError {
		t.Fatalf("start failed: %q", res.Content)
	}
	snap := reg.statusSnapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if strings.Contains(snap[0].goal, "\n") {
		t.Fatalf("label is not single-line: %q", snap[0].goal)
	}
	if got := len([]rune(snap[0].goal)); got > maxSubagentGoalLen+1 {
		t.Fatalf("label = %d runes, want ≤ %d", got, maxSubagentGoalLen+1)
	}
	awaitJobDone(t, reg, "bashcmd-bg1")
}
