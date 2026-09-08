package statusline

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// Source produces the latest semantic status-line surfaces from raw display
// facts. It is UI-agnostic: Changed is a wake-up edge, never a queue.
type Source interface {
	Submit(Input)
	Changed() <-chan struct{}
	Latest() Result
	Close(context.Context) error
}

// CommandDiagnosticsSource is optionally implemented by command-backed sources.
// Template and default sources intentionally expose no command diagnostics.
type CommandDiagnosticsSource interface {
	CommandDiagnostics() CommandDiagnostics
}

// CommandDiagnostics is the safe, coarse status of the latest completed command.
// Its fields are closed vocabularies: Header/Footer are default, custom, or stale;
// Error is none, unsupported, timeout, output_limit, invalid_statusml, exit, or failed.
type CommandDiagnostics struct {
	Header string
	Footer string
	Error  string
}

// CommandSurfaceDefault, CommandSurfaceCustom, and CommandSurfaceStale describe
// whether the latest command result uses a shipped, custom, or stale surface.
// CommandErrorNone, CommandErrorUnsupported, CommandErrorTimeout,
// CommandErrorOutputLimit, CommandErrorInvalidStatusML, CommandErrorExit, and
// CommandErrorFailed classify a command result without exposing failure details.
const (
	CommandSurfaceDefault       = "default"
	CommandSurfaceCustom        = "custom"
	CommandSurfaceStale         = "stale"
	CommandErrorNone            = "none"
	CommandErrorUnsupported     = "unsupported"
	CommandErrorTimeout         = "timeout"
	CommandErrorOutputLimit     = "output_limit"
	CommandErrorInvalidStatusML = "invalid_statusml"
	CommandErrorExit            = "exit"
	CommandErrorFailed          = "failed"
)

// Result holds independently selected semantic surfaces. It never contains
// terminal rendering or control sequences.
type Result struct {
	Header Surface
	Footer Surface
}

const (
	sourceCloseTimeout = time.Second
	commandDebounce    = 250 * time.Millisecond
	commandDeadline    = time.Second
)

type sourceTimer interface {
	Chan() <-chan time.Time
	Stop() bool
}

type realTimer struct{ *time.Timer }

func (t realTimer) Chan() <-chan time.Time { return t.C }

func newRealTimer(delay time.Duration) sourceTimer { return realTimer{Timer: time.NewTimer(delay)} }

type renderedStatusLine struct {
	line           Result
	headerSupplied bool
	footerSupplied bool
	err            error
	command        bool
	errorClass     string
}

type sourceRenderer func(context.Context, Input) renderedStatusLine

// NewDefaultSource creates the shipped source. It owns all default variants,
// including initial and partial-surface degradation.
func NewDefaultSource(refreshInterval time.Duration) Source {
	return newTemplateSource(TemplateSet{}, refreshInterval)
}

type statusLineSource struct {
	changed chan struct{}
	submit  chan struct{}
	done    chan struct{}

	mu         sync.Mutex
	latest     Result
	input      Input
	hasInput   bool
	generation uint64
	closed     bool
	closeOnce  sync.Once
	stopTicker func()
	newTimer   func(time.Duration) sourceTimer
	now        func() time.Time

	render             sourceRenderer
	debounce           time.Duration
	invocationTimeout  time.Duration
	workerCompleted    chan struct{}
	renderCtx          context.Context
	cancelRender       context.CancelFunc
	lastGoodHeader     Surface
	hasGoodHeader      bool
	lastGoodFooter     Surface
	hasGoodFooter      bool
	commandDiagnostics CommandDiagnostics
}

func newSource(ticks <-chan time.Time, render func(context.Context, Input) Result) *statusLineSource {
	return newSourceWithInitial(ticks, render, true)
}

func newSourceWithInitial(ticks <-chan time.Time, render func(context.Context, Input) Result, initial bool) *statusLineSource {
	return newSourceWithOptions(ticks, func(ctx context.Context, input Input) renderedStatusLine {
		return renderedStatusLine{line: render(ctx, input)}
	}, initial, 0, 0)
}

func newSourceWithOptions(ticks <-chan time.Time, render sourceRenderer, initial bool, debounce, timeout time.Duration) *statusLineSource {
	ctx, cancel := context.WithCancel(context.Background())
	s := &statusLineSource{
		changed: make(chan struct{}, 1), submit: make(chan struct{}, 1), done: make(chan struct{}),
		render: render, debounce: debounce, invocationTimeout: timeout,
		newTimer: newRealTimer, now: time.Now,
		workerCompleted: make(chan struct{}), renderCtx: ctx, cancelRender: cancel,
	}
	if initial {
		s.latest = cloneResult(render(ctx, Input{}).line)
	}
	go s.run(ticks)
	return s
}

func (s *statusLineSource) Submit(input Input) {
	s.mu.Lock()
	if s.closed || (s.hasInput && s.input == input) {
		s.mu.Unlock()
		return
	}
	s.input, s.hasInput, s.generation = input, true, s.generation+1
	s.mu.Unlock()
	select {
	case s.submit <- struct{}{}:
	default:
	}
}
func (s *statusLineSource) Changed() <-chan struct{} { return s.changed }
func (s *statusLineSource) Latest() Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneResult(s.latest)
}

func (s *statusLineSource) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		if s.stopTicker != nil {
			s.stopTicker()
		}
		s.cancelRender()
		close(s.done)
	})
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), sourceCloseTimeout)
	defer cancel()
	select {
	case <-s.workerCompleted:
		return nil
	case <-cleanup.Done():
		return cleanup.Err()
	}
}

type statusLineResult struct {
	generation uint64
	input      Input
	result     renderedStatusLine
}

