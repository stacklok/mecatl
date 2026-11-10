package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// Keep the retired marker shape as a negative control, not a production route.
type mcpInsertResourceMsg struct{ preview string }

// This client deliberately ignores cancellation, as a remote completion may race it.
type pickerMCP struct {
	*fakeMCP
	contexts []context.Context
}

func (f *pickerMCP) ListMCPResources(ctx context.Context, server string) ([]client.MCPResource, error) {
	f.contexts = append(f.contexts, ctx)
	return f.fakeMCP.ListMCPResources(ctx, server)
}
func (f *pickerMCP) ListMCPPrompts(ctx context.Context, server string) ([]client.MCPPrompt, error) {
	f.contexts = append(f.contexts, ctx)
	return f.fakeMCP.ListMCPPrompts(ctx, server)
}
func (f *pickerMCP) ReadMCPResource(ctx context.Context, server, uri string) ([]client.MCPResourceContents, error) {
	f.contexts = append(f.contexts, ctx)
	return f.fakeMCP.ReadMCPResource(ctx, server, uri)
}
func (f *pickerMCP) GetMCPPrompt(ctx context.Context, server, name string, args map[string]string) (string, []client.MCPPromptMessage, error) {
	f.contexts = append(f.contexts, ctx)
	return f.fakeMCP.GetMCPPrompt(ctx, server, name, args)
}

func pickerKey(m Model, code rune) (Model, tea.Cmd) {
	mm, cmd := m.Update(tea.KeyPressMsg{Code: code})
	return mm.(Model), cmd
}

func TestMCPPickerInventoryCloseReopen(t *testing.T) {
	for _, resources := range []bool{true, false} {
		name, open := "prompts", tea.KeyPressMsg{Code: tea.KeyF8}
		if resources {
			name, open = "resources", ctrlKey('r')
		}
		t.Run(name, func(t *testing.T) {
			f := &pickerMCP{fakeMCP: samplePromptMCP()}
			if resources {
				f.fakeMCP = sampleResourceMCP()
			}
			m := newMCPModel(t, aztec(), f)
			mm, cmd := m.Update(open)
			m = mm.(Model)
			m, _ = pickerKey(m, tea.KeyEsc)
			mm, current := m.Update(open)
			m = mm.(Model)
			// Execute the abandoned command only after reopening. It must capture the
			// old lifetime, even though the fake returns success on a canceled context.
			old := runCmd(cmd)
			if f.contexts[0].Err() != context.Canceled {
				t.Fatal("close did not cancel inventory context")
			}
			m = applyAll(m, old)
			st := mcpActive(m)
			if !st.loading || len(st.resources) != 0 || len(st.prompts) != 0 {
				t.Fatal("old inventory changed reopened picker")
			}
			m = applyAll(m, runCmd(current))
			if st.loading || len(st.resources)+len(st.prompts) != 2 {
				t.Fatal("current inventory not accepted")
			}
			applyAll(m, client.MCPErrMsg{Op: "list sources", Class: client.MCPErrServer, Err: status.Error(codes.Internal, "unowned")})
			if st.errMsg != "" {
				t.Fatal("unowned error changed picker")
			}
		})
	}
}

func TestMCPPickerInventoryFailureReopen(t *testing.T) {
	for _, open := range []tea.KeyPressMsg{ctrlKey('r'), {Code: tea.KeyF8}} {
		f := &fakeMCP{err: status.Error(codes.FailedPrecondition, "not configured")}
		m := newMCPModel(t, aztec(), f)
		mm, cmd := m.Update(open)
		m = mm.(Model)
		failure := runCmd(cmd)
		m = applyAll(m, failure)
		st := mcpActive(m)
		if st.loading || st.errCls != client.MCPErrNotConfigured || st.errMsg == "" {
			t.Fatal("inventory failure was not classified in the open view")
		}
		m, _ = pickerKey(m, tea.KeyEsc)
		f.err = nil
		mm, cmd = m.Update(open)
		m = mm.(Model)
		m = applyAll(m, failure)
		if !mcpActive(m).loading || mcpActive(m).errMsg != "" {
			t.Fatal("abandoned inventory error changed reopened view")
		}
		m = applyAll(m, runCmd(cmd))
		if mcpActive(m).loading || mcpActive(m).errMsg != "" {
			t.Fatal("inventory could not retry by reopening")
		}
		m.closeModal()
	}
}

