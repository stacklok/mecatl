package ui

import (
	"context"
	"io"
	"strconv"
	"sync"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// fakeRecver replays a scripted slice of responses then returns io.EOF. It
// optionally gates at a named event type so a test can hold the stream at the
// permission.ask until it has driven the approval — then release the tail.
//
// It also exposes a DETERMINISTIC, output-independent progress signal the teatest
// cases use to sequence input without polling the rendered output: reachedGate
// closes the instant the gated event (the permission.ask) has been yielded to the
// production ReadLoop, i.e. the moment the ui will receive it.
//
// This fires on the reader goroutine (which keeps getting scheduled even under a
// CPU-starved -race run) and does NOT depend on Bubble Tea's 60fps flush ticker
// reaching teatest's output buffer — the ticker is what starves under `task test`'s
// parallel `go test -race ./...`, making a WaitFor(tm.Output()) deadline fire
// before any frame is flushed. Gating on this (plus the reducer phase observer, then
// asserting on FinalModel) makes the cases robust to that starvation without
// weakening them.
type fakeRecver struct {
	mu          sync.Mutex
	script      []*mecatlv1.ConverseResponse
	idx         int
	gateType    string
	gatedBefore bool
	gate        chan struct{}
	released    bool

	reachedGate chan struct{} // closed when the gated event has been yielded
	gateSignal  bool          // guards reachedGate's one-shot close
}

func (f *fakeRecver) Recv() (*mecatlv1.ConverseResponse, error) {
	f.mu.Lock()
	if f.idx >= len(f.script) {
		f.mu.Unlock()
		return nil, io.EOF
	}
	r := f.script[f.idx]
	f.idx++
	// Gate AFTER yielding the gated event: the ask is delivered, then the next
	// Recv blocks until the test approves, so the post-approval tail is held
	// back. (Gating before would swallow the ask itself.)
	gate := f.gateType != "" && f.gatedBefore
	if !gate && f.gateType != "" && r.GetEvent().GetType() == f.gateType {
		f.gatedBefore = true
		f.signalGateLocked()
	}
	f.mu.Unlock()
	if gate {
		<-f.gate
	}
	return r, nil
}

// signalGateLocked closes reachedGate once (the gated event has been yielded to
// the ReadLoop). Caller holds f.mu.
func (f *fakeRecver) signalGateLocked() {
	if !f.gateSignal && f.reachedGate != nil {
		f.gateSignal = true
		close(f.reachedGate)
	}
}

func (f *fakeRecver) release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.released {
		f.released = true
		close(f.gate)
	}
}

// fakeSender records sent frames so a test can assert the ResumeApproval round
// trip, and releases the recv gate on approval so the post-approval tail flows.
type fakeSender struct {
	mu     sync.Mutex
	sent   []*mecatlv1.ConverseRequest
	onSend func(*mecatlv1.ConverseRequest)
}

func (f *fakeSender) Send(req *mecatlv1.ConverseRequest) error {
	f.mu.Lock()
	f.sent = append(f.sent, req)
	cb := f.onSend
	f.mu.Unlock()
	if cb != nil {
		cb(req)
	}
	return nil
}

func (f *fakeSender) frames() []*mecatlv1.ConverseRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*mecatlv1.ConverseRequest, len(f.sent))
	copy(out, f.sent)
	return out
}

