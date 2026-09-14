package client

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// fakeModelsClient is a scripted HarnessServiceClient for the ListModels +
// CreateSession wrapper tests. It embeds the interface and overrides only the two
// RPCs under test, recording the requests so the proto-build assertions can run.
type fakeModelsClient struct {
	mecatlv1.HarnessServiceClient

	listResp *mecatlv1.ListModelsResponse
	listErr  error
	lastList *mecatlv1.ListModelsRequest

	createResp *mecatlv1.CreateSessionResponse
	lastCreate *mecatlv1.CreateSessionRequest
}

func (f *fakeModelsClient) ListModels(_ context.Context, in *mecatlv1.ListModelsRequest, _ ...grpc.CallOption) (*mecatlv1.ListModelsResponse, error) {
	f.lastList = in
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.listResp, nil
}

func (f *fakeModelsClient) CreateSession(_ context.Context, in *mecatlv1.CreateSessionRequest, _ ...grpc.CallOption) (*mecatlv1.CreateSessionResponse, error) {
	f.lastCreate = in
	if f.createResp != nil {
		return f.createResp, nil
	}
	return &mecatlv1.CreateSessionResponse{SessionId: "sess-1"}, nil
}

func TestListModelsMapping(t *testing.T) {
	fake := &fakeModelsClient{listResp: &mecatlv1.ListModelsResponse{
		Models: []*mecatlv1.ModelInfo{
			{Id: "gpt-5", ProviderId: "openai", DisplayName: "GPT-5", Image: true, Reasoning: true, ContextLimit: 200000},
			{Id: "o3", ProviderId: "openai", Reasoning: true},
		},
	}}
	cl := newFakeClient(fake)

	ms, _, err := cl.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if fake.lastList == nil {
		t.Fatal("ListModels request not sent")
	}
	if len(ms) != 2 {
		t.Fatalf("models = %d, want 2", len(ms))
	}
	m0 := ms[0]
	if m0.ID != "gpt-5" || m0.ProviderID != "openai" || m0.DisplayName != "GPT-5" {
		t.Fatalf("model[0] = %+v", m0)
	}
	if !m0.Image || !m0.Reasoning || m0.ContextLimit != 200000 {
		t.Fatalf("model[0] caps = img:%v reason:%v ctx:%d", m0.Image, m0.Reasoning, m0.ContextLimit)
	}
	// The second model has no display name / no image — verify the bools default
	// false and the empty fields stay empty (no spurious fallback in the mapper).
	m1 := ms[1]
	if m1.Image || m1.DisplayName != "" || m1.ContextLimit != 0 {
		t.Fatalf("model[1] = %+v, want zero image/display/ctx", m1)
	}
}

func TestListModelsNilSafe(t *testing.T) {
	cl := newFakeClient(&fakeModelsClient{listResp: nil})
	ms, statuses, err := cl.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(ms) != 0 {
		t.Fatalf("models = %d, want 0 (nil response → empty)", len(ms))
	}
	if len(statuses) != 0 {
		t.Fatalf("statuses = %d, want 0 (nil response → empty)", len(statuses))
	}
	// mapModelInfo(nil) is zero.
	if got := mapModelInfo(nil); got != (ModelInfo{}) {
		t.Fatalf("mapModelInfo(nil) = %+v, want zero", got)
	}
}

func TestListModelsError(t *testing.T) {
	cl := newFakeClient(&fakeModelsClient{listErr: errors.New("boom")})
	if _, _, err := cl.ListModels(context.Background()); err == nil {
		t.Fatal("ListModels should propagate the RPC error")
	}
}

