package ui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"

	teakey "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

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
//   - Input (keys, mouse, paste, focus) reaches the active session only; window
//     size and terminal theme/capability messages reach every session.
//
// Side-effecting Deps hooks (terminal title, status-line source, lifecycle
// hook) are wrapped per session and forward only while that session is active.

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
	key       int
	active    *atomic.Int64 // the window's active session key
	hookOwner *atomic.Int64 // the session whose lifecycle-hook busy period is open
	cancel    context.CancelFunc
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

// scopedLifecycle forwards host lifecycle notifications only for the active
// session. The host hook has one busy period, so the session that opened it is
// the one allowed to close it, even if it has since moved to the background.
type scopedLifecycle struct {
	inner LifecycleNotifier
	scope *windowScope
}

func (h scopedLifecycle) Start(ctx context.Context, sessionID string) {
	if h.scope.isActive() {
		h.scope.hookOwner.Store(int64(h.scope.key))
		h.inner.Start(ctx, sessionID)
	}
}

func (h scopedLifecycle) PermissionRequest(ctx context.Context, sessionID, message string) {
	if h.scope.isActive() {
		h.inner.PermissionRequest(ctx, sessionID, message)
	}
}

func (h scopedLifecycle) PermissionResult(ctx context.Context, sessionID string) {
	if h.scope.isActive() {
		h.inner.PermissionResult(ctx, sessionID)
	}
}

func (h scopedLifecycle) Stop(ctx context.Context, sessionID string, failed bool, message string) {
	if h.scope.hookOwner.CompareAndSwap(int64(h.scope.key), 0) {
		h.inner.Stop(ctx, sessionID, failed, message)
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
}

type windowListState struct {
	cursor int
	notice string
}

type windowQuitConfirm struct{ others int }

type window struct {
	base        Deps
	keys        keyMap
	sessions    []windowSession
	activeKey   int
	nextKey     int
	activeCell  *atomic.Int64
	hookOwner   *atomic.Int64
	list        *windowListState
	quitConfirm *windowQuitConfirm

	// Terminal facts replayed to sessions created later.
	lastSize      *tea.WindowSizeMsg
	lastKeyboard  *tea.KeyboardEnhancementsMsg
	lastProfile   *tea.ColorProfileMsg
	sawBackground bool
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
		hookOwner:  new(atomic.Int64),
	}
	key, m := w.buildSession(sessionOpen{})
	w.sessions = []windowSession{{key: key, model: m}}
	w.activeKey = key
	w.activeCell.Store(int64(key))
	return w
}

func (w *window) buildSession(open sessionOpen) (int, Model) {
	key := w.nextKey
	w.nextKey++
	deps := w.base
	deps.window = &windowScope{key: key, active: w.activeCell, hookOwner: w.hookOwner}
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
	}
	if windowBroadcastMsg(msg) {
		return w.broadcast(msg)
	}
	if windowInputMsg(msg) && (w.list != nil || w.quitConfirm != nil) {
		return w, nil // the window overlay owns input
	}
	return w.updateSession(w.activeKey, msg)
}

func (w window) updateSession(key int, msg tea.Msg) (window, tea.Cmd) {
	i := w.index(key)
	if i < 0 {
		return w, nil
	}
	updated, cmd := w.sessions[i].model.Update(msg)
	if m, ok := updated.(Model); ok {
		w.sessions = append([]windowSession(nil), w.sessions...)
		w.sessions[i].model = m
	}
	return w, tagCmd(key, cmd)
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
	}
	return w.updateSession(msg.key, msg.msg)
}

func (w window) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if w.quitConfirm != nil {
		return w.onQuitConfirmKey(msg)
	}
	if w.list != nil {
		return w.onListKey(msg)
	}
	if teakey.Matches(msg, w.keys.Sessions) && w.activeModel().windowSessionsKeyLive() {
		w.list = &windowListState{cursor: max(0, w.index(w.activeKey))}
		return w, nil
	}
	return w.updateSession(w.activeKey, msg)
}

