package ui

import (
	"context"
	"fmt"
	"image/color"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	customization "github.com/stacklok/mecatl/cmd/mecatui/customization"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// heldRecver replays a script and blocks before the indexes in holds until the
// matching channel closes, so a test can park a run at an exact point.
type heldRecver struct {
	mu     sync.Mutex
	script []*mecatlv1.ConverseResponse
	idx    int
	holds  map[int]chan struct{}
}

func (r *heldRecver) Recv() (*mecatlv1.ConverseResponse, error) {
	r.mu.Lock()
	if hold, ok := r.holds[r.idx]; ok {
		delete(r.holds, r.idx)
		r.mu.Unlock()
		<-hold
		r.mu.Lock()
	}
	defer r.mu.Unlock()
	if r.idx >= len(r.script) {
		return nil, io.EOF
	}
	resp := r.script[r.idx]
	r.idx++
	return resp, nil
}

type windowStream struct {
	recv client.Recver
	send *fakeSender
}

// windowConv serves scripted Converse streams per session id, so each window
// session gets its own stream and sender.
type windowConv struct {
	*fakeConv
	mu      sync.Mutex
	streams map[string][]windowStream
	runCtx  map[string][]context.Context
	// createReqs records every CreateSessionWith request; worktreeErr fails a
	// NewWorktree create; placements is the GetSession placement per session.
	createReqs  []client.CreateSessionRequest
	worktreeErr error
	placements  map[string]client.Placement
}

func newWindowConv() *windowConv {
	return &windowConv{fakeConv: &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}, streams: map[string][]windowStream{}, runCtx: map[string][]context.Context{}, placements: map[string]client.Placement{}}
}

// CreateSessionWith models a server that binds a NewWorktree create to a fresh
// mecatl/brave-otter worktree and reports it as GetSession placement metadata.
func (c *windowConv) CreateSessionWith(ctx context.Context, req client.CreateSessionRequest) (string, client.Capabilities, client.ResolvedModel, error) {
	c.mu.Lock()
	c.createReqs = append(c.createReqs, req)
	failure := c.worktreeErr
	c.mu.Unlock()
	if req.NewWorktree && failure != nil {
		return "", client.Capabilities{}, client.ResolvedModel{}, failure
	}
	id, caps, resolved, err := c.CreateSession(ctx, req.Selection, req.Mode)
	if err == nil && req.NewWorktree {
		c.mu.Lock()
		c.placements[id] = client.Placement{Kind: "local", Label: "mecatl/brave-otter", Branch: "mecatl/brave-otter"}
		c.mu.Unlock()
	}
	return id, caps, resolved, err
}

func (c *windowConv) GetSession(ctx context.Context, id string) (client.SessionSnapshot, error) {
	snapshot, err := c.fakeConv.GetSession(ctx, id)
	c.mu.Lock()
	snapshot.Placement = c.placements[id]
	c.mu.Unlock()
	return snapshot, err
}

func (c *windowConv) requests() []client.CreateSessionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]client.CreateSessionRequest(nil), c.createReqs...)
}

func (c *windowConv) script(id string, recv client.Recver) *fakeSender {
	c.mu.Lock()
	defer c.mu.Unlock()
	send := &fakeSender{}
	c.streams[id] = append(c.streams[id], windowStream{recv: recv, send: send})
	return send
}

func (*windowConv) OpenConverse(context.Context) (*client.Stream, error) {
	return nil, fmt.Errorf("window sessions must bind their stream to a session")
}

// CreateSession returns ids whose ADR 0285 handles differ ("0001-sess-te…"),
// unlike fakeConv's shared "sess-test-00" prefix.
func (c *windowConv) CreateSession(ctx context.Context, sel client.ModelSelection, mode string) (string, client.Capabilities, client.ResolvedModel, error) {
	id, caps, resolved, err := c.fakeConv.CreateSession(ctx, sel, mode)
	if err != nil {
		return "", caps, resolved, err
	}
	return strings.TrimPrefix(id, "sess-test-") + "-sess-test", caps, resolved, nil
}

func (c *windowConv) OpenConverseForSession(ctx context.Context, id string) (*client.Stream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	queue := c.streams[id]
	if len(queue) == 0 {
		return nil, fmt.Errorf("no scripted stream for %s", id)
	}
	c.streams[id] = queue[1:]
	c.runCtx[id] = append(c.runCtx[id], ctx)
	return client.NewStream(queue[0].recv, queue[0].send), nil
}

func (c *windowConv) lastRunCtx(id string) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	runs := c.runCtx[id]
	if len(runs) == 0 {
		return nil
	}
	return runs[len(runs)-1]
}

// windowDriver mimics the Bubble Tea program loop: commands run on goroutines
// and their messages are reduced on the test goroutine, so session streams,
// ticks, and window tagging all behave as in production.
type windowDriver struct {
	t    *testing.T
	w    window
	msgs chan tea.Msg
	done chan struct{}
	quit bool
}

func newWindowDriver(t *testing.T, deps Deps) *windowDriver {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	deps.Ctx = ctx
	if deps.Theme.Name == "" {
		deps.Theme = testTheme()
	}
	if deps.Mode == "" {
		deps.Mode = "default"
	}
	deps.NoAltScreen = true
	d := &windowDriver{t: t, msgs: make(chan tea.Msg), done: make(chan struct{})}
	t.Cleanup(func() {
		close(d.done)
		cancel()
	})
	root, ok := NewWindow(normalizeTestModelDeps(deps)).(window)
	if !ok {
		t.Fatalf("NewWindow returned %T, want window", NewWindow(deps))
	}
	d.w = root
	d.run(d.w.Init())
	d.send(tea.WindowSizeMsg{Width: 110, Height: 32})
	return d
}

func (d *windowDriver) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go d.deliver(cmd)
}

