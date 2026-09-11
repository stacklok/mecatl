package llmendpoint

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transportRoundTripper func(*http.Request) (*http.Response, error)

func (f transportRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type transportBearerSource struct {
	refreshes int
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

func (*transportBearerSource) Token(context.Context) (string, error) {
	return "old", nil
}

func (s *transportBearerSource) Refresh(context.Context, string) (string, error) {
	s.refreshes++
	return "new", nil
}

func TestGatewayBearerTransportRetriesPersistentUnauthorizedOnce(t *testing.T) {
	source := &transportBearerSource{}
	calls := 0
	first := &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("first"))}
	second := &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("second"))}
	client, err := NewGatewayHTTPClient("https://gateway.example/v1", source, transportRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return first, nil
		}
		return second, nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Get("https://gateway.example/v1/responses")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp != second || calls != 2 || source.refreshes != 1 {
		t.Fatalf("response=%p calls=%d refreshes=%d", resp, calls, source.refreshes)
	}
}

func TestGatewayBearerTransportDoesNotRetryNonReplayableBody(t *testing.T) {
	source := &transportBearerSource{}
	calls := 0
	body := &trackedBody{Reader: strings.NewReader("original")}
	original := &http.Response{StatusCode: http.StatusUnauthorized, Body: body}
	client, err := NewGatewayHTTPClient("https://gateway.example/v1", source, transportRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return original, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "https://gateway.example/v1/responses", io.NopCloser(strings.NewReader("request")))
	if err != nil {
		t.Fatal(err)
	}
	if req.GetBody != nil {
		t.Fatal("request unexpectedly replayable")
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp != original || body.closed || calls != 1 || source.refreshes != 0 {
		t.Fatalf("response=%p closed=%v calls=%d refreshes=%d", resp, body.closed, calls, source.refreshes)
	}
}

func TestGatewayHTTPClientDoesNotFollowRedirects(t *testing.T) {
	source := &transportBearerSource{}
	calls := 0
	redirect := &http.Response{
		StatusCode: http.StatusFound,
		Header:     http.Header{"Location": []string{"https://gateway.example/v1/redirected"}},
		Body:       io.NopCloser(strings.NewReader("redirect")),
	}
	client, err := NewGatewayHTTPClient("https://gateway.example/v1", source, transportRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		return redirect, nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Get("https://gateway.example/v1/redirect")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp != redirect || resp.StatusCode != http.StatusFound || calls != 1 {
		t.Fatalf("response=%p status=%d calls=%d", resp, resp.StatusCode, calls)
	}
}
