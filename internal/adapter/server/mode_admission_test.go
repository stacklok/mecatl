package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// modeAdmissionService builds a Service with the given permission-mode
// ceiling, default mode, and enforcing-checker state.
func modeAdmissionService(t *testing.T, ceiling, defaultMode session.PermissionMode, checker bool) *server.Service {
	t.Helper()
	return modeAdmissionServiceOn(t, memstore.New(), mockllm.New(), ceiling, defaultMode, checker)
}

// modeAdmissionServiceOn is modeAdmissionService over an explicit store and
// provider, so a test can model a deployment restart over persisted sessions.
func modeAdmissionServiceOn(t *testing.T, store port.SessionStore, llm *mockllm.Provider, ceiling, defaultMode session.PermissionMode, checker bool) *server.Service {
	t.Helper()
	svc, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     llm,
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(allowRules(), nil),
			Model:   "test-model",
		}),
		Store:                      store,
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

// TestAutoDefaultWithoutCheckerRefusedAtConstruction pins that auto has no
// default-mode exemption: a service whose default is auto refuses to start
// without an enforcing checker, and starts with one.
func TestAutoDefaultWithoutCheckerRefusedAtConstruction(t *testing.T) {
	_, err := newPlacementTestService(server.Config{
		Engine:            agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model"}),
		Store:             memstore.New(),
		PlacementProvider: testPlacementProvider{},
		PlacementScope:    "test",
		DefaultMode:       session.ModeAuto,
		MaxAllowedMode:    session.ModeAuto,
	})
	if !errors.Is(err, server.ErrConfig) || !strings.Contains(err.Error(), "enforcing guardrails checker") {
		t.Fatalf("auto default without checker: err = %v, want ErrConfig naming the checker", err)
	}
	svc := modeAdmissionService(t, session.ModeAuto, session.ModeAuto, true)
	sess, err := svc.CreateSession(context.Background(), "", session.Limits{})
	if err != nil || sess.Mode != session.ModeAuto {
		t.Fatalf("auto default with checker: mode %v, err %v", sess, err)
	}
}

// TestInadmissibleDefaultModeRefusedAtConstruction pins that construction
// runs the complete session admission on the default mode and validates the
// ceiling, so a service that could never create its default session fails to
// start instead of failing every default CreateSession.
func TestInadmissibleDefaultModeRefusedAtConstruction(t *testing.T) {
	for _, tc := range []struct {
		name             string
		def, ceiling     session.PermissionMode
		checker          bool
		wantErrSubstring string
	}{
		{"unknown default", "bogus", session.ModeYolo, true, "unknown permission mode"},
		{"default above ceiling", session.ModeYolo, session.ModeAccept, true, "exceeds the server ceiling"},
		{"auto default above ceiling", session.ModeAuto, session.ModeDefault, true, "exceeds the server ceiling"},
		{"unknown ceiling", session.ModeDefault, "bogus", true, "unknown permission-mode ceiling"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newPlacementTestService(server.Config{
				Engine:                     agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(nil, nil), Model: "test-model"}),
				Store:                      memstore.New(),
				PlacementProvider:          testPlacementProvider{},
				PlacementScope:             "test",
				DefaultMode:                tc.def,
				MaxAllowedMode:             tc.ceiling,
				EnforcingCheckerConfigured: tc.checker,
			})
			if !errors.Is(err, server.ErrConfig) || !strings.Contains(err.Error(), tc.wantErrSubstring) {
				t.Fatalf("err = %v, want ErrConfig containing %q", err, tc.wantErrSubstring)
			}
		})
	}
	// Positive control: a default at the ceiling, and any default with no ceiling.
	modeAdmissionService(t, session.ModeAccept, session.ModeAccept, false)
	modeAdmissionService(t, "", session.ModeYolo, false)
}

