package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

func (m Model) runMCP() (tea.Model, tea.Cmd)          { return m.openMCP(mcpPanel) }
func (m Model) runMCPResources() (tea.Model, tea.Cmd) { return m.openMCP(mcpResources) }
func (m Model) runMCPPrompts() (tea.Model, tea.Cmd)   { return m.openMCP(mcpPrompts) }

// openMCP opens the selected MCP surface and starts its initial RPC.
func (m Model) openMCP(v mcpView) (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.MCP == nil {
		return m, nil
	}
	m.ta.Blur() // modal owns the keyboard while open
	m.modal = &mcpState{view: v, loading: true, deps: (&m).surfaceDeps(), mcp: m.deps.MCP}
	switch v {
	case mcpPanel:
		// Fetch the inventory and the ToolHive groups in parallel; groups are
		// best-effort (rendered alongside the sources, degraded on failure).
		return m, tea.Batch(
			client.ListMcpSourcesCmd(m.deps.Ctx, m.deps.MCP),
			client.ListToolHiveGroupsCmd(m.deps.Ctx, m.deps.MCP),
		)
	case mcpResources:
		return m, client.ListMcpResourcesCmd(m.deps.Ctx, m.deps.MCP, "")
	case mcpPrompts:
		return m, client.ListMcpPromptsCmd(m.deps.Ctx, m.deps.MCP, "")
	default:
		m.closeModal()
		_ = m.ta.Focus()
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

	loading bool   // an RPC is in flight (panel/list/read/get)
	errMsg  string // last classified MCP error, rendered distinctly
	errCls  client.MCPErrorClass

	// Inventory panel.
	sources    []client.MCPSource
	groups     []string // ToolHive groups (best-effort; see groupsErr)
	groupsErr  bool     // the groups fetch failed — degrade quietly, panel still works
	groupsDone bool     // a groups result (success or error) has arrived

	// Panel live-refresh indicator. refreshing is set while a manual re-probe is
	// in flight (distinct from the initial loading so already-shown sources stay
	// on screen); refreshed becomes true once a re-probe result has landed, so the
	// footer can read "updated" instead of the startup-snapshot caveat. No
	// wall-clock is used (keeps the View golden-stable): the indicator is purely
	// state-driven (refreshing… → updated).
	refreshing bool
	refreshed  bool

	// Resource picker.
	resources []client.MCPResource
	resCursor int
	preview   string // the read resource text shown in mcpResourcePrev

	// Prompt picker.
	prompts   []client.MCPPrompt
	prCursor  int
	argPrompt client.MCPPrompt // the prompt awaiting argument entry
	argFields []argField       // one input per required argument
	argCursor int              // focused arg field

	deps surfaceDeps // the shared ambient base (incl. ctx), set once at Open
	mcp  client.MCP  // the surface-specific RPC client, set once at Open
}

// argField is one required-argument input in the prompt-args sub-state.
type argField struct {
	name  string
	input textinput.Model
}

// Render returns the MCP surface body; the parent centers it.
func (s *mcpState) Render(_, _ int) (string, []ClickableRegion) {
	th := s.deps.theme
	caps := s.deps.caps
	hk := s.deps.marks
	switch s.view {
	case mcpPanel:
		return renderMCPPanel(th, *s, caps, hk), nil
	case mcpResources:
		return renderResourceList(th, *s, caps, hk), nil
	case mcpResourcePrev:
		return renderResourcePreview(th, *s, hk), nil
	case mcpPrompts:
		return renderPromptList(th, *s, caps, hk), nil
	case mcpPromptArgs:
		return renderPromptArgs(th, *s, hk), nil
	default:
		return "", nil
	}
}

// HandleKey routes key presses while the MCP surface is open, dispatching
// internally on s.view. esc steps back from a preview/arg-entry to its list
// first, and closes the panel/list at the top level (closed=true). Every key is
// handled=true (the modal owns the keyboard). Key bindings read from s.deps.keys.
func (s *mcpState) HandleKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool, closed bool) {
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
// surface intercepts keys before the global ctrl+o/ctrl+r/ctrl+p open bindings
// (see keyMap.Refresh).
func (s *mcpState) handlePanelKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool, closed bool) {
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		return nil, true, true
	case key.Matches(msg, s.deps.keys.Refresh):
		return s.refreshPanel(), true, false
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
			s.view = mcpResources
			s.preview = ""
			return nil, true, false
		case key.Matches(msg, s.deps.keys.Choose):
			// Insert the preview into the prompt input — a Model-side mutation the
			// surface cannot perform. Snapshot it into the insertion marker cmd;
			// the Model's updateMCPMsg (HandleMsg passes it through handled=false)
			// performs the insert and nils the modal.
			return func() tea.Msg { return mcpInsertResourceMsg{preview: s.preview} }, true, false
		}
		return nil, true, false
	}
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		return nil, true, true
	case key.Matches(msg, s.deps.keys.Up):
		if s.resCursor > 0 {
			s.resCursor--
		}
		return nil, true, false
	case key.Matches(msg, s.deps.keys.Down):
		if s.resCursor < len(s.resources)-1 {
			s.resCursor++
		}
		return nil, true, false
	case key.Matches(msg, s.deps.keys.Choose):
		if s.resCursor >= len(s.resources) {
			return nil, true, false
		}
		r := s.resources[s.resCursor]
		s.loading = true
		s.errMsg = ""
		return client.ReadMcpResourceCmd(s.deps.ctx, s.mcp, r.Server, r.URI), true, false
	}
	return nil, true, false
}

