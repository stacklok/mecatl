package server_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// fakeUserModel is a scripted server.UserModelLister for the GetUserModel tests:
// it returns the canned entries (or an error), reflecting a LIVE store read.
type fakeUserModel struct {
	entries []server.UserModelEntry
	err     error
	calls   int
}

func (f *fakeUserModel) List(context.Context) ([]server.UserModelEntry, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.entries, nil
}

// userModelService builds a Service carrying the given user-model lister. A nil
// lister means the user model is disabled (capabilities().UserModel false;
// GetUserModel returns an empty response).
func userModelService(t *testing.T, lister server.UserModelLister) *server.Service {
	t.Helper()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(allowRules(), nil),
		Model:   "test-model",
	})
	svc, err := server.NewService(server.Config{
		Engine:     engine,
		Store:      memstore.New(),
		Workspaces: func(root string) tool.Workspace { return memfs.NewWorkspace(root) },
		Now:        func() time.Time { return time.Unix(0, 0) },
		UserModel:  lister,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func cannedUserModel() *fakeUserModel {
	return &fakeUserModel{entries: []server.UserModelEntry{
		{Key: "name", Description: "the operator's name"},
		{Key: "stack", Description: "prefers Go + hexagonal architecture"},
	}}
}

func TestGRPCGetUserModel(t *testing.T) {
	fum := cannedUserModel()
	svc := userModelService(t, fum)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.GetUserModel(context.Background(), &mecatlv1.GetUserModelRequest{})
	if err != nil {
		t.Fatalf("GetUserModel: %v", err)
	}
	if len(resp.GetEntries()) != 2 {
		t.Fatalf("entries = %d, want 2", len(resp.GetEntries()))
	}
	if resp.GetEntries()[0].GetKey() != "name" || resp.GetEntries()[0].GetDescription() != "the operator's name" {
		t.Fatalf("entry[0] = %+v", resp.GetEntries()[0])
	}
	if resp.GetSizeBytes() == 0 || resp.GetSha256() == "" {
		t.Errorf("aggregate size/hash not populated: size=%d sha=%q", resp.GetSizeBytes(), resp.GetSha256())
	}
	if fum.calls != 1 {
		t.Errorf("List calls = %d, want 1 (live read)", fum.calls)
	}
}

func TestGRPCGetUserModelEmpty(t *testing.T) {
	// No lister wired (disabled) => empty response, no error.
	svc := userModelService(t, nil)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	resp, err := client.GetUserModel(context.Background(), &mecatlv1.GetUserModelRequest{})
	if err != nil {
		t.Fatalf("GetUserModel: %v", err)
	}
	if len(resp.GetEntries()) != 0 {
		t.Fatalf("entries = %d, want 0", len(resp.GetEntries()))
	}
}

func TestGRPCGetUserModelError(t *testing.T) {
	svc := userModelService(t, &fakeUserModel{err: errors.New("boom")})
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	_, err := client.GetUserModel(context.Background(), &mecatlv1.GetUserModelRequest{})
	if err == nil {
		t.Fatal("GetUserModel should surface a store fault as an error")
	}
}

func TestHTTPGetUserModel(t *testing.T) {
	svc := userModelService(t, cannedUserModel())
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	var resp mecatlv1.GetUserModelResponse
	if code := httpGet(t, srv, "/v1/usermodel", &resp); code != 200 {
		t.Fatalf("GET /v1/usermodel status = %d", code)
	}
	if len(resp.GetEntries()) != 2 {
		t.Fatalf("http entries = %d, want 2", len(resp.GetEntries()))
	}
}

type lifecycleUserModel struct {
	*fakeUserModel
	record tool.MemoryRecord
}

func (f *lifecycleUserModel) Inspect(_ context.Context, key string) (tool.MemoryRecord, bool, error) {
	return f.record, key == f.record.Current.Key, nil
}

func TestGRPCGetUserModelDetailIsExactReadOnlyProjection(t *testing.T) {
	revision := tool.MemoryRevision{Key: "user/locale", Value: "日本語 — français", Description: "preferred locale", Version: "v1", Status: tool.MemoryStatusActive, Writer: tool.MemoryWriterUser, Origin: tool.MemoryOriginExplicit, UpdatedAt: time.Unix(10, 0)}
	lister := &lifecycleUserModel{fakeUserModel: &fakeUserModel{entries: []server.UserModelEntry{{Key: revision.Key, Description: revision.Description}}}, record: tool.MemoryRecord{Current: revision, Revisions: []tool.MemoryRevision{revision}}}
	client, cleanup := dialGRPC(t, userModelService(t, lister))
	defer cleanup()
	resp, err := client.GetUserModel(context.Background(), &mecatlv1.GetUserModelRequest{Key: revision.Key})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetDetail().GetCurrent().GetValue() != revision.Value || !resp.GetDetail().GetHistoryAvailable() || len(resp.GetDetail().GetHistory()) != 1 {
		t.Fatalf("detail = %+v", resp.GetDetail())
	}
}

func TestGRPCGetUserModelDetailWithholdsImportedSecret(t *testing.T) {
	revision := tool.MemoryRevision{Key: "user/token", Value: "ghp_0123456789abcdefghijklmnop", Status: tool.MemoryStatusActive}
	lister := &lifecycleUserModel{fakeUserModel: &fakeUserModel{entries: []server.UserModelEntry{{Key: revision.Key}}}, record: tool.MemoryRecord{Current: revision, Revisions: []tool.MemoryRevision{revision}}}
	client, cleanup := dialGRPC(t, userModelService(t, lister))
	defer cleanup()
	resp, err := client.GetUserModel(context.Background(), &mecatlv1.GetUserModelRequest{Key: revision.Key})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.GetDetail().GetCurrent().GetValue(); got == revision.Value || got == "" {
		t.Fatalf("secret projection = %q", got)
	}
}

func TestGRPCGetUserModelWithholdsSecretDescriptionsAndSanitizesDetail(t *testing.T) {
	bad := "bad\x00\x1b\u0085\u202e\u2066\u200b\ufeff" + string([]byte{0xff})
	secretDescription := "token: ghp_0123456789abcdefghijklmnop"
	revision := tool.MemoryRevision{Key: "user/detail", Value: "ordinary 日本語 " + bad, Description: secretDescription, Version: tool.MemoryVersion(bad), Status: tool.MemoryStatus(bad), Writer: tool.MemoryWriter(bad), Origin: tool.MemoryOrigin(bad), Source: tool.MemorySource{SessionID: bad}}
	lister := &lifecycleUserModel{fakeUserModel: &fakeUserModel{entries: []server.UserModelEntry{{Key: revision.Key, Description: secretDescription}}}, record: tool.MemoryRecord{Current: revision, Revisions: []tool.MemoryRevision{revision}}}
	client, cleanup := dialGRPC(t, userModelService(t, lister))
	defer cleanup()
	resp, err := client.GetUserModel(context.Background(), &mecatlv1.GetUserModelRequest{Key: revision.Key})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.GetEntries()[0].GetDescription(); got == secretDescription || !strings.Contains(got, "withheld") {
		t.Fatalf("secret list description projected as %q", got)
	}
	current := resp.GetDetail().GetCurrent()
	if got := current.GetDescription(); got == secretDescription || !strings.Contains(got, "withheld") {
		t.Fatalf("secret detail description projected as %q", got)
	}
	for name, value := range map[string]string{"key": current.GetKey(), "value": current.GetValue(), "description": current.GetDescription(), "version": current.GetVersion(), "status": current.GetStatus(), "writer": current.GetWriter(), "origin": current.GetOrigin(), "source": current.GetSourceSessionId()} {
		if !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\x1b\u0085\u202e\u2066\u200b\ufeff") {
			t.Errorf("%s was not wire-sanitized: %q", name, value)
		}
	}
	if !strings.Contains(current.GetValue(), "ordinary 日本語") {
		t.Fatalf("ordinary Unicode lost: %q", current.GetValue())
	}
}

// TestUserModelCapability asserts the user_model cap flips with a wired lister.
func TestUserModelCapability(t *testing.T) {
	on := capsFromCreate(t, userModelService(t, cannedUserModel()))
	if !on.GetUserModel() {
		t.Error("user_model cap = false, want true (lister wired)")
	}
	off := capsFromCreate(t, userModelService(t, nil))
	if off.GetUserModel() {
		t.Error("user_model cap = true, want false (no lister)")
	}
}
