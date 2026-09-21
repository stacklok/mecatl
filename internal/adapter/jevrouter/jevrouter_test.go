package jevrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testQuestionID = "delegated-model-category"

func newTestRouter(t *testing.T, handler http.HandlerFunc, minimumConfidence float64) (*Router, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	r, err := New(Options{
		APIKey: "test-secret", Model: "jev-1.13.0", BaseURL: srv.URL,
		MinimumConfidence: minimumConfidence, HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, &calls
}

func validResponse(choice string, confidence float64, input, output int) string {
	other := "deep"
	if choice == other {
		other = "fast"
	}
	return fmt.Sprintf(`{"model":"jev-1.13.0","answers":{"%s":{"type":"choice","choice":%q,"probabilities":{%q:0.9,%q:0.1},"confidence":%v}},"usage":{"input_tokens":%d,"output_tokens":%d}}`, testQuestionID, choice, choice, other, confidence, input, output)
}

func testCategories() []Category {
	return []Category{{Name: "fast", Description: "small task"}, {Name: "deep", Description: "complex task"}}
}

func TestADR_0350_Scenario2_InvalidResponseFallsBack(t *testing.T) {
	tests := []struct {
		name, response, wantReason string
	}{
		{"unknown choice", validResponse("other", 0.9, 1, 2), MissInvalidResponse},
		{"malformed response", `{`, MissInvalidResponse},
		{"non-finite probability", `{"model":"jev-1.13.0","answers":{"delegated-model-category":{"type":"choice","choice":"fast","probabilities":{"fast":1e999,"deep":0},"confidence":1}},"usage":{"input_tokens":1,"output_tokens":2}}`, MissInvalidResponse},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newTestRouter(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.response)) }, 0)
			category, _, reason, ok := r.Route(t.Context(), "private task", testCategories())
			if ok || category != "" || reason != tc.wantReason {
				t.Fatalf("Route = (%q, reason=%q, ok=%v), want empty %q false", category, reason, ok, tc.wantReason)
			}
		})
	}

	r, calls := newTestRouter(t, func(http.ResponseWriter, *http.Request) { t.Fatal("over-limit request performed I/O") }, 0)
	category, _, reason, ok := r.Route(t.Context(), strings.Repeat("x", maxTextBytes+1), testCategories())
	if ok || category != "" || reason != MissOverLimit || calls.Load() != 0 {
		t.Fatalf("over-limit Route = (%q, reason=%q, ok=%v), calls=%d", category, reason, ok, calls.Load())
	}
}

func TestADR_0350_Scenario2_LowConfidenceAbstains(t *testing.T) {
	r, _ := newTestRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(validResponse("fast", 0.49, 3, 4)))
	}, 0.5)
	category, usage, reason, ok := r.Route(t.Context(), "task", testCategories())
	if ok || category != "" || reason != MissLowConfidence {
		t.Fatalf("Route = (%q, reason=%q, ok=%v), want low-confidence miss", category, reason, ok)
	}
	if usage.InputTokens != 3 || usage.OutputTokens != 4 {
		t.Fatalf("low-confidence usage = %+v, want 3/4", usage)
	}
}

func TestADR_0350_Scenario3_HitUsage(t *testing.T) {
	r, _ := newTestRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(validResponse("deep", 0.9, 11, 7)))
	}, 0)
	category, usage, reason, ok := r.Route(t.Context(), "task", testCategories())
	if !ok || category != "deep" || reason != "" {
		t.Fatalf("Route = (%q, reason=%q, ok=%v), want deep hit", category, reason, ok)
	}
	if usage.InputTokens != 11 || usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v, want 11/7", usage)
	}
}

func TestADR_0350_Scenario3_ErrorUsage(t *testing.T) {
	for _, tc := range []struct {
		name, response        string
		wantInput, wantOutput int
	}{
		{"reported", `{"model":"jev-1.13.0","answers":{},"usage":{"input_tokens":5,"output_tokens":2}}`, 5, 2},
		{"reported zero", `{"model":"jev-1.13.0","answers":{},"usage":{"input_tokens":0,"output_tokens":0}}`, 0, 0},
		{"absent", `{"model":"jev-1.13.0","answers":{}}`, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newTestRouter(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.response)) }, 0)
			category, usage, reason, ok := r.Route(t.Context(), "task", testCategories())
			if ok || category != "" || reason != MissInvalidResponse {
				t.Fatalf("Route = (%q, reason=%q, ok=%v), want protocol miss", category, reason, ok)
			}
			if usage.InputTokens != tc.wantInput || usage.OutputTokens != tc.wantOutput {
				t.Fatalf("usage = %+v, want %d/%d", usage, tc.wantInput, tc.wantOutput)
			}
		})
	}
}

