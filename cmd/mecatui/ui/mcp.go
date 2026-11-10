package ui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/bounded"
)

func (m Model) runMCP() (tea.Model, tea.Cmd)          { return m.openMCP(mcpPanel) }
func (m Model) runMCPResources() (tea.Model, tea.Cmd) { return m.openMCP(mcpResources) }
func (m Model) runMCPPrompts() (tea.Model, tea.Cmd)   { return m.openMCP(mcpPrompts) }

func (m Model) runMCPRefresh() (tea.Model, tea.Cmd) {
	direct := m.caps.MCPRefresh
	broker := m.caps.WorkspaceEnrollment
	if direct == broker {
		m.statusMsg = m.deps.Theme.Style("warning").Render("MCP refresh is unavailable for this server mode")
		return m, nil
	}
	if broker {
		if m.deps.WorkspaceEnrollment == nil {
			m.statusMsg = m.deps.Theme.Style("warning").Render("MCP refresh collaborator is unavailable")
			return m, nil
		}
		return m.runToolsConnect()
	}
	refresher, ok := m.deps.MCP.(client.MCPRefresher)
	if !ok || refresher == nil || m.sessionID == "" || m.phase != phaseIdle {
		m.statusMsg = m.deps.Theme.Style("warning").Render("MCP refresh is available only for an idle active session")
		return m, nil
	}
	m.statusMsg = m.deps.Theme.Style("muted").Render("refreshing MCP tools…")
	m.mcpRefreshRequestToken++
	return m, client.RefreshMcpSourcesCmd(m.deps.Ctx, refresher, m.sessionID, m.mcpRefreshRequestToken)
}

// openMCP opens the selected MCP surface and starts its initial RPC.
func (m Model) openMCP(v mcpView) (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.MCP == nil || (v != mcpPanel && !m.caps.MCP) {
		return m, nil
	}
	m.prompt.Blur() // modal owns the keyboard while open
	m.mcpRequestToken++
	state := &mcpState{view: v, loading: true, deps: (&m).surfaceDeps(), mcp: m.deps.MCP, sessionID: m.sessionID, requestToken: m.mcpRequestToken}
	if v == mcpPanel {
		state.setup = m.brokerMCPSetupState()
	}
	if reader, ok := m.deps.MCP.(client.MCPConnectorReader); ok {
		state.broker = reader
		state.brokerMode = m.caps.MCPConnectorStatus
	}
	m.modal = state
	switch v {
	case mcpPanel:
		if m.caps.MCPConnectorStatus {
			if state.broker == nil {
				m.closeModal()
				_ = m.prompt.Focus()
				return m, nil
			}
			state.brokerGeneration++
			return m, client.ListMCPConnectorsCmd(m.deps.Ctx, state.broker, state.sessionID, state.requestToken, state.brokerGeneration)
		}
		// Fetch the inventory and the ToolHive groups in parallel; groups are
		// best-effort (rendered alongside the sources, degraded on failure).
		return m, tea.Batch(
			client.ListMcpSourcesCmd(m.deps.Ctx, m.deps.MCP),
			client.ListToolHiveGroupsCmd(m.deps.Ctx, m.deps.MCP),
		)
	case mcpResources:
		return m, state.pickerRequest("list resources", func(ctx context.Context) tea.Cmd {
			return client.ListMcpResourcesCmd(ctx, state.mcp, "")
		})
	case mcpPrompts:
		return m, state.pickerRequest("list prompts", func(ctx context.Context) tea.Cmd {
			return client.ListMcpPromptsCmd(ctx, state.mcp, "")
		})
	default:
		m.closeModal()
		_ = m.prompt.Focus()
		return m, nil
	}
}

// mcpView identifies the active MCP surface view.
type mcpView int

const (
	mcpNone         mcpView = iota // no view
	mcpPanel                       // read-only inventory (sources → servers → diagnostics)
	mcpResources                   // resource picker (list → read → preview)
	mcpResourcePrev                // a read resource's preview pane
	mcpPrompts                     // prompt picker (list → args → get)
	mcpPromptArgs                  // required-arg entry for the selected prompt
)