func TestMCPPickerPendingFailureRetryAndInsertion(t *testing.T) {
	for _, kind := range []string{"resource", "prompt", "arguments"} {
		t.Run(kind, func(t *testing.T) {
			f := &pickerMCP{fakeMCP: samplePromptMCP()}
			open := tea.KeyPressMsg{Code: tea.KeyF8}
			if kind == "resource" {
				f.fakeMCP, open = sampleResourceMCP(), ctrlKey('r')
			}
			m := openOverlay(t, newMCPModel(t, aztec(), f), open)
			m.prompt.Rewrite("draft")
			if kind == "prompt" {
				m, _ = pickerKey(m, tea.KeyDown)
			}
			if kind == "arguments" {
				m, _ = pickerKey(m, tea.KeyEnter)
				m = applyAll(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
				m, _ = pickerKey(m, tea.KeyTab)
				m, _ = pickerKey(m, tea.KeyTab)
			}
			st := mcpActive(m)
			origin := st.view
			m, cmd := pickerKey(m, tea.KeyEnter)
			m, duplicate := pickerKey(m, tea.KeyEnter)
			if duplicate != nil {
				t.Fatal("duplicate activation started another command")
			}
			m = applyAll(m, client.SessionTitleMsg{Title: "root while pending", Revision: 1})
			if m.sessionTitle != "root while pending" || !st.loading {
				t.Fatal("pending picker swallowed unrelated root update")
			}
			m = applyAll(m, tea.KeyPressMsg{Code: 'y', Text: "y"}, tea.PasteMsg{Content: "paste"})
			if kind == "arguments" && st.argFields[0].input.Value() != "x" {
				t.Fatal("pending form was edited")
			}
			f.err = status.Error(codes.Unavailable, "failed\x1b[31m")
			failed := runCmd(cmd)
			m = applyAll(m, failed)
			if st.loading || st.view != origin || st.errCls != client.MCPErrServer || st.errMsg == "" {
				t.Fatal("failure did not stay in originating view")
			}
			if strings.Contains(st.errMsg, "\x1b") {
				t.Fatal("error retained terminal controls")
			}
			f.err = nil
			m, cmd = pickerKey(m, tea.KeyEnter)
			m = applyAll(m, failed)
			if !st.loading || st.errMsg != "" {
				t.Fatal("duplicate failure superseded retry")
			}
			success := runCmd(cmd)
			m = applyAll(m, success)
			if kind == "resource" {
				if st.view != mcpResourcePrev || m.prompt.Value() != "draft" {
					t.Fatal("resource read did not open preview")
				}
				// Insertion is synchronous; no delayed marker needs delivery.
				m, _ = pickerKey(m, tea.KeyEnter)
			}
			expected := joinPromptMessages(f.promptMsg)
			if kind == "resource" {
				expected = joinContents(f.contents)
			}
			if m.modal != nil || m.prompt.Value() != expected || !m.prompt.Focused() {
				t.Fatalf("insertion failed: modal=%T draft=%q", m.modal, m.prompt.Value())
			}
			m.prompt.Rewrite("newer draft")
			m = applyAll(m, success, failed)
			if m.prompt.Value() != "newer draft" || m.modal != nil {
				t.Fatal("duplicate result inserted twice")
			}
			for _, ctx := range f.contexts {
				if ctx.Err() != context.Canceled {
					t.Fatal("completed request context not released")
				}
			}
		})
	}
}

func TestMCPPickerAbandonedActionResults(t *testing.T) {
	for _, kind := range []string{"resource", "prompt", "arguments"} {
		for _, fail := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/success", true: "/failure"}[fail], func(t *testing.T) {
				f := &pickerMCP{fakeMCP: samplePromptMCP()}
				open := tea.KeyPressMsg{Code: tea.KeyF8}
				if kind == "resource" {
					f.fakeMCP, open = sampleResourceMCP(), ctrlKey('r')
				}
				m := openOverlay(t, newMCPModel(t, aztec(), f), open)
				if kind == "prompt" {
					m, _ = pickerKey(m, tea.KeyDown)
				}
				if kind == "arguments" {
					m, _ = pickerKey(m, tea.KeyEnter)
					m = applyAll(m, tea.KeyPressMsg{Code: 'x', Text: "x"})
					m, _ = pickerKey(m, tea.KeyTab)
					m, _ = pickerKey(m, tea.KeyTab)
				}
				m, cmd := pickerKey(m, tea.KeyEnter)
				m, _ = pickerKey(m, tea.KeyEsc)
				if kind == "arguments" {
					if mcpActive(m).view != mcpPrompts {
						t.Fatal("Back did not restore list")
					}
					m, _ = pickerKey(m, tea.KeyDown)
				} else {
					m = openOverlay(t, m, open)
					if kind == "prompt" {
						m, _ = pickerKey(m, tea.KeyDown)
					}
				}
				if fail {
					f.err = status.Error(codes.Internal, "abandoned")
				}
				old := runCmd(cmd)
				if f.contexts[len(f.contexts)-1].Err() != context.Canceled {
					t.Fatal("abandoned RPC not canceled")
				}
				f.err = nil
				// Start new work before the old receipt lands to expose mutable-generation bugs.
				m, current := pickerKey(m, tea.KeyEnter)
				st := mcpActive(m)
				view := st.view
				m.prompt.Rewrite("new draft")
				m = applyAll(m, old)
				if m.modal != st || st.view != view || !st.loading || st.errMsg != "" || m.prompt.Value() != "new draft" {
					t.Fatal("abandoned result changed current view/request/draft")
				}
				m = applyAll(m, runCmd(current))
				if kind == "resource" && st.view != mcpResourcePrev {
					t.Fatal("current read was lost")
				}
				if kind != "resource" && m.modal != nil {
					t.Fatal("current prompt was lost")
				}
			})
		}
	}
}

