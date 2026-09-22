package statusline

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStatusLine_Scenario4_DebounceTemplateRefreshAndClock(t *testing.T) {
	command := commandTest(t.TempDir(), "ready")
	source := NewCommandSource(command)
	t.Cleanup(func() { _ = source.Close(context.Background()) })

	source.Submit(Input{Session: Session{Title: "first"}, Terminal: Terminal{FooterAvailCols: 80}})
	source.Submit(Input{Session: Session{Title: "latest"}, Terminal: Terminal{FooterAvailCols: 80}})
	select {
	case <-source.Changed():
		t.Fatal("command published before the 250ms debounce")
	case <-time.After(150 * time.Millisecond):
	}
	waitStatusChange(t, source)
	if got := statusSurfaceText(source.Latest().Footer); got != "ready" {
		t.Fatalf("command footer = %q, want latest refresh", got)
	}

	templates := NewTemplateSource(TemplateSet{Footer: SurfaceTemplates{Full: `<footer><text>{{.Clock.Now.Format "15:04:05"}}</text></footer>`}}, time.Second)
	t.Cleanup(func() { _ = templates.Close(context.Background()) })
	templates.Submit(Input{Terminal: Terminal{FooterAvailCols: 80}})
	waitStatusChange(t, templates)
	first := statusSurfaceText(templates.Latest().Footer)
	select {
	case <-templates.Changed():
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("template clock did not refresh on its interval")
	}
	if got := statusSurfaceText(templates.Latest().Footer); got == first {
		t.Fatalf("template clock did not advance: %q", got)
	}
}

func TestStatusLine_Scenario4_CommandMaxWaitLifecycle(t *testing.T) {
	ticks := make(chan time.Time)
	started := make(chan string, 3)
	activeContexts := make(chan context.Context, 1)
	release := make(chan struct{})
	s := newSourceWithOptions(ticks, func(ctx context.Context, input Input) renderedStatusLine {
		started <- input.Session.Title
		if input.Session.Title == "active" {
			activeContexts <- ctx
			<-release
		}
		return renderedStatusLine{line: Result{Footer: Surface{Present: true, Spans: []Span{{Text: "unchanged"}}}}}
	}, false, time.Hour, 0)
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	clock := &manualStatusTimers{timers: make(chan *manualStatusTimer, 4)}
	s.newTimer = clock.New
	s.now = func() time.Time { return time.Unix(1, 0) }

	// Command requests use a trailing debounce: only the latest input starts.
	s.Submit(Input{Session: Session{Title: "discarded"}})
	first := clock.Next(t)
	s.Submit(Input{Session: Session{Title: "active"}})
	if !first.WaitStopped(t) {
		t.Fatal("new command input did not replace the prior debounce")
	}
	clock.Next(t).Fire()
	if got := <-started; got != "active" {
		t.Fatalf("started %q, want latest input", got)
	}

	// Interval ticks never cancel active work and coalesce to one immediate
	// refresh after it completes, even when several ticks arrive.
	activeCtx := <-activeContexts
	ticks <- time.Unix(2, 0)
	ticks <- time.Unix(3, 0)
	if err := activeCtx.Err(); err != nil {
		t.Fatalf("interval cancelled active command: %v", err)
	}
	close(release)
	select {
	case <-s.Changed():
	case <-time.After(time.Second):
		t.Fatal("active command did not publish")
	}
	if got := <-started; got != "active" {
		t.Fatalf("deferred refresh started %q, want active input", got)
	}
}

func TestStatusLineSourceSuppressesSemanticNoOpPublish(t *testing.T) {
	s := newSource(nil, func(context.Context, Input) Result { return Result{} })
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	result := statusLineResult{result: renderedStatusLine{line: Result{Footer: Surface{Present: true, Spans: []Span{{Text: "same"}}}}}}
	s.publish(result)
	<-s.Changed()
	s.publish(result)
	select {
	case <-s.Changed():
		t.Fatal("semantic no-op published")
	default:
	}
}

type manualStatusTimer struct {
	mu        sync.Mutex
	ch        chan time.Time
	stopped   bool
	stoppedCh chan struct{}
}

func (t *manualStatusTimer) Chan() <-chan time.Time { return t.ch }
func (t *manualStatusTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return false
	}
	t.stopped = true
	close(t.stoppedCh)
	return true
}
func (t *manualStatusTimer) Fire() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.stopped {
		t.ch <- time.Unix(1, 0)
	}
}
func (t *manualStatusTimer) WaitStopped(tb testing.TB) bool {
	tb.Helper()
	select {
	case <-t.stoppedCh:
		return true
	case <-time.After(time.Second):
		return false
	}
}