// mcpState holds the MCP surface state and its dependencies.
type mcpState struct {
	view mcpView

	zeroOffer bool // true after Render receives no usable surface geometry
	compact   bool // picker body or required controls do not fit

	loading bool   // an RPC is in flight (panel/list/read/get)
	errMsg  string // last classified MCP error, rendered distinctly
	errCls  client.MCPErrorClass

	// Inventory panel.
	sources     []client.MCPSource
	revision    uint64
	stale       bool
	reconciling bool
	groups      []string // ToolHive groups (best-effort; see groupsErr)
	groupsErr   bool     // the groups fetch failed — degrade quietly, panel still works
	groupsDone  bool     // a groups result (success or error) has arrived

	// Broker-only panel state. It is intentionally separate from direct MCP sources:
	// broker capability must never trigger direct source/resource/prompt/group RPCs.
	broker           client.MCPConnectorReader
	sessionID        string
	requestToken     uint64
	brokerGeneration uint64
	inventory        client.MCPConnectorInventory
	brokerMode       bool
	setup            brokerMCPSetupState

	// Panel live-refresh indicator. refreshing is set while a manual re-probe is
	// in flight (distinct from the initial loading so already-shown sources stay
	// on screen); refreshed becomes true once a re-probe result has landed, so the
	// footer can read "updated" instead of the startup-snapshot caveat. No
	// wall-clock is used (keeps the View golden-stable): the indicator is purely
	// state-driven (refreshing… → updated).
	refreshing bool
	refreshed  bool

	// Resource picker.
	resources       []client.MCPResource
	resList         bounded.List
	preview         string             // exact insertion payload, separate from display
	previewResource client.MCPResource // captured when the read starts
	previewLoaded   bool
	previewViewport bounded.Viewport
	previewDisplay  string
	previewRows     []string
	previewWidth    int

	// Prompt picker.
	prompts            []client.MCPPrompt
	prList             bounded.List
	argPrompt          client.MCPPrompt // the prompt awaiting argument entry
	argFields          []argField       // one input per required argument
	argCursor          int              // fields, then Cancel and Submit
	argOffset          int              // physical row offset in the field stack
	argFormGeneration  uint64
	argFocusGeneration uint64

	deps surfaceDeps // the shared ambient base (incl. ctx), set once at Open
	mcp  client.MCP  // the surface-specific RPC client, set once at Open

	pickerGeneration uint64
	pickerOp         string
	pickerCancel     context.CancelFunc
	intent           surfaceIntent
}

// mcpPickerResult wraps existing client messages without changing the wire API.
// Its provenance is captured when the command is built, never on completion.
type mcpPickerResult struct {
	owner      *mcpState
	generation uint64
	op         string
	view       mcpView
	result     tea.Msg
}

type promptInsertIntent struct{ text, label string }

func (promptInsertIntent) isSurfaceIntent() {}

func (s *mcpState) takeSurfaceIntent() surfaceIntent {
	intent := s.intent
	s.intent = nil
	return intent
}

func (s *mcpState) cancelPickerRequest() {
	if s.pickerCancel != nil {
		s.pickerCancel()
		s.pickerCancel = nil
	}
	s.pickerGeneration++
	s.pickerOp = ""
	s.loading = false
}

func (s *mcpState) pickerRequest(op string, build func(context.Context) tea.Cmd) tea.Cmd {
	s.cancelPickerRequest()
	ctx, cancel := context.WithCancel(s.deps.ctx)
	s.pickerCancel = cancel
	s.loading = true
	s.errMsg = ""
	s.pickerOp = op
	receipt := mcpPickerResult{owner: s, generation: s.pickerGeneration, op: op, view: s.view}
	cmd := build(ctx)
	return func() tea.Msg {
		result := receipt
		result.result = cmd()
		return result
	}
}

// mcpActive returns the open MCP modal state, if any.
func mcpActive(m Model) *mcpState {
	state, _ := m.modal.(*mcpState)
	return state
}

// brokerMCPSetupState projects the existing controller after Model validation.
// A stable owner-authorized idle session may explicitly refresh regardless of
// inventory/catalogue state; those facts never prove live connectivity.
type brokerMCPSetupState struct {
	eligible bool
	pending  bool
	busy     bool
	gen      uint64
	status   client.WorkspaceEnrollmentStatus
}

