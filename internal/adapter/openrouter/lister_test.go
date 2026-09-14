package openrouter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// roundTripFunc adapts a func to http.RoundTripper so a test can serve canned
// bytes (or assert the outbound request) without a network.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestListModels_MappingFromFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/models.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var sawAuth bool
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" {
			sawAuth = true
		}
		if r.URL.String() != openRouterModelsURL {
			t.Errorf("unexpected URL %q, want %q", r.URL.String(), openRouterModelsURL)
		}
		return newResp(http.StatusOK, string(fixture)), nil
	})}

	models, err := NewLister(client).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if sawAuth {
		t.Fatal("CWE-200: lister sent an Authorization header (must be keyless)")
	}
	if len(models) != 5 {
		t.Fatalf("got %d models, want 5", len(models))
	}

	byID := map[string]Model{}
	for _, m := range models {
		byID[m.ID] = m
	}

	// text+image, reasoning, tools, 1M context
	qwen, ok := byID["qwen/qwen3.7-plus"]
	if !ok {
		t.Fatal("missing qwen/qwen3.7-plus")
	}
	if !contains(qwen.InputModalities, "image") {
		t.Errorf("qwen InputModalities=%v, want image", qwen.InputModalities)
	}
	if !qwen.Reasoning || !qwen.ToolCall {
		t.Errorf("qwen reasoning=%v tools=%v, want both true", qwen.Reasoning, qwen.ToolCall)
	}
	if qwen.ContextLimit != 1_000_000 {
		t.Errorf("qwen ContextLimit=%d, want 1000000", qwen.ContextLimit)
	}
	// Slice C: the output ceiling is captured from top_provider.max_completion_tokens.
	if qwen.OutputLimit != 65536 {
		t.Errorf("qwen OutputLimit=%d, want 65536 (top_provider.max_completion_tokens)", qwen.OutputLimit)
	}
	if qwen.DisplayName != "Qwen: Qwen3.7 Plus" {
		t.Errorf("qwen DisplayName=%q", qwen.DisplayName)
	}

	// text-only no reasoning no tools
	fusion := byID["openrouter/fusion"]
	if contains(fusion.InputModalities, "image") {
		t.Errorf("fusion should be text-only, got %v", fusion.InputModalities)
	}
	if fusion.Reasoning || fusion.ToolCall {
		t.Errorf("fusion reasoning=%v tools=%v, want both false", fusion.Reasoning, fusion.ToolCall)
	}
	// fusion's top_provider.max_completion_tokens is null ⇒ OutputLimit 0 (the
	// composition helper then falls back to the catalog floor).
	if fusion.OutputLimit != 0 {
		t.Errorf("fusion OutputLimit=%d, want 0 (null max_completion_tokens)", fusion.OutputLimit)
	}

	// tools but no reasoning, image-capable
	gptChat := byID["openai/gpt-chat-latest"]
	if !gptChat.ToolCall || gptChat.Reasoning {
		t.Errorf("gpt-chat-latest tools=%v reasoning=%v, want tools=true reasoning=false", gptChat.ToolCall, gptChat.Reasoning)
	}
}

func TestListModels_PreservesOmittedVersusExplicitEmptyModalities(t *testing.T) {
	body := `{"data":[{"id":"omitted"},{"id":"empty","architecture":{"input_modalities":[]}}]}`
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusOK, body), nil
	})}
	models, err := NewLister(client).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0].InputModalities != nil || models[1].InputModalities == nil || len(models[1].InputModalities) != 0 {
		t.Fatalf("modalities = %#v, want nil for omitted and non-nil empty for explicit []", models)
	}
}

func TestListModels_NonOKStatusIsError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusInternalServerError, "nope"), nil
	})}
	if _, err := NewLister(client).ListModels(context.Background()); err == nil {
		t.Fatal("expected error on non-2xx status")
	}
}

func TestListModels_MalformedJSONIsError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusOK, "{not json"), nil
	})}
	if _, err := NewLister(client).ListModels(context.Background()); err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

func TestListModels_OversizedResponseIsError(t *testing.T) {
	// Build a body strictly larger than the cap.
	var sb strings.Builder
	sb.WriteString(`{"data":[`)
	for sb.Len() < maxResponseBytes+1024 {
		sb.WriteString(`{"id":"model/x","name":"X"},`)
	}
	sb.WriteString(`{"id":"model/y","name":"Y"}]}`)
	big := sb.String()
	if len(big) <= maxResponseBytes {
		t.Fatalf("test bug: body %d not over cap %d", len(big), maxResponseBytes)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusOK, big), nil
	})}
	_, err := NewLister(client).ListModels(context.Background())
	if err == nil {
		t.Fatal("expected oversized-response error")
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Errorf("error %q should mention the cap", err.Error())
	}
}

func TestListModels_TransportErrorIsError(t *testing.T) {
	wantErr := errors.New("boom")
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, wantErr
	})}
	if _, err := NewLister(client).ListModels(context.Background()); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestListModels_ContextCancelIsError(t *testing.T) {
	// A RoundTripper that blocks until the ctx is done, so a tiny ctx timeout trips
	// the fetch — exercises the context-deadline path without any network.
	blocking := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := NewLister(blocking).ListModels(ctx); err == nil {
		t.Fatal("expected ctx timeout error")
	}
}

func TestListModels_EmptyIDSkipped(t *testing.T) {
	body := `{"data":[{"id":"","name":"ghost"},{"id":"real/model","name":"Real"}]}`
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusOK, body), nil
	})}
	models, err := NewLister(client).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "real/model" {
		t.Fatalf("empty-id entry not skipped: %+v", models)
	}
}

// TestListModels_PerFieldTruncation: a single entry with an oversized id/name is
// rune-truncated in the map loop, not passed through whole (the per-field cap that
// backs up the 4 MiB whole-response cap). Built well under the response cap so the
// body itself is valid — the point is the per-field bound, not the response bound.
func TestListModels_PerFieldTruncation(t *testing.T) {
	bigID := strings.Repeat("a", maxIDRunes+50)
	bigName := strings.Repeat("b", maxNameRunes+50)
	body := `{"data":[{"id":"` + bigID + `","name":"` + bigName + `"}]}`
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusOK, body), nil
	})}
	models, err := NewLister(client).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	if got := len([]rune(models[0].ID)); got != maxIDRunes {
		t.Errorf("id rune length = %d, want truncated to %d", got, maxIDRunes)
	}
	if got := len([]rune(models[0].DisplayName)); got != maxNameRunes {
		t.Errorf("name rune length = %d, want truncated to %d", got, maxNameRunes)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