// TestListModelsProviderStatusMapping proves ListModelsResponse.provider_status
// (issue #262) maps nil-safely to []ProviderStatus, and (this wave) that the
// model_count + available_not_default fields survive the proto→struct mapping.
func TestListModelsProviderStatusMapping(t *testing.T) {
	fake := &fakeModelsClient{listResp: &mecatlv1.ListModelsResponse{
		ProviderStatus: []*mecatlv1.ProviderStatus{
			{ProviderId: "toolhive", State: "unreachable", Hint: "start it with `thv llm proxy start`"},
			{ProviderId: "toolhive2", State: "ok", Hint: "", ModelCount: 3, AvailableNotDefault: true},
			nil, // defensive: a nil row must never panic the mapper
		},
	}}
	cl := newFakeClient(fake)
	_, statuses, err := cl.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("statuses = %d, want 2 (the nil row skipped)", len(statuses))
	}
	if statuses[0].ProviderID != "toolhive" || statuses[0].State != "unreachable" || statuses[0].Hint == "" {
		t.Fatalf("statuses[0] = %+v", statuses[0])
	}
	// The two new fields default to zero/false on a row that doesn't set them.
	if statuses[0].ModelCount != 0 || statuses[0].AvailableNotDefault {
		t.Errorf("statuses[0] new fields = count:%d avail:%v, want 0/false (unset on the proto)",
			statuses[0].ModelCount, statuses[0].AvailableNotDefault)
	}
	if statuses[1].ProviderID != "toolhive2" || statuses[1].ModelCount != 3 || !statuses[1].AvailableNotDefault {
		t.Fatalf("statuses[1] = %+v, want {toolhive2 count:3 avail:true}", statuses[1])
	}
}