func (m Model) brokerMCPSetupState() brokerMCPSetupState {
	return brokerMCPSetupState{
		eligible: m.workspaceEnrollmentActive() && m.deps.WorkspaceEnrollment != nil && m.sessionID != "" &&
			m.phase == phaseIdle && m.sessionState == sessionStateIdle,
		pending: m.enrollment.ID != "" && m.enrollment.Status == client.WorkspaceEnrollmentPending,
		busy:    m.enrollment.busy,
		gen:     m.enrollment.controlGen,
		status:  m.enrollment.Status,
	}
}

func (s *mcpState) canConnect() bool {
	return s.brokerMode && !s.loading && !s.refreshing && s.setup.eligible &&
		!s.setup.pending && !s.setup.busy
}

func (m Model) syncMCPSetup() {
	if st := mcpActive(m); st != nil {
		st.setup = m.brokerMCPSetupState()
	}
}

// argField is one required-argument input in the prompt-args sub-state.
type argField struct {
	name  string
	input textinput.Model
}

// Render returns the MCP surface body; the parent centers it.
func (s *mcpState) Render(width, height int) (string, []ClickableRegion) {
	wasCompact := s.compact
	defer func() {
		if s.view == mcpPromptArgs && s.compact && !wasCompact {
			s.argFocusGeneration++
		}
	}()
	if width <= 0 || height <= 0 {
		s.zeroOffer = true
		s.compact = true
		return "", nil
	}
	s.zeroOffer = false
	s.compact = false
	th := s.deps.theme
	caps := s.deps.caps
	hk := s.deps.marks
	switch s.view {
	case mcpPanel:
		return renderMCPPanel(th, *s, caps, hk, width), nil
	case mcpResources, mcpPrompts:
		return s.renderPickerList(width, height), nil
	case mcpResourcePrev:
		return s.renderResourcePreview(width, height), nil
	case mcpPromptArgs:
		return s.renderPromptArgs(width, height), nil
	default:
		return "", nil
	}
}

// HandleKey routes key presses while the MCP surface is open, dispatching
// internally on s.view. esc steps back from a preview/arg-entry to its list
// first, and closes the panel/list at the top level (closed=true). Every key is
// handled=true (the modal owns the keyboard). Key bindings read from s.deps.keys.
func (s *mcpState) HandleKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool, closed bool) {
	if s.view != mcpPanel && s.loading && !key.Matches(msg, s.deps.keys.Close) {
		return nil, true, false
	}
	if (s.zeroOffer || s.compact) && !key.Matches(msg, s.deps.keys.Close) {
		return nil, true, false
	}
	switch s.view {
	case mcpPanel:
		return s.handlePanelKey(msg)
	case mcpResources, mcpResourcePrev:
		return s.handleResourceKey(msg)
	case mcpPrompts:
		return s.handlePromptListKey(msg)
	case mcpPromptArgs:
		return s.handlePromptArgsKey(msg)
	default:
		return nil, true, true
	}
}

// handlePanelKey drives the read-only inventory panel: esc closes it, r
// re-probes LIVE source status. r is a bare key safe here because the open
// surface intercepts keys before the global ctrl+o/ctrl+r/f8 open bindings
// (see keyMap.Refresh).
func (s *mcpState) handlePanelKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool, closed bool) {
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		return nil, true, true
	case key.Matches(msg, s.deps.keys.Refresh):
		return s.refreshPanel(), true, false
	case msg.String() == "c" && s.canConnect():
		msg := mcpWorkspaceEnrollmentActionMsg{action: connectAction, source: s, sessionID: s.sessionID, gen: s.setup.gen}
		return func() tea.Msg { return msg }, true, false
	case msg.String() == "x" && s.brokerMode && s.setup.pending && !s.setup.busy:
		msg := mcpWorkspaceEnrollmentActionMsg{action: mcpPickerActionCancel, source: s, sessionID: s.sessionID, gen: s.setup.gen}
		return func() tea.Msg { return msg }, true, false
	}
	return nil, true, false
}

// refreshPanel re-issues the inventory + groups fetch so the panel reflects the
// server's CURRENT MCP source status/diagnostics rather than the data last shown.
// It keeps the existing sources on screen (no flicker to empty) and flips the
// refreshing indicator; HandleMsg clears it and marks the panel "updated" when
// the fresh result lands. A second refresh while one is in flight is a no-op.
func (s *mcpState) refreshPanel() tea.Cmd {
	if s.refreshing {
		return nil
	}
	s.refreshing = true
	s.errMsg = ""
	if s.brokerMode && s.broker != nil {
		s.brokerGeneration++
		return client.ListMCPConnectorsCmd(s.deps.ctx, s.broker, s.sessionID, s.requestToken, s.brokerGeneration)
	}
	s.groupsDone = false
	s.groupsErr = false
	return tea.Batch(
		client.ListMcpSourcesCmd(s.deps.ctx, s.mcp),
		client.ListToolHiveGroupsCmd(s.deps.ctx, s.mcp),
	)
}

