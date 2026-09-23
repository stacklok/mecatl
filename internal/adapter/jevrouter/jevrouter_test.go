package jevrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
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

func routeTuple(ctx context.Context, router *Router, task string, categories []Category) (string, session.Usage, MissKind, bool) {
	result := router.Route(ctx, task, categories)
	category := result.Category
	if !result.OK {
		category = ""
	}
	return category, result.Usage, result.Miss, result.OK
}

func TestADR_0350_Scenario2_InvalidResponseFallsBack(t *testing.T) {
	tests := []struct {
		name, response string
		wantReason     MissKind
	}{
		{"unknown choice", validResponse("other", 0.9, 1, 2), MissUnknownCategory},
		{"malformed response", `{`, MissBadVerdict},
		{"non-finite probability", `{"model":"jev-1.13.0","answers":{"delegated-model-category":{"type":"choice","choice":"fast","probabilities":{"fast":1e999,"deep":0},"confidence":1}},"usage":{"input_tokens":1,"output_tokens":2}}`, MissBadVerdict},
		{"confidence below zero", validResponse("fast", -0.1, 1, 2), MissBadVerdict},
		{"confidence above one", validResponse("fast", 1.1, 1, 2), MissBadVerdict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newTestRouter(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(tc.response)) }, 0)
			category, _, reason, ok := routeTuple(t.Context(), r, "private task", testCategories())
			if ok || category != "" || reason != tc.wantReason {
				t.Fatalf("Route = (%q, reason=%q, ok=%v), want empty %q false", category, reason, ok, tc.wantReason)
			}
		})
	}

	r, calls := newTestRouter(t, func(http.ResponseWriter, *http.Request) { t.Fatal("over-limit request performed I/O") }, 0)
	category, _, reason, ok := routeTuple(t.Context(), r, strings.Repeat("x", DefaultMaximumInputBytes+1), testCategories())
	if ok || category != "" || reason != MissInputOverLimit || calls.Load() != 0 {
		t.Fatalf("over-limit Route = (%q, reason=%q, ok=%v), calls=%d", category, reason, ok, calls.Load())
	}
}