func TestMCPPickerPreviewBackRejectsOldRead(t *testing.T) {
	f := sampleResourceMCP()
	m := openOverlay(t, newMCPModel(t, aztec(), f), ctrlKey('r'))
	m, _ = pickerKey(m, tea.KeyDown)
	m, cmd := pickerKey(m, tea.KeyEnter)
	old := runCmd(cmd)
	m = applyAll(m, old)
	m, _ = pickerKey(m, tea.KeyEsc)
	st := mcpActive(m)
	if st.view != mcpResources || st.resList.Cursor() != 1 {
		t.Fatal("preview Back lost inventory selection")
	}
	m, current := pickerKey(m, tea.KeyEnter)
	m = applyAll(m, old)
	if st.view != mcpResources || !st.loading || st.preview != "" {
		t.Fatal("old read reopened preview after Back")
	}
	m = applyAll(m, runCmd(current))
	if st.view != mcpResourcePrev {
		t.Fatal("current read failed after Back")
	}
	m.closeModal()
}

func TestMCPPickerOwnedResultCannotCloseOtherSurface(t *testing.T) {
	m := openOverlay(t, newMCPModel(t, aztec(), samplePromptMCP()), tea.KeyPressMsg{Code: tea.KeyF8})
	m, _ = pickerKey(m, tea.KeyDown)
	m, cmd := pickerKey(m, tea.KeyEnter)
	m, _ = pickerKey(m, tea.KeyEsc)
	other := &hitDispatchSurface{deps: (&m).surfaceDeps()}
	m.modal = other
	m.prompt.Rewrite("new draft")
	m = applyAll(m, runCmd(cmd))
	if m.modal != other || m.prompt.Value() != "new draft" {
		t.Fatal("owned but abandoned result affected unrelated surface")
	}
}

func TestMCPPickerRawResultsInert(t *testing.T) {
	differentResources := []client.MCPResource{{Server: "other", URI: "mcp://other", Name: "different"}}
	raw := []tea.Msg{
		client.MCPResourcesMsg{Resources: differentResources},
		client.MCPPromptsMsg{Prompts: samplePromptMCP().prompts},
		client.MCPResourceReadMsg{Contents: sampleResourceMCP().contents},
		client.MCPPromptGotMsg{Name: "unowned", Messages: samplePromptMCP().promptMsg},
		mcpInsertResourceMsg{preview: "unowned"},
	}
	for _, where := range []string{"none", "resources", "prompts", "panel", "other"} {
		t.Run(where, func(t *testing.T) {
			f := sampleResourceMCP()
			f.resources = browserInventory().resources
			m := newMCPModel(t, aztec(), f)
			switch where {
			case "resources":
				m = openOverlay(t, m, ctrlKey('r'))
			case "prompts":
				m = openOverlay(t, m, tea.KeyPressMsg{Code: tea.KeyF8})
			case "panel":
				m = openOverlay(t, m, ctrlKey('o'))
			case "other":
				m.modal = &hitDispatchSurface{deps: (&m).surfaceDeps()}
			}
			modal := m.modal
			m.prompt.Rewrite("draft")
			var resources []client.MCPResource
			window, identity := 0, ""
			if st := mcpActive(m); st != nil && where == "resources" {
				_ = m.View()
				for range 3 {
					m = applyAll(m, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
				}
				resources = append(resources, st.resources...)
				window, identity = st.resList.Offset(), st.resList.CursorID()
				if window == 0 {
					t.Fatal("precondition: unowned inventory must preserve a browsed window")
				}
			}
			for _, msg := range raw {
				m = applyAll(m, msg)
				if m.modal != modal || m.prompt.Value() != "draft" {
					t.Fatal("unowned result changed modal/draft")
				}
			}
			if st := mcpActive(m); st != nil {
				if st.preview != "" || len(st.prompts) != 0 || (where == "resources" && st.view != mcpResources) {
					t.Fatal("unowned result changed picker state")
				}
				if where == "resources" {
					if st.resList.Offset() != window || st.resList.CursorID() != identity || len(st.resources) != len(resources) {
						t.Fatal("unowned resources result changed inventory window or identity")
					}
					for i := range resources {
						if st.resources[i] != resources[i] {
							t.Fatal("unowned resources result replaced the resource inventory")
						}
					}
				}
			}
		})
	}
}