// handleResourceKey handles the resource list and its preview. In the list,
// up/down move the cursor and enter reads the highlighted resource (→ preview);
// esc closes. In the preview, enter inserts the resource text into the prompt
// input (then closes the surface); esc steps back to the list.
func (s *mcpState) handleResourceKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool, closed bool) {
	if s.view == mcpResourcePrev {
		switch {
		case key.Matches(msg, s.deps.keys.Close):
			s.cancelPickerRequest()
			s.view = mcpResources
			s.preview = ""
			return nil, true, false
		case key.Matches(msg, s.deps.keys.Choose) && s.previewLoaded:
			s.intent = promptInsertIntent{text: s.preview, label: "loaded resource"}
			return nil, true, false
		}
		if move, ok := boundedNavigationMove(msg, s.deps.keys); ok {
			s.previewViewport.Move(move, len(s.previewRows))
		}
		return nil, true, false
	}
	if key.Matches(msg, s.deps.keys.Close) {
		return nil, true, true
	}
	if move, ok := boundedNavigationMove(msg, s.deps.keys); ok {
		s.resList.Move(move)
	} else if key.Matches(msg, s.deps.keys.Choose) && len(s.resources) > 0 {
		r := s.resources[s.resList.Cursor()]
		s.previewResource = r
		s.previewLoaded = false
		return s.pickerRequest("read resource", func(ctx context.Context) tea.Cmd {
			return client.ReadMcpResourceCmd(ctx, s.mcp, r.Server, r.URI)
		}), true, false
	}
	return nil, true, false
}

// mcpWorkspaceEnrollmentActionMsg asks the Model to use its existing
// whole-bundle enrollment controls from the broker inventory.
type mcpWorkspaceEnrollmentActionMsg struct {
	action, sessionID string
	source            *mcpState
	gen               uint64
}

// handlePromptListKey handles the prompt list: navigate, then enter selects. If
// the selected prompt has required args, it transitions to arg entry; otherwise
// it gets the prompt straight away. esc closes.
func (s *mcpState) handlePromptListKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool, closed bool) {
	if key.Matches(msg, s.deps.keys.Close) {
		return nil, true, true
	}
	if move, ok := boundedNavigationMove(msg, s.deps.keys); ok {
		s.prList.Move(move)
	} else if key.Matches(msg, s.deps.keys.Choose) && len(s.prompts) > 0 {
		return s.selectPrompt(s.prompts[s.prList.Cursor()]), true, false
	}
	return nil, true, false
}

// selectPrompt either enters the required-arg sub-state (when the prompt has
// required arguments) or fetches the prompt immediately (no required args).
func (s *mcpState) selectPrompt(p client.MCPPrompt) tea.Cmd {
	var fields []argField
	for _, a := range p.Arguments {
		if !a.Required {
			continue
		}
		ti := textinput.New()
		ti.Prompt = ""
		ti.Placeholder = "required value"
		fields = append(fields, argField{name: a.Name, input: ti})
	}
	if len(fields) == 0 {
		return s.pickerRequest("get prompt", func(ctx context.Context) tea.Cmd {
			return client.GetMcpPromptCmd(ctx, s.mcp, p.Server, p.Name, nil)
		})
	}
	s.cancelPickerRequest()
	s.view = mcpPromptArgs
	s.argPrompt = p
	s.argFields = fields
	s.argOffset = 0
	s.argFormGeneration++
	return s.focusArg(0)
}

