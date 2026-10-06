package ui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	teakey "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	customization "github.com/stacklok/mecatl/cmd/mecatui/customization"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
)

// The multi-session window (issue #2058, ADR 0374 plan Scenario 4) is the Bubble
// Tea root. It holds one ordinary per-session Model per window session and
// routes every message to exactly one of them:
//
//   - Commands a session returns are re-tagged with its key (tagCmd), including
//     the children of tea.Batch and tea.Sequence results, so a session's
//     stream reads, ticks, and RPC replies always come back to that session —
//     background sessions keep draining their Converse streams.
//   - Bubble Tea program commands (clipboard, raw output, exec, suspend, …) pass
//     through untagged. A session's tea.Quit is intercepted as a window intent.
//   - Input (keys, mouse, paste, focus) and tea.ResumeMsg reach the active
//     session only; window size and terminal theme/capability messages reach
//     every session. Any other untagged message is dropped (fail closed).
//
// The terminal title and status-line source are wrapped per session and
// forward only while that session is active. The lifecycle hook has one
// window-wide busy period (windowBusy) and forwards approvals for any session.
//
// From the list (Scenarios 5 and 6), n and w start a session through the normal
// create flow — w with the ADR 0374 NewWorktree intent, offered only when the
// server advertises create_worktrees — and d deletes a row with stop_active,
// optionally removing its server-created worktree when the inventory row
// allows it. A server without create_worktrees predates stop_active, so a
// refused delete there cancels the session's own run and retries a plain
// delete. A deleted row closes only its own session model.

// Window session row statuses.
const (
	windowStatusRunning       = "running"
	windowStatusNeedsApproval = "needs approval"
	windowStatusFailed        = "failed"
	windowStatusIdle          = "idle"
)

var errWorktreeCreateUnavailable = errors.New("server-created worktrees are unavailable")

// requestSessionCreator is the optional SessionCreator capability that carries a
// full client.CreateSessionRequest (the ADR 0374 NewWorktree intent).
type requestSessionCreator interface {
	CreateSessionWith(ctx context.Context, req client.CreateSessionRequest) (string, client.Capabilities, client.ResolvedModel, error)
}

// windowScope identifies one window session to its own Model and to the
// per-session hook wrappers.
type windowScope struct {
	key    int
	active *atomic.Int64 // the window's active session key
	busy   *windowBusy   // the lifecycle hook's window-wide busy period
	cancel context.CancelFunc
}

func (s *windowScope) isActive() bool { return s.active.Load() == int64(s.key) }

// scopedStatusSource lets only the active session write the shared status-line
// source. The window root owns the one Changed listener, and a session never
// closes the shared source.
type scopedStatusSource struct {
	inner customization.Source
	scope *windowScope
}

func (s scopedStatusSource) Submit(in customization.Input) {
	if s.scope.isActive() {
		s.inner.Submit(in)
	}
}
func (scopedStatusSource) Changed() <-chan struct{}       { return nil }
func (s scopedStatusSource) Latest() customization.Result { return s.inner.Latest() }
func (scopedStatusSource) Close(context.Context) error    { return nil }

func unwrapStatusSource(source customization.Source) customization.Source {
	if scoped, ok := source.(scopedStatusSource); ok {
		return scoped.inner
	}
	return source
}

// setStatusCommandCWD and clearStatusCommandCWD apply the direct command's local
// root only for the active session.
func setStatusCommandCWD(source customization.Source, cwd string) {
	if scoped, ok := source.(scopedStatusSource); ok {
		if !scoped.scope.isActive() {
			return
		}
		source = scoped.inner
	}
	customization.SetCommandCWD(source, cwd)
}

func clearStatusCommandCWD(source customization.Source) {
	if scoped, ok := source.(scopedStatusSource); ok {
		if !scoped.scope.isActive() {
			return
		}
		source = scoped.inner
	}
	customization.ClearCommandCWD(source)
}

// windowBusy is the host lifecycle hook's single busy period, shared by every
// window session: it opens when the first session becomes busy and closes when
// the last busy session ends, so overlapping runs never end it early.
type windowBusy struct {
	mu      sync.Mutex
	busy    map[int]bool
	failed  bool   // a session failed during this busy period
	message string // the first failure's preview
}

// begin marks key busy and reports whether that opened the busy period.
func (b *windowBusy) begin(key int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.busy[key] {
		return false
	}
	opened := len(b.busy) == 0
	if opened {
		b.failed, b.message = false, ""
	}
	if b.busy == nil {
		b.busy = map[int]bool{}
	}
	b.busy[key] = true
	return opened
}

// end marks key idle. closed reports that it was the last busy session; the
// period then reports failed if any session failed during it, with the first
// failure's preview, else the last session's preview.
func (b *windowBusy) end(key int, failed bool, message string) (closed, periodFailed bool, preview string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.busy[key] {
		return false, false, ""
	}
	delete(b.busy, key)
	if failed && !b.failed {
		b.failed, b.message = true, message
	}
	if len(b.busy) > 0 {
		return false, false, ""
	}
	if b.failed {
		return true, true, b.message
	}
	return true, false, message
}

// scopedLifecycle maps every window session onto the host hook's one busy
// period (windowBusy). Approval notifications forward for any session: a
// background approval is exactly when the host should notify.
type scopedLifecycle struct {
	inner LifecycleNotifier
	scope *windowScope
}