func TestADR_0350_Scenario2_LowConfidenceAbstains(t *testing.T) {
	r, _ := newTestRouter(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(validResponse("fast", 0.49, 3, 4)))
	}, 0.5)
	category, usage, reason, ok := routeTuple(t.Context(), r, "task", testCategories())
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
	category, usage, reason, ok := routeTuple(t.Context(), r, "task", testCategories())
	if !ok || category != "deep" || reason != missNone {
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
			category, usage, reason, ok := routeTuple(t.Context(), r, "task", testCategories())
			if ok || category != "" || reason != MissBadVerdict {
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
	if r.maximumInputBytes != DefaultMaximumInputBytes {
		t.Fatalf("omitted maximum input = %d, want %d", r.maximumInputBytes, DefaultMaximumInputBytes)
	}
	if _, err := New(Options{APIKey: "test", BaseURL: "https://example.com", MaximumInputBytes: HardMaximumInputBytes}); err != nil {
		t.Fatalf("hard maximum input rejected: %v", err)
	}
	if _, err := New(Options{APIKey: "test", BaseURL: "https://example.com", MaximumInputBytes: HardMaximumInputBytes + 1}); err == nil {
		t.Fatal("input limit above immutable ceiling accepted")
	}
	if _, _, _, ok := routeTuple(t.Context(), r, "task", testCategories()); !ok {
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
	if question["type"] != "choice" || question["instructions"] != classifierInstructions("") {
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
	_, _, reason, ok := routeTuple(t.Context(), r, "task", tooMany)
	if ok || reason != MissInputOverLimit || calls.Load() != 1 {
		t.Fatalf("category cap reason=%q ok=%v calls=%d", reason, ok, calls.Load())
	}

	fixed := len(classifierInstructions("")) + len(DefaultModel) + len(questionID)
	boundaryTask := strings.Repeat("x", DefaultMaximumInputBytes-fixed-len("fast")-len("small task")-len("deep")-len("complex task"))
	if _, _, reason, ok := routeTuple(t.Context(), r, boundaryTask, testCategories()); !ok {
		t.Fatalf("exact limit rejected: %q", reason)
	}
	hint := `med"ium`
	hintInstructions := classifierInstructions(hint)
	hintLimit := len("task") + len(DefaultModel) + len(questionID) + len(hintInstructions) + len("fast") + len("small task")
	var hintCalls atomic.Int32
	hintClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		hintCalls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"model":"jev-1.13.0","answers":{"delegated-model-category":{"type":"choice","choice":"fast","probabilities":{"fast":1},"confidence":1}},"usage":{"input_tokens":1,"output_tokens":1}}`)), Request: req}, nil
	})}
	for _, tc := range []struct {
		name  string
		limit int
		ok    bool
	}{
		{"exact rendered hint boundary", hintLimit, true},
		{"one byte below rendered hint boundary", hintLimit - 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hintRouter, err := New(Options{APIKey: "test", Model: DefaultModel, BaseURL: "https://example.com", HTTPClient: hintClient, DefaultCategory: hint, MaximumInputBytes: tc.limit})
			if err != nil {
				t.Fatal(err)
			}
			before := hintCalls.Load()
			result := hintRouter.Route(t.Context(), "task", []Category{{Name: "fast", Description: "small task"}})
			if result.OK != tc.ok {
				t.Fatalf("Route OK = %v, want %v: %+v", result.OK, tc.ok, result)
			}
			if tc.ok && hintCalls.Load() != before+1 {
				t.Fatal("exact-boundary request did not reach transport")
			}
			if !tc.ok && (result.Miss != MissInputOverLimit || hintCalls.Load() != before) {
				t.Fatalf("over-limit hint request = %+v, calls=%d want %d", result, hintCalls.Load(), before)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type cancelOnEOFBody struct {
	reader *strings.Reader
	cancel context.CancelFunc
}

func (b *cancelOnEOFBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	if errors.Is(err, io.EOF) && b.cancel != nil {
		b.cancel()
		b.cancel = nil
	}
	return n, err
}

func (*cancelOnEOFBody) Close() error { return nil }

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
	_, _, _, _ = routeTuple(t.Context(), retryRouter, "task", testCategories())
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
	_, _, _, _ = routeTuple(t.Context(), redirectRouter, "task", testCategories())
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
		category, _, reason, ok := routeTuple(t.Context(), r, "queued", testCategories())
		if ok || category != "" || reason != MissCapacityTimeout || calls.Load() != 0 {
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
		category, _, reason, ok := routeTuple(ctx, r, "cancelled", testCategories())
		if ok || category != "" || reason != MissCancelled || calls.Load() != 0 {
			t.Fatalf("cancelled Route = (%q, reason=%q, ok=%v), calls=%d", category, reason, ok, calls.Load())
		}
		<-r.semaphore
		category, _, reason, ok = routeTuple(t.Context(), r, "after cancellation", testCategories())
		if !ok || category != "fast" || reason != missNone || calls.Load() != 1 {
			t.Fatalf("post-cancellation Route = (%q, reason=%q, ok=%v), calls=%d", category, reason, ok, calls.Load())
		}
	})

	t.Run("request timeout cancels in flight and releases capacity", func(t *testing.T) {
		var calls atomic.Int32
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				<-req.Context().Done()
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
		category, _, reason, ok := routeTuple(t.Context(), r, "first", testCategories())
		if ok || category != "" || reason != MissTimeout {
			t.Fatalf("timed-out Route = (%q, reason=%v, ok=%v)", category, reason, ok)
		}
		category, _, reason, ok = routeTuple(t.Context(), r, "second", testCategories())
		if !ok || category != "fast" || reason != missNone || calls.Load() != 2 {
			t.Fatalf("post-timeout Route = (%q, reason=%q, ok=%v), calls=%d", category, reason, ok, calls.Load())
		}
	})

	t.Run("caller cancellation cancels in flight and releases capacity", func(t *testing.T) {
		var calls atomic.Int32
		entered := make(chan struct{})
		cancelled := make(chan struct{})
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if calls.Add(1) == 1 {
				close(entered)
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
			transportBounds{maxConcurrent: 1, queueTimeout: time.Second, requestTimeout: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		result := make(chan struct {
			category string
			reason   MissKind
			ok       bool
		}, 1)
		go func() {
			category, _, reason, ok := routeTuple(ctx, r, "first", testCategories())
			result <- struct {
				category string
				reason   MissKind
				ok       bool
			}{category, reason, ok}
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("request did not enter the transport")
		}
		cancel()
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("caller cancellation did not cancel the in-flight transport")
		}
		select {
		case got := <-result:
			if got.ok || got.category != "" || got.reason != MissCancelled {
				t.Fatalf("cancelled Route = (%q, reason=%q, ok=%v)", got.category, got.reason, got.ok)
			}
		case <-time.After(time.Second):
			t.Fatal("cancelled Route did not return promptly")
		}
		category, _, reason, ok := routeTuple(t.Context(), r, "second", testCategories())
		if !ok || category != "fast" || reason != missNone || calls.Load() != 2 {
			t.Fatalf("post-cancellation Route = (%q, reason=%q, ok=%v), calls=%d", category, reason, ok, calls.Load())
		}
	})

	t.Run("wrapped typed deadline is timeout without parsing text", func(t *testing.T) {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("opaque failure: %w", context.DeadlineExceeded)
		})}
		r, err := newRouter(Options{APIKey: "test", BaseURL: "https://example.com", HTTPClient: client},
			transportBounds{maxConcurrent: 1, queueTimeout: time.Second, requestTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, reason, ok := routeTuple(t.Context(), r, "task", testCategories()); ok || reason != MissTimeout {
			t.Fatalf("wrapped deadline reason=%v ok=%v", reason, ok)
		}
	})

	t.Run("completed response wins over late cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       &cancelOnEOFBody{reader: strings.NewReader(validResponse("fast", 1, 6, 2)), cancel: cancel},
				Request:    req,
			}, nil
		})}
		r, err := newRouter(Options{APIKey: "test", BaseURL: "https://example.com", HTTPClient: client},
			transportBounds{maxConcurrent: 1, queueTimeout: time.Second, requestTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		category, usage, reason, ok := routeTuple(ctx, r, "task", testCategories())
		if !ok || category != "fast" || reason != missNone || usage.InputTokens != 6 || usage.OutputTokens != 2 {
			t.Fatalf("late-cancel response = category=%q usage=%+v reason=%v ok=%v", category, usage, reason, ok)
		}
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("test did not establish late caller cancellation")
		}
	})

	t.Run("protocol usage survives caller-state override", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		body := `{"model":"jev-1.13.0","answers":{},"usage":{"input_tokens":5,"output_tokens":2}}`
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       &cancelOnEOFBody{reader: strings.NewReader(body), cancel: cancel},
				Request:    req,
			}, nil
		})}
		r, err := newRouter(Options{APIKey: "test", BaseURL: "https://example.com", HTTPClient: client},
			transportBounds{maxConcurrent: 1, queueTimeout: time.Second, requestTimeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		_, usage, reason, ok := routeTuple(ctx, r, "task", testCategories())
		if ok || reason != MissCancelled || usage.InputTokens != 5 || usage.OutputTokens != 2 {
			t.Fatalf("protocol override = usage=%+v reason=%v ok=%v", usage, reason, ok)
		}
	})

	validPaddedResponse := func(padding int) string {
		return `{"ignored":"` + strings.Repeat("x", padding) + `",` + strings.TrimPrefix(validResponse("fast", 1, 1, 1), "{")
	}
	for _, tc := range []struct {
		name    string
		padding int
		ok      bool
	}{
		{"valid response below explicit limit", int(responseLimit) - 4096, true},
		{"valid response above explicit limit", int(responseLimit) + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, _ := newTestRouter(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(validPaddedResponse(tc.padding)))
			}, 0)
			_, _, reason, ok := routeTuple(t.Context(), router, "task", testCategories())
			if ok != tc.ok {
				t.Fatalf("padded valid response ok=%v reason=%q, want ok=%v", ok, reason, tc.ok)
			}
			if !tc.ok && reason != MissClassifierError {
				t.Fatalf("oversized valid response reason=%q, want classifier error", reason)
			}
		})
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
	r, err := New(Options{APIKey: secret, Model: DefaultModel, BaseURL: srv.URL, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	category, _, reason, ok := routeTuple(t.Context(), r, task, testCategories())
	if ok || category != "" || reason != MissBadVerdict {
		t.Fatalf("unexpected result: %q %q %v", category, reason, ok)
	}
	for _, forbidden := range []string{secret, task, hostile} {
		if strings.Contains(fmt.Sprint(reason), forbidden) {
			t.Fatalf("reason leaked %q: %q", forbidden, reason)
		}
	}
}