// fakeConv is the ui's Converser+SessionCreator backed by a fakeRecver/Sender. It
// builds a real client.Stream so the test exercises the production ReadLoop,
// EventToMsg, and send path — the only thing faked is the transport.
type fakeConv struct {
	recv *fakeRecver
	send *fakeSender
	caps client.Capabilities // capabilities returned from CreateSession (zero = all-false)

	// sessionReady closes when CreateSession has returned the id to the ui command
	// goroutine — a deterministic, output-independent signal that the SessionReadyMsg
	// is on its way to the reducer (so a follow-up prompt won't be dropped by the
	// sessionID == "" guard in submitPrompt). See fakeRecver's doc for why the
	// teatest cases sequence on signals like this rather than on rendered output.
	sessionReady chan struct{}

	// recvers, when non-nil, makes OpenConverse hand a FRESH scripted fakeRecver per
	// call, round-robin over this slice (the last entry repeats once exhausted). The
	// queue-drain e2e needs this because each submitPrompt — the manual prompt AND the
	// auto-drained follow-up — opens a NEW Converse stream (one stream per prompt, as
	// in production), so a single shared recv would replay run 1's script into run 2.
	// All recvers share the one send (the sender just records frames). When recvers is
	// nil OpenConverse falls back to the single shared recv (the original behaviour the
	// approval cases rely on). recvIdx tracks the round-robin position.
	recvers []*fakeRecver
	recvIdx int

	// runCancelled, when non-nil, is closed the first time a run context handed to
	// OpenConverse is cancelled — the deterministic, output-flush-independent signal
	// that submitPrompt's per-run cancelRun fired. The double-ctrl+c quit path calls
	// cancelRun directly (it does NOT send a Cancel frame), so this ctx observation —
	// not a GetCancel() frame — is how a test proves that wiring end-to-end. A
	// background goroutine watches the run ctx; cancelOnce guards the one-shot close.
	runCancelled chan struct{}
	cancelOnce   sync.Once

	// createdSel records the model selection the LAST CreateSession carried — the
	// /models e2e asserts the picked (provider, model) threads into the create. created
	// is closed once (createdOnce) on the first create so a test can sequence on the
	// create having happened without polling rendered output.
	createdSel  client.ModelSelection
	createdWksp string // workspace the LAST CreateSession(InWorkspace) carried
	mode        string
	setModeErr  error
	setModeSeen []string
	created     chan struct{}
	createdOnce sync.Once
	// resolvedModel is the EFFECTIVE model the fake's create response echoes back —
	// the header e2e asserts it lands in m.effectiveModel and renders from turn zero.
	resolvedModel client.ResolvedModel
	// echoSelAsResolved, when true, makes CreateSession echo the REQUESTED selector
	// back as the resolved model (so the restart-now handoff e2e sees the new effective
	// model match the picked one). A zero selector still echoes the canned
	// resolvedModel. createCount counts CreateSession calls so the restart-now id
	// differs from the first session's; mu guards the recorders touched by the command
	// goroutine + the test goroutine. closedIDs records CloseSession arguments.
	echoSelAsResolved bool
	createCount       int
	closedIDs         []string
	mu                sync.Mutex
	// recreated, when non-nil, is closed on the SECOND CreateSession (the restart-now
	// handoff's re-create) — the deterministic signal a teatest sequences the model
	// switch on, output-independent. createdOnce guards `created`; a dedicated Once
	// guards this.
	recreated     chan struct{}
	recreatedOnce sync.Once
	// secondCreateErr, when non-nil, is returned ONLY by the SECOND CreateSession (the
	// restart-now re-create) — the first (startup connect) still succeeds. Drives the
	// restart-create-failure recovery test. recreated still closes (the attempt fired).
	secondCreateErr error
	// createErr, when non-nil, is returned by EVERY CreateSession (a persistent
	// transient failure) — drives the retry-re-failure-stays-recoverable test, where
	// the same condition that failed the first re-create is still present on the retry.
	createErr error
	// rejectSelector, when non-nil, is returned by CreateSession ONLY when the
	// carried selection is non-zero — modelling a server that fails the named
	// provider/model while a zero-selection (server default) create proceeds (to
	// success, or to createErr when that is ALSO set, letting a test give the two
	// legs DISTINCT errors). Checked BEFORE createErr. A gRPC InvalidArgument
	// status here models a REJECTION (the issue #41 fallback leg: createSessionCmd
	// retries once with the zero selection); any other error models a transient
	// selector-leg failure (no retry — the unchanged fatal path).
	rejectSelector error
	// getSessionResults scripts the GetSession refetch (issue #66 footer heal):
	// successive calls return successive entries, the last repeating once exhausted.
	// Empty ⇒ GetSession returns the canned resolvedModel. getSessionErr, when
	// non-nil, makes GetSession fail (the benign-error path). getSessionCount counts
	// calls (guarded by mu) so a test can assert the refetch fired (or did NOT).
	getSessionResults []client.ResolvedModel
	getSessionErr     error
	getSessionCount   int
}