func (h scopedLifecycle) Start(ctx context.Context, sessionID string) {
	if h.scope.busy.begin(h.scope.key) {
		h.inner.Start(ctx, sessionID)
	}
}

func (h scopedLifecycle) PermissionRequest(ctx context.Context, sessionID, message string) {
	h.inner.PermissionRequest(ctx, sessionID, message)
}

func (h scopedLifecycle) PermissionResult(ctx context.Context, sessionID string) {
	h.inner.PermissionResult(ctx, sessionID)
}

func (h scopedLifecycle) Stop(ctx context.Context, sessionID string, failed bool, message string) {
	if closed, periodFailed, preview := h.scope.busy.end(h.scope.key, failed, message); closed {
		h.inner.Stop(ctx, sessionID, periodFailed, preview)
	}
}

// newSessionModel builds one window session's Model. deps.window carries the
// session's scope; the model gets its own cancellable child of deps.Ctx and
// per-session wrappers for the side-effecting hooks.
func newSessionModel(deps Deps, open sessionOpen) Model {
	if deps.Ctx == nil {
		deps.Ctx = context.Background()
	}
	if scope := deps.window; scope != nil {
		deps.Ctx, scope.cancel = context.WithCancel(deps.Ctx)
		if deps.StatusSource != nil {
			deps.StatusSource = scopedStatusSource{inner: deps.StatusSource, scope: scope}
		}
		if title := deps.TerminalTitle; title != nil {
			deps.TerminalTitle = func(in customization.Input) {
				if scope.isActive() {
					title(in)
				}
			}
		}
		if deps.AgentHook != nil {
			deps.AgentHook = scopedLifecycle{inner: deps.AgentHook, scope: scope}
		}
	}
	if open.attachID != "" || open.create != nil {
		// Startup inputs belong to the launch session only.
		deps.Resume = nil
		deps.BrowseSessions = false
		deps.ConnectOpen = false
		deps.InitialPrompt = ""
		deps.StartupProgress = nil
		deps.ProbeKeyboardCapability = false
	}
	if req := open.create; req != nil {
		if req.Mode != "" {
			deps.Mode = req.Mode
		}
		if !req.Selection.IsZero() {
			deps.InitialModel = req.Selection
		}
	}
	m := New(deps)
	if open.create != nil {
		m.createNewWorktree = open.create.NewWorktree
	}
	if open.attachID != "" {
		m.phase = phaseIdle
	}
	return m
}

// windowSessionMsg is a message produced by a session's command, tagged with the
// session it belongs to.
type windowSessionMsg struct {
	key int
	msg tea.Msg
}

// windowSessionQuitMsg is a session's raw tea.Quit, intercepted by the window.
type windowSessionQuitMsg struct{}

// windowQuitRequestMsg is a user quit from a window session (Model.quitNow).
type windowQuitRequestMsg struct{}

// windowOpenSavedMsg hands a chat opened from /sessions to the window root.
type windowOpenSavedMsg struct {
	row        client.SessionListItem
	transcript conversation
	snapshot   client.SessionSnapshot
}

// windowStatusMsg is the window's single status-source listener firing.
type windowStatusMsg struct{ line customization.Result }

var (
	teaPackagePath = reflect.TypeOf(tea.QuitMsg{}).PkgPath()
	teaCmdType     = reflect.TypeOf(tea.Cmd(nil))
)

// teaCommandList reports whether msg is a Bubble Tea command list: tea.BatchMsg
// (concurrent) or the unexported sequence message (sequential).
func teaCommandList(msg tea.Msg) ([]tea.Cmd, bool, bool) {
	if batch, ok := msg.(tea.BatchMsg); ok {
		return batch, false, true
	}
	v := reflect.ValueOf(msg)
	if !v.IsValid() || v.Kind() != reflect.Slice || v.Type().Elem() != teaCmdType || v.Type().PkgPath() != teaPackagePath {
		return nil, false, false
	}
	cmds := make([]tea.Cmd, v.Len())
	for i := range cmds {
		cmds[i], _ = v.Index(i).Interface().(tea.Cmd)
	}
	return cmds, true, true
}

func tagCmd(key int, cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg { return tagMsg(key, cmd()) }
}

func tagMsg(key int, msg tea.Msg) tea.Msg {
	if msg == nil {
		return nil
	}
	if _, ok := msg.(tea.QuitMsg); ok {
		return windowSessionMsg{key: key, msg: windowSessionQuitMsg{}}
	}
	if cmds, _, ok := teaCommandList(msg); ok {
		v := reflect.ValueOf(msg)
		out := reflect.MakeSlice(v.Type(), len(cmds), len(cmds))
		for i, cmd := range cmds {
			if tagged := tagCmd(key, cmd); tagged != nil {
				out.Index(i).Set(reflect.ValueOf(tagged))
			}
		}
		return out.Interface()
	}
	if reflect.TypeOf(msg).PkgPath() == teaPackagePath {
		return msg // a Bubble Tea program command
	}
	return windowSessionMsg{key: key, msg: msg}
}

