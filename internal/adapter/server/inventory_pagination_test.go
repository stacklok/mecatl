package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

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
)

func inventoryService(t *testing.T, store port.SessionStore, ownership bool) *server.Service {
	t.Helper()
	eng := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(),
		Policy: permpolicy.NewPolicy(nil, nil),
	})
	svc, err := server.NewService(server.Config{
		Engine: eng, Store: store, OwnershipEnforced: ownership,
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        time.Now,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc
}

func saveInventorySession(t *testing.T, st port.SessionStore, id session.SessionID, owner *session.Principal, kind session.SessionKind, rel session.SessionRelationship) {
	t.Helper()
	s := session.New(id, session.ModeDefault, "/workspace", session.Limits{}, time.Unix(1_700_000_000, 0))
	if err := s.RestoreLabels(owner, session.Authority("")); err != nil {
		t.Fatalf("RestoreLabels(%q): %v", id, err)
	}
	if err := s.RestoreSessionMetadata(kind, rel); err != nil {
		t.Fatalf("RestoreSessionMetadata(%q): %v", id, err)
	}
	if err := s.RecordUserPrompt("prompt for "+string(id), nil); err != nil {
		t.Fatalf("RecordUserPrompt(%q): %v", id, err)
	}
	if err := st.Save(context.Background(), s); err != nil {
		t.Fatalf("Save(%q): %v", id, err)
	}
}

func TestSessionContinuityUX_Scenario3_PaginationContract(t *testing.T) {
	alice := &session.Principal{Issuer: "issuer", Subject: "alice"}
	bob := &session.Principal{Issuer: "issuer", Subject: "bob"}
	now := time.Unix(2_000, 123)
	st := memstore.New(memstore.WithNow(func() time.Time { return now }))
	for _, id := range []session.SessionID{"b", "a", "foreign"} {
		owner := alice
		if id == "foreign" {
			owner = bob
		}
		saveInventorySession(t, st, id, owner, session.SessionKindMain, session.SessionRelationship{})
	}
	svc := inventoryService(t, st, true)
	ctx := session.WithPrincipal(context.Background(), alice)

	first, err := svc.ListSessionPage(ctx, server.ListSessionsPageRequest{PageSize: 1})
	if err != nil {
		t.Fatalf("ListSessionPage(first): %v", err)
	}
	if len(first.Sessions) != 1 || first.TotalCount != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v, want one of two owned rows and an opaque cursor", first)
	}
	if first.NextCursor == first.Sessions[0].SessionID {
		t.Fatalf("cursor %q must not be the raw session id", first.NextCursor)
	}

	// A concurrent save changes the catalog generation. Continuing with the old
	// token must request a page-one restart rather than mixing generations.
	now = now.Add(time.Second)
	saveInventorySession(t, st, "new", alice, session.SessionKindMain, session.SessionRelationship{})
	_, err = svc.ListSessionPage(ctx, server.ListSessionsPageRequest{PageSize: 1, Cursor: first.NextCursor})
	if !errors.Is(err, port.ErrSessionMetadataCursorRestart) {
		t.Fatalf("ListSessionPage(second) error = %v, want cursor restart", err)
	}

	maxed, err := svc.ListSessionPage(ctx, server.ListSessionsPageRequest{PageSize: server.MaxSessionInventoryPageSize + 1})
	if err != nil {
		t.Fatalf("oversized page request: %v", err)
	}
	if len(maxed.Sessions) > server.MaxSessionInventoryPageSize {
		t.Fatalf("oversized request returned %d rows, max is %d", len(maxed.Sessions), server.MaxSessionInventoryPageSize)
	}
}

type saveLoadOnlyStore struct{ inner *memstore.Store }

func (s *saveLoadOnlyStore) Save(ctx context.Context, sess *session.Session) error {
	return s.inner.Save(ctx, sess)
}
func (s *saveLoadOnlyStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return s.inner.Load(ctx, id)
}

func TestSessionContinuityUX_Scenario3_TransportParity(t *testing.T) {
	st := memstore.New(memstore.WithNow(func() time.Time { return time.Unix(2_000, 0) }))
	rel := session.SessionRelationship{ParentSessionID: "parent", CallID: "call-1"}
	saveInventorySession(t, st, "child", nil, session.SessionKindSubagent, rel)
	svc := inventoryService(t, st, false)

	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	grpcList, err := client.ListSessions(context.Background(), &mecatlv1.ListSessionsRequest{PageSize: 1})
	if err != nil {
		t.Fatalf("gRPC ListSessions: %v", err)
	}
	grpcTranscript, err := client.GetSessionTranscript(context.Background(), &mecatlv1.GetSessionTranscriptRequest{SessionId: "child"})
	if err != nil {
		t.Fatalf("gRPC GetSessionTranscript: %v", err)
	}

	h := server.NewHTTPHandler(svc)
	listRec := httptest.NewRecorder()
	h.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/v1/sessions?page_size=1", nil))
	if listRec.Code != http.StatusOK {
		t.Fatalf("HTTP ListSessions status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var httpList mecatlv1.ListSessionsResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &httpList); err != nil {
		t.Fatalf("decode HTTP list: %v", err)
	}
	if !proto.Equal(grpcList, &httpList) {
		t.Fatalf("list transport drift:\ngRPC=%+v\nHTTP=%+v", grpcList, &httpList)
	}

	transcriptRec := httptest.NewRecorder()
	h.ServeHTTP(transcriptRec, httptest.NewRequest(http.MethodGet, "/v1/sessions/child/transcript", nil))
	if transcriptRec.Code != http.StatusOK {
		t.Fatalf("HTTP transcript status=%d body=%s", transcriptRec.Code, transcriptRec.Body.String())
	}
	var httpTranscript mecatlv1.GetSessionTranscriptResponse
	if err := json.Unmarshal(transcriptRec.Body.Bytes(), &httpTranscript); err != nil {
		t.Fatalf("decode HTTP transcript: %v", err)
	}
	if !proto.Equal(grpcTranscript, &httpTranscript) {
		t.Fatalf("transcript transport drift:\ngRPC=%+v\nHTTP=%+v", grpcTranscript, &httpTranscript)
	}
	row := grpcList.GetSessions()[0]
	if row.GetKind() != string(session.SessionKindSubagent) || row.GetRelationship().GetParentSessionId() != "parent" ||
		row.GetWorkspace() != "/workspace" || row.GetCapabilities().GetPublicChat() ||
		!row.GetCapabilities().GetAuthoritativeTranscript() || row.GetCapabilities().GetActivityReplay() ||
		row.GetReasonCode() != string(server.CapabilityReasonInspectOnlyKind) {
		t.Fatalf("inventory taxonomy/workspace/capability projection incomplete: %+v", row)
	}
	if grpcTranscript.GetKind() != string(session.SessionKindSubagent) || grpcTranscript.GetRelationship().GetCallId() != "call-1" {
		t.Fatalf("transcript taxonomy projection incomplete: %+v", grpcTranscript)
	}

	unsupported := inventoryService(t, &saveLoadOnlyStore{inner: memstore.New()}, false)
	unsupportedRec := httptest.NewRecorder()
	server.NewHTTPHandler(unsupported).ServeHTTP(unsupportedRec, httptest.NewRequest(http.MethodGet, "/v1/sessions", nil))
	if unsupportedRec.Code != http.StatusNotImplemented {
		t.Fatalf("unsupported pager HTTP status=%d, want %d", unsupportedRec.Code, http.StatusNotImplemented)
	}
}
