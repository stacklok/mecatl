package server_test

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// modeAdmissionService builds a Service with the given permission-mode
// ceiling, default mode, and enforcing-checker state.
func modeAdmissionService(t *testing.T, ceiling, defaultMode session.PermissionMode, checker bool) *server.Service {
	t.Helper()
	llm := mockllm.New()
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     llm,
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(allowRules(), nil),
			Model:   "test-model",
		}),
		Store:                      memstore.New(),
		PlacementProvider:          testPlacementProvider{},
		PlacementScope:             "test",
		Now:                        func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities:        llm.Capabilities(),
		DefaultMode:                defaultMode,
		MaxAllowedMode:             ceiling,
		EnforcingCheckerConfigured: checker,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// TestGRPCAutoYoloModesRoundTrip pins that AUTO and YOLO survive the gRPC
// boundary on CreateSession, SetMode, and the session read-back instead of
// collapsing to DEFAULT.
func TestGRPCAutoYoloModesRoundTrip(t *testing.T) {
	svc := modeAdmissionService(t, session.ModeYolo, session.ModeDefault, true)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, tc := range []struct {
		proto mecatlv1.PermissionMode
		want  session.PermissionMode
	}{
		{mecatlv1.PermissionMode_PERMISSION_MODE_AUTO, session.ModeAuto},
		{mecatlv1.PermissionMode_PERMISSION_MODE_YOLO, session.ModeYolo},
	} {
		cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Mode: tc.proto})
		if err != nil {
			t.Fatalf("CreateSession(%s): %v", tc.proto, err)
		}
		sess, err := svc.GetSession(ctx, session.SessionID(cs.GetSessionId()))
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if sess.Mode != tc.want {
			t.Errorf("CreateSession(%s) started in %q, want %q", tc.proto, sess.Mode, tc.want)
		}
		got, err := client.GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: cs.GetSessionId()})
		if err != nil {
			t.Fatalf("GetSession RPC: %v", err)
		}
		if got.GetSession().GetMode() != tc.proto {
			t.Errorf("GetSession mode = %s, want %s", got.GetSession().GetMode(), tc.proto)
		}

		plain, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Mode: mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT})
		if err != nil {
			t.Fatalf("CreateSession(DEFAULT): %v", err)
		}
		set, err := client.SetMode(ctx, &mecatlv1.SetModeRequest{SessionId: plain.GetSessionId(), Mode: tc.proto})
		if err != nil {
			t.Fatalf("SetMode(%s): %v", tc.proto, err)
		}
		if set.GetSession().GetMode() != tc.proto {
			t.Errorf("SetMode(%s) reply mode = %s", tc.proto, set.GetSession().GetMode())
		}
	}
}

// TestGRPCModeAdmissionRefusals pins that CreateSession and SetMode share the
// ceiling and enforcing-checker admission, refusing with InvalidArgument.
func TestGRPCModeAdmissionRefusals(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, tc := range []struct {
		name    string
		ceiling session.PermissionMode
		checker bool
		mode    mecatlv1.PermissionMode
		reason  string
	}{
		{"yolo above strict ceiling", session.ModeDefault, true, mecatlv1.PermissionMode_PERMISSION_MODE_YOLO, "exceeds the server ceiling"},
		{"auto above strict ceiling", session.ModeAccept, true, mecatlv1.PermissionMode_PERMISSION_MODE_AUTO, "exceeds the server ceiling"},
		{"auto without checker", session.ModeYolo, false, mecatlv1.PermissionMode_PERMISSION_MODE_AUTO, "enforcing guardrails checker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := modeAdmissionService(t, tc.ceiling, session.ModeDefault, tc.checker)
			client, cleanup := dialGRPC(t, svc)
			defer cleanup()

			_, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Mode: tc.mode})
			if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("CreateSession err = %v, want InvalidArgument naming %q", err, tc.reason)
			}

			cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{Mode: mecatlv1.PermissionMode_PERMISSION_MODE_DEFAULT})
			if err != nil {
				t.Fatalf("CreateSession(DEFAULT): %v", err)
			}
			_, err = client.SetMode(ctx, &mecatlv1.SetModeRequest{SessionId: cs.GetSessionId(), Mode: tc.mode})
			if status.Code(err) != codes.InvalidArgument || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("SetMode err = %v, want InvalidArgument naming %q", err, tc.reason)
			}
			sess, err := svc.GetSession(ctx, session.SessionID(cs.GetSessionId()))
			if err != nil {
				t.Fatalf("GetSession: %v", err)
			}
			if sess.Mode != session.ModeDefault {
				t.Fatalf("refused SetMode changed mode to %q", sess.Mode)
			}
		})
	}
}