// windowInputMsg reports user input, which reaches the active session only.
func windowInputMsg(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.KeyReleaseMsg, tea.PasteMsg, tea.PasteStartMsg, tea.PasteEndMsg, tea.ClipboardMsg,
		tea.MouseClickMsg, tea.MouseReleaseMsg, tea.MouseWheelMsg, tea.MouseMotionMsg, tea.FocusMsg, tea.BlurMsg:
		return true
	}
	return false
}

// windowBroadcastMsg reports terminal facts every session must see.
func windowBroadcastMsg(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.WindowSizeMsg, tea.BackgroundColorMsg, tea.ColorProfileMsg, tea.KeyboardEnhancementsMsg:
		return true
	}
	return false
}

type windowSession struct {
	key   int
	model Model
	// pending marks a session started from the list (n/w) whose CreateSession has
	// not answered yet; a creation error removes it and returns to origin.
	pending bool
	origin  int
	// updatedAt is when the session's status or transcript last changed (the
	// list's Updated column); activity is the fingerprint compared to detect it.
	updatedAt time.Time
	activity  windowActivity
}

type windowListState struct {
	cursor int    // index into the rows the filter shows
	filter string // "" shows every row; otherwise one windowStatus value
	notice string
}

type windowQuitConfirm struct{ others int }

// windowDeleteConfirm is the open delete confirmation for one list row.
// checking is true while the row's remove_worktree capability is fetched.
type windowDeleteConfirm struct {
	key            int
	id             string
	label          string
	checking       bool
	removeWorktree bool // the row may also remove its server-created worktree
	busy           bool // DeleteSession is in flight
	stopping       bool // old-server fallback: waiting for the cancelled run to end
}

type window struct {
	base          Deps
	keys          keyMap
	sessions      []windowSession
	activeKey     int
	nextKey       int
	activeCell    *atomic.Int64
	busy          *windowBusy
	list          *windowListState
	quitConfirm   *windowQuitConfirm
	deleteConfirm *windowDeleteConfirm

	// Terminal facts replayed to sessions created later.
	lastSize      *tea.WindowSizeMsg
	lastKeyboard  *tea.KeyboardEnhancementsMsg
	lastProfile   *tea.ColorProfileMsg
	sawBackground bool

	// now is the list's clock; nil means time.Now (tests pin it).
	now func() time.Time
}

// NewWindow builds the multi-session program root around the launch session.
func NewWindow(deps Deps) tea.Model {
	if deps.Ctx == nil {
		deps.Ctx = context.Background()
	}
	w := window{
		base:       deps,
		keys:       applyKeyOverrides(defaultKeys(), deps.KeyOverrides),
		nextKey:    1,
		activeCell: new(atomic.Int64),
		busy:       new(windowBusy),
	}
	key, m := w.buildSession(sessionOpen{})
	w.sessions = []windowSession{{key: key, model: m, updatedAt: w.clock(), activity: windowActivityOf(m)}}
	w.activeKey = key
	w.activeCell.Store(int64(key))
	return w
}

func (w *window) buildSession(open sessionOpen) (int, Model) {
	key := w.nextKey
	w.nextKey++
	deps := w.base
	deps.window = &windowScope{key: key, active: w.activeCell, busy: w.busy}
	if len(w.sessions) > 0 {
		deps.Theme = w.activeModel().deps.Theme
		deps.ThemeAutoDetect = w.base.ThemeAutoDetect && !w.sawBackground
	}
	return key, newSessionModel(deps, open)
}

func (w window) index(key int) int {
	for i, s := range w.sessions {
		if s.key == key {
			return i
		}
	}
	return -1
}

func (w window) modelFor(key int) (Model, bool) {
	if i := w.index(key); i >= 0 {
		return w.sessions[i].model, true
	}
	return Model{}, false
}

func (w window) activeModel() Model {
	m, _ := w.modelFor(w.activeKey)
	return m
}

// ActiveSessionID and ConnectRestartIntent expose the active session to the
// composition root after the program exits.
func (w window) ActiveSessionID() string { return w.activeModel().ActiveSessionID() }

// ActiveSessionMsg addresses msg to the active session as if that session's
// own command had produced it. The window drops untagged session results
// (fail closed), so a composition test that fabricates one delivers it this way.
func (w window) ActiveSessionMsg(msg tea.Msg) tea.Msg {
	return windowSessionMsg{key: w.activeKey, msg: msg}
}

// ConnectRestartIntent reports the active session's /connect restart request.
func (w window) ConnectRestartIntent() (ConnectRestartIntent, bool) {
	return w.activeModel().ConnectRestartIntent()
}

func (w window) Init() tea.Cmd {
	return tea.Batch(tagCmd(w.activeKey, w.activeModel().Init()), w.statusWaitCmd())
}

func (w window) statusWaitCmd() tea.Cmd {
	source, ctx := w.base.StatusSource, w.base.Ctx
	if source == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-source.Changed():
			if !ok {
				return nil
			}
			return windowStatusMsg{line: source.Latest()}
		}
	}
}