func (w window) onListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	list := *w.list
	w.list = &list
	switch {
	case teakey.Matches(msg, w.keys.Close), teakey.Matches(msg, w.keys.Sessions):
		w.list = nil
	case teakey.Matches(msg, w.keys.Up):
		list.cursor = max(0, list.cursor-1)
	case teakey.Matches(msg, w.keys.Down):
		list.cursor = min(len(w.sessions)-1, list.cursor+1)
	case teakey.Matches(msg, w.keys.Choose):
		if list.cursor < len(w.sessions) {
			return w.switchTo(w.sessions[list.cursor].key)
		}
	case msg.Text == "o":
		return w.openSavedPicker()
	case teakey.Matches(msg, w.keys.Quit), teakey.Matches(msg, w.keys.QuitD), teakey.Matches(msg, w.keys.Suspend):
		w.list = nil
		return w.updateSession(w.activeKey, msg)
	}
	return w, nil
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
	i := w.index(w.activeKey)
	updated, cmd := active.openSessions()
	if m, ok := updated.(Model); ok {
		w.sessions = append([]windowSession(nil), w.sessions...)
		w.sessions[i].model = m
	}
	return w, tagCmd(w.activeKey, cmd)
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
		if status := s.model.windowRowStatus(); status == windowStatusRunning || status == windowStatusNeedsApproval {
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
	w.sessions = append([]windowSession(nil), w.sessions...)
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
	w.sessions = append(append([]windowSession(nil), w.sessions...), windowSession{key: key, model: m})
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
	if m, ok := updated.(Model); ok {
		w.sessions[i].model = m
	}
	return w, tea.Batch(cmd, tagCmd(w.activeKey, adoptCmd))
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
		if w.hookOwner.CompareAndSwap(int64(key), 0) && w.base.AgentHook != nil {
			w.base.AgentHook.Stop(w.base.Ctx, m.sessionID, false, "")
		}
		if scope.cancel != nil {
			scope.cancel()
		}
	}
	w.sessions = append(append([]windowSession(nil), w.sessions[:i]...), w.sessions[i+1:]...)
	if key != w.activeKey || len(w.sessions) == 0 {
		return w, nil
	}
	next := w.sessions[min(i, len(w.sessions)-1)].key
	switched, cmd := w.switchTo(next)
	w, _ = switched.(window)
	return w, cmd
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
	switch {
	case w.quitConfirm != nil:
		m.windowOverlay = centerCard(m.deps.Theme, w.renderQuitConfirm(m), m.widthOr(), m.vp.Height())
	case w.list != nil:
		m.windowOverlay = w.renderList(m)
	}
	v := m.View()
	if m.windowOverlay != "" && m.phase == phaseFatal {
		v.Content = m.windowOverlay
	}
	return v
}

func (w window) renderList(m Model) string {
	th := m.deps.Theme
	hk := m.helpKeyMarkings()
	var b strings.Builder
	b.WriteString(th.Style("title").Render("sessions in this window") + "\n")
	b.WriteString(th.Style("muted").Render("o or /sessions opens a saved session") + "\n\n")
	for i, row := range w.listRows() {
		marker := "  "
		if i == w.list.cursor {
			marker = "▶ "
		}
		line := marker + fmt.Sprintf("%-15s", row.status) + row.label
		if row.branch != "" {
			line += "  (" + row.branch + ")"
		}
		if row.key == w.activeKey {
			line += "  · current"
		}
		style := th.Style("toolArgs")
		if i == w.list.cursor {
			style = th.Style("accent")
		}
		b.WriteString(renderToolCardText(style, line, m.widthOr()) + "\n")
	}
	if w.list.notice != "" {
		b.WriteString("\n" + th.Style("warning").Render(w.list.notice) + "\n")
	}
	b.WriteString("\n" + th.Style("muted").Render(hk.choose+": switch  o: saved sessions (/sessions)  "+hk.closeOnly+": close"))
	return b.String()
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