// GetSession scripts the footer-heal refetch. Successive calls walk
// getSessionResults (last entry repeats); empty falls back to the canned
// resolvedModel. getSessionErr forces the benign-error path. mu guards the
// recorders touched by the command goroutine + the test goroutine.
func (c *fakeConv) GetSession(_ context.Context, _ string) (client.SessionSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.getSessionCount++
	if c.getSessionErr != nil {
		return client.SessionSnapshot{}, c.getSessionErr
	}
	resolved := c.resolvedModel
	if len(c.getSessionResults) > 0 {
		i := c.getSessionCount - 1
		if i >= len(c.getSessionResults) {
			i = len(c.getSessionResults) - 1
		}
		resolved = c.getSessionResults[i]
	}
	mode := c.mode
	if mode == "" {
		mode = client.ModeDefaultString
	}
	return client.SessionSnapshot{Mode: mode, ResolvedModel: resolved}, nil
}

func (c *fakeConv) SetMode(_ context.Context, _ string, mode string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setModeSeen = append(c.setModeSeen, mode)
	if c.setModeErr != nil {
		return "", c.setModeErr
	}
	c.mode = mode
	return c.mode, nil
}

func (c *fakeConv) setModes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.setModeSeen...)
}

// getSessionCalls returns how many times GetSession was invoked (test-goroutine read).
func (c *fakeConv) getSessionCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.getSessionCount
}

func (c *fakeConv) CreateSession(ctx context.Context, sel client.ModelSelection, mode string) (string, client.Capabilities, client.ResolvedModel, error) {
	return c.CreateSessionInWorkspace(ctx, "", sel, mode)
}

// CreateSessionInWorkspace is the /worktrees switch path (issue #102): it
// records the carried workspace so a test can assert the picked worktree
// threaded into the create, then delegates to the shared create body.
func (c *fakeConv) CreateSessionInWorkspace(_ context.Context, workspace string, sel client.ModelSelection, mode string) (string, client.Capabilities, client.ResolvedModel, error) {
	c.mu.Lock()
	c.createdSel = sel
	c.createdWksp = workspace
	if mode != "" {
		c.mode = mode
	}
	c.createCount++
	n := c.createCount
	c.mu.Unlock()
	if c.created != nil {
		c.createdOnce.Do(func() { close(c.created) })
	}
	if n >= 2 && c.recreated != nil {
		c.recreatedOnce.Do(func() { close(c.recreated) })
	}
	if c.sessionReady != nil {
		select {
		case <-c.sessionReady:
		default:
			close(c.sessionReady)
		}
	}
	// A selector rejection fails only a NON-ZERO selection (the issue #41
	// server-rejection path). Checked FIRST so a test can pair it with createErr
	// and give the selector create and the zero-selection retry DISTINCT errors.
	if c.rejectSelector != nil && !sel.IsZero() {
		return "", client.Capabilities{}, client.ResolvedModel{}, c.rejectSelector
	}
	// A persistent create error fails EVERY (remaining) call — the retry-re-failure
	// path; with rejectSelector also set, this is the zero-selection retry's error.
	if c.createErr != nil {
		return "", client.Capabilities{}, client.ResolvedModel{}, c.createErr
	}
	// The SECOND create (restart-now re-create) can be forced to fail, leaving the
	// first (startup connect) succeeding — exercising the recoverable failure path.
	if n >= 2 && c.secondCreateErr != nil {
		return "", client.Capabilities{}, client.ResolvedModel{}, c.secondCreateErr
	}
	// The first session keeps the historical id; a re-create (restart-now) gets a
	// distinct id so the handoff e2e can prove the session was rebound.
	id := "sess-test-0001"
	if n > 1 {
		id = "sess-test-000" + strconv.Itoa(n)
	}
	resolved := c.resolvedModel
	if c.echoSelAsResolved && !sel.IsZero() {
		resolved = client.ResolvedModel{ProviderID: sel.ProviderID, ModelID: sel.ModelID}
	}
	if c.mode == "" {
		c.mode = client.ModeDefaultString
	}
	return id, c.caps, resolved, nil
}

// CloseSession records the id closed (restart-now closes the old session first).
func (c *fakeConv) CloseSession(_ context.Context, id string) error {
	c.mu.Lock()
	c.closedIDs = append(c.closedIDs, id)
	c.mu.Unlock()
	return nil
}

// closed returns a copy of the recorded CloseSession ids (test-goroutine read).
func (c *fakeConv) closed() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.closedIDs...)
}

