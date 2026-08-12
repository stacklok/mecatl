package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
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

const testBaseURL = "http://127.0.0.1:14000/v1"

func TestListModels_MappingFromFixture(t *testing.T) {
	fixture, err := os.ReadFile("testdata/models.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var gotAuth, gotURL string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotAuth = r.Header.Get("Authorization")
		gotURL = r.URL.String()
		return newResp(http.StatusOK, string(fixture)), nil
	})}

	models, err := NewLister(testBaseURL, "thv-proxy", client).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if gotURL != testBaseURL+"/models" {
		t.Errorf("URL = %q, want %q", gotURL, testBaseURL+"/models")
	}
	if gotAuth != "Bearer thv-proxy" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer thv-proxy")
	}
	if len(models) != 3 {
		t.Fatalf("got %d models, want 3", len(models))
	}
	byID := map[string]Model{}
	for _, m := range models {
		byID[m.ID] = m
	}
	if got := byID["claude-sonnet-4-6"].DisplayName; got != "Claude Sonnet 4.6" {
		t.Errorf("DisplayName = %q", got)
	}
	if got := byID["claude-sonnet-4-6"].ContextLimit; got != 1_000_000 {
		t.Errorf("ContextLimit = %d, want 1000000", got)
	}
	// display_name and context_window absent ⇒ their zero values pass through
	// (display falls back to id and context to catalog/default in composition).
	if m, ok := byID["no-display-name"]; !ok || m.DisplayName != "" || m.ContextLimit != 0 {
		t.Errorf("no-display-name entry: %+v, ok=%v", m, ok)
	}
}

func TestListModels_NoBearerToken_NoAuthHeader(t *testing.T) {
	var gotAuth string
	sawAuth := false
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotAuth, sawAuth = r.Header.Get("Authorization"), r.Header.Get("Authorization") != ""
		return newResp(http.StatusOK, `{"data":[]}`), nil
	})}
	if _, err := NewLister(testBaseURL, "", client).ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if sawAuth {
		t.Fatalf("unexpected Authorization header %q with an empty bearer token", gotAuth)
	}
}

func TestListModels_EmptyData(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusOK, `{"object":"list","data":[]}`), nil
	})}
	models, err := NewLister(testBaseURL, "", client).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("got %d models, want 0", len(models))
	}
}

func TestListModels_401IsStatusError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusUnauthorized, "nope"), nil
	})}
	_, err := NewLister(testBaseURL, "bad-token", client).ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error on 401")
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error %v is not a *StatusError", err)
	}
	if statusErr.Code != http.StatusUnauthorized {
		t.Errorf("StatusError.Code = %d, want 401", statusErr.Code)
	}
}

func TestListModels_403IsStatusError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusForbidden, "nope"), nil
	})}
	_, err := NewLister(testBaseURL, "", client).ListModels(context.Background())
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.Code != http.StatusForbidden {
		t.Fatalf("expected StatusError{403}, got %v", err)
	}
}

func TestListModels_500IsError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusInternalServerError, "nope"), nil
	})}
	_, err := NewLister(testBaseURL, "", client).ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error on 500")
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.Code != http.StatusInternalServerError {
		t.Fatalf("expected StatusError{500}, got %v", err)
	}
}

// TestListModels_DefaultClientDoesNotFollowRedirects pins CWE-918: a
// hostile/misconfigured listener on the probed port answering with a
// redirect must NEVER be allowed to bounce the request off-loopback. Uses a
// REAL httptest server (not the roundTripFunc mock) + NewLister's DEFAULT
// client (nil), since the CheckRedirect gate lives on that default — a mock
// RoundTripper bypasses net/http's redirect-following logic entirely and
// would not exercise the gate.
func TestListModels_DefaultClientDoesNotFollowRedirects(t *testing.T) {
	var hops int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hops, 1)
		http.Redirect(w, r, "http://evil.example.invalid/models", http.StatusFound)
	}))
	defer srv.Close()

	_, err := NewLister(srv.URL, "", nil).ListModels(context.Background())
	if err == nil {
		t.Fatal("expected an error: a 3xx response must surface, not be followed")
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.Code != http.StatusFound {
		t.Fatalf("expected StatusError{302}, got %v", err)
	}
	if got := atomic.LoadInt32(&hops); got != 1 {
		t.Fatalf("server received %d request(s), want exactly 1 (the redirect must not be followed)", got)
	}
}

