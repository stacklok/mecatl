package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type recordingDiagnostics struct {
	mu      sync.Mutex
	entries []string
}

func (d *recordingDiagnostics) Log(_ context.Context, _ port.Level, msg string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries = append(d.entries, fmt.Sprint(append([]any{msg}, args...)...))
}

func (d *recordingDiagnostics) With(...any) port.Diagnostics { return d }

func (d *recordingDiagnostics) contains(text string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.ContainsFunc(d.entries, func(entry string) bool { return strings.Contains(entry, text) })
}

type transcriptStore struct {
	inner       port.SessionStore
	mu          sync.Mutex
	loads       int
	saves       int
	loadErr     error
	loadErrByID map[session.SessionID]error
}

func (s *transcriptStore) Save(ctx context.Context, sess *session.Session) error {
	s.mu.Lock()
	s.saves++
	s.mu.Unlock()
	return s.inner.Save(ctx, sess)
}

func (s *transcriptStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	s.mu.Lock()
	s.loads++
	err := s.loadErr
	if byID, ok := s.loadErrByID[id]; ok {
		err = byID
	}
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.inner.Load(ctx, id)
}

func (s *transcriptStore) resetCounts() {
	s.mu.Lock()
	s.loads, s.saves = 0, 0
	s.mu.Unlock()
}

func (s *transcriptStore) counts() (loads, saves int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads, s.saves
}

type countingLease struct {
	inner port.SessionLease
	calls atomic.Int32
}

func (l *countingLease) Acquire(ctx context.Context, id session.SessionID, owner string) (port.Lease, error) {
	l.calls.Add(1)
	return l.inner.Acquire(ctx, id, owner)
}
func (l *countingLease) Renew(ctx context.Context, lease port.Lease) (port.Lease, error) {
	l.calls.Add(1)
	return l.inner.Renew(ctx, lease)
}
func (l *countingLease) Release(ctx context.Context, lease port.Lease) error {
	l.calls.Add(1)
	return l.inner.Release(ctx, lease)
}

