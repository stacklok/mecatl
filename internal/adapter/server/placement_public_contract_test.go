package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
)

const adrPrivateRoot = "/srv/private/repository"

type adrPlacementProvider struct{ selectors *WorktreeSelectorIssuer }

func (p adrPlacementProvider) Bind(_ context.Context, req PlacementBindRequest) (PlacementBinding, error) {
	if req.Selector.Kind == PlacementSelectorNoFS {
		ref := session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "none", Revision: "v1"}
		return PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, nofs.New(), nil), Metadata: PlacementMetadata{Label: "No filesystem"}}, nil
	}
	if req.Selector.IsWorktree() {
		current, _ := (adrWorktrees{}).List(context.Background(), req.Selector.SourceRef.ID)
		choice, err := p.selectors.Match(req.Selector.ID, req.Principal, req.Selector.Source, current)
		if err != nil {
			return PlacementBinding{}, err
		}
		ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: choice.Path, Revision: choice.Head}
		return PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace(choice.Path), nil), Metadata: PlacementMetadata{Label: choice.Branch, Branch: choice.Branch, Revision: choice.Head}}, nil
	}
	return adrPlacementBinding(), nil
}

func (adrPlacementProvider) Reattach(_ context.Context, req PlacementReattachRequest) (PlacementBinding, error) {
	binding := adrPlacementBinding()
	if req.Ref.Kind == session.EnvKindNoFS {
		return adrPlacementProvider{}.Bind(context.Background(), PlacementBindRequest{Selector: NoFSPlacement()})
	}
	binding.Ref = req.Ref
	binding.Environment = tool.MustEnvironment(req.Ref, memfs.NewWorkspace(adrPrivateRoot), nil)
	return binding, nil
}

func adrPlacementBinding() PlacementBinding {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: adrPrivateRoot, Revision: "rev-private"}
	return PlacementBinding{
		Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace(adrPrivateRoot), nil),
		Metadata: PlacementMetadata{Label: "Primary repository", Branch: "main", Revision: "display-rev"},
	}
}

type adrWorktrees struct{}

func (adrWorktrees) List(context.Context, string) ([]Worktree, error) {
	return []Worktree{{Path: adrPrivateRoot, Branch: "main", Head: "abc123"}, {Path: "/srv/private/feature", Branch: "feature", Head: "def456"}}, nil
}

func (p adrPlacementProvider) ListWorktrees(_ context.Context, req PlacementDiscoveryRequest) ([]ScopedWorktree, error) {
	current, _ := (adrWorktrees{}).List(context.Background(), req.SourceRef.ID)
	out := make([]ScopedWorktree, 0, len(current))
	for _, choice := range current {
		out = append(out, ScopedWorktree{Selector: p.selectors.Issue(req.Principal, req.Source, choice), Label: choice.Branch, Branch: choice.Branch, Revision: choice.Head})
	}
	return out, nil
}