func (w window) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case windowSessionMsg:
		return w.onSessionMsg(msg)
	case windowStatusMsg:
		w, cmd := w.updateSession(w.activeKey, statusLineChangedMsg(msg))
		return w, tea.Batch(cmd, w.statusWaitCmd())
	case tea.KeyPressMsg:
		return w.onKey(msg)
	case windowDeleteCapsMsg:
		return w.onDeleteCaps(msg)
	case windowDeletedMsg:
		return w.onDeleted(msg)
	}
	if windowBroadcastMsg(msg) {
		return w.broadcast(msg)
	}
	if windowInputMsg(msg) {
		if w.list != nil || w.quitConfirm != nil {
			return w, nil // the window overlay owns input
		}
		return w.updateSession(w.activeKey, msg)
	}
	if _, ok := msg.(tea.ResumeMsg); ok {
		return w.updateSession(w.activeKey, msg) // only the active session can suspend
	}
	// Fail closed. Every message a session's command produces arrives tagged
	// (tagCmd); Bubble Tea's own program messages (clipboard, raw output,
	// suspend, …) have already been acted on by the program and need no session.
	// Anything else may be a background session's result whose tagging was lost
	// — for example, a Bubble Tea upgrade changing the command-list type tagMsg
	// rewrites — so applying it to the active session would cross sessions. It is
	// dropped; the ui has no diagnostics port to report it.
	return w, nil
}

func (w window) updateSession(key int, msg tea.Msg) (window, tea.Cmd) {
	i := w.index(key)
	if i < 0 {
		return w, nil
	}
	updated, cmd := w.sessions[i].model.Update(msg)
	return w.withModel(i, updated), tagCmd(key, cmd)
}

// withModel stores a session's updated Model at index i on a copy of the
// session slice, so earlier window values never observe the change. A change in
// the session's status or transcript also moves its Updated time.
func (w window) withModel(i int, updated tea.Model) window {
	if m, ok := updated.(Model); ok {
		w.sessions = slices.Clone(w.sessions)
		w.sessions[i].model = m
		if activity := windowActivityOf(m); activity != w.sessions[i].activity {
			w.sessions[i].activity = activity
			w.sessions[i].updatedAt = w.clock()
		}
	}
	return w
}

func (w window) broadcast(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.lastSize = &msg
	case tea.KeyboardEnhancementsMsg:
		w.lastKeyboard = &msg
	case tea.ColorProfileMsg:
		w.lastProfile = &msg
	case tea.BackgroundColorMsg:
		w.sawBackground = true
	}
	cmds := make([]tea.Cmd, 0, len(w.sessions))
	for _, s := range w.sessions {
		var cmd tea.Cmd
		w, cmd = w.updateSession(s.key, msg)
		cmds = append(cmds, cmd)
	}
	return w, tea.Batch(cmds...)
}

func (w window) onSessionMsg(msg windowSessionMsg) (tea.Model, tea.Cmd) {
	switch inner := msg.msg.(type) {
	case windowQuitRequestMsg:
		if msg.key != w.activeKey {
			return w, nil
		}
		return w.requestQuit()
	case windowSessionQuitMsg:
		if msg.key != w.activeKey {
			return w, nil
		}
		return w.quitAll()
	case windowOpenSavedMsg:
		return w.openSaved(inner)
	case client.ConnectErrMsg:
		if i := w.index(msg.key); i >= 0 && w.sessions[i].pending {
			return w.failCreate(msg.key, inner.Err)
		}
	}
	w, cmd := w.updateSession(msg.key, msg.msg)
	if i := w.index(msg.key); i >= 0 && w.sessions[i].pending && w.sessions[i].model.sessionID != "" {
		w.sessions[i].pending = false
	}
	if c := w.deleteConfirm; c != nil && c.stopping && c.key == msg.key && !windowStatusActive(w.rowStatus(msg.key)) {
		next := *c
		next.stopping = false
		w.deleteConfirm = &next
		cmd = tea.Batch(cmd, w.deleteCmd(next, client.DeleteSessionOptions{}, false))
	}
	return w, cmd
}

func (w window) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if w.quitConfirm != nil {
		return w.onQuitConfirmKey(msg)
	}
	if w.deleteConfirm != nil {
		return w.onDeleteConfirmKey(msg)
	}
	if w.list != nil {
		return w.onListKey(msg)
	}
	if teakey.Matches(msg, w.keys.Sessions) && w.activeModel().windowSessionsKeyLive() {
		w.list = w.listAt(w.activeKey, "")
		return w, nil
	}
	return w.updateSession(w.activeKey, msg)
}

func (w window) onListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	list := *w.list
	w.list = &list
	rows := w.visibleRows()
	selected := func() (windowRow, bool) {
		if list.cursor >= 0 && list.cursor < len(rows) {
			return rows[list.cursor], true
		}
		return windowRow{}, false
	}
	switch {
	case teakey.Matches(msg, w.keys.Close), teakey.Matches(msg, w.keys.Sessions):
		w.list = nil
	case teakey.Matches(msg, w.keys.Up):
		list.cursor = max(0, list.cursor-1)
	case teakey.Matches(msg, w.keys.Down):
		list.cursor = max(0, min(len(rows)-1, list.cursor+1))
	case teakey.Matches(msg, w.keys.NextTab):
		w.cycleFilter(1)
	case msg.String() == "shift+tab", teakey.Matches(msg, w.keys.ModeSwitch):
		w.cycleFilter(-1)
	case teakey.Matches(msg, w.keys.Choose):
		if row, ok := selected(); ok {
			return w.switchTo(row.key)
		}
	case msg.Text == "o":
		return w.openSavedPicker()
	case msg.Text == "n":
		return w.newSession(false)
	case msg.Text == "w":
		if w.activeModel().caps.CreateWorktrees {
			return w.newSession(true)
		}
	case msg.Text == "d":
		if row, ok := selected(); ok {
			return w.startDelete(row.key)
		}
	case teakey.Matches(msg, w.keys.Quit), teakey.Matches(msg, w.keys.QuitD), teakey.Matches(msg, w.keys.Suspend):
		w.list = nil
		return w.updateSession(w.activeKey, msg)
	}
	return w, nil
}