// mcpInsertResourceMsg is the resource-preview insertion marker: it carries the
// read resource's preview text so the Model's updateMCPMsg can drop it into the
// prompt input after the surface closes (the preview lives on the surface, so it
// is snapshotted into the marker at key time).
type mcpInsertResourceMsg struct{ preview string }

// handlePromptListKey handles the prompt list: navigate, then enter selects. If
// the selected prompt has required args, it transitions to arg entry; otherwise
// it gets the prompt straight away. esc closes.
func (s *mcpState) handlePromptListKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool, closed bool) {
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		return nil, true, true
	case key.Matches(msg, s.deps.keys.Up):
		if s.prCursor > 0 {
			s.prCursor--
		}
		return nil, true, false
	case key.Matches(msg, s.deps.keys.Down):
		if s.prCursor < len(s.prompts)-1 {
			s.prCursor++
		}
		return nil, true, false
	case key.Matches(msg, s.deps.keys.Choose):
		if s.prCursor >= len(s.prompts) {
			return nil, true, false
		}
		return s.selectPrompt(s.prompts[s.prCursor]), true, false
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
		ti.Placeholder = a.Name
		fields = append(fields, argField{name: a.Name, input: ti})
	}
	if len(fields) == 0 {
		s.loading = true
		s.errMsg = ""
		return client.GetMcpPromptCmd(s.deps.ctx, s.mcp, p.Server, p.Name, nil)
	}
	fields[0].input.Focus()
	s.view = mcpPromptArgs
	s.argPrompt = p
	s.argFields = fields
	s.argCursor = 0
	return textinput.Blink
}

// handlePromptArgsKey drives the required-arg entry: up/down move between fields,
// enter on the last field submits GetMcpPrompt with the collected args, esc steps
// back to the prompt list (handled, NOT closed — it is a sub-view, not the top
// level). Other keys feed the focused input.
func (s *mcpState) handlePromptArgsKey(msg tea.KeyPressMsg) (cmd tea.Cmd, handled bool, closed bool) {
	switch {
	case key.Matches(msg, s.deps.keys.Close):
		s.view = mcpPrompts
		s.argFields = nil
		return nil, true, false
	case msg.String() == keyMenuUp, msg.String() == "shift+tab":
		s.focusArg(s.argCursor - 1)
		return nil, true, false
	case msg.String() == keyMenuDown, msg.String() == "tab":
		s.focusArg(s.argCursor + 1)
		return nil, true, false
	case key.Matches(msg, s.deps.keys.Choose):
		// enter on any field but the last advances; on the last, submits.
		if s.argCursor < len(s.argFields)-1 {
			s.focusArg(s.argCursor + 1)
			return nil, true, false
		}
		return s.submitPromptArgs(), true, false
	}
	var inputCmd tea.Cmd
	s.argFields[s.argCursor].input, inputCmd = s.argFields[s.argCursor].input.Update(msg)
	return inputCmd, true, false
}

