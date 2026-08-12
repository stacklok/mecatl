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

// mcpView is the active MCP overlay (none = the overlay is closed). The overlay
// is a read-only / pick-and-act surface layered over the conversation; it does
// not change the run phase, so it can be opened only while idle and is dismissed
// with esc. Stage D ships three: the inventory panel, the resource picker, and
// the prompt picker (the last gaining an arg-entry sub-state).
type mcpView int

const (
	mcpNone         mcpView = iota // overlay closed
	mcpPanel                       // read-only inventory (sources → servers → diagnostics)
	mcpResources                   // resource picker (list → read → preview)
	mcpResourcePrev                // a read resource's preview pane
	mcpPrompts                     // prompt picker (list → args → get)
	mcpPromptArgs                  // required-arg entry for the selected prompt
)

// mcpState holds all MCP overlay state on the Model. It is value-embedded so the
// Model stays a plain struct that Update copies. Slices are replaced wholesale on
// each RPC result (never mutated in place) so the value-copy semantics hold.
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
}

// argField is one required-argument input in the prompt-args sub-state.
type argField struct {
	name  string
	input textinput.Model
}

// openMCP opens an overlay and kicks off its initial RPC. Only callable while
// idle; returns the model unchanged otherwise. The command is the RPC; its result
// arrives as a client MCP msg handled in updateMCPMsg.
func (m Model) openMCP(v mcpView) (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.MCP == nil {
		return m, nil
	}
	m.ta.Blur() // overlay owns the keyboard while open
	m.mcp = mcpState{view: v, loading: true}
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
		m.mcp = mcpState{}
		_ = m.ta.Focus()
		return m, nil
	}
}

// closeMCP dismisses the overlay and returns focus to the prompt input.
func (m Model) closeMCP() (tea.Model, tea.Cmd) {
	m.mcp = mcpState{}
	cmd := m.ta.Focus()
	return m, cmd
}

// insertIntoInput drops text into the prompt textarea, closes the overlay, and
// sets a live "press <submit> to send" status hint — the single mechanism both
// the prompt path and the resource-insert path use to populate the input for a
// normal Converse turn. label names what was loaded (e.g. "loaded prompt foo").
func (m Model) insertIntoInput(text, label string) (tea.Model, tea.Cmd) {
	mm, cmd := m.closeMCP()
	m2 := mm.(Model)
	m2.ta.SetValue(text)
	m2.statusMsg = label + " — press " + firstKey(m2.keys.Submit, "enter") + " to send"
	m2.refreshView()
	return m2, cmd
}

// onMCPKey routes key presses while an overlay is open. esc closes the active
// overlay (stepping back from a preview/arg-entry to its list first); the rest is
// per-view navigation. Returns handled=false when no overlay is open so the
// caller falls through to the normal idle key handling.
func (m Model) onMCPKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.mcp.view == mcpNone {
		return m, nil, false
	}
	switch m.mcp.view {
	case mcpPanel:
		mm, cmd := m.onPanelKey(msg)
		return mm, cmd, true
	case mcpResources, mcpResourcePrev:
		mm, cmd := m.onResourceKey(msg)
		return mm, cmd, true
	case mcpPrompts:
		mm, cmd := m.onPromptListKey(msg)
		return mm, cmd, true
	case mcpPromptArgs:
		mm, cmd := m.onPromptArgsKey(msg)
		return mm, cmd, true
	default:
		mm, cmd := m.closeMCP()
		return mm, cmd, true
	}
}

// onPanelKey: the panel is read-only — esc closes it, r re-probes LIVE source
// status. r is a bare key safe here because the open overlay intercepts keys
// before the global ctrl+o/ctrl+r/ctrl+p open bindings (see keyMap.Refresh).
func (m Model) onPanelKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Close):
		return m.closeMCP()
	case key.Matches(msg, m.keys.Refresh):
		return m.refreshPanel()
	}
	return m, nil
}