// submitPromptArgs validates the entered values and fires GetMcpPrompt. Every
// arg field is required (only required args get a field), so a blank value is a
// client input error: we surface it via the existing error-render seam
// (errCls=MCPErrInput), keep the form open, and focus the first empty field —
// without making the RPC round-trip.
func (s *mcpState) submitPromptArgs() tea.Cmd {
	if s.loading || s.compact || s.zeroOffer {
		return nil
	}
	args := make(map[string]string, len(s.argFields))
	firstEmpty := -1
	for i, f := range s.argFields {
		v := strings.TrimSpace(f.input.Value())
		if v == "" && firstEmpty < 0 {
			firstEmpty = i
		}
		args[f.name] = v
	}
	if firstEmpty >= 0 {
		s.errCls = client.MCPErrInput
		s.errMsg = "required argument \"" + s.argFields[firstEmpty].name + "\" is empty"
		return s.focusArg(firstEmpty)
	}
	s.argFocusGeneration++
	p := s.argPrompt
	return s.pickerRequest("get prompt", func(ctx context.Context) tea.Cmd {
		return client.GetMcpPromptCmd(ctx, s.mcp, p.Server, p.Name, args)
	})
}

// HandleWheel browses only the visible picker region, never changing selection.
func (s *mcpState) HandleWheel(msg tea.MouseWheelMsg) (cmd tea.Cmd, handled bool) {
	if !s.compact && !s.zeroOffer && !s.loading {
		if move, ok := boundedWheelMove(msg); ok {
			switch s.view {
			case mcpResources:
				s.resList.Scroll(move)
			case mcpPrompts:
				s.prList.Scroll(move)
			case mcpResourcePrev:
				s.previewViewport.Move(move, len(s.previewRows))
			}
		}
	}
	return nil, true
}

// handlePickerResult accepts only the active request in its originating view.
func (s *mcpState) handlePickerResult(receipt mcpPickerResult) {
	if receipt.owner != s || receipt.generation != s.pickerGeneration || receipt.op != s.pickerOp ||
		receipt.view != s.view || !s.loading {
		return
	}
	s.cancelPickerRequest()
	switch msg := receipt.result.(type) {
	case client.MCPResourcesMsg:
		s.setResources(msg.Resources)
	case client.MCPResourceReadMsg:
		s.preview = joinContents(msg.Contents)
		s.previewDisplay = connectDisplay(s.preview)
		s.previewRows, s.previewWidth = nil, 0
		s.previewViewport.Reset()
		s.previewLoaded = true
		s.view = mcpResourcePrev
	case client.MCPPromptsMsg:
		s.setPrompts(msg.Prompts)
	case client.MCPPromptGotMsg:
		s.intent = promptInsertIntent{text: joinPromptMessages(msg.Messages), label: "loaded prompt " + connectDisplay(msg.Name)}
	case client.MCPErrMsg:
		s.errCls = msg.Class
		s.errMsg = terminaltext.Sanitize(msg.Op + ": " + msg.Err.Error())
	}
}

// HandleMsg reduces owned picker receipts and existing panel messages.
//
//nolint:gocyclo // multiple MCP message types share one reducer
func (s *mcpState) HandleMsg(msg tea.Msg) (cmd tea.Cmd, handled bool, closed bool) {
	switch msg := msg.(type) {
	case mcpArgumentEditorMsg:
		if msg.owner != s || msg.form != s.argFormGeneration || msg.focus != s.argFocusGeneration || msg.field != s.argCursor {
			return nil, true, false
		}
		return s.updateArgument(msg.msg), true, false
	case tea.PasteMsg:
		return s.updateArgument(msg), true, false
	case mcpPickerResult:
		s.handlePickerResult(msg)
		return nil, true, false
	case client.MCPResourcesMsg, client.MCPResourceReadMsg, client.MCPPromptsMsg, client.MCPPromptGotMsg:
		return nil, true, false
	case client.MCPConnectorStatusMsg:
		if !s.brokerMode || msg.RequestToken != s.requestToken || msg.SessionID != s.sessionID || msg.Generation != s.brokerGeneration {
			return nil, true, false
		}
		s.loading = false
		s.refreshing = false
		s.refreshed = true
		s.inventory = msg.Inventory
		return nil, true, false
	case client.MCPConnectorErrMsg:
		if !s.brokerMode || msg.RequestToken != s.requestToken || msg.SessionID != s.sessionID || msg.Generation != s.brokerGeneration {
			return nil, true, false
		}
		s.loading = false
		s.refreshing = false
		s.errCls = msg.Class
		s.errMsg = "list broker catalogue: " + msg.Err.Error()
		return nil, true, false
	case client.MCPSourcesMsg:
		if s.view != mcpPanel {
			return nil, true, false
		}
		s.loading = false
		if s.refreshing {
			s.refreshing = false
			s.refreshed = true // panel now shows live-re-probed state, not the startup snapshot
		}
		s.sources = msg.Sources
		s.revision = msg.Revision
		s.stale = msg.Stale
		s.reconciling = msg.Reconciling
		return nil, true, false
	case client.MCPGroupsMsg:
		if s.view != mcpPanel {
			return nil, true, false
		}
		s.groups = msg.Groups
		s.groupsDone = true
		return nil, true, false
	case workspaceEnrollmentMsg:
		return nil, false, false
	case mcpWorkspaceEnrollmentActionMsg:
		return nil, false, false
	case client.MCPErrMsg:
		if s.view != mcpPanel || (msg.Op != "list sources" && msg.Op != "list groups") {
			return nil, true, false
		}
		// A failed ToolHive-groups fetch is best-effort: degrade quietly so the
		// inventory panel still renders. Picker errors require owned receipts above.
		if msg.Op == "list groups" {
			s.groupsErr = true
			s.groupsDone = true
			return nil, true, false
		}
		s.loading = false
		s.refreshing = false // a failed re-probe clears the indicator; the error is shown instead
		s.errCls = msg.Class
		s.errMsg = msg.Op + ": " + msg.Err.Error()
		return nil, true, false
	default:
		return nil, false, false
	}
}