// focusArg moves focus to field i (clamped), blurring the rest.
func (s *mcpState) focusArg(i int) {
	if i < 0 {
		i = 0
	}
	if i > len(s.argFields)-1 {
		i = len(s.argFields) - 1
	}
	for j := range s.argFields {
		if j == i {
			s.argFields[j].input.Focus()
		} else {
			s.argFields[j].input.Blur()
		}
	}
	s.argCursor = i
}

// submitPromptArgs validates the entered values and fires GetMcpPrompt. Every
// arg field is required (only required args get a field), so a blank value is a
// client input error: we surface it via the existing error-render seam
// (errCls=MCPErrInput), keep the form open, and focus the first empty field —
// without making the RPC round-trip.
func (s *mcpState) submitPromptArgs() tea.Cmd {
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
		s.focusArg(firstEmpty)
		return nil
	}
	p := s.argPrompt
	s.loading = true
	s.errMsg = ""
	return client.GetMcpPromptCmd(s.deps.ctx, s.mcp, p.Server, p.Name, args)
}

// HandleWheel consumes the wheel while the MCP modal is open: the modal captures
// input and has no wheel-scrollable pane of its own (the lists are cursor-driven,
// key-driven), so it returns handled=true (default-consume, mirroring soul).
func (*mcpState) HandleWheel(tea.MouseWheelMsg) (cmd tea.Cmd, handled bool) {
	return nil, true
}

// HandleMsg reduces MCP messages into surface state. Insertion messages pass
// through to the Model for textarea mutation.
//
//nolint:gocyclo // multiple MCP message types share one reducer
func (s *mcpState) HandleMsg(msg tea.Msg) (cmd tea.Cmd, handled bool, closed bool) {
	switch msg := msg.(type) {
	case client.MCPSourcesMsg:
		s.loading = false
		if s.refreshing {
			s.refreshing = false
			s.refreshed = true // panel now shows live-re-probed state, not the startup snapshot
		}
		s.sources = msg.Sources
		return nil, true, false
	case client.MCPGroupsMsg:
		s.groups = msg.Groups
		s.groupsDone = true
		return nil, true, false
	case client.MCPResourcesMsg:
		s.loading = false
		s.resources = msg.Resources
		if s.resCursor >= len(s.resources) {
			s.resCursor = 0
		}
		return nil, true, false
	case client.MCPResourceReadMsg:
		s.loading = false
		s.preview = joinContents(msg.Contents)
		s.view = mcpResourcePrev
		return nil, true, false
	case client.MCPPromptsMsg:
		s.loading = false
		s.prompts = msg.Prompts
		if s.prCursor >= len(s.prompts) {
			s.prCursor = 0
		}
		return nil, true, false
	case client.MCPPromptGotMsg:
		// Rendered prompt → prompt input buffer; close the surface so the user can
		// review/edit and send it as a normal Converse turn with enter. handled=false
		// makes dispatchSurfaceMsg return before its closed path, so this msg falls
		// through to the Model's updateMCPMsg, whose insertIntoInput nils the modal;
		// closed=true records that the surface is done with it.
		return nil, false, true
	case mcpInsertResourceMsg:
		// The resource-preview insertion marker: same shape as PromptGot —
		// handled=false falls through to the Model's updateMCPMsg insertion arm;
		// closed=true records the surface is done with it.
		return nil, false, true
	case client.MCPErrMsg:
		// A failed ToolHive-groups fetch is best-effort: degrade quietly so the
		// inventory panel still renders. The shared error seam is reserved for the
		// primary RPC of each view (sources/resources/prompts/get).
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

func (*mcpState) Close() {}

// updateMCPMsg handles insertion messages that require Model-owned textarea mutation.
func (m Model) updateMCPMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case client.MCPPromptGotMsg:
		mm, cmd := m.insertIntoInput(joinPromptMessages(msg.Messages), "loaded prompt "+msg.Name)
		return mm, cmd, true
	case mcpInsertResourceMsg:
		mm, cmd := m.insertIntoInput(msg.preview, "loaded resource")
		return mm, cmd, true
	default:
		return m, nil, false
	}
}