// cycleFilter moves the list to the next (step 1) or previous (step -1) status
// filter, keeping the selected session selected when the new filter shows it.
func (w *window) cycleFilter(step int) {
	rows := w.visibleRows()
	selectedKey := -1
	if w.list.cursor >= 0 && w.list.cursor < len(rows) {
		selectedKey = rows[w.list.cursor].key
	}
	at := max(0, slices.Index(windowFilters, w.list.filter))
	w.list.filter = windowFilters[(at+step+len(windowFilters))%len(windowFilters)]
	w.list.cursor = 0
	for i, row := range w.visibleRows() {
		if row.key == selectedKey {
			w.list.cursor = i
		}
	}
}

// listAt opens (or keeps) the list with key selected and notice shown. An open
// list keeps its filter; the selection falls back to the first row the filter
// shows.
func (w window) listAt(key int, notice string) *windowListState {
	next := windowListState{notice: notice}
	if w.list != nil {
		next.filter = w.list.filter
	}
	w.list = &next
	for i, row := range w.visibleRows() {
		if row.key == key {
			next.cursor = i
		}
	}
	return &next
}

// openSavedPicker opens the active session's /sessions picker from the list.
func (w window) openSavedPicker() (tea.Model, tea.Cmd) {
	active := w.activeModel()
	if active.deps.Sessions == nil || active.deps.Transcript == nil {
		w.list.notice = "saved sessions are unavailable on this server"
		return w, nil
	}
	if active.phase != phaseIdle {
		w.list.notice = "/sessions opens when this session is idle"
		return w, nil
	}
	w.list = nil
	updated, cmd := active.openSessions()
	return w.withModel(w.index(w.activeKey), updated), tagCmd(w.activeKey, cmd)
}

func (w window) onQuitConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Text == "y", teakey.Matches(msg, w.keys.Choose), teakey.Matches(msg, w.keys.Quit):
		return w.quitAll()
	case msg.Text == "n", teakey.Matches(msg, w.keys.Close):
		w.quitConfirm = nil
	}
	return w, nil
}

// switchTo makes key the active session and re-submits its status state.
func (w window) switchTo(key int) (tea.Model, tea.Cmd) {
	w.list = nil
	w.deleteConfirm = nil
	if w.index(key) < 0 {
		return w, nil
	}
	w.activeKey = key
	w.activeCell.Store(int64(key))
	active := w.activeModel()
	active.submitStatusLine()
	return w, tagCmd(key, active.refreshStatusContextCmd())
}

// requestQuit quits at once unless other sessions are running or awaiting
// approval, in which case it asks once how many will be cancelled.
func (w window) requestQuit() (tea.Model, tea.Cmd) {
	others := 0
	for _, s := range w.sessions {
		if s.key == w.activeKey {
			continue
		}
		if windowStatusActive(s.model.windowRowStatus()) {
			others++
		}
	}
	if others == 0 {
		return w.quitAll()
	}
	w.list = nil
	w.quitConfirm = &windowQuitConfirm{others: others}
	return w, nil
}

// quitAll ends every session's stream and exits; sessions stay saved server-side.
func (w window) quitAll() (tea.Model, tea.Cmd) {
	w.quitConfirm = nil
	w.list = nil
	w.deleteConfirm = nil
	w.sessions = slices.Clone(w.sessions)
	for i := range w.sessions {
		w.sessions[i].model = w.sessions[i].model.quitCleanup()
		if scope := w.sessions[i].model.deps.window; scope != nil && scope.cancel != nil {
			scope.cancel()
		}
	}
	return w, tea.Quit
}

// replayTerminal gives a new session the terminal facts the window has seen.
func (w window) replayTerminal(m Model) (Model, []tea.Cmd) {
	var msgs []tea.Msg
	if w.lastSize != nil {
		msgs = append(msgs, *w.lastSize)
	}
	if w.lastKeyboard != nil {
		msgs = append(msgs, *w.lastKeyboard)
	}
	if w.lastProfile != nil {
		msgs = append(msgs, *w.lastProfile)
	}
	var cmds []tea.Cmd
	for _, msg := range msgs {
		updated, cmd := m.Update(msg)
		if mm, ok := updated.(Model); ok {
			m = mm
		}
		cmds = append(cmds, cmd)
	}
	return m, cmds
}

// addSession adds a session to the window and makes it active.
func (w window) addSession(open sessionOpen) (window, tea.Cmd) {
	key, m := w.buildSession(open)
	m, cmds := w.replayTerminal(m)
	if open.attachID == "" {
		cmds = append(cmds, m.Init())
	}
	w.sessions = append(slices.Clone(w.sessions), windowSession{key: key, model: m, updatedAt: w.clock(), activity: windowActivityOf(m)})
	switched, switchCmd := w.switchTo(key)
	w, _ = switched.(window)
	return w, tea.Batch(tagCmd(key, tea.Batch(cmds...)), switchCmd)
}

