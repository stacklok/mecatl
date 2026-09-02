package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// titleService builds a Service over a jsonlstore with a no-op engine, mirroring
// listSessionsService but local to this file's title-focused tests.
func titleService(t *testing.T, store port.SessionStore) *server.Service {
	t.Helper()
	llm := mockllm.New(mockllm.TextTurn("hi"))
	eng := agent.NewEngine(agent.Deps{
		LLM:     llm,
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine:     eng,
		Store:      store,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(0, 0) },
		DefaultResolvedModel: server.ResolvedModel{
			ProviderID: "openai",
			ModelID:    "test-model",
		},
		DefaultCapabilities: llm.Capabilities(),
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// saveSessionWithPrompt builds a completed session that recorded ONE genuine
// user prompt (via the aggregate) — the Title seeding path the loop uses. It
// optionally seeds the snapshot Title directly (seedTitle) to test the
// non-empty snapshot branch. It returns the session after Save.
func saveSessionWithPrompt(ctx context.Context, t *testing.T, st port.SessionStore, id session.SessionID, promptText string, seedTitle bool) *session.Session {
	t.Helper()
	s := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	// Record the user prompt through the aggregate (the loop's recordPrompt path
	// uses RecordUserPromptWithParts; the title is seeded by SetTitle right after).
	if err := s.RecordUserPrompt(promptText, nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if seedTitle {
		s.SetTitle(promptText)
	}
	if err := s.RecordAssistant(session.NewAssistantMessage("ok", "", nil)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := s.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return s
}

func TestDeriveTitleFromFirstGenuine(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	// A session whose Title was NOT seeded (the lazy fallback applies): the first
	// genuine user prompt's text is returned, clamped.
	s := saveSessionWithPrompt(ctx, t, st, "t1", "Fix the flaky CI job", false)
	// Reload the session fresh so Title is the persisted (empty) value, then derive.
	loaded, err := st.Load(ctx, s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Title != "" {
		t.Fatalf("fixture Title = %q, want empty (lazy path)", loaded.Title)
	}
	got := server.DeriveTitle(loaded)
	if got != "Fix the flaky CI job" {
		t.Fatalf("DeriveTitle = %q, want the first genuine prompt clamped", got)
	}
	// sess.Title MUST NOT be mutated (no write-on-read).
	if loaded.Title != "" {
		t.Fatalf("DeriveTitle mutated sess.Title to %q (write-on-read)", loaded.Title)
	}
	// The exported helper should agree.
	if got2 := server.DeriveTitle(loaded); got2 != got {
		t.Fatalf("DeriveTitle non-deterministic: %q then %q", got, got2)
	}
}

func TestDeriveTitleSkipsSynthesisedSummary(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	s := session.New("t2", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	// First RoleUser message is a synthesised compaction summary — must be SKIPPED.
	if err := s.RecordUserPrompt(session.CompactionSummaryMarker+" earlier turns…", nil); err != nil {
		t.Fatalf("RecordUserPrompt summary: %v", err)
	}
	// The genuine prompt follows.
	if err := s.RecordUserPrompt("the real task", nil); err != nil {
		t.Fatalf("RecordUserPrompt real: %v", err)
	}
	if err := s.RecordAssistant(session.NewAssistantMessage("ok", "", nil)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := s.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := st.Load(ctx, s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := server.DeriveTitle(loaded)
	if got != "the real task" {
		t.Fatalf("DeriveTitle = %q, want %q (skipped the synthesised summary)", got, "the real task")
	}
	if loaded.Title != "" {
		t.Fatalf("DeriveTitle mutated sess.Title (write-on-read): %q", loaded.Title)
	}
}

func TestDeriveTitleEmptyForCompacted(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	s := session.New("t3", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := s.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	// ONLY synthesised summaries — no genuine prompt → empty title.
	if err := s.RecordUserPrompt(session.CompactionSummaryMarker+" …", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := s.RecordUserPrompt(session.Tier4SummaryMarker+"\n…", nil); err != nil {
		t.Fatalf("RecordUserPrompt tier4: %v", err)
	}
	if err := s.RecordAssistant(session.NewAssistantMessage("ok", "", nil)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := s.Complete(); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := st.Load(ctx, s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := server.DeriveTitle(loaded); got != "" {
		t.Fatalf("DeriveTitle = %q, want empty (only synthesised summaries)", got)
	}
}

func TestDeriveTitleClampsLongPrompt(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	long := strings.Repeat("x", 200)
	s := saveSessionWithPrompt(ctx, t, st, "t4", long, false)
	loaded, err := st.Load(ctx, s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := server.DeriveTitle(loaded)
	want := strings.Repeat("x", 120) + "…"
	if got != want {
		t.Fatalf("DeriveTitle len = %d, want %d (clamped)", len([]rune(got)), len([]rune(want)))
	}
}

func TestDeriveTitlePrefersSeededSnapshotTitle(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	// Title WAS seeded (the loop path) — DeriveTitle returns it verbatim, NOT the
	// re-derived first prompt (which would be identical here, but the point is the
	// snapshot value wins without a re-walk).
	s := saveSessionWithPrompt(ctx, t, st, "t5", "the first prompt", true)
	loaded, err := st.Load(ctx, s.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Title != "the first prompt" {
		t.Fatalf("fixture Title = %q, want %q (seeded)", loaded.Title, "the first prompt")
	}
	got := server.DeriveTitle(loaded)
	if got != "the first prompt" {
		t.Fatalf("DeriveTitle = %q, want the seeded snapshot Title", got)
	}
}

// TestListSessionsCarriesTitle asserts the ListSessions wire (gRPC + HTTP) carries
// the derived Title on the SessionSummary row.
func TestListSessionsCarriesTitle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := jsonlstore.New(dir)
	if err != nil {
		t.Fatalf("jsonlstore: %v", err)
	}
	// The loop seeds the snapshot Title from the first genuine prompt (via
	// SetTitle); the fast MetaList path returns the snapshot Title ONLY (the
	// lazy deriveTitle walk needs the full conversation, which MetaList skips).
	// So seed the Title here to mirror production — this test asserts the Title
	// CARRIES on the wire, not the lazy fallback (tested separately via
	// TestDeriveTitle* / TestGetSessionCarriesTitle/lazy).
	s := saveSessionWithPrompt(ctx, t, st, "ls-title", "List my sessions please", true)
	setSessionMtime(t, dir, s.ID, time.Unix(1800000000, 0).UTC())
	svc := titleService(t, st)

	t.Run("gRPC", func(t *testing.T) {
		client, cleanup := dialGRPC(t, svc)
		defer cleanup()
		resp, err := client.ListSessions(ctx, &mecatlv1.ListSessionsRequest{})
		if err != nil {
			t.Fatalf("ListSessions: %v", err)
		}
		if len(resp.GetSessions()) != 1 {
			t.Fatalf("sessions = %d, want 1", len(resp.GetSessions()))
		}
		row := resp.GetSessions()[0]
		if row.GetTitle() != "List my sessions please" {
			t.Errorf("gRPC row Title = %q, want %q", row.GetTitle(), "List my sessions please")
		}
	})
	t.Run("HTTP", func(t *testing.T) {
		srv := httptest.NewServer(server.NewHTTPHandler(svc))
		defer srv.Close()
		var resp mecatlv1.ListSessionsResponse
		if code := httpGet(t, srv, "/v1/sessions", &resp); code != 200 {
			t.Fatalf("GET /v1/sessions status = %d", code)
		}
		if len(resp.GetSessions()) != 1 {
			t.Fatalf("sessions = %d, want 1", len(resp.GetSessions()))
		}
		if got := resp.GetSessions()[0].GetTitle(); got != "List my sessions please" {
			t.Errorf("HTTP row Title = %q, want %q", got, "List my sessions please")
		}
	})
}

// TestGetSessionCarriesTitle asserts the GetSession wire (gRPC + HTTP) carries
// the Title — the snapshot Title when seeded, and the lazy derived fallback when empty.
func TestGetSessionCarriesTitle(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	// Seeded Title path.
	seeded := saveSessionWithPrompt(ctx, t, st, "gs-seeded", "Seeded title prompt", true)
	// Empty-Title (lazy fallback) path.
	lazy := saveSessionWithPrompt(ctx, t, st, "gs-lazy", "Lazy fallback prompt", false)
	svc := titleService(t, st)

	t.Run("gRPC seeded", func(t *testing.T) {
		client, cleanup := dialGRPC(t, svc)
		defer cleanup()
		resp, err := client.GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: string(seeded.ID)})
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if got := resp.GetSession().GetTitle(); got != "Seeded title prompt" {
			t.Errorf("gRPC seeded Title = %q, want %q", got, "Seeded title prompt")
		}
	})
	t.Run("gRPC lazy fallback", func(t *testing.T) {
		client, cleanup := dialGRPC(t, svc)
		defer cleanup()
		resp, err := client.GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: string(lazy.ID)})
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if got := resp.GetSession().GetTitle(); got != "Lazy fallback prompt" {
			t.Errorf("gRPC lazy Title = %q, want %q (derived fallback)", got, "Lazy fallback prompt")
		}
		// sess.Title must NOT have been mutated by the GetSession read.
		loaded, err := st.Load(ctx, lazy.ID)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if loaded.Title != "" {
			t.Errorf("GetSession mutated persisted Title to %q (write-on-read)", loaded.Title)
		}
	})
	t.Run("HTTP seeded", func(t *testing.T) {
		srv := httptest.NewServer(server.NewHTTPHandler(svc))
		defer srv.Close()
		resp, err := http.Get(srv.URL + "/v1/sessions/" + string(seeded.ID))
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
		}
		var out struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.Title != "Seeded title prompt" {
			t.Errorf("HTTP seeded Title = %q, want %q", out.Title, "Seeded title prompt")
		}
	})
	t.Run("HTTP lazy fallback", func(t *testing.T) {
		srv := httptest.NewServer(server.NewHTTPHandler(svc))
		defer srv.Close()
		resp, err := http.Get(srv.URL + "/v1/sessions/" + string(lazy.ID))
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
		}
		var out struct {
			Title string `json:"title"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.Title != "Lazy fallback prompt" {
			t.Errorf("HTTP lazy Title = %q, want %q (derived fallback)", out.Title, "Lazy fallback prompt")
		}
	})
}