//nolint:gocyclo // It is the single lifecycle state machine for timer, invocation, and shutdown ownership.
func (s *statusLineSource) run(ticks <-chan time.Time) {
	defer close(s.workerCompleted)
	defer close(s.changed)
	results := make(chan statusLineResult, 1)
	var timer sourceTimer
	var timerC <-chan time.Time
	var activeCancel context.CancelFunc
	active := false
	ready := false
	refreshPending := false

	stopTimer := func() {
		if timer != nil && !timer.Stop() {
			select {
			case <-timer.Chan():
			default:
			}
		}
		timerC = nil
	}
	schedule := func(delay time.Duration) {
		stopTimer()
		if delay <= 0 {
			ready = true
			return
		}
		ready = false
		timer = s.newTimer(delay)
		timerC = timer.Chan()
	}
	start := func(now time.Time) {
		if active || !ready {
			return
		}
		s.mu.Lock()
		if s.closed || !s.hasInput {
			s.mu.Unlock()
			return
		}
		input, generation := s.input, s.generation
		input.Clock.Now = now
		s.mu.Unlock()
		ready, active = false, true
		ctx := s.renderCtx
		var cancel context.CancelFunc
		if s.invocationTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, s.invocationTimeout)
		} else {
			ctx, cancel = context.WithCancel(ctx)
		}
		activeCancel = cancel
		go func(cancel context.CancelFunc) {
			result := s.render(ctx, input)
			cancel()
			results <- statusLineResult{generation: generation, input: input, result: result}
		}(cancel)
	}
	for {
		if ready && !active {
			start(s.now())
		}
		select {
		case <-s.done:
			if activeCancel != nil {
				activeCancel()
			}
			if active {
				<-results // Close joins the command/tree reader before closing Changed.
			}
			return
		case <-s.submit:
			if activeCancel != nil {
				activeCancel()
			}
			schedule(s.debounce)
		case now, ok := <-ticks:
			if !ok {
				ticks = nil
				continue
			}
			if active {
				// An interval is a maximum wait, not a replacement request: let the
				// contained command finish, then run one deferred refresh.
				refreshPending = true
				continue
			}
			schedule(0)
			start(now)
		case <-timerC:
			timerC = nil
			ready = true
		case completed := <-results:
			active, activeCancel = false, nil
			s.publish(completed)
			if refreshPending {
				refreshPending = false
				schedule(0)
			}
		}
	}
}

func (s *statusLineSource) publish(completed statusLineResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || completed.generation != s.generation {
		return
	}
	line := completed.result.line
	if completed.result.err != nil {
		if s.hasGoodHeader {
			line.Header = staleSurface(s.lastGoodHeader, completed.input.Terminal.HeaderAvailCols)
		}
		if s.hasGoodFooter {
			line.Footer = staleSurface(s.lastGoodFooter, completed.input.Terminal.FooterAvailCols)
		}
	} else {
		if completed.result.headerSupplied {
			s.lastGoodHeader, s.hasGoodHeader = cloneNormalizedSurface(line.Header), true
		}
		if completed.result.footerSupplied {
			s.lastGoodFooter, s.hasGoodFooter = cloneNormalizedSurface(line.Footer), true
		}
	}
	if completed.result.command {
		s.commandDiagnostics = CommandDiagnostics{
			Header: commandSurfaceState(completed.result.err != nil, s.hasGoodHeader, completed.result.headerSupplied),
			Footer: commandSurfaceState(completed.result.err != nil, s.hasGoodFooter, completed.result.footerSupplied),
			Error:  completed.result.errorClass,
		}
	}
	line = cloneResult(line)
	if reflect.DeepEqual(s.latest, line) {
		return
	}
	s.latest = cloneResult(line)
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

func commandSurfaceState(failed, hasGood, supplied bool) string {
	if failed {
		if hasGood {
			return CommandSurfaceStale
		}
		return CommandSurfaceDefault
	}
	if supplied {
		return CommandSurfaceCustom
	}
	return CommandSurfaceDefault
}

func staleSurface(surface Surface, width int) Surface {
	const stale = " [stale]"
	if !surface.Present {
		return surface
	}
	appendMarker := cloneNormalizedSurface(surface)
	appendMarker.Spans = append(appendMarker.Spans, Span{Text: stale, Token: TokenWarning})
	if ansi.StringWidth(statusSurfaceText(appendMarker)) <= width {
		return appendMarker
	}
	prependMarker := cloneNormalizedSurface(surface)
	prependMarker.Spans = append([]Span{{Text: "[stale] ", Token: TokenWarning}}, prependMarker.Spans...)
	if ansi.StringWidth(statusSurfaceText(prependMarker)) <= width {
		return prependMarker
	}
	return Surface{Present: true, Spans: []Span{{Text: "[stale]", Token: TokenWarning}}}
}

func cloneResult(result Result) Result {
	return Result{Header: cloneNormalizedSurface(result.Header), Footer: cloneNormalizedSurface(result.Footer)}
}
func cloneNormalizedSurface(surface Surface) Surface {
	return Surface{Present: surface.Present, Spans: normalizeSpans(surface.Spans)}
}
func normalizeSpans(spans []Span) []Span {
	if len(spans) == 0 {
		return nil
	}
	out := make([]Span, 0, len(spans))
	for _, span := range spans {
		span.Text, span.Color, span.Underline = sanitizeTerminal(span.Text), "", false
		if !validLink(span.Href) {
			span.Href = ""
		}
		if !isToken(string(span.Token)) {
			span.Token = TokenText
		}
		out = append(out, span)
	}
	return out
}