func (s *mcpState) Close() {
	s.argFormGeneration++
	for i := range s.argFields {
		s.argFields[i].input.Blur()
	}
	s.cancelPickerRequest()
	s.intent = nil
}

// updateMCPMsg preserves the unrelated authorization and enrollment reducers.
func (m Model) updateMCPMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	if model, cmd, handled := m.updateMCPAuthorizationMsg(msg); handled {
		return model, cmd, true
	}
	switch msg := msg.(type) {
	case mcpWorkspaceEnrollmentActionMsg:
		state := mcpActive(m)
		if state == nil || state != msg.source || msg.sessionID != m.sessionID || msg.gen != m.enrollment.controlGen {
			return m, nil, true
		}
		m.syncMCPSetup()
		if m.enrollment.busy || (msg.action == connectAction && !state.canConnect()) ||
			(msg.action == mcpPickerActionCancel && !state.setup.pending) {
			return m, nil, true
		}
		var mm tea.Model
		var cmd tea.Cmd
		switch msg.action {
		case connectAction:
			mm, cmd = m.runToolsConnect()
		case mcpPickerActionCancel:
			mm, cmd = m.runToolsCancel()
		default:
			return m, nil, true
		}
		model := mm.(Model)
		if state, ok := model.modal.(*mcpState); ok {
			state.setup = model.brokerMCPSetupState()
		}
		return model, cmd, true
	default:
		return m, nil, false
	}
}

// insertIntoInput inserts text, closes the modal, and updates the status hint.
func (m Model) insertIntoInput(text, label string) (tea.Model, tea.Cmd) {
	m.closeModal()
	m.prompt.Rewrite(text)
	m.statusMsg = label + " — press " + firstKey(m.keys.Submit, "enter") + " to send"
	m.refreshView()
	return m, m.prompt.Focus()
}

// joinContents flattens read-resource contents into a previewable string. Binary
// blobs (no Text) are summarised rather than dumped.
func joinContents(cs []client.MCPResourceContents) string {
	var b strings.Builder
	for i, c := range cs {
		if i > 0 {
			b.WriteString("\n")
		}
		switch {
		case c.Text != "":
			b.WriteString(c.Text)
		case len(c.Blob) > 0:
			fmt.Fprintf(&b, "[binary %s, %d bytes]", c.MimeType, len(c.Blob))
		default:
			b.WriteString("[empty]")
		}
	}
	return b.String()
}

// joinPromptMessages renders a got prompt's messages into a single prompt-input
// string, prefixing each with its role for context.
func joinPromptMessages(ms []client.MCPPromptMessage) string {
	var b strings.Builder
	for i, msg := range ms {
		if i > 0 {
			b.WriteString("\n\n")
		}
		if msg.Role != "" {
			b.WriteString(msg.Role + ": ")
		}
		b.WriteString(msg.Text)
	}
	return b.String()
}

// mcpDisabledNote is the empty-inventory copy for an MCP overlay when MCP is NOT
// enabled on the connected server (caps.MCP == false), with the remedy. This is
// the Option-C payoff: the relayed caps let the ui distinguish "not enabled" from
// "enabled but empty", which a ui-local guess never could for an external server.
const mcpDisabledNote = "MCP is not enabled on this server.\n" +
	"Run a full mecated with MCP configured (or pass --server to one) to use it."