// TestOperatorDefaultAutoAdmittedWithoutChecker pins that the operator's
// configured default mode stays admissible without an enforcing checker (the
// operator chose it; startup WARNs), both for an unspecified create and for
// an explicit request of that same mode.
func TestOperatorDefaultAutoAdmittedWithoutChecker(t *testing.T) {
	svc := modeAdmissionService(t, session.ModeAuto, session.ModeAuto, false)
	ctx := context.Background()
	for _, mode := range []session.PermissionMode{"", session.ModeAuto} {
		sess, err := svc.CreateSession(ctx, mode, session.Limits{})
		if err != nil {
			t.Fatalf("CreateSession(%q): %v", mode, err)
		}
		if sess.Mode != session.ModeAuto {
			t.Fatalf("CreateSession(%q) mode = %q, want auto", mode, sess.Mode)
		}
	}
}

// TestHTTPAutoYoloModes pins the HTTP mode mapping and the shared admission.
func TestHTTPAutoYoloModes(t *testing.T) {
	decode := func(resp *http.Response) (int, string) {
		defer resp.Body.Close()
		var out struct {
			SessionID string `json:"session_id"`
			Mode      string `json:"mode"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if out.SessionID == "" {
			return resp.StatusCode, ""
		}
		return resp.StatusCode, out.SessionID + " " + out.Mode
	}
	post := func(t *testing.T, url, body string) (int, string) {
		t.Helper()
		resp, err := http.Post(url, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %s: %v", url, err)
		}
		return decode(resp)
	}

	srv := httptest.NewServer(server.NewHTTPHandler(modeAdmissionService(t, session.ModeYolo, session.ModeDefault, true)))
	defer srv.Close()
	for _, mode := range []string{"auto", "yolo"} {
		code, got := post(t, srv.URL+"/v1/sessions", `{"mode":"`+mode+`"}`)
		if code != http.StatusCreated || got == "" {
			t.Fatalf("create %s = %d %q, want 201", mode, code, got)
		}
		id := strings.Fields(got)[0]
		resp, err := http.Get(srv.URL + "/v1/sessions/" + id)
		if err != nil {
			t.Fatalf("GET session: %v", err)
		}
		if code, got = decode(resp); code != http.StatusOK || !strings.HasSuffix(got, " "+mode) {
			t.Fatalf("created %s session reads back %d %q", mode, code, got)
		}
		code, got = post(t, srv.URL+"/v1/sessions/"+id+"/mode", `{"mode":"default"}`)
		if code != http.StatusOK || !strings.HasSuffix(got, " default") {
			t.Fatalf("set default = %d %q", code, got)
		}
		code, got = post(t, srv.URL+"/v1/sessions/"+id+"/mode", `{"mode":"`+mode+`"}`)
		if code != http.StatusOK || !strings.HasSuffix(got, " "+mode) {
			t.Fatalf("set %s = %d %q", mode, code, got)
		}
	}

	strict := httptest.NewServer(server.NewHTTPHandler(modeAdmissionService(t, session.ModeDefault, session.ModeDefault, true)))
	defer strict.Close()
	if code, _ := post(t, strict.URL+"/v1/sessions", `{"mode":"yolo"}`); code != http.StatusBadRequest {
		t.Fatalf("create yolo above ceiling = %d, want 400", code)
	}
}

// TestApprovePlanRefusesAutoYoloTarget pins that plan approval cannot flip a
// session into auto or yolo: it refuses instead of silently denying the plan.
func TestApprovePlanRefusesAutoYoloTarget(t *testing.T) {
	for _, target := range []session.PermissionMode{session.ModeAuto, session.ModeYolo} {
		llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "PresentPlan", `{"note":"step 1"}`)))
		svc := planApprovalService(t, llm, allowRules())
		sess, err := svc.CreateSession(context.Background(), session.ModePlan, session.Limits{})
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		_, finishParked := parkPlanAsk(t, svc, sess.ID)
		_, err = svc.ApprovePlan(context.Background(), sess.ID, target, "")
		if !errors.Is(err, server.ErrInvalidArgument) {
			t.Fatalf("ApprovePlan(%s) err = %v, want ErrInvalidArgument", target, err)
		}
		got, err := svc.GetSession(context.Background(), sess.ID)
		if err != nil {
			t.Fatalf("GetSession: %v", err)
		}
		if got.State != session.StateAwaiting {
			t.Fatalf("refused ApprovePlan(%s) consumed the plan ask: state %q", target, got.State)
		}
		finishParked()
	}
}