// openSaved adds a chat opened from /sessions, or switches to it when it is
// already in the window.
func (w window) openSaved(open windowOpenSavedMsg) (tea.Model, tea.Cmd) {
	for _, s := range w.sessions {
		if s.model.sessionID == open.row.ID {
			return w.switchTo(s.key)
		}
	}
	w, cmd := w.addSession(sessionOpen{attachID: open.row.ID})
	i := w.index(w.activeKey)
	updated, adoptCmd, _ := w.sessions[i].model.adoptAuthoritativeTranscript(open.row, open.transcript, open.snapshot)
	return w.withModel(i, updated), tea.Batch(cmd, tagCmd(w.activeKey, adoptCmd))
}

// closeSession removes one session, ending only its own stream. The shared
// status source and every other session's stream are left untouched.
func (w window) closeSession(key int) (window, tea.Cmd) {
	i := w.index(key)
	if i < 0 {
		return w, nil
	}
	m := w.sessions[i].model.quitCleanup()
	if scope := m.deps.window; scope != nil {
		if closed, failed, preview := w.busy.end(key, false, ""); closed && w.base.AgentHook != nil {
			w.base.AgentHook.Stop(w.base.Ctx, m.sessionID, failed, preview)
		}
		if scope.cancel != nil {
			scope.cancel()
		}
	}
	w.sessions = slices.Delete(slices.Clone(w.sessions), i, i+1)
	if key != w.activeKey || len(w.sessions) == 0 {
		return w, nil
	}
	next := w.sessions[min(i, len(w.sessions)-1)].key
	switched, cmd := w.switchTo(next)
	w, _ = switched.(window)
	return w, cmd
}

// newSession starts a session from the list through the normal create flow,
// optionally in a fresh server-created worktree (ADR 0374), and switches to it.
func (w window) newSession(worktree bool) (tea.Model, tea.Cmd) {
	origin := w.activeKey
	w, cmd := w.addSession(sessionOpen{create: &client.CreateSessionRequest{NewWorktree: worktree}})
	i := w.index(w.activeKey)
	w.sessions[i].pending = true
	w.sessions[i].origin = origin
	return w, cmd
}

// failCreate removes a list-started session whose create failed, returns to
// the session it was started from, and shows the error in the list.
func (w window) failCreate(key int, err error) (tea.Model, tea.Cmd) {
	origin := w.sessions[w.index(key)].origin
	w, cmd := w.closeSession(key)
	if w.index(origin) >= 0 && origin != w.activeKey {
		switched, switchCmd := w.switchTo(origin)
		w, _ = switched.(window)
		cmd = tea.Batch(cmd, switchCmd)
	}
	notice := "could not create the session"
	if err != nil {
		notice += ": " + terminaltext.SanitizeSingleLine(err.Error())
	}
	w.list = w.listAt(w.activeKey, notice)
	return w, cmd
}

// windowDeleteCapsMsg reports whether a row may also remove its worktree.
type windowDeleteCapsMsg struct {
	key            int
	id             string
	removeWorktree bool
}

// windowDeletedMsg reports a DeleteSession result for one row.
type windowDeletedMsg struct {
	key            int
	label          string
	removeWorktree bool
	// fallback allows the old-server cancel-then-delete retry on refusal.
	fallback bool
	result   client.DeleteSessionResult
	err      error
}

// windowInventoryPageLimit bounds the inventory pages read to find one row's
// remove_worktree capability.
const windowInventoryPageLimit = 20

// startDelete opens the confirmation for key's row and fetches the row's
// remove_worktree capability from the session inventory.
func (w window) startDelete(key int) (tea.Model, tea.Cmd) {
	m, _ := w.modelFor(key)
	if m.sessionID == "" {
		w.list.notice = "this session is still starting"
		return w, nil
	}
	if w.base.SessionManagement == nil {
		w.list.notice = "deleting sessions is unavailable on this server"
		return w, nil
	}
	label := ""
	for _, row := range w.listRows() {
		if row.key == key {
			label = row.label
		}
	}
	w.list.notice = ""
	w.deleteConfirm = &windowDeleteConfirm{key: key, id: m.sessionID, label: label, checking: true}
	ctx, pager, id := w.base.Ctx, w.base.Sessions, m.sessionID
	return w, func() tea.Msg {
		out := windowDeleteCapsMsg{key: key, id: id}
		if pager == nil {
			return out
		}
		cursor := ""
		for range windowInventoryPageLimit {
			page, err := pager.ListSessionPage(ctx, cursor)
			if err != nil {
				return out
			}
			for _, row := range page.Sessions {
				if row.ID == id {
					out.removeWorktree = row.Capabilities.RemoveWorktree
					return out
				}
			}
			if page.NextCursor == "" {
				return out
			}
			cursor = page.NextCursor
		}
		return out
	}
}

func (w window) onDeleteCaps(msg windowDeleteCapsMsg) (tea.Model, tea.Cmd) {
	if c := w.deleteConfirm; c != nil && c.key == msg.key && c.id == msg.id && c.checking {
		next := *c
		next.checking = false
		next.removeWorktree = msg.removeWorktree
		w.deleteConfirm = &next
	}
	return w, nil
}