// insertIntoInput inserts text, closes the modal, and updates the status hint.
func (m Model) insertIntoInput(text, label string) (tea.Model, tea.Cmd) {
	m.closeModal()
	m.ta.SetValue(text)
	m.statusMsg = label + " — press " + firstKey(m.keys.Submit, "enter") + " to send"
	m.refreshView()
	return m, m.ta.Focus()
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

// renderMCPPanel renders the read-only inventory: sources → servers → diagnostics.
// It also carries the startup-snapshot caveat in its footer copy.
func renderMCPPanel(th theme.Theme, st mcpState, caps client.Capabilities, hk helpKeys) string {
	var b strings.Builder
	empty := !st.loading && len(st.sources) == 0 && st.errMsg == ""
	b.WriteString(renderMCPListHeader(th, st, caps, "MCP inventory", empty, "No MCP sources configured on this server."))
	for _, s := range st.sources {
		state := "enabled"
		if !s.Enabled {
			state = "disabled"
		}
		head := fmt.Sprintf("%s  [%s]  %s",
			sanitizeTerminal(s.Name), sanitizeTerminal(s.Kind), state)
		if s.Group != "" {
			head += "  group=" + sanitizeTerminal(s.Group)
		}
		b.WriteString(th.Style("toolName").Render(head) + "\n")
		for _, srv := range s.Servers {
			line := fmt.Sprintf("  • %s  %s  %s",
				sanitizeTerminal(srv.Name), sanitizeTerminal(srv.Transport), sanitizeTerminal(srv.URL))
			b.WriteString(th.Style("toolArgs").Render(line) + "\n")
		}
		for _, d := range s.Diagnostics {
			b.WriteString(th.Style("errorText").Render("  ! "+sanitizeTerminal(d)) + "\n")
		}
	}
	b.WriteString(renderGroupsLine(th, st))
	b.WriteString("\n" + th.Style("muted").Render(mcpPanelFooter(st, hk)))
	return b.String()
}

// mcpPanelFooter is the panel's footer hint. Before any manual refresh it carries
// the startup-snapshot caveat; after a successful re-probe it reads "updated" so
// the user knows the panel reflects LIVE source status. Both forms advertise the
// r-refresh and esc-close keys, sourced from the LIVE Refresh/Close markings
// (issue #457). No wall-clock — the wording is state-driven so the View stays
// golden-stable.
func mcpPanelFooter(st mcpState, hk helpKeys) string {
	refreshClose := hk.refresh + " refresh · " + hk.closeOnly + " close"
	switch {
	case st.refreshing:
		return "refreshing… · " + refreshClose
	case st.refreshed:
		return "updated — live MCP source status · " + refreshClose
	default:
		return "snapshot from mecated startup — servers started later won't appear · " + refreshClose
	}
}

// renderGroupsLine renders the best-effort ToolHive-groups line for the panel. It
// degrades quietly on a fetch error (a muted note) and handles the empty case.
func renderGroupsLine(th theme.Theme, st mcpState) string {
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
		clean[i] = sanitizeTerminal(g)
	}
	return "\n" + th.Style("toolName").Render("ToolHive groups: "+strings.Join(clean, ", ")) + "\n"
}