func newADR0288Service(t *testing.T) *Service {
	t.Helper()
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test"})
	var key [worktreeSelectorKeySize]byte
	for i := range key {
		key[i] = byte(i + 1)
	}
	issuer, err := NewWorktreeSelectorIssuer(key[:])
	if err != nil {
		t.Fatal(err)
	}
	provider := adrPlacementProvider{selectors: issuer}
	svc, err := NewService(Config{
		Engine: eng, Store: memstore.New(),
		Now: func() time.Time { return time.Unix(1, 0) }, PlacementProvider: provider, PlacementScope: "test",

		SessionEngine: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
			return SessionEngineResult{Engine: eng, Close: func() error { return nil }}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestADR_0288_CreateSessionAcceptsOnlyDefaultOrNoFS(t *testing.T) {
	h := NewHarnessServer(newADR0288Service(t))
	for _, req := range []*mecatlv1.CreateSessionRequest{{}, {Profile: "no-fs"}} {
		resp, err := h.CreateSession(context.Background(), req)
		if err != nil {
			t.Fatalf("CreateSession(%q): %v", req.GetProfile(), err)
		}
		if resp.GetSessionId() == "" || resp.GetPlacement() == nil {
			t.Fatalf("response = %+v", resp)
		}
		if got := resp.GetPlacement(); req.GetProfile() == "" && (got.GetLabel() != "Primary repository" || got.GetBranch() != "main" || got.GetRevision() != "display-rev") {
			t.Fatalf("create placement metadata = %+v", got)
		}
		if strings.Contains(resp.String(), adrPrivateRoot) || strings.Contains(resp.String(), "rev-private") {
			t.Fatalf("private placement escaped in response: %v", resp)
		}
	}
}

func TestADR_0288_PublicHarnessContractContainsNoFilesystemPaths(t *testing.T) {
	for _, msg := range []interface{ ProtoReflect() protoreflect.Message }{
		&mecatlv1.CreateSessionRequest{}, &mecatlv1.Session{}, &mecatlv1.SessionSummary{},
		&mecatlv1.ListCommandsRequest{}, &mecatlv1.ListWorktreesRequest{}, &mecatlv1.Worktree{},
		&mecatlv1.CreateTeamRequest{}, &mecatlv1.Parallel{},
	} {
		fields := msg.ProtoReflect().Descriptor().Fields()
		for _, forbidden := range []protoreflect.Name{"workspace", "path", "winner_workspace", "environment_id"} {
			if fields.ByName(forbidden) != nil {
				t.Fatalf("%s still exposes %q", msg.ProtoReflect().Descriptor().FullName(), forbidden)
			}
		}
	}

	svc := newADR0288Service(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/sessions", bytes.NewBufferString(`{"workspace":"/attacker"}`))
	rec := httptest.NewRecorder()
	NewHTTPHandler(svc).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown field") {
		t.Fatalf("obsolete workspace request = %d %s", rec.Code, rec.Body.String())
	}
}

func TestInvariant_physical_placement_paths_never_cross_public_api(t *testing.T) {
	svc := newADR0288Service(t)
	h := NewHarnessServer(svc)
	created, err := h.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.GetSession(context.Background(), &mecatlv1.GetSessionRequest{SessionId: created.GetSessionId()})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(adrPrivateRoot)) || bytes.Contains(raw, []byte("rev-private")) {
		t.Fatalf("private placement escaped: %s", raw)
	}
}

func TestADR_0288_DiscoveryIsSessionScopedAndOwnerAuthorized(t *testing.T) {
	svc := newADR0288Service(t)
	h := NewHarnessServer(svc)
	created, err := h.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.ListWorktrees(context.Background(), &mecatlv1.ListWorktreesRequest{SessionId: created.GetSessionId()})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetWorktrees()) != 2 || resp.GetWorktrees()[0].GetSelector() == "" {
		t.Fatalf("worktrees = %+v", resp.GetWorktrees())
	}
	for _, wt := range resp.GetWorktrees() {
		if strings.Contains(wt.String(), "/srv/") {
			t.Fatalf("physical path escaped: %v", wt)
		}
	}
}

func TestADR_0288_ClearSessionCreatesEmptyInheritedSuccessor(t *testing.T) {
	svc := newADR0288Service(t)
	h := NewHarnessServer(svc)
	created, err := h.CreateSession(context.Background(), &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := h.ClearSession(context.Background(), &mecatlv1.ClearSessionRequest{SourceSessionId: created.GetSessionId()})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.GetSessionId() == "" || cleared.GetSessionId() == created.GetSessionId() {
		t.Fatalf("clear response = %+v", cleared)
	}
	sess, err := svc.GetSession(context.Background(), session.SessionID(cleared.GetSessionId()))
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Conversation.Messages) != 0 || sess.EnvironmentRef.ID != adrPrivateRoot {
		t.Fatalf("successor = %+v", sess)
	}
	if got := cleared.GetPlacement(); got.GetLabel() != "Primary repository" || got.GetBranch() != "main" || got.GetRevision() != "display-rev" {
		t.Fatalf("clear placement metadata = %+v", got)
	}
	if strings.Contains(cleared.String(), adrPrivateRoot) {
		t.Fatalf("private path escaped: %v", cleared)
	}
}