func TestListModels_MalformedJSONIsError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusOK, "{not json"), nil
	})}
	if _, err := NewLister(testBaseURL, "", client).ListModels(context.Background()); err == nil {
		t.Fatal("expected error on malformed JSON")
	}
}

func TestListModels_OversizedResponseIsError(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"data":[`)
	for sb.Len() < maxResponseBytes+1024 {
		sb.WriteString(`{"id":"model-x","display_name":"X"},`)
	}
	sb.WriteString(`{"id":"model-y","display_name":"Y"}]}`)
	big := sb.String()
	if len(big) <= maxResponseBytes {
		t.Fatalf("test bug: body %d not over cap %d", len(big), maxResponseBytes)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusOK, big), nil
	})}
	_, err := NewLister(testBaseURL, "", client).ListModels(context.Background())
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
	if _, err := NewLister(testBaseURL, "", client).ListModels(context.Background()); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestListModels_ContextCancelIsError(t *testing.T) {
	blocking := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := NewLister(testBaseURL, "", blocking).ListModels(ctx); err == nil {
		t.Fatal("expected ctx timeout error")
	}
}

func TestListModels_EmptyIDSkipped(t *testing.T) {
	body := `{"data":[{"id":"","display_name":"ghost"},{"id":"real-model","display_name":"Real"}]}`
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusOK, body), nil
	})}
	models, err := NewLister(testBaseURL, "", client).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "real-model" {
		t.Fatalf("empty-id entry not skipped: %+v", models)
	}
}

// TestListModels_ControlBytesStripped: CWE-117/116 — a hostile id/display_name
// carrying a terminal-escape / NUL / DEL sequence must never survive into a
// picker row.
func TestListModels_ControlBytesStripped(t *testing.T) {
	// Built via byte concatenation (not embedded as literal control bytes in the
	// source) so the malicious id/display_name never touch the .go file itself:
	// a terminal-escape sequence (ESC ']0;pwn' BEL), a NUL, and a DEL.
	rawID := "model-" + string([]byte{0x1b}) + "]0;pwn" + string([]byte{0x07}) + "-x"
	rawName := "evil" + string([]byte{0x00}) + "name" + string([]byte{0x7f})
	payload, err := json.Marshal(struct {
		Data []wireModel `json:"data"`
	}{Data: []wireModel{{ID: rawID, DisplayName: rawName}}})
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusOK, string(payload)), nil
	})}
	models, err := NewLister(testBaseURL, "", client).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	controlBytes := string([]byte{0x1b, 0x00, 0x07, 0x7f})
	if strings.ContainsAny(models[0].ID, controlBytes) {
		t.Errorf("ID retains a control byte: %q", models[0].ID)
	}
	if strings.ContainsAny(models[0].DisplayName, controlBytes) {
		t.Errorf("DisplayName retains a control byte: %q", models[0].DisplayName)
	}
	if want := "model-]0;pwn-x"; models[0].ID != want {
		t.Errorf("ID = %q, want %q", models[0].ID, want)
	}
	if want := "evilname"; models[0].DisplayName != want {
		t.Errorf("DisplayName = %q, want %q", models[0].DisplayName, want)
	}
}
func TestListModels_PerFieldTruncation(t *testing.T) {
	bigID := strings.Repeat("a", maxIDRunes+50)
	bigName := strings.Repeat("b", maxNameRunes+50)
	body := `{"data":[{"id":"` + bigID + `","display_name":"` + bigName + `"}]}`
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return newResp(http.StatusOK, body), nil
	})}
	models, err := NewLister(testBaseURL, "", client).ListModels(context.Background())
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
