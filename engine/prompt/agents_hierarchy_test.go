package prompt_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
)

func TestAgentsHierarchyFallbackCompatibility(t *testing.T) {
	ws := memfs.NewWorkspace("/source")
	for name, text := range map[string]string{"AGENTS.md": "root-only=keep; mode=old", "website/AGENTS.md": "  ", "website/CLAUDE.md": "website-only=keep; mode=middle", "website/sub/AGENTS.md": "mode=new"} {
		if err := ws.Write(t.Context(), name, []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	a := prompt.RootAssembler{Source: ws, SourceID: "binding@revision", SourcePrefix: "website"}
	state := &session.InstructionSnapshot{}
	messages, rows, err := a.Assemble(t.Context(), []string{".", "sub"}, state, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || len(rows) != 3 {
		t.Fatalf("messages=%v rows=%v", messages, rows)
	}
	for i, want := range []string{"root-only=keep; mode=old", "website-only=keep; mode=middle", "mode=new"} {
		if !strings.Contains(messages[i].Text, want) || messages[i].Role != session.RoleUser || rows[i].SourceID != "binding@revision" || !rows[i].HasGuidance {
			t.Fatalf("row %d: %v %v", i, messages[i], rows[i])
		}
	}
	if rows[0].Directory != "." || rows[0].File != "AGENTS.md" || rows[1].Directory != "." || rows[1].File != "website/CLAUDE.md" || rows[2].Directory != "sub" || rows[2].File != "website/sub/AGENTS.md" {
		t.Fatalf("ancestor-to-nearest conflict order and provenance: %v", rows)
	}
	fault := &faultingHierarchyWorkspace{Workspace: ws}
	_, _, err = (prompt.RootAssembler{Source: fault, SourceID: "fault", SourcePrefix: "."}).Assemble(t.Context(), []string{"nested"}, &session.InstructionSnapshot{}, 65536)
	if !errors.Is(err, context.DeadlineExceeded) || fault.readClaude {
		t.Fatalf("fault=%v fallback=%v", err, fault.readClaude)
	}
	// Missing AGENTS at the starting folder uses CLAUDE, while an examined fault
	// remains cached even if the backend changes later in the session.
	missing := memfs.NewWorkspace("/missing")
	if err := missing.Write(t.Context(), "CLAUDE.md", []byte("root fallback")); err != nil {
		t.Fatal(err)
	}
	msgs, fallbackRows, err := (prompt.RootAssembler{Source: missing, SourceID: "missing@1", SourcePrefix: "."}).Assemble(t.Context(), []string{"."}, &session.InstructionSnapshot{}, 65536)
	if err != nil || len(msgs) != 1 || !strings.Contains(msgs[0].Text, "root fallback") || fallbackRows[0].File != "CLAUDE.md" {
		t.Fatalf("missing AGENTS fallback: msgs=%v rows=%v err=%v", msgs, fallbackRows, err)
	}
	cached := &session.InstructionSnapshot{}
	faultSource := prompt.RootAssembler{Source: fault, SourceID: "fault@1", SourcePrefix: "."}
	_, _, err = faultSource.Assemble(t.Context(), []string{"nested"}, cached, 65536)
	if !errors.Is(err, context.DeadlineExceeded) || len(cached.Scopes) != 2 || !cached.Scopes[1].Unavailable {
		t.Fatalf("unavailable scope not recorded: %+v err=%v", cached, err)
	}
	fault.readClaude = false
	retained, rows, err := faultSource.Assemble(t.Context(), []string{"nested"}, cached, 65536)
	if err != nil || fault.readClaude || len(cached.Scopes) != 2 || len(retained) != 2 || len(rows) != 2 || !rows[0].HasGuidance || rows[1].HasGuidance || !strings.Contains(retained[1].Text, "selected guidance unavailable") {
		t.Fatalf("examined fault reread, fell back or disappeared from model context: %+v messages=%v err=%v", cached, retained, err)
	}
}

type cancelAfterMissingWorkspace struct {
	*memfs.Workspace
	cancel context.CancelFunc
	reads  int
}

func (w *cancelAfterMissingWorkspace) Read(_ context.Context, _ string) ([]byte, error) {
	w.reads++
	w.cancel()
	return nil, fs.ErrNotExist
}

func TestAgentsHierarchyDiscoveryCosts(t *testing.T) {
	cancelCtx, cancel := context.WithCancel(t.Context())
	cancelSource := &cancelAfterMissingWorkspace{Workspace: memfs.NewWorkspace("/cancel"), cancel: cancel}
	_, _, cancelErr := (prompt.RootAssembler{Source: cancelSource, SourceID: "cancel@1", SourcePrefix: "."}).Assemble(cancelCtx, []string{"."}, &session.InstructionSnapshot{}, 256)
	if !errors.Is(cancelErr, context.Canceled) || cancelSource.reads != 1 {
		t.Fatalf("candidate cancellation: reads=%d err=%v", cancelSource.reads, cancelErr)
	}
	ws := &countedHierarchyWorkspace{Workspace: memfs.NewWorkspace("/remote")}
	for name, text := range map[string]string{"AGENTS.md": "root", "a/CLAUDE.md": "a", "a/b/AGENTS.md": "b", "z/AGENTS.md": "z"} {
		if err := ws.Write(t.Context(), name, []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	state := &session.InstructionSnapshot{}
	a := prompt.NewMultiAssembler(prompt.RootAssembler{Source: ws, SourceID: "remote@1", SourcePrefix: "."})
	_, _, err := a.Assemble(t.Context(), []string{".", "a/b", "z/deep"}, state, 256)
	if err != nil {
		t.Fatal(err)
	}
	first := ws.reads
	if first > 2*5 || first == 0 {
		t.Fatalf("new-pair reads=%d", first)
	}
	_, _, err = a.Assemble(t.Context(), []string{".", "a/b", "z/deep"}, state, 256)
	if err != nil || ws.reads != first {
		t.Fatalf("cached reads=%d first=%d error=%v", ws.reads, first, err)
	}
	if len(state.Directories) != 3 || len(state.Scopes) != 5 {
		t.Fatalf("state=%+v", state)
	}
	limited := &session.InstructionSnapshot{}
	_, _, err = a.Assemble(t.Context(), []string{".", strings.TrimSuffix(strings.Repeat("deep/", 100), "/")}, limited, 64)
	if err != nil || !limited.DiscoveryExhausted {
		t.Fatalf("metadata bound state=%+v err=%v", limited, err)
	}
	before := ws.reads
	_, _, err = a.Assemble(t.Context(), []string{"new"}, limited, 64)
	if err != nil || ws.reads != before {
		t.Fatalf("exhausted reads=%d before=%d err=%v", ws.reads, before, err)
	}
	probeLimited := &countedHierarchyWorkspace{Workspace: memfs.NewWorkspace("/probe-limited")}
	probeState := &session.InstructionSnapshot{}
	_, _, err = (prompt.RootAssembler{Source: probeLimited, SourceID: "budget@1", SourcePrefix: "."}).Assemble(t.Context(), []string{".", "a"}, probeState, 32)
	if err != nil || !probeState.DiscoveryExhausted || len(probeState.Scopes) != 1 || probeLimited.reads != 2 {
		t.Fatalf("scope reservation failed before candidate: reads=%d state=%+v err=%v", probeLimited.reads, probeState, err)
	}
	shortState := &session.InstructionSnapshot{}
	many := prompt.NewMultiAssembler(
		prompt.RootAssembler{Source: probeLimited, SourceID: "budget@1", SourcePrefix: "."},
		prompt.RootAssembler{Source: probeLimited, SourceID: "budget@2", SourcePrefix: "."},
	)
	shortMessages, shortRows, err := many.Assemble(t.Context(), []string{".", "a"}, shortState, 32)
	notices := 0
	for _, row := range shortRows {
		if !row.HasGuidance {
			notices++
		}
	}
	if err != nil || notices != 1 || len(shortMessages) != len(shortRows) {
		t.Fatalf("metadata-full notice must be global: notices=%d messages=%v rows=%v err=%v", notices, shortMessages, shortRows, err)
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	_, _, err = a.Assemble(canceled, []string{"unseen"}, &session.InstructionSnapshot{}, 256)
	if !errors.Is(err, context.Canceled) || ws.reads != before {
		t.Fatalf("cancellation read=%d err=%v", ws.reads, err)
	}

	// Two independently admitted bindings share lexical ancestors only within each source.
	const fixtureLatency = 7 * time.Millisecond
	ws.latency = fixtureLatency
	other := &countedHierarchyWorkspace{Workspace: memfs.NewWorkspace("/other"), latency: fixtureLatency}
	for _, name := range []string{"CLAUDE.md", "a/AGENTS.md", "a/b/CLAUDE.md"} {
		if err := other.Write(t.Context(), name, []byte(name)); err != nil {
			t.Fatal(err)
		}
	}
	both := prompt.NewMultiAssembler(prompt.RootAssembler{Source: ws, SourceID: "remote@1", SourcePrefix: "."}, prompt.RootAssembler{Source: other, SourceID: "other@1", SourcePrefix: "."})
	multiState := &session.InstructionSnapshot{}
	_, _, err = prompt.AssembleWithManifest(t.Context(), both, []string{"a/b", "z/deep/branch/leaf"}, multiState, 1024)
	if err != nil || len(multiState.Scopes) != 14 || ws.reads-before > 14 || other.reads > 14 {
		t.Fatalf("multi-source cost: scopes=%d calls=%d,%d err=%v", len(multiState.Scopes), ws.reads-before, other.reads, err)
	}
	calls := ws.reads - before + other.reads
	if cost := ws.cost + other.cost; cost != time.Duration(calls)*fixtureLatency || cost > time.Duration(2*len(multiState.Scopes))*fixtureLatency {
		t.Fatalf("fixture remote cost=%v for %d calls and %d pairs", cost, calls, len(multiState.Scopes))
	}
	for _, scope := range multiState.Scopes {
		if scope.Directory == ".." || strings.HasPrefix(scope.Directory, "../") || !scope.Examined {
			t.Fatalf("unadmitted or unexamined scope: %+v", scope)
		}
	}
	for _, source := range []*countedHierarchyWorkspace{ws, other} {
		source.reads = 0
	}
	_, _, err = both.Assemble(t.Context(), []string{"a/b", "z/deep/branch/leaf"}, multiState, 1024)
	if err != nil || ws.reads+other.reads != 0 {
		t.Fatalf("shared ancestor reread: calls=%d,%d err=%v", ws.reads, other.reads, err)
	}
	metadata := 0
	for _, dir := range multiState.Directories {
		metadata += len(dir) + 1
	}
	for _, scope := range multiState.Scopes {
		metadata += len(scope.SourceID) + len(scope.Directory) + len(scope.File) + 1
	}
	if metadata > 1024 {
		t.Fatalf("metadata=%d exceeds allowance", metadata)
	}
	// Previously absent scopes are snapshots too, even if the remote backend changes.
	if err := ws.Write(t.Context(), "z/deep/AGENTS.md", []byte("late")); err != nil {
		t.Fatal(err)
	}
	_, rows, err := both.Assemble(t.Context(), []string{"z/deep"}, multiState, 1024)
	if err != nil || ws.reads+other.reads != 0 {
		t.Fatalf("examined absence reread: calls=%d,%d err=%v", ws.reads, other.reads, err)
	}
	for _, row := range rows {
		if strings.Contains(row.File, "z/deep/AGENTS.md") {
			t.Fatalf("late file entered snapshot: %+v", row)
		}
	}
	revised := prompt.RootAssembler{Source: ws, SourceID: "remote@2", SourcePrefix: "."}
	revisedMessages, revisedRows, err := revised.Assemble(t.Context(), []string{"z/deep"}, multiState, 1024)
	if err != nil || ws.reads == 0 {
		t.Fatalf("changed binding revision did not discover: reads=%d err=%v", ws.reads, err)
	}
	foundLate := false
	for i, row := range revisedRows {
		foundLate = foundLate || row.SourceID == "remote@2" && row.File == "z/deep/AGENTS.md" && strings.Contains(revisedMessages[i].Text, "late")
	}
	if !foundLate {
		t.Fatalf("new binding revision reused old absence: %v", revisedRows)
	}
	full := &countedHierarchyWorkspace{Workspace: memfs.NewWorkspace("/full")}
	if err := full.Write(t.Context(), "AGENTS.md", []byte(strings.Repeat("x", 128))); err != nil {
		t.Fatal(err)
	}
	next := &countedHierarchyWorkspace{Workspace: memfs.NewWorkspace("/next")}
	fullMessages, fullRows, err := prompt.NewMultiAssembler(
		prompt.RootAssembler{Source: full, SourceID: "full@1", SourcePrefix: "."},
		prompt.RootAssembler{Source: next, SourceID: "next@1", SourcePrefix: "."},
	).Assemble(t.Context(), []string{".", "deep/new"}, &session.InstructionSnapshot{}, 128)
	if err != nil || next.reads != 0 || full.reads != 1 {
		t.Fatalf("content exhaustion: reads=%d,%d err=%v", full.reads, next.reads, err)
	}
	if len(fullRows) < 2 || fullRows[len(fullRows)-1].HasGuidance || !strings.Contains(fullMessages[len(fullMessages)-1].Text, "content") {
		t.Fatalf("content exhaustion not disclosed: %v %v", fullMessages, fullRows)
	}
}
