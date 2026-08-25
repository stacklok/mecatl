package main

import (
	"context"
	"errors"
	"flag"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

type fakeStartupResumeSource struct {
	rows            []client.SessionListItem
	listErr         error
	transcripts     map[string]client.SessionTranscript
	transcriptErrs  map[string]error
	snapshots       map[string]client.SessionSnapshot
	snapshotErrs    map[string]error
	transcriptCalls []string
	listCalls       int
}

func (f *fakeStartupResumeSource) ListSessions(context.Context) ([]client.SessionListItem, error) {
	f.listCalls++
	return f.rows, f.listErr
}

func (f *fakeStartupResumeSource) GetSessionTranscript(_ context.Context, id string) (client.SessionTranscript, error) {
	f.transcriptCalls = append(f.transcriptCalls, id)
	if err := f.transcriptErrs[id]; err != nil {
		return client.SessionTranscript{}, err
	}
	return f.transcripts[id], nil
}

func (f *fakeStartupResumeSource) GetSession(_ context.Context, id string) (client.SessionSnapshot, error) {
	if err := f.snapshotErrs[id]; err != nil {
		return client.SessionSnapshot{}, err
	}
	if snapshot, ok := f.snapshots[id]; ok {
		return snapshot, nil
	}
	return client.SessionSnapshot{State: "completed", Workspace: "/workspace"}, nil
}

func TestSessionContinuityUX_Scenario6_FlagGrammar(t *testing.T) {
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		for _, args := range [][]string{{"--resume", "opaque-id"}, {"--resume-latest"}} {
			_, cfg, err := parseTransportFlags(mode, &strings.Builder{}, args)
			if err != nil {
				t.Fatalf("mode %s args %v: %v", mode, args, err)
			}
			if cfg.resumeID == "" && !cfg.resumeLatest {
				t.Fatalf("mode %s args %v did not retain selector", mode, args)
			}
		}
	}
	if _, _, err := parseTransportFlags(modeLocal, &strings.Builder{}, []string{"--resume", "a", "--resume-latest"}); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("conflicting selectors error = %v", err)
	}
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		var out strings.Builder
		_, _, err := parseTransportFlags(mode, &out, []string{"--help"})
		if !errors.Is(err, flag.ErrHelp) || !strings.Contains(out.String(), "-resume string") || !strings.Contains(out.String(), "-resume-latest") {
			t.Fatalf("mode %s help missing resume flags: err=%v\n%s", mode, err, out.String())
		}
	}
}

func TestSessionContinuityUX_Scenario6_ExactResumeDoesNotRequireInventory(t *testing.T) {
	source := &fakeStartupResumeSource{
		listErr: errors.New("inventory paging unavailable"),
		transcripts: map[string]client.SessionTranscript{
			"opaque-id": {SessionID: "opaque-id", Complete: true, Kind: client.SessionKindMain},
		},
		snapshots: map[string]client.SessionSnapshot{
			"opaque-id": {State: "completed", Workspace: "/workspace"},
		},
	}

	got, err := resolveStartupResume(context.Background(), source, "opaque-id", false)
	if err != nil {
		t.Fatalf("exact resume with unavailable inventory: %v", err)
	}
	if source.listCalls != 0 {
		t.Fatalf("exact resume listed inventory %d times; want 0", source.listCalls)
	}
	if got == nil || got.Row.ID != "opaque-id" || got.Transcript.SessionID != "opaque-id" {
		t.Fatalf("exact resume selection = %+v", got)
	}
}