// refreshPanel re-issues the inventory + groups fetch so the panel reflects the
// server's CURRENT MCP source status/diagnostics rather than the data last shown.
// It keeps the existing sources on screen (no flicker to empty) and flips the
// refreshing indicator; updateMCPMsg clears it and marks the panel "updated" when
// the fresh result lands. A second refresh while one is in flight is a no-op.
func (m Model) refreshPanel() (tea.Model, tea.Cmd) {
	if m.mcp.refreshing {
		return m, nil
	}
	m.mcp.refreshing = true
	m.mcp.errMsg = ""
	m.mcp.groupsDone = false
	m.mcp.groupsErr = false
	return m, tea.Batch(
		client.ListMcpSourcesCmd(m.deps.Ctx, m.deps.MCP),
		client.ListToolHiveGroupsCmd(m.deps.Ctx, m.deps.MCP),
	)
}

// onResourceKey handles the resource list and its preview. In the list, up/down
// move the cursor and enter reads the highlighted resource (→ preview). In the
// preview, enter inserts the resource text into the prompt input (then closes the
// overlay); esc steps back to the list.
func (m Model) onResourceKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.mcp.view == mcpResourcePrev {
		switch {
		case key.Matches(msg, m.keys.Close):
			m.mcp.view = mcpResources
			m.mcp.preview = ""
			return m, nil
		case key.Matches(msg, m.keys.Choose):
			return m.insertIntoInput(m.mcp.preview, "loaded resource")
		}
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Close):
		return m.closeMCP()
	case key.Matches(msg, m.keys.Up):
		if m.mcp.resCursor > 0 {
			m.mcp.resCursor--
		}
		return m, nil
	case key.Matches(msg, m.keys.Down):
		if m.mcp.resCursor < len(m.mcp.resources)-1 {
			m.mcp.resCursor++
		}
		return m, nil
	case key.Matches(msg, m.keys.Choose):
		if m.mcp.resCursor >= len(m.mcp.resources) {
			return m, nil
		}
		r := m.mcp.resources[m.mcp.resCursor]
		m.mcp.loading = true
		m.mcp.errMsg = ""
		return m, client.ReadMcpResourceCmd(m.deps.Ctx, m.deps.MCP, r.Server, r.URI)
	}
	return m, nil
}

// onPromptListKey handles the prompt list: navigate, then enter selects. If the
// selected prompt has required args, it transitions to arg entry; otherwise it
// gets the prompt straight away.
func (m Model) onPromptListKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Close):
		return m.closeMCP()
	case key.Matches(msg, m.keys.Up):
		if m.mcp.prCursor > 0 {
			m.mcp.prCursor--
		}
		return m, nil
	case key.Matches(msg, m.keys.Down):
		if m.mcp.prCursor < len(m.mcp.prompts)-1 {
			m.mcp.prCursor++
		}
		return m, nil
	case key.Matches(msg, m.keys.Choose):
		if m.mcp.prCursor >= len(m.mcp.prompts) {
			return m, nil
		}
		return m.selectPrompt(m.mcp.prompts[m.mcp.prCursor])
	}
	return m, nil
}

// selectPrompt either enters the required-arg sub-state (when the prompt has
// required arguments) or fetches the prompt immediately (no required args).
func (m Model) selectPrompt(p client.MCPPrompt) (tea.Model, tea.Cmd) {
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
		m.mcp.loading = true
		m.mcp.errMsg = ""
		return m, client.GetMcpPromptCmd(m.deps.Ctx, m.deps.MCP, p.Server, p.Name, nil)
	}
	fields[0].input.Focus()
	m.mcp.view = mcpPromptArgs
	m.mcp.argPrompt = p
	m.mcp.argFields = fields
	m.mcp.argCursor = 0
	return m, textinput.Blink
}