func (c *fakeConv) OpenConverse(ctx context.Context) (*client.Stream, error) {
	// Observe the per-run context: when it is cancelled (the double-ctrl+c quit calls
	// cancelRun, or endRun cancels), close runCancelled once. This is how the program
	// test proves cancelRun fired without inspecting unexported model fields.
	if c.runCancelled != nil {
		go func() {
			<-ctx.Done()
			c.cancelOnce.Do(func() { close(c.runCancelled) })
		}()
	}
	if len(c.recvers) > 0 {
		i := c.recvIdx
		if i >= len(c.recvers) {
			i = len(c.recvers) - 1 // repeat the last script once exhausted
		}
		c.recvIdx++
		return client.NewStream(c.recvers[i], c.send), nil
	}
	return client.NewStream(c.recv, c.send), nil
}

// fakeClipboard is a scripted client.Clipboard for the ctrl+v tests: Read returns
// its canned mime/data or err, and records how many times it was called so the
// cap-gated "image never read" assertion can be made (actually the gate is decided
// at result time, so calls counts that Read WAS invoked). It implements
// client.Clipboard so the ui's clipboard path runs with no subprocess.
type fakeClipboard struct {
	mime  string
	data  []byte
	err   error
	calls int
	// seq, when non-nil, returns a DIFFERENT data slice per successive Read (the
	// i-th call returns seq[i], the last repeats once exhausted). Lets a test stage
	// byte-distinguishable images across multiple ctrl+v presses so the submit-time
	// part ordering is assertable. mime still applies to every read.
	seq [][]byte

	// wrote records the payloads passed to Write (the best-effort shell-clipboard
	// copy fallback) so the in-app text-selection copy tests can assert the shell
	// path was invoked with the exact payload. writeErr, when set, is returned by
	// Write to model a missing/failed backend (which the UI must swallow).
	wrote    [][]byte
	writeErr error

	// primary/primaryErr script ReadPrimary (the middle-click primary-selection
	// read); primaryCalls counts invocations so the request tests can assert the
	// shell read actually ran (or was gated off).
	primary      string
	primaryErr   error
	primaryCalls int
}

func (f *fakeClipboard) Read(_ context.Context) (string, []byte, error) {
	i := f.calls
	f.calls++
	if f.err != nil {
		return "", nil, f.err
	}
	if len(f.seq) > 0 {
		if i >= len(f.seq) {
			i = len(f.seq) - 1
		}
		return f.mime, f.seq[i], nil
	}
	return f.mime, f.data, nil
}

// ReadPrimary returns the scripted primary-selection text or error, counting
// calls — the offline stand-in for the wl-paste --primary / xclip -selection
// primary subprocess.
func (f *fakeClipboard) ReadPrimary(_ context.Context) (string, error) {
	f.primaryCalls++
	if f.primaryErr != nil {
		return "", f.primaryErr
	}
	return f.primary, nil
}

// Write records the payload (so the copy tests can assert the shell-clipboard
// fallback was invoked) and returns writeErr, modelling the best-effort backend.
func (f *fakeClipboard) Write(_ context.Context, _ string, data []byte) error {
	f.wrote = append(f.wrote, append([]byte(nil), data...))
	return f.writeErr
}

// fakeMCP is a scripted client.MCP for the overlay tests: each method returns its
// canned data or a canned error. err, when set, is returned by every call so the
// overlay's classified-error rendering can be exercised. It implements client.MCP
// so the ui's MCP commands run with no proto and no network.
type fakeMCP struct {
	resources []client.MCPResource
	contents  []client.MCPResourceContents
	prompts   []client.MCPPrompt
	promptDsc string
	promptMsg []client.MCPPromptMessage
	sources   []client.MCPSource
	groups    []string

	err error // when non-nil, every RPC returns it (already a gRPC status)

	getPromptCalls int // how many times GetMCPPrompt was invoked (validation guard)

	// nextSources, when non-nil, is returned by the SECOND (and later)
	// ListMCPSources call — modelling a server whose live MCP status changed since
	// the first fetch, so a panel refresh can be asserted to pick it up. Likewise
	// nextGroups for ListToolHiveGroups. sourcesCalls counts ListMCPSources calls.
	nextSources  []client.MCPSource
	nextGroups   []string
	sourcesCalls int
}