type manualStatusTimers struct{ timers chan *manualStatusTimer }

func (t *manualStatusTimers) New(time.Duration) sourceTimer {
	timer := &manualStatusTimer{ch: make(chan time.Time, 1), stoppedCh: make(chan struct{})}
	t.timers <- timer
	return timer
}
func (t *manualStatusTimers) Next(tb testing.TB) *manualStatusTimer {
	tb.Helper()
	select {
	case timer := <-t.timers:
		return timer
	case <-time.After(time.Second):
		tb.Fatal("source did not create debounce timer")
		return nil
	}
}

func TestStatusLine_Scenario4_OptionalInterval(t *testing.T) {
	withoutInterval := NewDefaultSource(0)
	t.Cleanup(func() { _ = withoutInterval.Close(context.Background()) })
	withoutInterval.Submit(Input{Terminal: Terminal{HeaderAvailCols: 80, FooterAvailCols: 80}})
	waitStatusChange(t, withoutInterval)
	select {
	case <-withoutInterval.Changed():
		t.Fatal("source without an interval refreshed while idle")
	case <-time.After(250 * time.Millisecond):
	}

	minimumInterval := NewDefaultSource(100 * time.Millisecond)
	t.Cleanup(func() { _ = minimumInterval.Close(context.Background()) })
	minimumInterval.Submit(Input{Terminal: Terminal{HeaderAvailCols: 80, FooterAvailCols: 80}})
	waitStatusChange(t, minimumInterval)
	select {
	case <-minimumInterval.Changed():
		t.Fatal("source accepted an interval below one second")
	case <-time.After(250 * time.Millisecond):
	}

	command := commandTest(t.TempDir(), "tick")
	command.RefreshInterval = time.Second
	commandInterval := NewCommandSource(command)
	t.Cleanup(func() { _ = commandInterval.Close(context.Background()) })
	commandInterval.Submit(Input{Terminal: Terminal{FooterAvailCols: 80}})
	waitStatusChange(t, commandInterval)
	select {
	case <-commandInterval.Changed():
		t.Fatal("semantic no-op command refresh published")
	case <-time.After(1500 * time.Millisecond):
	}
}

func TestStatusLine_Scenario4_ProcessLifecycleAndGenerationGuard(t *testing.T) {
	command := commandTest(t.TempDir(), "latest")
	source := NewCommandSource(command)
	t.Cleanup(func() { _ = source.Close(context.Background()) })

	source.Submit(Input{Session: Session{Title: "first"}, Terminal: Terminal{FooterAvailCols: 80}})
	time.Sleep(300 * time.Millisecond) // let the debounced first tree start
	source.Submit(Input{Session: Session{Title: "latest"}, Terminal: Terminal{FooterAvailCols: 80}})
	select {
	case <-source.Changed():
	case <-time.After(2 * time.Second):
		t.Fatal("replacement did not cancel and join the older command tree")
	}
	if got := statusSurfaceText(source.Latest().Footer); got != "latest" {
		t.Fatalf("stale command completion won: footer = %q", got)
	}
}

func TestStatusLine_Scenario4_FailureDegradesWithoutLeakage(t *testing.T) {
	const secret = "status-command-secret"
	marker := t.TempDir() + "/first"
	command := commandTest(t.TempDir(), "fail", marker)
	source := NewCommandSource(command)
	t.Cleanup(func() { _ = source.Close(context.Background()) })
	source.Submit(Input{Session: Session{Title: "first"}, Terminal: Terminal{FooterAvailCols: 80}})
	waitStatusChange(t, source)
	source.Submit(Input{Session: Session{Title: "failure"}, Terminal: Terminal{FooterAvailCols: 80}})
	waitStatusChange(t, source)
	text := statusSurfaceText(source.Latest().Header) + statusSurfaceText(source.Latest().Footer)
	if !strings.Contains(text, "good [stale]") || strings.Contains(text, secret) {
		t.Fatalf("failure degradation = %q, want stale last-good surface without command output", text)
	}
	diagnostics := source.(CommandDiagnosticsSource).CommandDiagnostics()
	if diagnostics != (CommandDiagnostics{Header: CommandSurfaceDefault, Footer: CommandSurfaceStale, Error: CommandErrorExit}) {
		t.Fatalf("command diagnostics = %#v", diagnostics)
	}
}