// onPromptArgsKey drives the required-arg entry: up/down move between fields,
// enter on the last field submits GetMcpPrompt with the collected args, esc steps
// back to the prompt list. Other keys feed the focused input.
func (m Model) onPromptArgsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Close):
		m.mcp.view = mcpPrompts
		m.mcp.argFields = nil
		return m, nil
	case msg.String() == keyMenuUp, msg.String() == "shift+tab":
		m.focusArg(m.mcp.argCursor - 1)
		return m, nil
	case msg.String() == keyMenuDown, msg.String() == "tab":
		m.focusArg(m.mcp.argCursor + 1)
		return m, nil
	case key.Matches(msg, m.keys.Choose):
		// enter on any field but the last advances; on the last, submits.
		if m.mcp.argCursor < len(m.mcp.argFields)-1 {
			m.focusArg(m.mcp.argCursor + 1)
			return m, nil
		}
		return m.submitPromptArgs()
	}
	var cmd tea.Cmd
	m.mcp.argFields[m.mcp.argCursor].input, cmd =
		m.mcp.argFields[m.mcp.argCursor].input.Update(msg)
	return m, cmd
}

// focusArg moves focus to field i (clamped), blurring the rest.
func (m *Model) focusArg(i int) {
	if i < 0 {
		i = 0
	}
	if i > len(m.mcp.argFields)-1 {
		i = len(m.mcp.argFields) - 1
	}
	for j := range m.mcp.argFields {
		if j == i {
			m.mcp.argFields[j].input.Focus()
		} else {
			m.mcp.argFields[j].input.Blur()
		}
	}
	m.mcp.argCursor = i
}

// submitPromptArgs validates the entered values and fires GetMcpPrompt. Every
// arg field is required (only required args get a field), so a blank value is a
// client input error: we surface it via the existing error-render seam
// (errCls=MCPErrInput), keep the form open, and focus the first empty field —
// without making the RPC round-trip.
func (m Model) submitPromptArgs() (tea.Model, tea.Cmd) {
	args := make(map[string]string, len(m.mcp.argFields))
	firstEmpty := -1
	for i, f := range m.mcp.argFields {
		v := strings.TrimSpace(f.input.Value())
		if v == "" && firstEmpty < 0 {
			firstEmpty = i
		}
		args[f.name] = v
	}
	if firstEmpty >= 0 {
		m.mcp.errCls = client.MCPErrInput
		m.mcp.errMsg = "required argument \"" + m.mcp.argFields[firstEmpty].name + "\" is empty"
		m.focusArg(firstEmpty)
		return m, nil
	}
	p := m.mcp.argPrompt
	m.mcp.loading = true
	m.mcp.errMsg = ""
	return m, client.GetMcpPromptCmd(m.deps.Ctx, m.deps.MCP, p.Server, p.Name, args)
}

// updateMCPMsg reduces the client MCP result/error msgs into the overlay. The
// success msgs land the data and clear the loading flag; MCPErrMsg lands the
// classified error for distinct rendering. A prompt "get" success closes the
// overlay and drops the rendered text into the prompt input (the existing
// Converse flow then sends it on enter). A resource read shows a preview pane.
func (m Model) updateMCPMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case client.MCPSourcesMsg:
		m.mcp.loading = false
		if m.mcp.refreshing {
			m.mcp.refreshing = false
			m.mcp.refreshed = true // panel now shows live-re-probed state, not the startup snapshot
		}
		m.mcp.sources = msg.Sources
		return m, nil, true
	case client.MCPGroupsMsg:
		m.mcp.groups = msg.Groups
		m.mcp.groupsDone = true
		return m, nil, true
	case client.MCPResourcesMsg:
		m.mcp.loading = false
		m.mcp.resources = msg.Resources
		if m.mcp.resCursor >= len(m.mcp.resources) {
			m.mcp.resCursor = 0
		}
		return m, nil, true
	case client.MCPResourceReadMsg:
		m.mcp.loading = false
		m.mcp.preview = joinContents(msg.Contents)
		m.mcp.view = mcpResourcePrev
		return m, nil, true
	case client.MCPPromptsMsg:
		m.mcp.loading = false
		m.mcp.prompts = msg.Prompts
		if m.mcp.prCursor >= len(m.mcp.prompts) {
			m.mcp.prCursor = 0
		}
		return m, nil, true
	case client.MCPPromptGotMsg:
		// Rendered prompt → prompt input buffer; close the overlay so the user can
		// review/edit and send it as a normal Converse turn with enter.
		mm, cmd := m.insertIntoInput(joinPromptMessages(msg.Messages), "loaded prompt "+msg.Name)
		return mm, cmd, true
	case client.MCPErrMsg:
		// A failed ToolHive-groups fetch is best-effort: degrade quietly so the
		// inventory panel still renders. The shared error seam is reserved for the
		// primary RPC of each overlay (sources/resources/prompts/get).
		if msg.Op == "list groups" {
			m.mcp.groupsErr = true
			m.mcp.groupsDone = true
			return m, nil, true
		}
		m.mcp.loading = false
		m.mcp.refreshing = false // a failed re-probe clears the indicator; the error is shown instead
		m.mcp.errCls = msg.Class
		m.mcp.errMsg = msg.Op + ": " + msg.Err.Error()
		return m, nil, true
	default:
		return m, nil, false
	}
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