func (d *windowDriver) deliver(cmd tea.Cmd) {
	msg := cmd()
	if cmds, sequential, ok := teaCommandList(msg); ok {
		for _, child := range cmds {
			if child == nil {
				continue
			}
			if sequential {
				d.deliver(child)
			} else {
				d.run(child)
			}
		}
		return
	}
	if msg == nil {
		return
	}
	select {
	case d.msgs <- msg:
	case <-d.done:
	}
}

func (d *windowDriver) send(msg tea.Msg) {
	d.t.Helper()
	if _, ok := msg.(tea.QuitMsg); ok {
		d.quit = true
		return
	}
	model, cmd := d.w.Update(msg)
	next, ok := model.(window)
	if !ok {
		d.t.Fatalf("window Update returned %T", model)
	}
	d.w = next
	d.run(cmd)
}

func (d *windowDriver) until(what string, ready func(window) bool) {
	d.t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for !ready(d.w) {
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-deadline.C:
			d.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// settle drains messages for a short while without a target condition.
func (d *windowDriver) settle() {
	d.t.Helper()
	quiet := time.NewTimer(150 * time.Millisecond)
	defer quiet.Stop()
	for {
		select {
		case msg := <-d.msgs:
			d.send(msg)
		case <-quiet.C:
			return
		}
	}
}

func (d *windowDriver) press(keys ...tea.KeyPressMsg) {
	d.t.Helper()
	for _, k := range keys {
		d.send(k)
	}
}

func (d *windowDriver) typeText(text string) {
	d.t.Helper()
	for _, r := range text {
		d.send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func (d *windowDriver) submit(text string) {
	d.t.Helper()
	d.typeText(text)
	d.press(tea.KeyPressMsg{Code: tea.KeyEnter})
}

func (d *windowDriver) model(key int) Model {
	d.t.Helper()
	m, ok := d.w.modelFor(key)
	if !ok {
		d.t.Fatalf("no window session %d", key)
	}
	return m
}

func (d *windowDriver) mutate(key int, fn func(*Model)) {
	d.t.Helper()
	for i := range d.w.sessions {
		if d.w.sessions[i].key == key {
			fn(&d.w.sessions[i].model)
			return
		}
	}
	d.t.Fatalf("no window session %d", key)
}

// addCreated adds a default-placement session and waits until it is ready.
func (d *windowDriver) addCreated() int {
	d.t.Helper()
	w, cmd := d.w.addSession(sessionOpen{create: &client.CreateSessionRequest{}})
	d.w = w
	d.run(cmd)
	key := d.w.activeKey
	d.until("created session ready", func(w window) bool {
		m, _ := w.modelFor(key)
		return m.sessionID != "" && m.phase == phaseIdle
	})
	return key
}

// switchTo opens the window list, moves to key's row, and chooses it.
func (d *windowDriver) switchTo(key int) {
	d.t.Helper()
	d.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	if d.w.list == nil {
		d.t.Fatal("left did not open the window session list")
	}
	for i := 0; i < len(d.w.sessions); i++ {
		d.press(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	for _, row := range d.w.listRows() {
		if row.key == key {
			break
		}
		d.press(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	d.press(tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.w.activeKey != key || d.w.list != nil {
		d.t.Fatalf("switch: active=%d list open=%v, want %d closed", d.w.activeKey, d.w.list != nil, key)
	}
}

func viewText(m Model) string { return stripANSIstr(m.View().Content) }

func readyWindow(t *testing.T, conv *windowConv, tweak func(*Deps)) *windowDriver {
	t.Helper()
	deps := Deps{Session: conv, Conv: conv}
	if tweak != nil {
		tweak(&deps)
	}
	d := newWindowDriver(t, deps)
	d.until("startup session ready", func(w window) bool {
		m := w.activeModel()
		return m.sessionID != "" && m.phase == phaseIdle
	})
	return d
}

func streamScript(label string, events ...*mecatlv1.Event) []*mecatlv1.ConverseResponse {
	script := []*mecatlv1.ConverseResponse{ev(&mecatlv1.Event{Type: "session.init", Seq: 1}), ev(&mecatlv1.Event{Type: "turn.start", Seq: 2, Turn: 1})}
	for _, e := range events {
		script = append(script, ev(e))
	}
	return append(script, ev(&mecatlv1.Event{Type: "result", Seq: 99, Turn: 1, Result: &mecatlv1.Result{Stop: "end_turn", Text: label}}))
}

func delta(seq int64, text string) *mecatlv1.Event {
	return &mecatlv1.Event{Type: "message.delta", Seq: seq, Turn: 1, Text: text}
}

func TestTUIMultiSession_Scenario4_LeftOpensListOnlyOnEmptyPrompt(t *testing.T) {
	d := readyWindow(t, newWindowConv(), nil)
	left := tea.KeyPressMsg{Code: tea.KeyLeft}

	d.press(left)
	if d.w.list == nil {
		t.Fatal("left on an empty idle prompt must open the window session list")
	}
	d.press(tea.KeyPressMsg{Code: tea.KeyEscape})
	if d.w.list != nil {
		t.Fatal("esc must close the window session list")
	}

	d.typeText("ab")
	d.press(left)
	if d.w.list != nil {
		t.Fatal("left with prompt text must keep moving the cursor")
	}
	d.typeText("X")
	if got := d.w.activeModel().prompt.Value(); got != "aXb" {
		t.Fatalf("left with text must reach the prompt as cursor movement: prompt=%q", got)
	}
	d.mutate(d.w.activeKey, func(m *Model) { m.prompt.Reset() })

	d.mutate(d.w.activeKey, func(m *Model) { m.showHelp = true })
	d.press(left)
	if d.w.list != nil {
		t.Fatal("left inside an overlay must keep its overlay meaning")
	}
	d.mutate(d.w.activeKey, func(m *Model) { m.showHelp = false; m.phase = phaseAwaitingApproval })
	d.press(left)
	if d.w.list != nil {
		t.Fatal("left in the approval modal must keep its modal meaning")
	}

	rebound := readyWindow(t, newWindowConv(), func(deps *Deps) { deps.KeyOverrides = map[string][]string{"Sessions": {"ctrl+o"}} })
	rebound.press(left)
	if rebound.w.list != nil {
		t.Fatal("a rebound sessions action must free left")
	}
	rebound.press(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if rebound.w.list == nil {
		t.Fatal("the rebound sessions chord must open the window session list")
	}
}

func TestTUIMultiSession_Scenario4_ListRowsAndUniqueLabels(t *testing.T) {
	d := readyWindow(t, newWindowConv(), nil)
	first := d.w.activeKey
	second := d.addCreated()
	third := d.addCreated()
	fourth := d.addCreated()
	fifth := d.addCreated()

	d.mutate(first, func(m *Model) { m.sessionTitle = "Fix bug"; m.phase = phaseRunning })
	d.mutate(second, func(m *Model) { m.sessionTitle = "Fix bug"; m.phase = phaseAwaitingApproval })
	d.mutate(third, func(m *Model) {
		m.sessionTitle = "Docs"
		m.lastRunFailed = true
		m.activePlacement = client.Placement{Kind: "local", Label: "mecatl/brave-otter", Branch: "mecatl/brave-otter"}
	})
	d.mutate(fourth, func(m *Model) { m.sessionTitle = "Plan"; m.sessionState = "completed" })
	d.mutate(fifth, func(m *Model) { m.sessionTitle = "Spike"; m.sessionState = "cancelled" })

	handle := func(key int) string { return client.SessionHandle(d.model(key).sessionID) }
	if handle(first) == "" || handle(first) == handle(second) {
		t.Fatalf("fixture handles must be distinct: %q %q", handle(first), handle(second))
	}
	want := map[int]windowRow{
		first:  {key: first, status: windowStatusRunning, label: "Fix bug " + handle(first)},
		second: {key: second, status: windowStatusNeedsApproval, label: "Fix bug " + handle(second)},
		third:  {key: third, status: windowStatusFailed, label: "Docs", branch: "mecatl/brave-otter"},
		fourth: {key: fourth, status: windowStatusIdle, label: "Plan"},
		fifth:  {key: fifth, status: windowStatusIdle, label: "Spike"},
	}
	rows := d.w.listRows()
	if len(rows) != len(want) {
		t.Fatalf("rows = %d, want %d", len(rows), len(want))
	}
	for _, row := range rows {
		if row != want[row.key] {
			t.Errorf("row %d = %+v, want %+v", row.key, row, want[row.key])
		}
	}

	d.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	view := stripANSIstr(d.w.View().Content)
	for _, fragment := range []string{"Fix bug " + handle(first), "Fix bug " + handle(second), "needs approval", "failed", "mecatl/brave-otter", "Plan", "idle", "running"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("window list view is missing %q:\n%s", fragment, view)
		}
	}
	if strings.Contains(view, "Docs "+handle(third)) {
		t.Errorf("a unique title must not carry a handle:\n%s", view)
	}
}

func TestTUIMultiSession_Scenario4_BackgroundRunKeepsStreaming(t *testing.T) {
	conv := newWindowConv()
	release := make(chan struct{})
	conv.script("0001-sess-test", &heldRecver{
		script: streamScript("alpha done", delta(3, "alpha-one "), delta(4, "alpha-two "), delta(5, "alpha-three")),
		holds:  map[int]chan struct{}{4: release},
	})
	conv.script("0002-sess-test", &heldRecver{script: streamScript("bravo done", delta(3, "bravo-one"))})
	d := readyWindow(t, conv, nil)
	first := d.w.activeKey

	d.submit("start alpha")
	d.until("first alpha delta", func(w window) bool {
		m, _ := w.modelFor(first)
		return strings.Contains(viewText(m), "alpha-one")
	})
	second := d.addCreated()
	if d.w.activeKey != second {
		t.Fatal("a new session must become active")
	}
	if got := d.w.rowStatus(first); got != windowStatusRunning {
		t.Fatalf("background run status = %q, want running", got)
	}
	if strings.Contains(stripANSIstr(d.w.View().Content), "alpha-one") {
		t.Fatal("the active view must show the newly active session, not the background transcript")
	}

	close(release)
	d.submit("start bravo")
	d.until("both runs finish", func(w window) bool {
		a, _ := w.modelFor(first)
		b, _ := w.modelFor(second)
		return a.phase == phaseIdle && b.phase == phaseIdle && strings.Contains(viewText(b), "bravo-one")
	})
	if b := viewText(d.model(second)); strings.Contains(b, "alpha") {
		t.Fatalf("background events leaked into the active session:\n%s", b)
	}

	d.switchTo(first)
	view := stripANSIstr(d.w.View().Content)
	one, two, three := strings.Index(view, "alpha-one"), strings.Index(view, "alpha-two"), strings.Index(view, "alpha-three")
	if one < 0 || two < one || three < two {
		t.Fatalf("switching back must show every background event in order (%d,%d,%d):\n%s", one, two, three, view)
	}
}

func TestTUIMultiSession_Scenario4_BackgroundAskSurfacesOnSwitch(t *testing.T) {
	conv := newWindowConv()
	toAsk, afterAsk := make(chan struct{}), make(chan struct{})
	askScript := preApprovalScript()
	askIndex := 7
	if askScript[askIndex].GetEvent().GetType() != "permission.ask" {
		t.Fatalf("preApprovalScript changed: index %d is %q", askIndex, askScript[askIndex].GetEvent().GetType())
	}
	sendA := conv.script("0001-sess-test", &heldRecver{script: askScript, holds: map[int]chan struct{}{askIndex: toAsk, askIndex + 1: afterAsk}})
	sendA.onSend = func(req *mecatlv1.ConverseRequest) {
		if req.GetResumeApproval() != nil {
			close(afterAsk)
		}
	}
	holdB := make(chan struct{})
	sendB := conv.script("0002-sess-test", &heldRecver{script: streamScript("bravo", delta(3, "bravo-one"), delta(4, "bravo-two")), holds: map[int]chan struct{}{4: holdB}})
	defer close(holdB)

	d := readyWindow(t, conv, nil)
	first := d.w.activeKey
	d.submit("write a note")
	d.until("alpha run streaming", func(w window) bool {
		m, _ := w.modelFor(first)
		return strings.Contains(viewText(m), "Reading the greeting file")
	})
	second := d.addCreated()
	d.submit("start bravo")
	d.until("bravo streaming", func(w window) bool {
		m, _ := w.modelFor(second)
		return strings.Contains(viewText(m), "bravo-one")
	})

	close(toAsk)
	d.until("background ask", func(w window) bool { return w.rowStatus(first) == windowStatusNeedsApproval })
	if d.w.activeKey != second {
		t.Fatal("a background ask must not steal focus")
	}
	if view := stripANSIstr(d.w.View().Content); !strings.Contains(view, "needs approval") {
		t.Fatalf("the active footer must badge the background ask:\n%s", view)
	}

	d.switchTo(first)
	if m := d.w.activeModel(); m.phase != phaseAwaitingApproval || !strings.Contains(stripANSIstr(d.w.View().Content), "Write") {
		t.Fatalf("switching to the asking session must open its ask: phase=%v\n%s", m.phase, stripANSIstr(d.w.View().Content))
	}
	d.press(tea.KeyPressMsg{Code: 'a', Text: "a"})
	d.until("alpha run resolved", func(w window) bool {
		m, _ := w.modelFor(first)
		return m.phase == phaseIdle && strings.Contains(viewText(m), "Done.")
	})
	approvals := func(s *fakeSender) int {
		n := 0
		for _, frame := range s.frames() {
			if frame.GetResumeApproval() != nil {
				n++
			}
		}
		return n
	}
	if approvals(sendA) != 1 || approvals(sendB) != 0 {
		t.Fatalf("verdict frames: alpha=%d bravo=%d, want 1 and 0", approvals(sendA), approvals(sendB))
	}
	if got := d.w.rowStatus(second); got != windowStatusRunning {
		t.Fatalf("the other session's run must be untouched, status=%q", got)
	}
}

type titleRecorder struct {
	mu      sync.Mutex
	handles []string
}

func (r *titleRecorder) set(in customization.Input) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handles = append(r.handles, in.Session.Handle)
}

func (r *titleRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.handles
	r.handles = nil
	return out
}

func TestTUIMultiSession_Scenario4_InputRoutesToActiveOnly(t *testing.T) {
	titles := &titleRecorder{}
	d := readyWindow(t, newWindowConv(), func(deps *Deps) {
		deps.TerminalTitle = titles.set
		deps.ThemeAutoDetect = true
	})
	first := d.w.activeKey
	second := d.addCreated()

	d.typeText("zz")
	d.send(tea.PasteMsg{Content: "pp"})
	if a, b := d.model(first).prompt.Value(), d.model(second).prompt.Value(); a != "" || b != "zzpp" {
		t.Fatalf("keys/paste: background=%q active=%q, want empty and zzpp", a, b)
	}
	for _, msg := range []tea.Msg{
		tea.KeyPressMsg{Code: 'q'}, tea.KeyReleaseMsg{Code: 'q'}, tea.PasteMsg{Content: "x"},
		tea.MouseClickMsg{X: 1, Y: 1}, tea.MouseWheelMsg{X: 1, Y: 1}, tea.MouseMotionMsg{X: 1, Y: 1}, tea.MouseReleaseMsg{X: 1, Y: 1},
		tea.FocusMsg{}, tea.BlurMsg{},
	} {
		if !windowInputMsg(msg) {
			t.Errorf("%T must route to the active session only", msg)
		}
	}
	for _, msg := range []tea.Msg{tea.WindowSizeMsg{}, tea.BackgroundColorMsg{}, tea.ColorProfileMsg{}, tea.KeyboardEnhancementsMsg{}} {
		if !windowBroadcastMsg(msg) {
			t.Errorf("%T must reach every session", msg)
		}
	}

	d.send(tea.WindowSizeMsg{Width: 123, Height: 41})
	if d.model(first).width != 123 || d.model(second).width != 123 {
		t.Fatalf("window size must reach every session: %d %d", d.model(first).width, d.model(second).width)
	}
	d.send(tea.BackgroundColorMsg{Color: color.White})
	if d.model(first).deps.Theme.Name != theme.Solar().Name || d.model(second).deps.Theme.Name != theme.Solar().Name {
		t.Fatalf("theme must reach every session: %q %q", d.model(first).deps.Theme.Name, d.model(second).deps.Theme.Name)
	}

	titles.take()
	_ = d.w.View()
	_ = d.model(first).View()
	activeHandle := client.SessionHandle(d.model(second).sessionID)
	got := titles.take()
	if len(got) == 0 {
		t.Fatal("the active session must set the terminal title")
	}
	for _, h := range got {
		if h != activeHandle {
			t.Fatalf("terminal title written by a background session: %q (active %q)", h, activeHandle)
		}
	}
}

func TestTUIMultiSession_Scenario4_QuitConfirmsBackgroundSessions(t *testing.T) {
	conv := newWindowConv()
	hold := make(chan struct{})
	defer close(hold)
	conv.script("0001-sess-test", &heldRecver{script: streamScript("alpha", delta(3, "alpha-one"), delta(4, "alpha-two")), holds: map[int]chan struct{}{4: hold}})
	d := readyWindow(t, conv, nil)
	first := d.w.activeKey
	d.submit("start alpha")
	d.until("alpha streaming", func(w window) bool {
		m, _ := w.modelFor(first)
		return strings.Contains(viewText(m), "alpha-one")
	})
	d.addCreated()

	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	d.press(ctrlC, ctrlC)
	d.until("quit confirmation", func(w window) bool { return w.quitConfirm != nil })
	if d.quit {
		t.Fatal("quit must wait for confirmation while another session runs")
	}
	if view := stripANSIstr(d.w.View().Content); !strings.Contains(view, "1 other session") || !strings.Contains(view, "cancelled") {
		t.Fatalf("confirmation must name how many sessions will be cancelled:\n%s", view)
	}
	d.press(tea.KeyPressMsg{Code: 'n', Text: "n"})
	d.settle()
	if d.quit || d.w.quitConfirm != nil {
		t.Fatalf("declining must keep the window open: quit=%v confirm=%v", d.quit, d.w.quitConfirm != nil)
	}
	if ctx := conv.lastRunCtx("0001-sess-test"); ctx == nil || ctx.Err() != nil || d.w.rowStatus(first) != windowStatusRunning {
		t.Fatal("declining must leave the background run streaming")
	}

	d.press(ctrlC, ctrlC)
	d.until("quit confirmation again", func(w window) bool { return w.quitConfirm != nil })
	d.press(tea.KeyPressMsg{Code: 'y', Text: "y"})
	d.until("quit", func(window) bool { return d.quit })
	if ctx := conv.lastRunCtx("0001-sess-test"); ctx.Err() == nil {
		t.Fatal("confirming must cancel the background run")
	}

	single := readyWindow(t, newWindowConv(), nil)
	single.press(ctrlC, ctrlC)
	single.until("single-session quit", func(window) bool { return single.quit })
	if single.w.quitConfirm != nil {
		t.Fatal("single-session quit must not ask for confirmation")
	}
}

func TestTUIMultiSession_Scenario4_SessionsPickerAddsToWindow(t *testing.T) {
	conv := newWindowConv()
	row := func(id, title string) client.SessionListItem {
		return client.SessionListItem{ID: id, Title: title, Kind: client.SessionKindMain, State: "completed", ModifiedAt: nowMinusMinutes(1),
			Capabilities: client.SessionInventoryCapabilities{PublicChat: true, Inspect: true, CopyID: true, ViewTranscript: true}}
	}
	lister := &fakeSessionLister{sessions: []client.SessionListItem{row("chat-1", "Saved chat")}}
	loader := &fakeSessionTranscriptLoader{transcript: client.SessionTranscript{Complete: true}}
	d := readyWindow(t, conv, func(deps *Deps) { deps.Sessions = lister; deps.Transcript = loader })
	first := d.w.activeKey

	openAndChoose := func(id string) {
		t.Helper()
		loader.transcript.SessionID = id
		d.submit("/sessions")
		d.until("/sessions loaded", func(w window) bool {
			m := w.activeModel()
			s := sessionsSurface(&m)
			return s != nil && !s.loading && len(s.filtered) == 1
		})
		d.press(tea.KeyPressMsg{Code: tea.KeyEnter})
	}

	openAndChoose("chat-1")
	d.until("saved chat added to the window", func(w window) bool { return w.activeModel().sessionID == "chat-1" })
	if len(d.w.sessions) != 2 || d.model(first).sessionID != "0001-sess-test" {
		t.Fatalf("opening a saved chat must add it, not replace: sessions=%d first=%q", len(d.w.sessions), d.model(first).sessionID)
	}
	if m := d.model(first); sessionsSurface(&m) != nil || m.phase != phaseIdle {
		t.Fatalf("the originating session must close its picker and stay idle: phase=%v", m.phase)
	}
	if conv.createCount != 1 {
		t.Fatalf("attaching must not create a session: creates=%d", conv.createCount)
	}

	lister.sessions = []client.SessionListItem{row("0001-sess-test", "Startup chat")}
	openAndChoose("0001-sess-test")
	d.until("switched to the existing row", func(w window) bool { return w.activeKey == first })
	if len(d.w.sessions) != 2 {
		t.Fatalf("opening a session already in the window must not duplicate it: sessions=%d", len(d.w.sessions))
	}
}

type recordingStatusSource struct {
	mu      sync.Mutex
	handles []string
	closed  int
	changed chan struct{}
}

func (s *recordingStatusSource) Submit(in customization.Input) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handles = append(s.handles, in.Session.Handle)
}
func (s *recordingStatusSource) Changed() <-chan struct{}   { return s.changed }
func (*recordingStatusSource) Latest() customization.Result { return customization.Result{} }
func (s *recordingStatusSource) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed++
	return nil
}
func (s *recordingStatusSource) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.handles
	s.handles = nil
	return out
}

type recordingHook struct {
	mu     sync.Mutex
	events []string
}

func (h *recordingHook) record(event, id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, event+":"+id)
}
func (h *recordingHook) Start(_ context.Context, id string) { h.record("start", id) }
func (h *recordingHook) PermissionRequest(_ context.Context, id, _ string) {
	h.record("permission", id)
}
func (h *recordingHook) PermissionResult(_ context.Context, id string) { h.record("result", id) }
func (h *recordingHook) Stop(_ context.Context, id string, _ bool, _ string) {
	h.record("stop", id)
}

func TestTUIMultiSession_Scenario4_SharedDepsScopedToActive(t *testing.T) {
	conv := newWindowConv()
	holdB := make(chan struct{})
	conv.script("0002-sess-test", &heldRecver{script: streamScript("bravo", delta(3, "bravo-one"), delta(4, "bravo-two")), holds: map[int]chan struct{}{4: holdB}})
	source := &recordingStatusSource{changed: make(chan struct{})}
	hook := &recordingHook{}
	d := readyWindow(t, conv, func(deps *Deps) { deps.StatusSource = source; deps.AgentHook = hook })
	first := d.w.activeKey
	second := d.addCreated()
	firstHandle := client.SessionHandle(d.model(first).sessionID)
	secondHandle := client.SessionHandle(d.model(second).sessionID)

	source.take()
	d.send(windowSessionMsg{key: first, msg: client.SessionTitleMsg{Title: "background title", Revision: 1}})
	d.send(windowSessionMsg{key: first, msg: client.TurnStartMsg{Turn: 1}})
	for _, h := range source.take() {
		if h == firstHandle {
			t.Fatal("a background session wrote to the shared status source")
		}
	}
	d.submit("start bravo")
	d.until("bravo streaming", func(w window) bool {
		m, _ := w.modelFor(second)
		return strings.Contains(viewText(m), "bravo-one")
	})
	if got := source.take(); len(got) == 0 || got[len(got)-1] != secondHandle {
		t.Fatalf("the active session must keep writing the status source: %v", got)
	}

	d.switchTo(first)
	if got := source.take(); len(got) == 0 || got[len(got)-1] != firstHandle {
		t.Fatalf("switching must re-submit the newly active session: %v", got)
	}
	hook.mu.Lock()
	events := append([]string(nil), hook.events...)
	hook.mu.Unlock()
	for _, e := range events {
		if e == "start:"+d.model(first).sessionID {
			t.Fatalf("a background session notified the host hook: %v", events)
		}
	}

	w, _ := d.w.closeSession(first)
	d.w = w
	if source.closed != 0 {
		t.Fatal("closing one session must not close the shared status source")
	}
	if d.w.activeKey != second {
		t.Fatalf("closing the active session must activate the remaining one, got %d", d.w.activeKey)
	}
	close(holdB)
	d.until("the other session's stream keeps flowing", func(w window) bool {
		m, _ := w.modelFor(second)
		return m.phase == phaseIdle && strings.Contains(viewText(m), "bravo-two")
	})
}

func TestTUIMultiSession_Scenario4_ListLeadsToSavedSessions(t *testing.T) {
	lister := &fakeSessionLister{sessions: sampleSessions()}
	d := readyWindow(t, newWindowConv(), func(deps *Deps) {
		deps.Sessions = lister
		deps.Transcript = &fakeSessionTranscriptLoader{}
	})
	d.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	if view := stripANSIstr(d.w.View().Content); !strings.Contains(view, "/sessions") {
		t.Fatalf("the window list must name /sessions:\n%s", view)
	}
	d.press(tea.KeyPressMsg{Code: 'o', Text: "o"})
	if d.w.list != nil {
		t.Fatal("o must close the window list")
	}
	if m := d.w.activeModel(); sessionsSurface(&m) == nil {
		t.Fatal("o must open the /sessions picker")
	}

	bare := readyWindow(t, newWindowConv(), nil)
	bare.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	if view := stripANSIstr(bare.w.View().Content); !strings.Contains(view, "/sessions") {
		t.Fatalf("the hint must show even when no saved-session inventory is wired:\n%s", view)
	}
}

func keyText(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

// openListAt opens the window session list with the cursor on key's row.
func (d *windowDriver) openListAt(key int) {
	d.t.Helper()
	if d.w.list == nil {
		d.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	}
	if d.w.list == nil {
		d.t.Fatal("left did not open the window session list")
	}
	for i := 0; i < len(d.w.sessions); i++ {
		d.press(tea.KeyPressMsg{Code: tea.KeyUp})
	}
	for _, row := range d.w.listRows() {
		if row.key == key {
			return
		}
		d.press(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	d.t.Fatalf("no list row for session %d", key)
}

func windowView(d *windowDriver) string { return stripANSIstr(d.w.View().Content) }

func TestTUIMultiSession_Scenario5_NewSession(t *testing.T) {
	conv := newWindowConv()
	d := readyWindow(t, conv, nil)
	first := d.w.activeKey

	d.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	if view := windowView(d); !strings.Contains(view, "n: new session") {
		t.Fatalf("the window list must offer n:\n%s", view)
	}
	d.press(keyText('n'))
	if d.w.list != nil {
		t.Fatal("n must close the list and switch to the new session")
	}
	d.until("new session ready", func(w window) bool {
		m := w.activeModel()
		return w.activeKey != first && m.sessionID != "" && m.phase == phaseIdle
	})
	if len(d.w.sessions) != 2 || d.model(first).sessionID != "0001-sess-test" || d.w.activeModel().sessionID != "0002-sess-test" {
		t.Fatalf("n must add a second session and keep the first: sessions=%d", len(d.w.sessions))
	}
	for _, req := range conv.requests() {
		if req.NewWorktree {
			t.Fatalf("n must create on the default placement, got %+v", req)
		}
	}
	if row := d.w.listRows()[1]; row.branch != "" {
		t.Fatalf("a default-placement session has no branch, got %q", row.branch)
	}
}

func TestTUIMultiSession_Scenario5_NewWorktreeSession(t *testing.T) {
	conv := newWindowConv()
	conv.caps = client.Capabilities{CreateWorktrees: true}
	d := readyWindow(t, conv, nil)
	first := d.w.activeKey

	d.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	if view := windowView(d); !strings.Contains(view, "w: new worktree session") {
		t.Fatalf("a create_worktrees server must offer w:\n%s", view)
	}
	d.press(keyText('w'))
	d.until("worktree session shows its branch", func(w window) bool {
		m := w.activeModel()
		return w.activeKey != first && m.phase == phaseIdle && m.activePlacement.Branch == "mecatl/brave-otter"
	})
	reqs := conv.requests()
	if len(reqs) != 1 || !reqs[0].NewWorktree {
		t.Fatalf("w must create with NewWorktree, got %+v", reqs)
	}
	d.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	if view := windowView(d); !strings.Contains(view, "(mecatl/brave-otter)") {
		t.Fatalf("the new worktree session must show its branch:\n%s", view)
	}

	// A creation error is shown and adds no row.
	conv.mu.Lock()
	conv.worktreeErr = fmt.Errorf("create session: worktree creation is unavailable: unborn HEAD")
	conv.mu.Unlock()
	before := len(d.w.sessions)
	active := d.w.activeKey
	d.press(keyText('w'))
	d.until("creation error shown", func(w window) bool { return w.list != nil && w.list.notice != "" })
	if len(d.w.sessions) != before || d.w.activeKey != active {
		t.Fatalf("a failed create must add no row and keep the active session: sessions=%d/%d active=%d/%d", len(d.w.sessions), before, d.w.activeKey, active)
	}
	if view := windowView(d); !strings.Contains(view, "unborn HEAD") {
		t.Fatalf("the creation error must be shown:\n%s", view)
	}

	// Without create_worktrees the action is hidden and inert.
	plain := newWindowConv()
	bare := readyWindow(t, plain, nil)
	bare.press(tea.KeyPressMsg{Code: tea.KeyLeft})
	if view := windowView(bare); strings.Contains(view, "w: new worktree") {
		t.Fatalf("w must be hidden without create_worktrees:\n%s", view)
	}
	bare.press(keyText('w'))
	bare.settle()
	if len(bare.w.sessions) != 1 || len(plain.requests()) != 0 || plain.createCount != 1 {
		t.Fatalf("w must be inert without create_worktrees: sessions=%d reqs=%d creates=%d", len(bare.w.sessions), len(plain.requests()), plain.createCount)
	}
}

// windowDeleter records DeleteSession calls and replays scripted outcomes.
type windowDeleter struct {
	mu      sync.Mutex
	calls   []string
	opts    []client.DeleteSessionOptions
	results []client.DeleteSessionResult
	errs    []error
}

func (*windowDeleter) RenameSession(context.Context, string, string) (client.SessionSnapshot, error) {
	return client.SessionSnapshot{}, nil
}

func (f *windowDeleter) DeleteSession(_ context.Context, id string, opts client.DeleteSessionOptions) (client.DeleteSessionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.calls)
	f.calls = append(f.calls, id)
	f.opts = append(f.opts, opts)
	var res client.DeleteSessionResult
	var err error
	if n < len(f.results) {
		res = f.results[n]
	}
	if n < len(f.errs) {
		err = f.errs[n]
	}
	return res, err
}

func (f *windowDeleter) recorded() ([]string, []client.DeleteSessionOptions) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...), append([]client.DeleteSessionOptions(nil), f.opts...)
}

func inventoryRow(id string, removeWorktree bool) client.SessionListItem {
	return client.SessionListItem{ID: id, Kind: client.SessionKindMain, State: "idle",
		Capabilities: client.SessionInventoryCapabilities{PublicChat: true, Delete: true, RemoveWorktree: removeWorktree}}
}

func TestTUIMultiSession_Scenario6_DeleteStopsAndRemovesRow(t *testing.T) {
	conv := newWindowConv()
	hold := make(chan struct{})
	defer close(hold)
	conv.script("0001-sess-test", &heldRecver{script: streamScript("alpha", delta(3, "alpha-one"), delta(4, "alpha-two")), holds: map[int]chan struct{}{4: hold}})
	deleter := &windowDeleter{}
	lister := &fakeSessionLister{sessions: []client.SessionListItem{inventoryRow("0001-sess-test", false), inventoryRow("0002-sess-test", false), inventoryRow("0003-sess-test", false)}}
	d := readyWindow(t, conv, func(deps *Deps) { deps.SessionManagement = deleter; deps.Sessions = lister })
	first := d.w.activeKey
	d.submit("start alpha")
	d.until("alpha streaming", func(w window) bool {
		m, _ := w.modelFor(first)
		return strings.Contains(viewText(m), "alpha-one")
	})
	second := d.addCreated()
	third := d.addCreated()

	// Deleting the running background session asks first, then stops and deletes it.
	d.openListAt(first)
	if view := windowView(d); !strings.Contains(view, "d: delete") {
		t.Fatalf("the window list must offer d:\n%s", view)
	}
	d.press(keyText('d'))
	d.until("delete confirmation", func(w window) bool { return w.deleteConfirm != nil && !w.deleteConfirm.checking })
	if view := windowView(d); !strings.Contains(view, "Delete") || !strings.Contains(view, "stopped") || strings.Contains(view, "remove worktree") {
		t.Fatalf("confirmation must say a running session is stopped and offer no worktree removal:\n%s", view)
	}
	if ids, _ := deleter.recorded(); len(ids) != 0 {
		t.Fatal("d must not delete before confirmation")
	}
	d.press(keyText('y'))
	d.until("row removed", func(w window) bool { return len(w.sessions) == 2 })
	ids, opts := deleter.recorded()
	if len(ids) != 1 || ids[0] != "0001-sess-test" || !opts[0].StopActive || opts[0].RemoveWorktree {
		t.Fatalf("delete calls = %v %+v, want one stop_active delete of the first session", ids, opts)
	}
	if ctx := conv.lastRunCtx("0001-sess-test"); ctx == nil || ctx.Err() == nil {
		t.Fatal("deleting a session must end its stream")
	}
	if d.w.activeKey != third {
		t.Fatalf("deleting a background session must keep the active one, got %d", d.w.activeKey)
	}
	if view := windowView(d); !strings.Contains(view, "deleted") {
		t.Fatalf("the list must confirm the deletion:\n%s", view)
	}

	// Declining keeps the session.
	d.openListAt(second)
	d.press(keyText('d'))
	d.until("delete confirmation", func(w window) bool { return w.deleteConfirm != nil && !w.deleteConfirm.checking })
	d.press(tea.KeyPressMsg{Code: tea.KeyEscape})
	if d.w.deleteConfirm != nil || len(d.w.sessions) != 2 {
		t.Fatal("esc must cancel the deletion")
	}

	// Deleting the active session switches to the next row.
	d.openListAt(third)
	d.press(keyText('d'))
	d.until("delete confirmation", func(w window) bool { return w.deleteConfirm != nil && !w.deleteConfirm.checking })
	d.press(keyText('y'))
	d.until("active row removed", func(w window) bool { return len(w.sessions) == 1 })
	if d.w.activeKey != second {
		t.Fatalf("deleting the active session must switch to the remaining row, got %d want %d", d.w.activeKey, second)
	}

	// Deleting the last session opens a new default session.
	d.openListAt(second)
	d.press(keyText('d'))
	d.until("delete confirmation", func(w window) bool { return w.deleteConfirm != nil && !w.deleteConfirm.checking })
	d.press(keyText('y'))
	d.until("replacement session ready", func(w window) bool {
		m := w.activeModel()
		return len(w.sessions) == 1 && w.activeKey != second && m.sessionID != "" && m.phase == phaseIdle
	})
	if got := d.w.activeModel().sessionID; got != "0004-sess-test" {
		t.Fatalf("replacement session = %q, want a newly created one", got)
	}
	if ids, _ := deleter.recorded(); len(ids) != 3 {
		t.Fatalf("delete calls = %v, want 3", ids)
	}
}

func TestTUIMultiSession_Scenario6_DeleteOffersWorktreeRemoval(t *testing.T) {
	conv := newWindowConv()
	deleter := &windowDeleter{
		results: []client.DeleteSessionResult{{}, {WorktreeRemoved: false, WorktreeRetainedReason: "dirty"}, {WorktreeRemoved: true}},
		errs:    []error{fmt.Errorf("delete session: worktree removal refused: the worktree has uncommitted or untracked changes")},
	}
	lister := &fakeSessionLister{sessions: []client.SessionListItem{inventoryRow("0001-sess-test", false), inventoryRow("0002-sess-test", true), inventoryRow("0003-sess-test", true)}}
	d := readyWindow(t, conv, func(deps *Deps) { deps.SessionManagement = deleter; deps.Sessions = lister })
	first := d.w.activeKey
	second := d.addCreated()
	third := d.addCreated()

	// A row without remove_worktree offers only the plain delete; r is inert.
	d.openListAt(first)
	d.press(keyText('d'))
	d.until("plain confirmation", func(w window) bool { return w.deleteConfirm != nil && !w.deleteConfirm.checking })
	if view := windowView(d); strings.Contains(view, "remove worktree") {
		t.Fatalf("a row without remove_worktree must not offer it:\n%s", view)
	}
	d.press(keyText('r'))
	d.settle()
	if ids, _ := deleter.recorded(); len(ids) != 0 || d.w.deleteConfirm == nil {
		t.Fatalf("r must be inert without remove_worktree: calls=%v", ids)
	}
	d.press(tea.KeyPressMsg{Code: tea.KeyEscape})

	// A server-created worktree row offers removal and explains it.
	d.openListAt(second)
	d.press(keyText('d'))
	d.until("worktree confirmation", func(w window) bool { return w.deleteConfirm != nil && !w.deleteConfirm.checking })
	view := windowView(d)
	for _, fragment := range []string{"delete and remove worktree", "ignored files", "branch is kept"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("worktree confirmation is missing %q:\n%s", fragment, view)
		}
	}

	// A refusal is shown and the session is kept.
	d.press(keyText('r'))
	d.until("refusal shown", func(w window) bool { return w.deleteConfirm == nil && w.list != nil && w.list.notice != "" })
	if len(d.w.sessions) != 3 {
		t.Fatalf("a refused delete must keep the session, sessions=%d", len(d.w.sessions))
	}
	if view := windowView(d); !strings.Contains(view, "uncommitted") {
		t.Fatalf("the refusal must be shown:\n%s", view)
	}

	// A post-stop dirty worktree: chat deleted, worktree kept, with the reason.
	d.openListAt(second)
	d.press(keyText('d'))
	d.until("worktree confirmation", func(w window) bool { return w.deleteConfirm != nil && !w.deleteConfirm.checking })
	d.press(keyText('r'))
	d.until("row removed", func(w window) bool { return len(w.sessions) == 2 })
	if view := windowView(d); !strings.Contains(view, "chat deleted, worktree kept") || !strings.Contains(view, "uncommitted") {
		t.Fatalf("a kept worktree must be reported with its reason:\n%s", view)
	}

	// A clean removal reports that the branch is kept.
	d.openListAt(third)
	d.press(keyText('d'))
	d.until("worktree confirmation", func(w window) bool { return w.deleteConfirm != nil && !w.deleteConfirm.checking })
	d.press(keyText('r'))
	d.until("row removed", func(w window) bool { return len(w.sessions) == 1 })
	if view := windowView(d); !strings.Contains(view, "worktree removed") {
		t.Fatalf("a removed worktree must be reported:\n%s", view)
	}

	ids, opts := deleter.recorded()
	want := []string{"0002-sess-test", "0002-sess-test", "0003-sess-test"}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Fatalf("delete calls = %v, want %v", ids, want)
	}
	for _, o := range opts {
		if !o.StopActive || !o.RemoveWorktree {
			t.Fatalf("worktree deletes must send stop_active and remove_worktree, got %+v", opts)
		}
	}
}