// mcpEmptyCopy returns the empty-state line for an MCP list: the "not enabled"
// note (with remedy) when caps.MCP is false, else the "enabled but empty" note.
func mcpEmptyCopy(caps client.Capabilities, emptyNote string) string {
	if !caps.MCP {
		return mcpDisabledNote
	}
	return emptyNote
}

// renderMCPListHeader renders the shared title, status, and empty-state fragment
// for an MCP inventory list.
func renderMCPListHeader(th theme.Theme, st mcpState, caps client.Capabilities, title string, empty bool, emptyNote string) string {
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render(title) + "\n")
	if status := mcpStatusLine(th, st); status != "" {
		b.WriteString(status + "\n")
	}
	b.WriteString("\n")
	if empty {
		b.WriteString(th.Style("muted").Render(mcpEmptyCopy(caps, emptyNote)) + "\n")
	}
	return b.String()
}

// renderMCPInventoryRow wraps raw inventory text before styling it. An omitted
// width preserves the existing direct-render callers while the modal surface
// passes its parent-derived content width.
func renderMCPInventoryRow(style theme.Theme, styleName, text string, width int) string {
	return renderToolCardText(style.Style(styleName), text, width)
}

// renderMCPPanel renders the read-only inventory: sources → servers → diagnostics.
// It also carries the startup-snapshot caveat in its footer copy.
func renderMCPPanel(th theme.Theme, st mcpState, caps client.Capabilities, hk helpKeys, widths ...int) string {
	width := 0
	if len(widths) > 0 {
		width = widths[0]
	}
	var b strings.Builder
	if st.brokerMode {
		return renderBrokerMCPPanel(th, st, hk, width)
	}
	empty := !st.loading && len(st.sources) == 0 && st.errMsg == ""
	b.WriteString(renderMCPListHeader(th, st, caps, "MCP inventory", empty, "No MCP sources configured on this server."))
	for _, s := range st.sources {
		state := "enabled"
		if !s.Enabled {
			state = "disabled"
		}
		head := fmt.Sprintf("%s  [%s]  %s",
			terminaltext.Sanitize(s.Name), terminaltext.Sanitize(s.Kind), state)
		if s.Group != "" {
			head += "  group=" + terminaltext.Sanitize(s.Group)
		}
		b.WriteString(renderMCPInventoryRow(th, "toolName", head, width) + "\n")
		for _, srv := range s.Servers {
			line := fmt.Sprintf("  • %s  %s  %s",
				terminaltext.Sanitize(srv.Name), terminaltext.Sanitize(srv.Transport), terminaltext.Sanitize(srv.URL))
			b.WriteString(renderMCPInventoryRow(th, "toolArgs", line, width) + "\n")
		}
		for _, d := range s.Diagnostics {
			b.WriteString(renderMCPInventoryRow(th, "errorText", "  ! "+terminaltext.Sanitize(d), width) + "\n")
		}
	}
	b.WriteString(renderGroupsLine(th, st, width))
	b.WriteString("\n" + th.Style("muted").Render(mcpPanelFooter(st, hk)))
	return b.String()
}