func (w window) onDeleteConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	c := *w.deleteConfirm
	if c.busy {
		if c.stopping && (msg.Text == "n" || teakey.Matches(msg, w.keys.Close)) {
			w.deleteConfirm = nil // stop waiting; the session is kept
		}
		return w, nil
	}
	switch {
	case msg.Text == "n", teakey.Matches(msg, w.keys.Close):
		w.deleteConfirm = nil
		return w, nil
	case c.checking:
		return w, nil
	case msg.Text == "y", teakey.Matches(msg, w.keys.Choose):
		return w.confirmDelete(c, false)
	case msg.Text == "r" && c.removeWorktree:
		return w.confirmDelete(c, true)
	}
	return w, nil
}

// confirmDelete deletes the row with stop_active, so a running or awaiting
// session is stopped first (ADR 0374). A server without create_worktrees
// predates stop_active; a plain delete there may fall back to cancel-then-delete.
func (w window) confirmDelete(c windowDeleteConfirm, removeWorktree bool) (tea.Model, tea.Cmd) {
	c.busy = true
	w.deleteConfirm = &c
	m, _ := w.modelFor(c.key)
	fallback := !removeWorktree && !m.caps.CreateWorktrees
	return w, w.deleteCmd(c, client.DeleteSessionOptions{StopActive: true, RemoveWorktree: removeWorktree}, fallback)
}

func (w window) deleteCmd(c windowDeleteConfirm, opts client.DeleteSessionOptions, fallback bool) tea.Cmd {
	ctx, deleter := w.base.Ctx, w.base.SessionManagement
	return func() tea.Msg {
		res, err := deleter.DeleteSession(ctx, c.id, opts)
		return windowDeletedMsg{key: c.key, label: c.label, removeWorktree: opts.RemoveWorktree, fallback: fallback, result: res, err: err}
	}
}

// stopThenDelete is the old-server fallback: the server ignored stop_active and
// refused deleting an active session, so the window cancels that session's own
// run and retries a plain delete once it is no longer running or awaiting
// approval (onSessionMsg). The retry never falls back again.
func (w window) stopThenDelete(msg windowDeletedMsg) (tea.Model, tea.Cmd) {
	i := w.index(msg.key)
	if i < 0 {
		return w, nil
	}
	m := w.sessions[i].model
	c := windowDeleteConfirm{key: msg.key, id: m.sessionID, label: msg.label, busy: true}
	if !windowStatusActive(m.windowRowStatus()) {
		// No run of ours to cancel (another client may own it): retry once.
		w.deleteConfirm = &c
		return w, w.deleteCmd(c, client.DeleteSessionOptions{}, false)
	}
	c.stopping = true
	w.deleteConfirm = &c
	updated, cmd := m.onRunningCancel()
	return w.withModel(i, updated), tagCmd(msg.key, cmd)
}

// windowStatusActive reports a running or approval-waiting session.
func windowStatusActive(status string) bool {
	return status == windowStatusRunning || status == windowStatusNeedsApproval
}

// onDeleted removes a deleted row (closing only its own stream) or shows why
// the server refused. Deleting the last row opens a new default session.
func (w window) onDeleted(msg windowDeletedMsg) (tea.Model, tea.Cmd) {
	if c := w.deleteConfirm; c != nil && c.key == msg.key {
		w.deleteConfirm = nil
	}
	if msg.err != nil && msg.fallback && client.IsDeleteRefusedActive(msg.err) {
		return w.stopThenDelete(msg)
	}
	if msg.err != nil {
		w.list = w.listAt(msg.key, "could not delete "+msg.label+": "+terminaltext.SanitizeSingleLine(msg.err.Error()))
		return w, nil
	}
	notice := "chat deleted"
	switch {
	case msg.removeWorktree && msg.result.WorktreeRemoved:
		notice = "chat deleted and worktree removed; its branch is kept"
	case msg.removeWorktree:
		notice = "chat deleted, worktree kept: " + windowRetainedReason(msg.result.WorktreeRetainedReason)
	}
	w, cmd := w.closeSession(msg.key)
	if len(w.sessions) == 0 {
		var createCmd tea.Cmd
		w, createCmd = w.addSession(sessionOpen{create: &client.CreateSessionRequest{}})
		cmd = tea.Batch(cmd, createCmd)
		notice += "; opened a new session"
	}
	w.list = w.listAt(w.activeKey, notice)
	return w, cmd
}

// windowRetainedReason puts a DeleteSession worktree_retained_reason in plain words.
func windowRetainedReason(reason string) string {
	switch reason {
	case "dirty":
		return "it has uncommitted or untracked changes"
	case "shared":
		return "another session or schedule still uses it"
	case "remove_failed":
		return "removing it failed"
	case "":
		return "the server did not remove it"
	}
	return terminaltext.SanitizeSingleLine(reason)
}

// windowRow is one row of the window session list.
type windowRow struct {
	key    int
	status string
	label  string
	branch string
}

func (w window) rowStatus(key int) string {
	m, _ := w.modelFor(key)
	return m.windowRowStatus()
}

