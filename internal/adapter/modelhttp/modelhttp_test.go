package modelhttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type trackingBody struct {
	io.Reader
	closed bool
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}

func TestGetOwnsOnlyBoundedHTTPMechanics(t *testing.T) {
	t.Run("GET prepare and close", func(t *testing.T) {
		body := &trackingBody{Reader: strings.NewReader("ok")}
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet || req.URL.String() != "https://models.invalid/v1/models" {
				t.Fatalf("request = %s %s", req.Method, req.URL)
			}
			if req.Header.Get("X-Adapter-Owned") != "yes" {
				t.Fatal("adapter prepare callback was not applied")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: body, Request: req}, nil
		})}
		got, err := Get(context.Background(), client, "https://models.invalid/v1/models", "test", 2, func(req *http.Request) {
			req.Header.Set("X-Adapter-Owned", "yes")
		})
		if err != nil || string(got) != "ok" {
			t.Fatalf("Get = %q, %v", got, err)
		}
		if !body.closed {
			t.Fatal("response body was not closed")
		}
	})

	t.Run("status is typed and body-free", func(t *testing.T) {
		body := &trackingBody{Reader: strings.NewReader("secret provider body")}
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusForbidden, Body: body, Request: req}, nil
		})}
		_, err := Get(context.Background(), client, "https://models.invalid/v1/models", "test", 8, nil)
		var statusErr *StatusError
		if !errors.As(err, &statusErr) || statusErr.StatusCode() != http.StatusForbidden {
			t.Fatalf("error = %v, want StatusError{403}", err)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("status error disclosed the response body")
		}
		if !body.closed {
			t.Fatal("non-2xx response body was not closed")
		}
	})

	t.Run("body cap", func(t *testing.T) {
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("abc")), Request: req}, nil
		})}
		if _, err := Get(context.Background(), client, "https://models.invalid/v1/models", "test", 2, nil); err == nil || !strings.Contains(err.Error(), "cap") {
			t.Fatalf("oversize error = %v", err)
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		})}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Get(ctx, client, "https://models.invalid/v1/models", "test", 8, nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	})
}

func TestDefaultClient(t *testing.T) {
	redirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	got := DefaultClient(nil, redirect)
	if got.Timeout != DefaultTimeout || got.CheckRedirect == nil {
		t.Fatalf("default client = %#v", got)
	}
	injected := &http.Client{}
	if DefaultClient(injected, redirect) != injected {
		t.Fatal("injected client was replaced")
	}
}