// TestAutoRequiresCheckerEvenWhenNotDefault pins that, without an enforcing
// checker, explicit auto creation and switching (back) to auto are refused on
// every ceiling that would otherwise admit auto, while yolo stays admissible.
func TestAutoRequiresCheckerEvenWhenNotDefault(t *testing.T) {
	ctx := context.Background()
	for _, def := range []session.PermissionMode{session.ModeDefault, session.ModeYolo} {
		svc := modeAdmissionService(t, session.ModeYolo, def, false)
		if _, err := svc.CreateSession(ctx, session.ModeAuto, session.Limits{}); !errors.Is(err, server.ErrInvalidArgument) {
			t.Fatalf("default %s: CreateSession(auto) err = %v, want ErrInvalidArgument", def, err)
		}
		sess, err := svc.CreateSession(ctx, "", session.Limits{})
		if err != nil {
			t.Fatalf("default %s: CreateSession: %v", def, err)
		}
		if _, err := svc.SetMode(ctx, sess.ID, session.ModeAuto); !errors.Is(err, server.ErrInvalidArgument) {
			t.Fatalf("default %s: SetMode(auto) err = %v, want ErrInvalidArgument", def, err)
		}
		if _, err := svc.SetMode(ctx, sess.ID, session.ModeYolo); err != nil {
			t.Fatalf("default %s: SetMode(yolo) positive control: %v", def, err)
		}
	}
}

// TestStoredModeReadmittedAfterTighterRestart persists a permissive session,
// reopens the store under a tighter deployment, and proves prompt, fork, and
// clear refuse BEFORE any provider work, without touching the session; a
// SetMode to an admitted mode then lets the same session continue.
func TestStoredModeReadmittedAfterTighterRestart(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		stored  session.PermissionMode
		ceiling session.PermissionMode
		checker bool
	}{
		{"yolo above default ceiling", session.ModeYolo, session.ModeAccept, true},
		{"auto above default ceiling", session.ModeAuto, session.ModeAccept, true},
		{"auto after checker removed", session.ModeAuto, session.ModeYolo, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := memstore.New()
			loose := modeAdmissionServiceOn(t, store, mockllm.New(), session.ModeYolo, session.ModeDefault, true)
			sess, err := loose.CreateSession(ctx, tc.stored, session.Limits{})
			if err != nil {
				t.Fatalf("CreateSession(%s) on loose deployment: %v", tc.stored, err)
			}

			llm := mockllm.New(mockllm.TextTurn("ran"))
			tight := modeAdmissionServiceOn(t, store, llm, tc.ceiling, session.ModeDefault, tc.checker)
			if run, err := tight.StartRun(ctx, sess.ID, "go"); !errors.Is(err, server.ErrFailedPrecondition) || run != nil {
				t.Fatalf("StartRun = run %v, err %v; want ErrFailedPrecondition", run, err)
			}
			if _, err := tight.ForkSessionSuccessor(ctx, server.ForkSuccessorRequest{Source: sess.ID}); !errors.Is(err, server.ErrFailedPrecondition) {
				t.Fatalf("ForkSessionSuccessor err = %v, want ErrFailedPrecondition", err)
			}
			if _, err := tight.ClearSessionSuccessor(ctx, sess.ID, server.SuccessorPlacement{}); !errors.Is(err, server.ErrFailedPrecondition) {
				t.Fatalf("ClearSessionSuccessor err = %v, want ErrFailedPrecondition", err)
			}
			if llm.Calls() != 0 {
				t.Fatalf("provider called %d time(s) before refusal, want 0", llm.Calls())
			}
			kept, err := store.Load(ctx, sess.ID)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if kept.Mode != tc.stored || kept.State != sess.State {
				t.Fatalf("refusal mutated the session: mode %q state %q", kept.Mode, kept.State)
			}

			if _, err := tight.SetMode(ctx, sess.ID, session.ModeDefault); err != nil {
				t.Fatalf("SetMode(default): %v", err)
			}
			run, err := tight.StartRun(ctx, sess.ID, "go")
			if err != nil {
				t.Fatalf("StartRun after SetMode(default): %v", err)
			}
			for range run.Events() {
			}
			tight.FinishRun(sess.ID, run)
			if llm.Calls() != 1 {
				t.Fatalf("provider calls after re-admission = %d, want 1", llm.Calls())
			}
		})
	}
}