// TestCreateSessionCarriesModelSelection asserts the SINGLE proto-build point sets
// provider_id/model_id from the selection — and leaves them empty for the zero
// selection (⇒ the server default).
func TestCreateSessionCarriesModelSelection(t *testing.T) {
	t.Run("non-zero selection sets both fields", func(t *testing.T) {
		fake := &fakeModelsClient{}
		cl := newFakeClient(fake)
		_, _, _, err := cl.CreateSession(context.Background(),
			mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT,
			ModelSelection{ProviderID: "openrouter", ModelID: "anthropic/claude"})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if fake.lastCreate.GetProviderId() != "openrouter" || fake.lastCreate.GetModelId() != "anthropic/claude" {
			t.Fatalf("request = provider:%q model:%q, want openrouter/anthropic-claude",
				fake.lastCreate.GetProviderId(), fake.lastCreate.GetModelId())
		}
	})
	t.Run("zero selection leaves both empty (server default)", func(t *testing.T) {
		fake := &fakeModelsClient{}
		cl := newFakeClient(fake)
		_, _, _, err := cl.CreateSession(context.Background(),
			mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, ModelSelection{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if fake.lastCreate.GetProviderId() != "" || fake.lastCreate.GetModelId() != "" {
			t.Fatalf("zero selection set provider:%q model:%q, want both empty",
				fake.lastCreate.GetProviderId(), fake.lastCreate.GetModelId())
		}
	})
}

// TestCreateSessionCarriesReasoningEffort asserts the proto-build point sets
// reasoning_effort from the selection (ADR 0055), leaves it empty for an unset
// effort, and that the response's resolved_model.reasoning_effort flows back into
// client.ResolvedModel.ReasoningEffort (the EFFECTIVE effort the server resolved).
func TestCreateSessionCarriesReasoningEffort(t *testing.T) {
	t.Run("effort set on the request", func(t *testing.T) {
		fake := &fakeModelsClient{}
		cl := newFakeClient(fake)
		_, _, _, err := cl.CreateSession(context.Background(),
			mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT,
			ModelSelection{ProviderID: "openai", ModelID: "gpt-5", ReasoningEffort: "high"})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if fake.lastCreate.GetReasoningEffort() != "high" {
			t.Fatalf("request reasoning_effort = %q, want high", fake.lastCreate.GetReasoningEffort())
		}
	})
	t.Run("effort-only selection (no provider/model) still carries the effort", func(t *testing.T) {
		fake := &fakeModelsClient{}
		cl := newFakeClient(fake)
		_, _, _, err := cl.CreateSession(context.Background(),
			mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT,
			ModelSelection{ReasoningEffort: "low"})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if fake.lastCreate.GetReasoningEffort() != "low" {
			t.Fatalf("request reasoning_effort = %q, want low", fake.lastCreate.GetReasoningEffort())
		}
		if fake.lastCreate.GetProviderId() != "" || fake.lastCreate.GetModelId() != "" {
			t.Fatalf("effort-only selection set provider:%q model:%q, want both empty",
				fake.lastCreate.GetProviderId(), fake.lastCreate.GetModelId())
		}
	})
	t.Run("unset effort leaves the field empty", func(t *testing.T) {
		fake := &fakeModelsClient{}
		cl := newFakeClient(fake)
		_, _, _, err := cl.CreateSession(context.Background(),
			mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT,
			ModelSelection{ProviderID: "openai", ModelID: "gpt-5"})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if fake.lastCreate.GetReasoningEffort() != "" {
			t.Fatalf("unset effort set reasoning_effort = %q, want empty", fake.lastCreate.GetReasoningEffort())
		}
	})
	t.Run("resolved effort flows back from the response", func(t *testing.T) {
		// The server normalises/clamps the request (e.g. openai "max" → "high"); the
		// EFFECTIVE value rides resolved_model.reasoning_effort and must land verbatim on
		// the client.ResolvedModel.
		fake := &fakeModelsClient{createResp: &mecatlv1.CreateSessionResponse{
			SessionId: "sess-1",
			ResolvedModel: &mecatlv1.ResolvedModel{
				ProviderId: "openai", ModelId: "gpt-5", ReasoningEffort: "high",
			},
		}}
		cl := newFakeClient(fake)
		_, _, resolved, err := cl.CreateSession(context.Background(),
			mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT,
			ModelSelection{ProviderID: "openai", ModelID: "gpt-5", ReasoningEffort: "max"})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if resolved.ReasoningEffort != "high" {
			t.Fatalf("resolved.ReasoningEffort = %q, want high (server-clamped, echoed back)", resolved.ReasoningEffort)
		}
	})
}

func TestCreateSessionUsesSessionMediaCapabilities(t *testing.T) {
	fake := &fakeModelsClient{createResp: &mecatlv1.CreateSessionResponse{
		SessionId:           "sess-1",
		Capabilities:        &mecatlv1.ServerCapabilities{Image: false, Teams: true},
		SessionCapabilities: &mecatlv1.SessionCapabilities{Image: true},
	}}
	cl := newFakeClient(fake)
	_, caps, _, err := cl.CreateSession(context.Background(), mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT, ModelSelection{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if !caps.Image || !caps.Teams {
		t.Fatalf("capabilities = %+v, want session image overlaid while global feature bits survive", caps)
	}
}

// TestReasoningEffortIsZero asserts an effort-only selection is NOT zero (the client
// must still send it) while a fully-empty selection is.
func TestReasoningEffortIsZero(t *testing.T) {
	if (ModelSelection{ReasoningEffort: "high"}).IsZero() {
		t.Error("an effort-only selection should NOT be zero (it must be sent)")
	}
	if !(ModelSelection{}).IsZero() {
		t.Error("a fully-empty selection should be zero")
	}
	// Matches stays model-only — the effort does not participate.
	sel := ModelSelection{ProviderID: "openai", ModelID: "gpt-5", ReasoningEffort: "high"}
	if !sel.Matches(ModelInfo{ProviderID: "openai", ID: "gpt-5"}) {
		t.Error("Matches should ignore the effort (model-only predicate)")
	}
}

// TestModelSelectionHelpers covers IsZero + Matches (the picker's row-marker /
// reconcile predicates).
func TestModelSelectionHelpers(t *testing.T) {
	if !(ModelSelection{}).IsZero() {
		t.Error("empty selection should be zero")
	}
	if (ModelSelection{ProviderID: "openai"}).IsZero() {
		t.Error("a selection with a provider should not be zero")
	}
	sel := ModelSelection{ProviderID: "openai", ModelID: "gpt-5"}
	if !sel.Matches(ModelInfo{ProviderID: "openai", ID: "gpt-5"}) {
		t.Error("selection should match the same provider+id")
	}
	if sel.Matches(ModelInfo{ProviderID: "openai", ID: "o3"}) {
		t.Error("selection should NOT match a different id")
	}
	if sel.Matches(ModelInfo{ProviderID: "openrouter", ID: "gpt-5"}) {
		t.Error("selection should NOT match a different provider")
	}
}

func TestModeHelpers(t *testing.T) {
	if got := ModeString(ModeFromString("accept-edits")); got != "accept-edits" {
		t.Fatalf("accept-edits round trip = %q", got)
	}
	cases := []struct {
		in   string
		want string
	}{
		{"default", "plan"},
		{"plan", "accept-edits"},
		{"accept-edits", "default"},
		{"", "plan"},
	}
	for _, tc := range cases {
		if got := NextMode(tc.in); got != tc.want {
			t.Errorf("NextMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
