package ui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type titleRenamer struct {
	calls []struct{ id, title string }
	snap  client.SessionSnapshot
	err   error
}

func (r *titleRenamer) RenameSession(_ context.Context, id, title string) (client.SessionSnapshot, error) {
	r.calls = append(r.calls, struct{ id, title string }{id, title})
	return r.snap, r.err
}
func (*titleRenamer) DeleteSession(context.Context, string) error { return nil }

func titleModel(t *testing.T, renamer *titleRenamer) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{
		SessionManagement: renamer,
		Theme:             theme.New("aztec", theme.AztecPalette()),
		Ctx:               t.Context(),
		NoAltScreen:       true,
	})
	m.sessionID, m.sessionTitle, m.phase = "active", "Fallback", phaseIdle
	return m
}

func TestSessionTitleGeneration_Scenario1_TitleCommandPersistsOperatorTitle(t *testing.T) {
	r := &titleRenamer{snap: client.SessionSnapshot{Title: "Operator title", TitleProvenance: "operator"}}
	m := titleModel(t, r)
	m.prompt.Rewrite("/title Operator title")

	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	if m.sessionTitle != "Operator title" || cmd == nil {
		t.Fatalf("optimistic title/cmd = %q/%v, want operator title and rename command", m.sessionTitle, cmd != nil)
	}
	m = applyAll(m, cmd())
	if len(r.calls) != 1 || r.calls[0].id != "active" || r.calls[0].title != "Operator title" || m.sessionTitle != "Operator title" {
		t.Fatalf("rename calls/title = %+v/%q", r.calls, m.sessionTitle)
	}
}

func TestSessionTitleGeneration_Scenario1_TitleCommandReadAndRejectsBlank(t *testing.T) {
	m := titleModel(t, &titleRenamer{})
	m.prompt.Rewrite("/title")
	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	if cmd != nil || len(m.conv.blocks) != 1 || !strings.Contains(m.conv.blocks[0].raw, "Fallback") || !strings.Contains(m.conv.blocks[0].raw, "generated") {
		t.Fatalf("/title notice = %#v, want local provenance notice", m.conv.blocks)
	}
	m.prompt.Rewrite("/title   ")
	mm, cmd = m.submitPrompt()
	m = mm.(Model)
	if cmd != nil || !strings.Contains(stripANSIstr(m.statusMsg), "title cannot be blank") {
		t.Fatalf("blank /title status/cmd = %q/%v", m.statusMsg, cmd != nil)
	}
}

func TestSessionTitleGeneration_Scenario1_TitleCommandReconcilesFailure(t *testing.T) {
	r := &titleRenamer{err: errors.New("rejected")}
	m := titleModel(t, r)
	m.prompt.Rewrite("/title optimistic")
	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	if m.sessionTitle != "optimistic" {
		t.Fatalf("optimistic title = %q", m.sessionTitle)
	}
	m = applyAll(m, cmd())
	if m.sessionTitle != "Fallback" {
		t.Fatalf("failed rename title = %q, want fallback", m.sessionTitle)
	}
}

func TestSessionTitleGeneration_Scenario1_TitleCommandIsClientOnly(t *testing.T) {
	r := &titleRenamer{snap: client.SessionSnapshot{Title: "Local", TitleProvenance: "operator"}}
	m := titleModel(t, r)
	m.prompt.Rewrite("/title Local")
	mm, cmd := m.submitPrompt()
	m = mm.(Model)
	if len(m.conv.blocks) != 0 || m.phase != phaseIdle || cmd == nil {
		t.Fatalf("/title changed conversation/phase or omitted rename: blocks=%d phase=%v cmd=%v", len(m.conv.blocks), m.phase, cmd != nil)
	}
}

func TestSessionTitleGeneration_Scenario4_ClientReconnectReconcilesTitle(t *testing.T) {
	conv := &fakeConv{getSessionTitle: "Authoritative"}
	m := newTestModelFromDeps(Deps{Session: conv, Theme: theme.New("aztec", theme.AztecPalette()), Ctx: t.Context(), NoAltScreen: true})
	m.sessionID, m.sessionTitle, m.liveReconGen, m.liveGen = "active", "stale optimistic", 1, 1
	mm, _ := m.updateLiveMsg(liveMsg{gen: 1, msg: client.SessionTitleMsg{Title: "Generated", Provenance: "generated", GenerationState: "generated"}})
	m = mm.(Model)
	if m.sessionTitle != "Generated" {
		t.Fatalf("live generated title = %q", m.sessionTitle)
	}
	mm, cmd := m.updateReconnectMsg(reconnectMsg{gen: 1, msg: client.LiveReconnectedMsg{}})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("reconnect did not refetch the authoritative session metadata")
	}
	msg, ok := cmd().(client.ResolvedModelMsg)
	if !ok {
		t.Fatalf("reconnect command = %T, want title refetch", cmd())
	}
	m = applyAll(m, msg)
	if m.sessionTitle != "Authoritative" {
		t.Fatalf("reconnected title = %q", m.sessionTitle)
	}
	m = applyAll(m, client.ResolvedModelMsg{SessionID: "former", Title: "wrong"})
	if m.sessionTitle != "Authoritative" {
		t.Fatalf("stale session refetch changed title to %q", m.sessionTitle)
	}
}

func TestSessionTitleGeneration_Scenario4_QuietFailureOffersManualTitle(t *testing.T) {
	m := titleModel(t, &titleRenamer{})
	failed := client.SessionTitleMsg{Title: "Fallback", Provenance: "fallback", GenerationState: "exhausted", LatestAttempt: client.TitleAttemptSummary{ID: "a1", Outcome: "deferred"}}
	m = applyAll(m, failed, failed)
	if len(m.conv.blocks) != 1 || !strings.Contains(m.conv.blocks[0].raw, "/title <text>") || strings.Contains(m.conv.blocks[0].raw, "provider") {
		t.Fatalf("failure notices = %#v", m.conv.blocks)
	}
}