// listRows labels each session by title; titles shared by several rows each
// gain their ADR 0285 short handle.
func (w window) listRows() []windowRow {
	titles := make([]string, len(w.sessions))
	count := map[string]int{}
	for i, s := range w.sessions {
		titles[i] = s.model.windowTitle()
		count[titles[i]]++
	}
	rows := make([]windowRow, len(w.sessions))
	for i, s := range w.sessions {
		label := titles[i]
		if handle := client.SessionHandle(s.model.sessionID); count[label] > 1 && handle != "" {
			label += " " + handle
		}
		rows[i] = windowRow{key: s.key, status: s.model.windowRowStatus(), label: label,
			branch: terminaltext.SanitizeSingleLine(s.model.activePlacement.Branch)}
	}
	return rows
}

// badge names background sessions waiting on an approval.
func (w window) badge() string {
	waiting := 0
	for _, s := range w.sessions {
		if s.key != w.activeKey && s.model.windowRowStatus() == windowStatusNeedsApproval {
			waiting++
		}
	}
	if waiting == 0 {
		return ""
	}
	noun := "session needs"
	if waiting > 1 {
		noun = "sessions need"
	}
	return fmt.Sprintf("⚠ %d %s approval (%s sessions)", waiting, noun, w.listChord())
}

func (w window) listChord() string {
	chord := firstKey(w.keys.Sessions, "left")
	if chord == "left" {
		return "←"
	}
	return chord
}

func (w window) View() tea.View {
	m := w.activeModel()
	m.windowBadge = w.badge()
	if w.list != nil && w.quitConfirm == nil {
		// The session list owns the whole screen, like a full-screen surface.
		v := m.View()
		v.Content = w.renderList(m)
		return v
	}
	if w.quitConfirm != nil {
		m.windowOverlay = centerCard(m.deps.Theme, w.renderQuitConfirm(m), m.widthOr(), m.vp.Height())
	}
	v := m.View()
	if m.windowOverlay != "" && m.phase == phaseFatal {
		v.Content = m.windowOverlay
	}
	return v
}

// windowText word-wraps one plain-text line to the window width and styles it.
func windowText(style lipgloss.Style, text string, width int) string {
	rows := strings.Split(wrapCardText(text, width), "\n")
	for i, row := range rows {
		rows[i] = style.Render(row)
	}
	return strings.Join(rows, "\n")
}

func (window) renderDeleteConfirm(m Model, c windowDeleteConfirm) string {
	th := m.deps.Theme
	width := m.widthOr()
	lines := []string{
		th.Style("askTitle").Render("Delete " + c.label + "?"),
		windowText(th.Style("toolArgs"), "If it is running or waiting for approval, it is stopped first.", width),
	}
	muted := th.Style("muted")
	switch {
	case c.stopping:
		lines = append(lines, windowText(muted, "stopping its run first (this server cannot stop and delete in one step)…  n/esc: stop waiting", width))
	case c.busy:
		lines = append(lines, muted.Render("deleting…"))
	case c.checking:
		lines = append(lines, muted.Render("checking the session…  n/esc: keep"))
	case c.removeWorktree:
		lines = append(lines,
			windowText(th.Style("toolArgs"), "It runs in a worktree mecatl created.", width),
			windowText(th.Style("toolArgs"), "Removing the worktree also deletes ignored files in it. Its branch is kept.", width),
			windowText(muted, "y: delete the chat  r: delete and remove worktree  n/esc: keep", width))
	default:
		lines = append(lines, muted.Render("y: delete  n/esc: keep"))
	}
	return strings.Join(lines, "\n")
}

func (w window) renderQuitConfirm(m Model) string {
	th := m.deps.Theme
	noun := "other session is"
	if w.quitConfirm.others > 1 {
		noun = "other sessions are"
	}
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("Quit mecatui?") + "\n\n")
	fmt.Fprintf(&b, "%d %s running or awaiting approval and will be cancelled.\n", w.quitConfirm.others, noun)
	b.WriteString("Sessions stay saved; reopen them with /sessions.\n\n")
	b.WriteString(th.Style("muted").Render("y: quit  n/esc: stay"))
	return b.String()
}

// windowSessionsKeyLive reports whether the sessions key opens the window list:
// an empty prompt with no overlay, modal, approval, or authorization open.
func (m Model) windowSessionsKeyLive() bool {
	if bodyOwnerOpen(m) || m.clearPending != nil || m.prompt.Value() != "" {
		return false
	}
	switch m.phase {
	case phaseAwaitingApproval, phaseAuthorizing, phaseReplay:
		return false
	}
	return true
}

// windowRowStatus derives the list status from state the session already
// tracks; idle covers completed and cancelled turns.
func (m Model) windowRowStatus() string {
	switch m.phase {
	case phaseAwaitingApproval:
		return windowStatusNeedsApproval
	case phaseRunning, phaseConnecting, phaseAuthorizing:
		return windowStatusRunning
	case phaseFatal:
		return windowStatusFailed
	}
	if m.lastRunFailed || m.sessionState == "failed" {
		return windowStatusFailed
	}
	return windowStatusIdle
}

func (m Model) windowTitle() string {
	if title := strings.TrimSpace(terminaltext.SanitizeSingleLine(m.sessionTitle)); title != "" {
		return title
	}
	return "new session"
}