// gatedLease parks the named owner's first Acquire until release closes, so
// a test can let a peer mutate the session while that owner waits for the
// lease after its pre-lease admission.
type gatedLease struct {
	port.SessionLease
	owner   string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (l *gatedLease) Acquire(ctx context.Context, id session.SessionID, owner string) (port.Lease, error) {
	if owner == l.owner {
		first := false
		l.once.Do(func() { first = true })
		if first {
			close(l.entered)
			<-l.release
		}
	}
	return l.SessionLease.Acquire(ctx, id, owner)
}

// leasedModeService is modeAdmissionServiceOn sharing a lease backend, so two
// services model replicas on different configurations during a rollout.
func leasedModeService(t *testing.T, store port.SessionStore, lease port.SessionLease, owner string, llm *mockllm.Provider, ceiling session.PermissionMode) *server.Service {
	t.Helper()
	svc, err := newPlacementTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM:     llm,
			Catalog: tool.NewCatalog(),
			Policy:  permpolicy.NewPolicy(allowRules(), nil),
			Model:   "test-model",
		}),
		Store:                      store,
		SessionLease:               lease,
		LeaseOwner:                 owner,
		LeaseTTL:                   time.Minute,
		LeaseRenewInterval:         time.Hour,
		DefaultCapabilities:        llm.Capabilities(),
		MaxAllowedMode:             ceiling,
		EnforcingCheckerConfigured: true,
	})
	if err != nil {
		t.Fatalf("new service %s: %v", owner, err)
	}
	t.Cleanup(svc.Close)
	return svc
}

// TestStoredModeReadmittedUnderAcquiredLease races two replicas sharing one
// store and lease backend: the stricter replica admits the pre-lease Default
// snapshot, a looser peer takes the lease and saves Yolo while it waits, and
// the stricter replica must refuse the authoritative Yolo snapshot once it
// holds the lease, before any provider work and without mutating it.
func TestStoredModeReadmittedUnderAcquiredLease(t *testing.T) {
	ctx := context.Background()
	store := memstore.New()
	lease := &gatedLease{
		SessionLease: memlease.New(wallclock.Clock{}, time.Minute),
		owner:        "strict", entered: make(chan struct{}), release: make(chan struct{}),
	}
	looseLLM := mockllm.New()
	loose := leasedModeService(t, store, lease, "loose", looseLLM, session.ModeYolo)
	strictLLM := mockllm.New(mockllm.TextTurn("ran"))
	strict := leasedModeService(t, store, lease, "strict", strictLLM, session.ModeAccept)

	sess, err := loose.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	result := make(chan error, 1)
	go func() {
		run, err := strict.StartRun(ctx, sess.ID, "go")
		if run != nil {
			for range run.Events() {
			}
			strict.FinishRun(sess.ID, run)
		}
		result <- err
	}()
	select {
	case <-lease.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("strict replica never reached lease acquisition")
	}
	if _, err := loose.SetMode(ctx, sess.ID, session.ModeYolo); err != nil {
		t.Fatalf("peer SetMode(yolo): %v", err)
	}
	close(lease.release)
	select {
	case err := <-result:
		if !errors.Is(err, server.ErrFailedPrecondition) {
			t.Fatalf("StartRun err = %v, want ErrFailedPrecondition", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("strict StartRun did not return")
	}
	if strictLLM.Calls() != 0 || looseLLM.Calls() != 0 {
		t.Fatalf("provider calls strict=%d loose=%d before refusal, want 0", strictLLM.Calls(), looseLLM.Calls())
	}
	kept, err := store.Load(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if kept.Mode != session.ModeYolo || kept.State != session.StateIdle {
		t.Fatalf("refusal mutated the session: mode %q state %q", kept.Mode, kept.State)
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