func (f *fakeMCP) ListMCPResources(_ context.Context, _ string) ([]client.MCPResource, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.resources, nil
}

func (f *fakeMCP) ReadMCPResource(_ context.Context, _, _ string) ([]client.MCPResourceContents, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.contents, nil
}

func (f *fakeMCP) ListMCPPrompts(_ context.Context, _ string) ([]client.MCPPrompt, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.prompts, nil
}

func (f *fakeMCP) GetMCPPrompt(_ context.Context, _, _ string, _ map[string]string) (string, []client.MCPPromptMessage, error) {
	f.getPromptCalls++
	if f.err != nil {
		return "", nil, f.err
	}
	return f.promptDsc, f.promptMsg, nil
}

func (f *fakeMCP) ListMCPSources(_ context.Context) ([]client.MCPSource, error) {
	f.sourcesCalls++
	if f.err != nil {
		return nil, f.err
	}
	if f.sourcesCalls > 1 && f.nextSources != nil {
		return f.nextSources, nil
	}
	return f.sources, nil
}

func (f *fakeMCP) ListToolHiveGroups(_ context.Context) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.sourcesCalls > 1 && f.nextGroups != nil {
		return f.nextGroups, nil
	}
	return f.groups, nil
}

// fakeSkills is a scripted client.SkillLister for the /skills panel tests:
// ListSkills returns the canned skills slice, or err when set. calls counts the
// invocations so a test can assert the RPC fired. It implements client.SkillLister
// so the ui's skills path runs with no proto and no network.
type fakeSkills struct {
	skills []client.Skill
	err    error
	calls  int
}

func (f *fakeSkills) ListSkills(_ context.Context) ([]client.Skill, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.skills, nil
}

// fakeAgents is a scripted client.AgentLister for the /agents inventory panel
// tests: ListAgents returns the canned agents slice, or err when set. calls
// counts the invocations so a test can assert the RPC fired. It implements
// client.AgentLister so the ui's /agents path runs with no proto and no network.
type fakeAgents struct {
	agents []client.Agent
	err    error
	calls  int
}

func (f *fakeAgents) ListAgents(_ context.Context) ([]client.Agent, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.agents, nil
}

// fakeSoul is a scripted client.SoulFetcher for the /soul inspection panel tests:
// GetSoul returns the canned soul, or err when set. It implements
// client.SoulFetcher so the ui's /soul path runs with no proto and no network.
type fakeSoul struct {
	soul  client.Soul
	err   error
	calls int
}

func (f *fakeSoul) GetSoul(_ context.Context) (client.Soul, error) {
	f.calls++
	if f.err != nil {
		return client.Soul{}, f.err
	}
	return f.soul, nil
}

// fakeUserModel is a scripted client.UserModelLister for the /usermodel panel
// tests: GetUserModel returns the canned model, or err when set.
type fakeUserModel struct {
	model client.UserModel
	err   error
	calls int
}

func (f *fakeUserModel) GetUserModel(_ context.Context) (client.UserModel, error) {
	f.calls++
	if f.err != nil {
		return client.UserModel{}, f.err
	}
	return f.model, nil
}

// fakeModels is a scripted client.ModelLister for the /models picker tests:
// ListModels returns the canned list (+ statuses), or err when set.
type fakeModels struct {
	models   []client.ModelInfo
	statuses []client.ProviderStatus
	err      error
	calls    int
}

func (f *fakeModels) ListModels(_ context.Context) ([]client.ModelInfo, []client.ProviderStatus, error) {
	f.calls++
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.models, f.statuses, nil
}

// fakeStore is a spy client SelectionStore (ui.SelectionStore) for the /models
// tests: it records the last Save / SaveGlobalDefault and can be made to fail.
type fakeStore struct {
	lastWS        string
	lastSel       client.ModelSelection
	saves         int
	err           error
	lastGlobalSel client.ModelSelection
	globalSaves   int
}

func (s *fakeStore) Save(ws string, sel client.ModelSelection) error {
	s.saves++
	s.lastWS = ws
	s.lastSel = sel
	return s.err
}

func (s *fakeStore) SaveGlobalDefault(sel client.ModelSelection) error {
	s.globalSaves++
	s.lastGlobalSel = sel
	return s.err
}