func TestADR_0350_Scenario4_RequestLimits(t *testing.T) {
	var body map[string]any
	r, calls := newTestRouter(t, func(w http.ResponseWriter, req *http.Request) {
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(validResponse("fast", 1, 1, 1)))
	}, 0)
	if _, _, _, ok := r.Route(t.Context(), "task", testCategories()); !ok {
		t.Fatal("bounded request should route")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	if body["state"] != "task" || body["model"] != "jev-1.13.0" {
		t.Fatalf("request = %#v", body)
	}
	questions, ok := body["questions"].(map[string]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("questions = %#v", body["questions"])
	}
	question, ok := questions[testQuestionID].(map[string]any)
	if !ok {
		t.Fatalf("question %q = %#v", testQuestionID, questions[testQuestionID])
	}
	if question["type"] != "choice" || question["instructions"] != classifierInstructions {
		t.Fatalf("question shape = %#v", question)
	}
	criteria, ok := question["criteria"].(map[string]any)
	if !ok || len(criteria) != 2 || criteria["fast"] != "small task" || criteria["deep"] != "complex task" {
		t.Fatalf("criteria = %#v", question["criteria"])
	}
	for name, description := range criteria {
		if name == "task" || description == "task" {
			t.Fatalf("raw task leaked into trusted criteria: %#v", criteria)
		}
	}

	tooMany := make([]Category, maxCategories+1)
	for i := range tooMany {
		tooMany[i] = Category{Name: fmt.Sprintf("c%d", i), Description: "d"}
	}
	_, _, reason, ok := r.Route(t.Context(), "task", tooMany)
	if ok || reason != MissOverLimit || calls.Load() != 1 {
		t.Fatalf("category cap reason=%q ok=%v calls=%d", reason, ok, calls.Load())
	}

	fixed := len(classifierInstructions) + len(defaultModel) + len(questionID)
	boundaryTask := strings.Repeat("x", maxTextBytes-fixed-len("fast")-len("small task")-len("deep")-len("complex task"))
	if _, _, reason, ok := r.Route(t.Context(), boundaryTask, testCategories()); !ok {
		t.Fatalf("exact limit rejected: %q", reason)
	}
	if _, _, reason, ok := r.Route(t.Context(), boundaryTask+"x", testCategories()); ok || reason != MissOverLimit {
		t.Fatalf("over limit accepted: %q", reason)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestADR_0350_Scenario4_BoundedTransport(t *testing.T) {
	bounds := defaultBounds()
	if bounds.maxConcurrent != 8 || bounds.queueTimeout != 10*time.Second || bounds.requestTimeout != 10*time.Second || responseLimit != 1<<20 {
		t.Fatalf("transport bounds drifted: %+v response=%d", bounds, responseLimit)
	}
	for _, baseURL := range []string{
		"http://example.com",
		"https://user@example.com",
		"https://example.com/v1?tenant=x",
		"https://example.com/v1#fragment",
		"https://example.com/v1/../admin",
	} {
		if _, err := New(Options{APIKey: "x", BaseURL: baseURL}); err == nil {
			t.Fatalf("SDK accepted invalid base URL %q", baseURL)
		}
	}
	if _, err := New(Options{APIKey: "x", BaseURL: "https://example.com/prefix"}); err != nil {
		t.Fatalf("SDK rejected HTTPS path prefix: %v", err)
	}

	var retryCalls atomic.Int32
	retryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		retryCalls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(retryServer.Close)
	retryRouter, err := New(Options{APIKey: "test", BaseURL: retryServer.URL, HTTPClient: retryServer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = retryRouter.Route(t.Context(), "task", testCategories())
	if retryCalls.Load() != 1 {
		t.Fatalf("SDK retries = %d calls, want one attempt", retryCalls.Load())
	}

	var redirectedCalls atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectedCalls.Add(1) }))
	t.Cleanup(redirectTarget.Close)
	redirectSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", redirectTarget.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirectSource.Close)
	redirectRouter, err := New(Options{APIKey: "test", BaseURL: redirectSource.URL, HTTPClient: redirectSource.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, _ = redirectRouter.Route(t.Context(), "task", testCategories())
	if redirectedCalls.Load() != 0 {
		t.Fatal("redirect target received the classified task")
	}

	t.Run("queue timeout is distinct and sends no request", func(t *testing.T) {
		var calls atomic.Int32
		r, err := newRouter(Options{
			APIKey: "test", BaseURL: "https://example.com",
			HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, fmt.Errorf("unexpected request")
			})},
		}, transportBounds{maxConcurrent: 1, queueTimeout: 5 * time.Millisecond, requestTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		r.semaphore <- struct{}{}
		category, _, reason, ok := r.Route(t.Context(), "queued", testCategories())
		if ok || category != "" || reason != MissQueueTimeout || calls.Load() != 0 {
			t.Fatalf("saturated Route = (%q, reason=%q, ok=%v), calls=%d", category, reason, ok, calls.Load())
		}
		<-r.semaphore
	})

	t.Run("queued cancellation sends no request", func(t *testing.T) {
		var calls atomic.Int32
		r, err := newRouter(Options{
			APIKey: "test", BaseURL: "https://example.com",
			HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(validResponse("fast", 1, 1, 1))),
					Request:    req,
				}, nil
			})},
		}, transportBounds{maxConcurrent: 1, queueTimeout: time.Second, requestTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		r.semaphore <- struct{}{}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		category, _, reason, ok := r.Route(ctx, "cancelled", testCategories())
		if ok || category != "" || reason != MissError || calls.Load() != 0 {
			t.Fatalf("cancelled Route = (%q, reason=%q, ok=%v), calls=%d", category, reason, ok, calls.Load())
		}
		<-r.semaphore
		category, _, reason, ok = r.Route(t.Context(), "after cancellation", testCategories())
		if !ok || category != "fast" || reason != "" || calls.Load() != 1 {
			t.Fatalf("post-cancellation Route = (%q, reason=%q, ok=%v), calls=%d", category, reason, ok, calls.Load())
		}
	})

	t.Run("request timeout cancels in flight and releases capacity", func(t *testing.T) {
		var calls atomic.Int32
		cancelled := make(chan struct{})
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				<-req.Context().Done()
				close(cancelled)
				return nil, req.Context().Err()
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(validResponse("fast", 1, 1, 1))),
				Request:    req,
			}, nil
		})}
		r, err := newRouter(Options{APIKey: "test", BaseURL: "https://example.com", HTTPClient: client},
			transportBounds{maxConcurrent: 1, queueTimeout: time.Second, requestTimeout: 5 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		category, _, reason, ok := r.Route(t.Context(), "first", testCategories())
		if ok || category != "" || reason != MissError {
			t.Fatalf("timed-out Route = (%q, reason=%q, ok=%v)", category, reason, ok)
		}
		select {
		case <-cancelled:
		default:
			t.Fatal("request deadline did not cancel the in-flight transport")
		}
		category, _, reason, ok = r.Route(t.Context(), "second", testCategories())
		if !ok || category != "fast" || reason != "" || calls.Load() != 2 {
			t.Fatalf("post-timeout Route = (%q, reason=%q, ok=%v), calls=%d", category, reason, ok, calls.Load())
		}
	})

	large, _ := newTestRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", int(responseLimit)+1)))
	}, 0)
	if _, _, reason, ok := large.Route(t.Context(), "task", testCategories()); ok || reason != MissError {
		t.Fatalf("oversized response reason=%q ok=%v", reason, ok)
	}
}

func TestJevReasonAndCredentialRedaction(t *testing.T) {
	secret, task, hostile := "credential-value", "private delegated task", "raw hostile response"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if got := req.Header.Get("Authorization"); got != "Bearer "+secret {
			t.Fatalf("authorization = %q", got)
		}
		_, _ = w.Write([]byte(hostile))
	}))
	t.Cleanup(srv.Close)
	r, err := New(Options{APIKey: secret, Model: defaultModel, BaseURL: srv.URL, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	category, _, reason, ok := r.Route(t.Context(), task, testCategories())
	if ok || category != "" || reason != MissInvalidResponse {
		t.Fatalf("unexpected result: %q %q %v", category, reason, ok)
	}
	for _, forbidden := range []string{secret, task, hostile} {
		if strings.Contains(reason, forbidden) {
			t.Fatalf("reason leaked %q: %q", forbidden, reason)
		}
	}
}