func TestSessionContinuityUX_Scenario6_LatestSelection(t *testing.T) {
	source := &fakeStartupResumeSource{
		rows: []client.SessionListItem{
			{ID: "live", ModifiedAt: 90, Kind: client.SessionKindMain, State: "running", Capabilities: client.SessionInventoryCapabilities{PublicChat: true}},
			{ID: "awaiting", ModifiedAt: 80, Kind: client.SessionKindMain, State: "awaiting", ReasonCode: client.CapabilityReasonAwaitingApproval},
			{ID: "child", ModifiedAt: 70, Kind: client.SessionKindSubagent, Capabilities: client.SessionInventoryCapabilities{Inspect: true}},
			{ID: "newest-readable", ModifiedAt: 60, Kind: client.SessionKindMain, State: "completed", Capabilities: client.SessionInventoryCapabilities{PublicChat: true}},
			{ID: "older", ModifiedAt: 50, Kind: client.SessionKindMain, State: "completed", Capabilities: client.SessionInventoryCapabilities{PublicChat: true}},
		},
		transcripts: map[string]client.SessionTranscript{
			"newest-readable": {SessionID: "newest-readable", Complete: true},
			"older":           {SessionID: "older", Complete: true},
		},
	}
	got, err := resolveStartupResume(context.Background(), source, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Row.ID != "newest-readable" || len(source.transcriptCalls) != 1 || source.transcriptCalls[0] != "newest-readable" {
		t.Fatalf("selection=%+v transcript calls=%v", got, source.transcriptCalls)
	}
}

func TestADR_0108_StartupStaticValidation(t *testing.T) {
	child := client.SessionListItem{ID: "child", Kind: client.SessionKindSubagent, Capabilities: client.SessionInventoryCapabilities{Inspect: true}, ReasonCode: client.CapabilityReasonInspectOnlyKind}
	source := &fakeStartupResumeSource{
		rows: []client.SessionListItem{child},
		transcripts: map[string]client.SessionTranscript{
			"child": {SessionID: "child", Complete: true, Kind: client.SessionKindSubagent},
		},
		snapshotErrs: map[string]error{"missing": errors.New("not found")},
	}
	_, err := resolveStartupResume(context.Background(), source, "child", false)
	var resumeErr *startupResumeError
	if !errors.As(err, &resumeErr) || resumeErr.Reason != client.CapabilityReasonInspectOnlyKind {
		t.Fatalf("inspect-only error = %#v", err)
	}
	_, err = resolveStartupResume(context.Background(), source, "missing", false)
	if !errors.As(err, &resumeErr) || resumeErr.Reason != startupReasonNotFound || strings.Contains(err.Error(), "owner") {
		t.Fatalf("missing error = %#v", err)
	}

	main := client.SessionListItem{ID: "main", Kind: client.SessionKindMain, State: "completed", Capabilities: client.SessionInventoryCapabilities{PublicChat: true}}
	source = &fakeStartupResumeSource{rows: []client.SessionListItem{main}, transcriptErrs: map[string]error{"main": errors.New("backend path")}}
	_, err = resolveStartupResume(context.Background(), source, "main", false)
	if !errors.As(err, &resumeErr) || resumeErr.Reason != client.CapabilityReasonTranscriptUnavailable || strings.Contains(err.Error(), "backend path") {
		t.Fatalf("transcript error = %#v", err)
	}
}

// TestResumeLatestOrNew_FlagGrammar covers the --resume-latest-or-new selector: it
// parses in both modes, and it is mutually exclusive with --resume and
// --resume-latest (one startup intent).
func TestResumeLatestOrNew_FlagGrammar(t *testing.T) {
	for _, mode := range []transportMode{modeLocal, modeConnect} {
		_, cfg, err := parseTransportFlags(mode, &strings.Builder{}, []string{"--resume-latest-or-new"})
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		if !cfg.resumeLatestOrNew {
			t.Fatalf("mode %s did not retain --resume-latest-or-new", mode)
		}
	}
	conflicts := [][]string{
		{"--resume", "a", "--resume-latest-or-new"},
		{"--resume-latest", "--resume-latest-or-new"},
	}
	for _, args := range conflicts {
		if _, _, err := parseTransportFlags(modeLocal, &strings.Builder{}, args); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("args %v conflict error = %v", args, err)
		}
	}
}

// TestResumeLatestOrNew_FallsBackToNewOnMiss proves --resume-latest-or-new degrades a
// "no eligible chat" miss into a fresh session (nil selection + the configured
// workspace) instead of failing startup, while a plain --resume-latest still errors on
// the same empty inventory.
func TestResumeLatestOrNew_FallsBackToNewOnMiss(t *testing.T) {
	empty := func() *fakeStartupResumeSource { return &fakeStartupResumeSource{} }

	// --resume-latest-or-new: miss → new session, no error.
	sel, ws, err := startupResumeConfig(context.Background(), empty(), config{resumeLatestOrNew: true, workspace: "/ws"})
	if err != nil {
		t.Fatalf("or-new miss returned error: %v", err)
	}
	if sel != nil {
		t.Fatalf("or-new miss must yield no selection, got %+v", sel)
	}
	if ws != "/ws" {
		t.Fatalf("or-new miss workspace = %q, want /ws", ws)
	}

	// --resume-latest: same empty inventory still fails startup.
	if _, _, err := startupResumeConfig(context.Background(), empty(), config{resumeLatest: true, workspace: "/ws"}); err == nil {
		t.Fatal("plain --resume-latest on empty inventory must error")
	}
}

// TestResumeLatestOrNew_AdoptsWhenEligible proves the happy path is unchanged from
// --resume-latest: when an eligible chat exists it is adopted (selection + its stored
// workspace).
func TestResumeLatestOrNew_AdoptsWhenEligible(t *testing.T) {
	source := &fakeStartupResumeSource{
		rows: []client.SessionListItem{
			{ID: "newest", ModifiedAt: 60, Kind: client.SessionKindMain, State: "completed", Capabilities: client.SessionInventoryCapabilities{PublicChat: true}},
		},
		transcripts: map[string]client.SessionTranscript{"newest": {SessionID: "newest", Complete: true}},
		snapshots:   map[string]client.SessionSnapshot{"newest": {State: "completed", Workspace: "/adopted"}},
	}
	sel, ws, err := startupResumeConfig(context.Background(), source, config{resumeLatestOrNew: true, workspace: "/ws"})
	if err != nil {
		t.Fatalf("or-new adopt returned error: %v", err)
	}
	if sel == nil || sel.Row.ID != "newest" {
		t.Fatalf("or-new adopt selection = %+v", sel)
	}
	if ws != "/adopted" {
		t.Fatalf("or-new adopt workspace = %q, want /adopted", ws)
	}
}

// TestResumeLatestOrNew_ListFailureStillErrors proves the fallback is scoped to the
// not-found miss ONLY: a genuine inventory-list failure still surfaces (retryable
// infrastructure error), never silently degraded to a new session.
func TestResumeLatestOrNew_ListFailureStillErrors(t *testing.T) {
	source := &fakeStartupResumeSource{listErr: errors.New("inventory unavailable")}
	if _, _, err := startupResumeConfig(context.Background(), source, config{resumeLatestOrNew: true, workspace: "/ws"}); err == nil {
		t.Fatal("or-new must surface a list failure, not fall back to a new session")
	}
}