func newTranscriptService(t *testing.T, store port.SessionStore, eventLog port.EventLog, ownership bool, effects *atomic.Int32, lease port.SessionLease) *server.Service {
	t.Helper()
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Model: "test"})
	svc, err := newPlacementTestService(server.Config{
		Engine:            eng,
		Store:             store,
		EventLog:          eventLog,
		OwnershipEnforced: ownership,
		Workspaces: func(root string) tool.Workspace {
			effects.Add(1)
			return memfs.NewWorkspace(root)
		},
		EnvironmentResolver: func(context.Context, session.EnvironmentRef) (tool.Environment, error) {
			effects.Add(1)
			return tool.Environment{}, errors.New("must not resolve")
		},
		SessionEngine: func(context.Context, server.ProviderSelector, []mcp.ServerConfig, server.SessionProfile, string, session.PermissionMode) (server.SessionEngineResult, error) {
			effects.Add(1)
			return server.SessionEngineResult{}, errors.New("must not rebuild")
		},
		SessionLease: lease,
		LeaseOwner:   "transcript-test",
		Now:          func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func TestSessionContinuityUX_Scenario3_AuthoritativeTranscript(t *testing.T) {
	ctx := context.Background()
	base := memstore.New()
	store := &transcriptStore{inner: base}
	effects := &atomic.Int32{}
	lease := &countingLease{inner: memlease.New(wallclock.Clock{}, time.Minute)}
	svc := newTranscriptService(t, store, nil, false, effects, lease)

	sess := session.New("empty", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := base.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	store.resetCounts()

	got, err := svc.GetTranscript(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetTranscript: %v", err)
	}
	if !got.Complete || len(got.Messages) != 0 {
		t.Fatalf("empty transcript = complete %v, %d messages; want true, 0", got.Complete, len(got.Messages))
	}
	if loads, saves := store.counts(); loads != 1 || saves != 0 {
		t.Fatalf("store calls = %d loads, %d saves; want exactly 1 load, 0 saves", loads, saves)
	}
	if effects.Load() != 0 || lease.calls.Load() != 0 {
		t.Fatalf("transcript triggered runtime effects: callbacks=%d lease calls=%d", effects.Load(), lease.calls.Load())
	}
}

func TestADR_0108_TranscriptBackendErrorsAreRedacted(t *testing.T) {
	const raw = "snapshot decode failed at /secret/backend/session.jsonl"
	diag := &recordingDiagnostics{}
	svc, err := newPlacementTestService(server.Config{
		Engine:      agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Model: "test"}),
		Store:       &transcriptStore{inner: memstore.New(), loadErr: errors.New(raw)},
		Workspaces:  func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Diagnostics: diag,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	got, err := svc.GetTranscript(context.Background(), "opaque-id")
	if got != nil || !errors.Is(err, server.ErrInternal) {
		t.Fatalf("GetTranscript = (%v, %v), want nil internal error", got, err)
	}
	if strings.Contains(err.Error(), raw) || strings.Contains(err.Error(), "/secret/backend") {
		t.Fatalf("caller error leaked backend detail: %v", err)
	}
	if !diag.contains(raw) {
		t.Fatalf("operator diagnostics did not retain backend cause: %v", diag.entries)
	}
}

func TestADR_0108_TranscriptAbsenceIsNotEmptySuccess(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("snapshot decode failed")
	for _, tc := range []struct {
		name  string
		store *transcriptStore
		want  error
	}{
		{name: "missing", store: &transcriptStore{inner: memstore.New()}, want: server.ErrNotFound},
		{name: "corrupt", store: &transcriptStore{inner: memstore.New(), loadErr: boom}, want: server.ErrInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTranscriptService(t, tc.store, nil, false, &atomic.Int32{}, nil)
			got, err := svc.GetTranscript(ctx, "absent")
			if got != nil || !errors.Is(err, tc.want) {
				t.Fatalf("GetTranscript = (%v, %v), want nil and %v", got, err, tc.want)
			}
		})
	}
}

func TestSessionContinuityUX_TranscriptOwnershipIsCheckedOnSingleLoad(t *testing.T) {
	ctx := context.Background()
	base := memstore.New()
	store := &transcriptStore{inner: base}
	alice := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	bob := &session.Principal{Issuer: alice.Issuer, Subject: "bob", GrantType: session.GrantTypeUser}
	sess := session.New("owned", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := sess.RestoreLabels(alice, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	if err := base.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	store.resetCounts()
	svc := newTranscriptService(t, store, nil, true, &atomic.Int32{}, nil)

	got, err := svc.GetTranscript(session.WithPrincipal(ctx, bob), sess.ID)
	if got != nil || !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("GetTranscript as non-owner = (%v, %v), want nil, not found", got, err)
	}
	if loads, saves := store.counts(); loads != 1 || saves != 0 {
		t.Fatalf("store calls = %d loads, %d saves; want exactly 1 load, 0 saves", loads, saves)
	}
}

// TestStoreFailureIsVisibleToTheOperatorButNotCorrelated is the operability
// half of the concealment contract its sibling above pins. Concealment is owed
// to the CALLER; without any operator signal a storage outage and mass deletion
// look identical from both sides at once. The line must therefore exist AND
// carry no target: not the session id, not the store's error (which routinely
// embeds the record path). A genuine not-found is the normal case and stays
// silent, so the line's rate is a real outage signal rather than probe noise.
func TestStoreFailureIsVisibleToTheOperatorButNotCorrelated(t *testing.T) {
	ctx := context.Background()
	base := memstore.New()
	alice := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	brokenID := session.SessionID("broken-record")
	broken := session.New(brokenID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := broken.RestoreLabels(alice, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	if err := base.Save(ctx, broken); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loadFailure := errors.New("decode failed reading /var/lib/mecatl/broken-record.json")
	store := &transcriptStore{inner: base, loadErrByID: map[session.SessionID]error{brokenID: loadFailure}}
	diag := &recordingDiagnostics{}
	svc, err := newPlacementTestService(server.Config{
		Engine:            agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Model: "test"}),
		Store:             store,
		OwnershipEnforced: true,
		Workspaces:        func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Diagnostics:       diag,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	aliceCtx := session.WithPrincipal(ctx, alice)

	// A genuine absence must stay silent: probing must not be able to generate log volume.
	if _, err := svc.GetSession(aliceCtx, session.SessionID("never-existed")); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("GetSession(missing) = %v, want ErrNotFound", err)
	}
	if len(diag.entries) != 0 {
		t.Fatalf("a genuine not-found logged: %v", diag.entries)
	}

	// A broken store must be visible to the operator...
	if _, err := svc.GetSession(aliceCtx, brokenID); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("GetSession(broken) = %v, want ErrNotFound", err)
	}
	if !diag.contains("session load failed") {
		t.Fatalf("store failure produced no operator diagnostic: %v", diag.entries)
	}
	// ...without naming the target it failed on.
	if diag.contains(string(brokenID)) || diag.contains(loadFailure.Error()) {
		t.Fatalf("operator diagnostic is target-correlated: %v", diag.entries)
	}
}

func TestTranscriptOwnershipModeConcealsLoadFailure(t *testing.T) {
	ctx := context.Background()
	base := memstore.New()
	alice := &session.Principal{Issuer: "https://idp.example", Subject: "alice", GrantType: session.GrantTypeUser}
	bob := &session.Principal{Issuer: alice.Issuer, Subject: "bob", GrantType: session.GrantTypeUser}
	foreignID := session.SessionID("foreign-corrupt")
	foreign := session.New(foreignID, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := foreign.RestoreLabels(alice, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	if err := base.Save(ctx, foreign); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loadFailure := errors.New("snapshot decode failed for foreign record")
	store := &transcriptStore{inner: base, loadErrByID: map[session.SessionID]error{foreignID: loadFailure}}
	diag := &recordingDiagnostics{}
	svc, err := newPlacementTestService(server.Config{
		Engine:            agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Model: "test"}),
		Store:             store,
		OwnershipEnforced: true,
		Workspaces:        func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Diagnostics:       diag,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	bobCtx := session.WithPrincipal(ctx, bob)
	missingID := session.SessionID("missing")

	for _, id := range []session.SessionID{missingID, foreignID} {
		got, getErr := svc.GetTranscript(bobCtx, id)
		if got != nil || !errors.Is(getErr, server.ErrNotFound) {
			t.Fatalf("GetTranscript(%q) = (%v, %v), want nil not found", id, got, getErr)
		}
	}
	if diag.contains(loadFailure.Error()) || diag.contains(string(foreignID)) {
		t.Fatalf("load failure produced target-correlated diagnostics: %v", diag.entries)
	}

	grpc := server.NewHarnessServer(svc)
	httpHandler := server.NewHTTPHandler(svc)
	for _, id := range []session.SessionID{missingID, foreignID} {
		_, getErr := grpc.GetSessionTranscript(bobCtx, &mecatlv1.GetSessionTranscriptRequest{SessionId: string(id)})
		if status.Code(getErr) != codes.NotFound {
			t.Fatalf("gRPC transcript(%q) code = %s, want NotFound (err=%v)", id, status.Code(getErr), getErr)
		}
		req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+string(id)+"/transcript", nil).WithContext(bobCtx)
		rec := httptest.NewRecorder()
		httpHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("HTTP transcript(%q) status = %d, want 404; body=%s", id, rec.Code, rec.Body.String())
		}
	}
}

func TestSessionContinuityUX_TranscriptHTTPProjection(t *testing.T) {
	ctx := context.Background()
	base := memstore.New()
	sess := session.New("http-transcript", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := sess.SeedHistory([]session.Message{{
		Role: session.RoleAssistant, Text: "visible", Reasoning: "private", ProviderPhase: "commentary", ReasoningItemID: "private-id",
	}}); err != nil {
		t.Fatalf("SeedHistory: %v", err)
	}
	if err := base.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	svc := newTranscriptService(t, base, nil, false, &atomic.Int32{}, nil)
	httpServer := httptest.NewServer(server.NewHTTPHandler(svc))
	defer httpServer.Close()

	resp, err := http.Get(httpServer.URL + "/v1/sessions/" + string(sess.ID) + "/transcript")
	if err != nil {
		t.Fatalf("GET transcript: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body mecatlv1.GetSessionTranscriptResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode transcript: %v", err)
	}
	if body.GetSessionId() != string(sess.ID) || !body.GetComplete() || len(body.GetMessages()) != 1 || body.GetMessages()[0].GetText() != "visible" {
		t.Fatalf("transcript response = %+v", &body)
	}
	message := body.GetMessages()[0]
	if message.GetReasoning() != "" || message.GetProviderPhase() != "" || message.GetReasoningItemId() != "" {
		t.Fatalf("HTTP transcript leaked provider-private replay state: %+v", message)
	}
}

func TestSessionContinuityUX_Scenario3_CompactedAndReasoningTranscript(t *testing.T) {
	ctx := context.Background()
	base := memstore.New()
	sess := session.New("compacted", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	assistant := session.Message{Role: session.RoleAssistant, Text: "current tail", Reasoning: "provider-secret", ProviderPhase: "final_answer", ReasoningItemID: "rs-secret"}
	want := []session.Message{session.NewUserMessage("Summary of earlier conversation"), assistant}
	if err := sess.SeedHistory(want); err != nil {
		t.Fatalf("SeedHistory: %v", err)
	}
	if err := base.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	svc := newTranscriptService(t, base, nil, false, &atomic.Int32{}, nil)

	resp, err := server.NewHarnessServer(svc).GetSessionTranscript(ctx, &mecatlv1.GetSessionTranscriptRequest{SessionId: string(sess.ID)})
	if err != nil {
		t.Fatalf("GetSessionTranscript: %v", err)
	}
	if len(resp.GetMessages()) != 2 || resp.GetMessages()[0].GetText() != want[0].Text || resp.GetMessages()[1].GetText() != assistant.Text {
		t.Fatalf("messages = %+v, want current summary/tail", resp.GetMessages())
	}
	got := resp.GetMessages()[1]
	if got.GetReasoning() != "" || got.GetProviderPhase() != "" || got.GetReasoningItemId() != "" {
		t.Fatalf("provider-private replay leaked: reasoning=%q phase=%q item=%q", got.GetReasoning(), got.GetProviderPhase(), got.GetReasoningItemId())
	}
}

type brokenActivityLog struct {
	reads *atomic.Int32
}

func (brokenActivityLog) Append(context.Context, session.SessionID, session.Event) error { return nil }
func (l brokenActivityLog) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	l.reads.Add(1)
	return func(yield func(session.Event, error) bool) {
		if !yield(session.Event{Type: session.EvMessageDelta}, nil) {
			return
		}
		yield(session.Event{}, errors.New("activity gap"))
	}
}

func TestInvariant_event_replay_never_attests_model_context(t *testing.T) {
	ctx := context.Background()
	base := memstore.New()
	sess := session.New("activity-gap", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := base.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reads := &atomic.Int32{}
	svc := newTranscriptService(t, base, brokenActivityLog{reads: reads}, false, &atomic.Int32{}, nil)

	transcript, err := svc.GetTranscript(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetTranscript: %v", err)
	}
	if !transcript.Complete || !transcript.Activity.Available || transcript.Activity.Complete || transcript.Activity.Authoritative {
		t.Fatalf("status = transcript complete %v, activity %+v", transcript.Complete, transcript.Activity)
	}
	if reads.Load() != 0 {
		t.Fatalf("GetTranscript read EventLog %d times; want 0", reads.Load())
	}
	events, err := svc.StreamSessionEvents(ctx, sess.ID)
	if err != nil {
		t.Fatalf("StreamSessionEvents: %v", err)
	}
	var sawErr bool
	for _, readErr := range events {
		if readErr != nil {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("fixture did not expose the activity read gap")
	}
}