// renderBrokerMCPPanel renders the broker inventory without projecting machine
// status tokens or making connection, authorization, or readiness claims.
func renderBrokerMCPPanel(th theme.Theme, st mcpState, hk helpKeys, width int) string {
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("MCP inventory") + "\n")
	if line := mcpStatusLine(th, st); line != "" {
		b.WriteString(line + "\n")
	}
	if !st.loading && st.inventory.Availability != "available" {
		state := "Status unavailable"
		if st.inventory.Availability == unavailableText {
			state = "Broker state unavailable"
		}
		b.WriteString(th.Style("muted").Render(state) + "\n")
	} else if !st.loading {
		label := brokerEnrollmentLabel(st.inventory.EnrollmentState)
		switch {
		case st.setup.pending:
			label = "Setup in progress"
		case st.setup.status == client.WorkspaceEnrollmentConnected:
			label = "Catalogue ready"
		case st.setup.status == client.WorkspaceEnrollmentCancelled:
			label = "No active setup"
		}
		b.WriteString(renderMCPInventoryRow(th, "toolArgs", "Enrollment: "+label, width) + "\n")
		for _, row := range st.inventory.Connectors {
			catalogue := brokerCatalogueLabel(row.CatalogueState)
			count := fmt.Sprintf("%d", row.ToolCount)
			if row.CatalogueState != "declared" && row.CatalogueState != "discovered" {
				count = "—"
			}
			b.WriteString(renderMCPInventoryRow(th, "toolName", fmt.Sprintf("%s  %s", terminaltext.Sanitize(row.Name), catalogue), width) + "\n")
			b.WriteString(renderMCPInventoryRow(th, "toolArgs", "  "+count+" tools", width) + "\n")
		}
		if st.inventory.Truncated {
			b.WriteString(th.Style("muted").Render("Connector list truncated.") + "\n")
		}
	}
	b.WriteString("\n" + th.Style("muted").Render("Catalogue status · not a live connection check") + "\n")
	if setup := brokerMCPSetupLine(st); setup != "" {
		b.WriteString(th.Style("muted").Render(setup) + "\n")
	}
	b.WriteString("\n" + th.Style("muted").Render(hk.refresh+" refresh local state · "+hk.closeOnly+" close"))
	return b.String()
}

func brokerMCPSetupLine(st mcpState) string {
	switch {
	case st.setup.busy:
		return ""
	case st.setup.pending:
		return "x cancel setup"
	case st.canConnect():
		return "c connect tools"
	default:
		return ""
	}
}

func brokerEnrollmentLabel(state string) string {
	switch state {
	case "not_required":
		return "No setup required"
	case "not_started":
		return "No active setup"
	case "pending":
		return "Setup in progress"
	case "completed":
		return "Catalogue ready"
	default:
		return "Status unavailable"
	}
}

func brokerCatalogueLabel(state string) string {
	switch state {
	case "hidden":
		return "Awaiting discovery"
	case "declared":
		return "Tools declared"
	case "discovered":
		return "Tools discovered"
	default:
		return "Status unavailable"
	}
}

// mcpPanelFooter reports cached reconciler status. The r key reloads that cache;
// explicit direct/broker mutation is the separate /mcp-refresh command.
func mcpPanelFooter(st mcpState, hk helpKeys) string {
	refreshClose := hk.refresh + " reload status · " + hk.closeOnly + " close"
	prefix := fmt.Sprintf("revision %d · cached", st.revision)
	switch {
	case st.refreshing:
		return "reloading status… · " + refreshClose
	case st.reconciling:
		return prefix + " · reconciling… · " + refreshClose
	case st.stale:
		return prefix + " · stale · " + refreshClose
	case st.refreshed:
		return prefix + " · updated · " + refreshClose
	default:
		return prefix + " · " + refreshClose
	}
}

// renderGroupsLine renders the best-effort ToolHive-groups line for the panel. It
// degrades quietly on a fetch error (a muted note) and handles the empty case.
func renderGroupsLine(th theme.Theme, st mcpState, widths ...int) string {
	width := 0
	if len(widths) > 0 {
		width = widths[0]
	}
	if !st.groupsDone {
		return ""
	}
	if st.groupsErr {
		return "\n" + th.Style("muted").Render("ToolHive groups: unavailable") + "\n"
	}
	if len(st.groups) == 0 {
		return "\n" + th.Style("muted").Render("ToolHive groups: none") + "\n"
	}
	clean := make([]string, len(st.groups))
	for i, g := range st.groups {
		clean[i] = terminaltext.Sanitize(g)
	}
	return "\n" + renderMCPInventoryRow(th, "toolName", "ToolHive groups: "+strings.Join(clean, ", "), width) + "\n"
}

// mcpStatusLine renders the overlay's loading/error status: a classified error
// (distinct per class) takes precedence over the loading spinner-text.
func mcpStatusLine(th theme.Theme, st mcpState) string {
	if st.errMsg != "" {
		label := st.errCls.String()
		return th.Style("errorText").Render("✗ " + label + ": " + terminaltext.Sanitize(st.errMsg))
	}
	if st.loading {
		return th.Style("muted").Render("loading…")
	}
	return ""
}

// hasRequiredArgs reports whether a prompt has any required argument.
func hasRequiredArgs(p client.MCPPrompt) bool {
	for _, a := range p.Arguments {
		if a.Required {
			return true
		}
	}
	return false
}