// renderMCPOverlay draws the active overlay centred over the conversation region.
// It mirrors the permission modal's overlay treatment (a bordered card via
// lipgloss.Place). All server-derived strings are terminal-sanitized.
// hk carries the LIVE keyMap markings (issue #457) so every keyMap-backed
// navigation hint (Up/Down on lists, Choose, Close, Refresh) and the resource-
// preview collapse marker reference rebound chords. The prompt-argument form's
// raw arrow controls remain literal. Defaults stay byte-identical.
func renderMCPOverlay(th theme.Theme, st mcpState, caps client.Capabilities, hk helpKeys, width, height int) string {
	var body string
	switch st.view {
	case mcpPanel:
		body = renderMCPPanel(th, st, caps, hk)
	case mcpResources:
		body = renderResourceList(th, st, caps, hk)
	case mcpResourcePrev:
		body = renderResourcePreview(th, st, hk)
	case mcpPrompts:
		body = renderPromptList(th, st, caps, hk)
	case mcpPromptArgs:
		body = renderPromptArgs(th, st, hk)
	default:
		return ""
	}
	return centerCard(th, body, width, height)
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

// renderMCPPanel renders the read-only inventory: sources → servers → diagnostics.
// It also carries the startup-snapshot caveat in its footer copy.
func renderMCPPanel(th theme.Theme, st mcpState, caps client.Capabilities, hk helpKeys) string {
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("MCP inventory") + "\n")
	if footer := mcpStatusLine(th, st); footer != "" {
		b.WriteString(footer + "\n")
	}
	b.WriteString("\n")
	if !st.loading && len(st.sources) == 0 && st.errMsg == "" {
		b.WriteString(th.Style("muted").Render(mcpEmptyCopy(caps, "No MCP sources configured on this server.")) + "\n")
	}
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
	b.WriteString(th.Style("askTitle").Render("MCP resources") + "\n")
	if footer := mcpStatusLine(th, st); footer != "" {
		b.WriteString(footer + "\n")
	}
	b.WriteString("\n")
	if !st.loading && len(st.resources) == 0 && st.errMsg == "" {
		b.WriteString(th.Style("muted").Render(mcpEmptyCopy(caps, "No resources advertised by the connected MCP servers.")) + "\n")
	}
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
	b.WriteString(th.Style("askTitle").Render("MCP prompts") + "\n")
	if footer := mcpStatusLine(th, st); footer != "" {
		b.WriteString(footer + "\n")
	}
	b.WriteString("\n")
	if !st.loading && len(st.prompts) == 0 && st.errMsg == "" {
		b.WriteString(th.Style("muted").Render(mcpEmptyCopy(caps, "No prompts advertised by the connected MCP servers.")) + "\n")
	}
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
	// Arrow field navigation is a genuinely fixed form control (onPromptArgsKey
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