// renderResourceList renders the scrollable resource picker.
func renderResourceList(th theme.Theme, st mcpState, caps client.Capabilities, hk helpKeys) string {
	var b strings.Builder
	empty := !st.loading && len(st.resources) == 0 && st.errMsg == ""
	b.WriteString(renderMCPListHeader(th, st, caps, "MCP resources", empty, "No resources advertised by the connected MCP servers."))
	for i, r := range st.resources {
		label := r.Name
		if label == "" {
			label = r.URI
		}
		line := fmt.Sprintf("%s  %s", sanitizeTerminal(label), sanitizeTerminal(r.Server))
		b.WriteString(renderRow(th, line, i == st.resCursor) + "\n")
	}
	b.WriteString("\n" + th.Style("muted").Render(hk.navUp+"/"+hk.navDown+" move · "+hk.choose+" read · "+hk.closeOnly+" close"))
	return b.String()
}

// renderResourcePreview renders a read resource's text in a preview pane.
// hk carries the LIVE keyMap markings (issue #457): the ExpandTools chord for the
// collapse marker when the preview exceeds the line cap, and the Choose/Close
// chords for the insert/back footer.
func renderResourcePreview(th theme.Theme, st mcpState, hk helpKeys) string {
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("resource preview") + "\n\n")
	b.WriteString(th.Style("toolArgs").Render(truncateLinesTailMark(st.preview, maxToolResultLines, "", hk.expandTools)) + "\n")
	b.WriteString("\n" + th.Style("muted").Render(hk.choose+" insert into prompt · "+focusBackHint(hk)))
	return b.String()
}

// renderPromptList renders the scrollable prompt picker.
func renderPromptList(th theme.Theme, st mcpState, caps client.Capabilities, hk helpKeys) string {
	var b strings.Builder
	empty := !st.loading && len(st.prompts) == 0 && st.errMsg == ""
	b.WriteString(renderMCPListHeader(th, st, caps, "MCP prompts", empty, "No prompts advertised by the connected MCP servers."))
	for i, p := range st.prompts {
		marker := ""
		if hasRequiredArgs(p) {
			marker = "  (args)"
		}
		line := fmt.Sprintf("%s  %s%s", sanitizeTerminal(p.Name), sanitizeTerminal(p.Server), marker)
		b.WriteString(renderRow(th, line, i == st.prCursor) + "\n")
	}
	b.WriteString("\n" + th.Style("muted").Render(hk.navUp+"/"+hk.navDown+" move · "+hk.choose+" select · "+hk.closeOnly+" close"))
	return b.String()
}

// renderPromptArgs renders the required-argument entry form.
func renderPromptArgs(th theme.Theme, st mcpState, hk helpKeys) string {
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render(
		"arguments for "+sanitizeTerminal(st.argPrompt.Name)) + "\n")
	if line := mcpStatusLine(th, st); line != "" {
		b.WriteString(line + "\n")
	}
	b.WriteString("\n")
	for i, f := range st.argFields {
		label := th.Style("toolName").Render(sanitizeTerminal(f.name))
		if i == st.argCursor {
			label = "› " + label
		} else {
			label = "  " + label
		}
		b.WriteString(label + "\n  " + f.input.View() + "\n")
	}
	// Arrow field navigation is a genuinely fixed form control (handlePromptArgsKey
	// consumes the raw up/down strings); Choose and Close are keyMap-backed.
	b.WriteString("\n" + th.Style("muted").Render("↑/↓ field · "+hk.choose+" next/submit · "+focusBackHint(hk)))
	return b.String()
}

// renderRow renders one selectable list row, highlighting the cursor row.
func renderRow(th theme.Theme, text string, selected bool) string {
	if selected {
		return th.Style("askButtonActive").Render("› " + text)
	}
	return th.Style("toolArgs").Render("  " + text)
}

// mcpStatusLine renders the overlay's loading/error status: a classified error
// (distinct per class) takes precedence over the loading spinner-text.
func mcpStatusLine(th theme.Theme, st mcpState) string {
	if st.errMsg != "" {
		label := st.errCls.String()
		return th.Style("errorText").Render("✗ " + label + ": " + sanitizeTerminal(st.errMsg))
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
