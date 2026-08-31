package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// problemBody is the decoded RFC 9457 document, declared in the external test
// package so the assertions read against the WIRE shape a client sees rather
// than the internal struct.
type problemBody struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail"`
	Instance string `json:"instance"`
	Code     string `json:"code"`
	Error    string `json:"error"`
}

// getProblem issues a request expected to fail and decodes the problem body.
func getProblem(t *testing.T, url string) (*http.Response, problemBody) {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx // httptest, bounded by the test deadline.
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var p problemBody
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode problem body (%s): %v", raw, err)
	}
	return resp, p
}

// TestSDKServerEnablers_Scenario2_ProblemJSONShape is AC2.1.
//
// An HTTP failure returns application/problem+json carrying type, title, status,
// detail, and the stable mecatl code — plus the retained `error` compatibility
// key so a client written against the pre-RFC-9457 body keeps working.
func TestSDKServerEnablers_Scenario2_ProblemJSONShape(t *testing.T) {
	svc := compatibilityInfoService(t, "", port.ProviderCapabilities{})
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	resp, p := getProblem(t, srv.URL+"/v1/sessions/does-not-exist")

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	if p.Code != "session_not_found" {
		t.Errorf("code = %q, want %q", p.Code, "session_not_found")
	}
	if p.Type != "urn:mecatl:error:session_not_found" {
		t.Errorf("type = %q, want the code as a URN", p.Type)
	}
	if p.Title == "" {
		t.Error("title is empty; RFC 9457 title is the human-readable summary")
	}
	if p.Status != http.StatusNotFound {
		t.Errorf("body status = %d, want it to match the HTTP status 404", p.Status)
	}
	if p.Detail == "" {
		t.Error("detail is empty")
	}
	// The compatibility extension must still carry the message a pre-RFC-9457
	// client read, or the "retained during the transition" promise is empty.
	if p.Error == "" {
		t.Error(`the "error" compatibility key is missing; an existing client reading it would break`)
	}
	if p.Error != p.Detail {
		t.Errorf(`"error" (%q) and "detail" (%q) disagree; old and new clients must see the same message`, p.Error, p.Detail)
	}
	// instance is omitted rather than fabricated: mecatl has no request-id yet.
	if p.Instance != "" {
		t.Errorf("instance = %q, want it omitted until a real request id exists", p.Instance)
	}
}

// TestSDKServerEnablers_Scenario2_ErrorCodeTransportParity is AC2.2.
//
// The SAME domain failure reports the SAME stable code on both transports. gRPC
// status codes are far coarser than the domain, so without the ErrorInfo detail
// a gRPC client could not tell one FailedPrecondition from another — and the
// SDK's single normalized error surface would be false on the gRPC side.
func TestSDKServerEnablers_Scenario2_ErrorCodeTransportParity(t *testing.T) {
	svc := compatibilityInfoService(t, "", port.ProviderCapabilities{})
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := client.GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: "does-not-exist"})
	if err == nil {
		t.Fatal("GetSession on an unknown id succeeded")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error is not a gRPC status: %v", err)
	}
	if st.Code() != codes.NotFound {
		t.Errorf("gRPC code = %v, want %v", st.Code(), codes.NotFound)
	}

	var grpcCode string
	for _, d := range st.Details() {
		if info, isInfo := d.(*errdetails.ErrorInfo); isInfo {
			grpcCode = info.GetReason()
			if info.GetDomain() == "" {
				t.Error("ErrorInfo.Domain is empty; a Reason is only unique within its Domain")
			}
		}
	}
	if grpcCode == "" {
		t.Fatal("no ErrorInfo detail on the gRPC status; a client cannot distinguish domain failures from the coarse code alone")
	}

	_, p := getProblem(t, srv.URL+"/v1/sessions/does-not-exist")
	if grpcCode != p.Code {
		t.Errorf("stable code differs by transport: grpc=%q http=%q", grpcCode, p.Code)
	}
}

// TestSDKServerEnablers_Scenario2_RequestShapeFailuresCarryACode covers the
// writeError path — failures raised inside a handler that have a status but no
// service sentinel. They must still be machine-readable.
func TestSDKServerEnablers_Scenario2_RequestShapeFailuresCarryACode(t *testing.T) {
	svc := compatibilityInfoService(t, "", port.ProviderCapabilities{})
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", strings.NewReader("{not json")) //nolint:noctx // httptest.
	if err != nil {
		t.Fatalf("POST /v1/sessions: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	var p problemBody
	if uerr := json.Unmarshal(raw, &p); uerr != nil {
		t.Fatalf("decode problem body (%s): %v", raw, uerr)
	}

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	if p.Code != "invalid_argument" {
		t.Errorf("code = %q, want %q", p.Code, "invalid_argument")
	}
	if p.Error == "" {
		t.Error(`the "error" compatibility key is missing on the request-shape path too`)
	}
}

// TestSDKServerEnablers_Scenario2_SuccessResponsesAreUnchanged guards the blast
// radius: only ERROR bodies moved to problem+json. A success must still be
// application/json, or every existing client breaks on the happy path.
func TestSDKServerEnablers_Scenario2_SuccessResponsesAreUnchanged(t *testing.T) {
	svc := compatibilityInfoService(t, "", port.ProviderCapabilities{})
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/compatibility") //nolint:noctx // httptest.
	if err != nil {
		t.Fatalf("GET /v1/compatibility: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("success Content-Type = %q, want application/json (only errors moved to problem+json)", ct)
	}
}

var _ = mockllm.New
