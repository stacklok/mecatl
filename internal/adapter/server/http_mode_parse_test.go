package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestHTTPCreateRejectsUnrecognisedMode pins that an HTTP create or SetMode
// with a mode string the server does not recognise is refused with 400,
// instead of falling back to the server default (here yolo). A missing or
// UNSPECIFIED mode still means "server default", and the proto enum spelling
// is accepted like the bare names.
func TestHTTPCreateRejectsUnrecognisedMode(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	svc := modeAdmissionServiceOn(t, store, mockllm.New(), session.ModeYolo, session.ModeYolo, true)
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()
	post := func(t *testing.T, url, body string) (int, string) {
		t.Helper()
		resp, err := http.Post(url, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", url, err)
		}
		defer resp.Body.Close()
		var out struct {
			SessionID string `json:"session_id"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out.SessionID
	}

	for _, mode := range []string{"read-only", "PERMISSION_MODE_BOGUS"} {
		if code, id := post(t, srv.URL+"/v1/sessions", `{"mode":"`+mode+`"}`); code != http.StatusBadRequest || id != "" {
			t.Fatalf("create %q = %d %q, want 400 and no session", mode, code, id)
		}
	}
	stored, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(stored) != 0 {
		t.Fatalf("refused creates stored %d sessions, want 0", len(stored))
	}

	var planID string
	for _, tc := range []struct {
		body string
		want session.PermissionMode
	}{
		{`{}`, session.ModeYolo},
		{`{"mode":""}`, session.ModeYolo},
		{`{"mode":"PERMISSION_MODE_UNSPECIFIED"}`, session.ModeYolo},
		{`{"mode":"PERMISSION_MODE_PLAN"}`, session.ModePlan},
		{`{"mode":"permission_mode_accept_edits"}`, session.ModeAccept},
		{`{"mode":"PERMISSION_MODE_DEFAULT"}`, session.ModeDefault},
	} {
		code, id := post(t, srv.URL+"/v1/sessions", tc.body)
		if code != http.StatusCreated || id == "" {
			t.Fatalf("create %s = %d %q, want 201", tc.body, code, id)
		}
		sess, err := svc.GetSession(ctx, session.SessionID(id))
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if sess.Mode != tc.want {
			t.Fatalf("create %s started in %q, want %q", tc.body, sess.Mode, tc.want)
		}
		if tc.want == session.ModePlan {
			planID = id
		}
	}

	if code, _ := post(t, srv.URL+"/v1/sessions/"+planID+"/mode", `{"mode":"read-only"}`); code != http.StatusBadRequest {
		t.Fatalf("set unrecognised mode = %d, want 400", code)
	}
	if code, _ := post(t, srv.URL+"/v1/sessions/"+planID+"/mode", `{"mode":"PERMISSION_MODE_YOLO"}`); code != http.StatusOK {
		t.Fatalf("set proto-spelled yolo = %d, want 200", code)
	}
	sess, err := svc.GetSession(ctx, session.SessionID(planID))
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.Mode != session.ModeYolo {
		t.Fatalf("after SetMode the session is in %q, want yolo", sess.Mode)
	}
}

// TestApprovePlanAutoYoloTransportParity pins that HTTP and gRPC give the same
// answer for a plan approval into auto or yolo: InvalidArgument, with the plan
// ask left pending, rather than HTTP reading the target as "keep planning".
func TestApprovePlanAutoYoloTransportParity(t *testing.T) {
	for _, tc := range []struct {
		http  []string
		proto mecatlv1.PermissionMode
	}{
		{[]string{"auto", "PERMISSION_MODE_AUTO"}, mecatlv1.PermissionMode_PERMISSION_MODE_AUTO},
		{[]string{"yolo", "permission_mode_yolo"}, mecatlv1.PermissionMode_PERMISSION_MODE_YOLO},
	} {
		llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"note":"step 1"}`)))
		svc := planApprovalService(t, llm, allowRules())
		sess, err := svc.CreateSession(context.Background(), session.ModePlan, session.Limits{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		_, finishParked := parkPlanAsk(t, svc, sess.ID)
		srv := httptest.NewServer(server.NewHTTPHandler(svc))
		for _, target := range tc.http {
			if code, body := approvePlanHTTPStatus(t, srv, string(sess.ID), target); code != http.StatusBadRequest {
				t.Fatalf("HTTP plan:approve %q = %d %s, want 400", target, code, body)
			}
		}
		srv.Close()

		client, cleanup := dialGRPC(t, svc)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		stream, err := client.ApprovePlan(ctx, &mecatlv1.ApprovePlanRequest{SessionId: string(sess.ID), TargetMode: tc.proto})
		if err != nil {
			t.Fatalf("gRPC ApprovePlan(%s): %v", tc.proto, err)
		}
		if _, err := stream.Recv(); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("gRPC ApprovePlan(%s) = %v, want InvalidArgument", tc.proto, err)
		}
		cancel()
		cleanup()

		got, err := svc.GetSession(context.Background(), sess.ID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if got.State != session.StateAwaiting {
			t.Fatalf("refused approvals consumed the plan ask: state %q", got.State)
		}
		finishParked()
	}
}
