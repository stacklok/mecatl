//go:build kind_execution_e2e

package k8s_execution_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/stacklok/mecatl/adapters/redisstore"
	tuiClient "github.com/stacklok/mecatl/cmd/mecatui/client"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/executionclient"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type borrowedScriptCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

type borrowedScriptTurn struct {
	ToolCalls []borrowedScriptCall `json:"tool_calls,omitempty"`
	Text      string               `json:"text,omitempty"`
}

func installBorrowedScript(t *testing.T, ctx context.Context, kubeconfig string, turns ...borrowedScriptTurn) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"turns": turns})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "mock-script.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := command(ctx, kubeconfig, "create", "configmap", "execution-mock", "-n", namespace, "--from-file=mock-script.json="+path, "--dry-run=client", "-o", "yaml")
	rendered, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	apply := command(ctx, kubeconfig, "apply", "-f", "-")
	apply.Stdin = bytes.NewReader(rendered)
	if out, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("install mock script: %v: %s", err, out)
	}
}

func borrowedCall(id, name string, args map[string]any) borrowedScriptTurn {
	return borrowedScriptTurn{ToolCalls: []borrowedScriptCall{{ID: id, Name: name, Args: args}}}
}

func borrowedProofTurn(label, path string) borrowedScriptTurn {
	return borrowedCall(label+"-shell", "Shell", map[string]any{"command": "printf '" + label + "\\n' > " + path})
}

func assertBorrowedEvents(t *testing.T, ctx context.Context, addr, token, id string, expected ...string) {
	t.Helper()
	status, body := request(t, ctx, http.MethodGet, "http://"+addr+"/v1/sessions/"+id+"/events", token, nil)
	if status != http.StatusOK {
		t.Fatalf("fire event replay status=%d", status)
	}
	calls := map[string]string{}
	results := map[string]*mecatlv1.ToolResult{}
	for _, line := range bytes.Split(body, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		var event mecatlv1.Event
		if err := json.Unmarshal(bytes.TrimPrefix(line, []byte("data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.ToolCall != nil {
			calls[event.ToolCall.Id] = event.ToolCall.Name
		}
		if event.ToolResult != nil {
			results[event.ToolResult.CallId] = event.ToolResult
		}
	}
	for _, name := range expected {
		if calls[name+"-shell"] != "Shell" || results[name+"-shell"] == nil || results[name+"-shell"].IsError || !strings.Contains(results[name+"-shell"].Content, "[exit code: 0]") {
			t.Fatalf("fire %s missing successful remote Shell execution: calls=%v status=%s", name, calls, mockSSEStatus(body))
		}
		if calls[name+"-read"] != "Read" || results[name+"-read"] == nil || results[name+"-read"].IsError || !strings.Contains(results[name+"-read"].Content, name) {
			t.Fatalf("fire %s missing successful remote Read proof: calls=%v status=%s", name, calls, mockSSEStatus(body))
		}
	}
}

func assertBorrowedSnapshot(t *testing.T, snapshot *session.Session, label string) {
	t.Helper()
	if snapshot.State != session.StateCompleted {
		t.Fatalf("%s snapshot state=%s, want completed", label, snapshot.State)
	}
	calls := map[session.ToolCallID]string{}
	results := map[session.ToolCallID]*session.ToolResult{}
	for _, message := range snapshot.Conversation.Messages {
		for _, call := range message.ToolCalls {
			calls[call.ID] = call.Name
		}
		if message.ToolResult != nil {
			results[message.ToolResult.CallID] = message.ToolResult
		}
	}
	shellID, readID := session.ToolCallID(label+"-shell"), session.ToolCallID(label+"-read")
	if calls[shellID] != "Shell" || results[shellID] == nil || results[shellID].IsError || !strings.Contains(results[shellID].Content, "[exit code: 0]") {
		result := results[shellID]
		roles := make([]session.Role, 0, len(snapshot.Conversation.Messages))
		summaries := 0
		for _, message := range snapshot.Conversation.Messages {
			roles = append(roles, message.Role)
			if session.IsSynthesisedSummary(message.Text) {
				summaries++
			}
		}
		t.Fatalf("%s snapshot lacks successful remote Shell execution: calls=%v result_present=%t result_error=%t exit_zero=%t roles=%v summaries=%d", label, calls, result != nil, result != nil && result.IsError, result != nil && strings.Contains(result.Content, "[exit code: 0]"), roles, summaries)
	}
	if calls[readID] != "Read" || results[readID] == nil || results[readID].IsError || !strings.Contains(results[readID].Content, label) {
		t.Fatalf("%s snapshot lacks successful remote Read proof", label)
	}
}

func waitBorrowedFire(t *testing.T, ctx context.Context, wire *tuiClient.Client, name string, start time.Time) string {
	t.Helper()
	for ctx.Err() == nil {
		fires, err := wire.ListFires(ctx, name)
		if err != nil {
			t.Fatalf("ListFires(%s): %v", name, err)
		}
		for _, fire := range fires {
			if fire.FiredAt.Before(start) {
				continue
			}
			if fire.Stop != "" {
				if fire.Stop != "end_turn" || fire.SessionID == "" {
					t.Fatalf("schedule %s failed: stop=%s err=%s", name, fire.Stop, fire.Err)
				}
				return fire.SessionID
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatal("scheduled fire never finished:", ctx.Err())
	return ""
}

func TestKindExecutionBorrowedSchedule(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	if os.Getenv("MECATL_KUBE_CONTEXT") == "" {
		t.Fatal("explicit owned Kind context required")
	}
	id := time.Now().UnixNano()
	manual := fmt.Sprintf("borrowed-manual-%d", id)
	timer := fmt.Sprintf("borrowed-timer-%d", id)
	proofPath := func(label string) string { return fmt.Sprintf("borrowed-%s-%d.txt", label, id) }
	due := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)
	installBorrowedScript(t, ctx, kubeconfig,
		borrowedScriptTurn{ToolCalls: []borrowedScriptCall{
			{ID: "create-manual", Name: "Schedule", Args: map[string]any{"verb": "create", "name": manual, "prompt": "run manual remote proof", "cron": "0 0 * * *", "mutating": true}},
			{ID: "create-timer", Name: "Schedule", Args: map[string]any{"verb": "create", "name": timer, "prompt": "run timer remote proof", "one_shot": due.Format(time.RFC3339), "mutating": true}},
		}},
		borrowedProofTurn("origin", proofPath("origin")),
		borrowedScriptTurn{Text: "origin complete"},
	)
	runKubectl(t, ctx, kubeconfig, "rollout", "restart", "deployment/mecak8s", "-n", namespace)
	runKubectl(t, ctx, kubeconfig, "rollout", "status", "deployment/mecak8s", "-n", namespace, "--timeout=240s")
	issuer := portForward(t, ctx, kubeconfig, "service/oidc-issuer", 8443)
	token := fixtureToken(t, ctx, issuer.addr, filepath.Join(state, "pki"), "alice")
	issuer.stop()
	agent := portForward(t, ctx, kubeconfig, "service/mecak8s", 8081)
	defer func() { agent.stop() }()
	grpc := portForward(t, ctx, kubeconfig, "service/mecak8s", 8080)
	defer func() { grpc.stop() }()
	wire, err := tuiClient.Dial(tuiClient.DialConfig{Server: grpc.addr, AuthToken: token})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { wire.Close() }()
	origin := ""
	if source := os.Getenv("MECATL_EXECUTION_BORROWED_SOURCE"); source != "" {
		if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(source) {
			t.Fatal("borrowed source must be a test-owned session ID")
		}
		origin = successorSession(t, ctx, agent.addr, source, token, "clear")
	} else {
		origin = createSession(t, ctx, agent.addr, token)
	}
	ref := environmentForBinding(t, ctx, kubeconfig, origin)
	pvc := readExecutionStatus(t, ctx, kubeconfig, ref.ID).PVCUID
	if pvc == "" {
		t.Fatal("origin has no retained PVC")
	}
	originResult := prompt(t, ctx, agent.addr, origin, token, "create two borrowed schedules and write origin proof")
	calls := map[string]bool{}
	for _, line := range bytes.Split(originResult, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		var event mecatlv1.Event
		if err := json.Unmarshal(bytes.TrimPrefix(line, []byte("data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.ToolResult != nil && !event.ToolResult.IsError {
			calls[event.ToolResult.CallId] = true
		}
	}
	for _, name := range []string{"create-manual", "create-timer", "origin-shell"} {
		if !calls[name] {
			t.Fatalf("origin lacked successful tool call %s: %s", name, mockSSEStatus(originResult))
		}
	}
	if got := environmentForBinding(t, ctx, kubeconfig, origin); got != ref {
		t.Fatal("origin environment changed across in-chat create")
	}
	providerForward := portForward(t, ctx, kubeconfig, "service/mecatl-execution", 8443)
	defer providerForward.stop()
	providerTLS := loadLiveIssuedTLS(t, filepath.Join(state, "pki"), "mecak8s", "mecatl-execution.execution-qualification.svc.cluster.local")
	provider, err := executionclient.New(providerForward.addr, providerTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	waitProviderRPCReady(t, ctx, provider, providerForward.addr, providerTLS)
	owner := executionenv.Owner{Issuer: "https://oidc-issuer.execution-qualification.svc.cluster.local:8443", Subject: "alice"}
	if attached, err := provider.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: ref, Owner: owner, BindingID: origin}, Purpose: executionenv.PurposeSession}); err != nil || !attached.Ready {
		t.Fatalf("published origin reference cannot attach after run: ready=%t code=%s", attached.Ready, remoteErrorCode(err))
	}
	probe := executionenv.ReferenceRequest{Environment: ref, Owner: owner, SourceBindingID: origin, BindingID: fmt.Sprintf("borrowed-probe-%d", id), OperationID: fmt.Sprintf("borrowed-probe-op-%d", id)}
	if err := provider.ReserveSuccessor(ctx, probe); err != nil {
		t.Fatalf("native provider cannot reserve from completed origin: code=%s err=%v", remoteErrorCode(err), err)
	}
	if err := provider.AbortReference(ctx, probe); err != nil {
		t.Fatalf("abort unpublished qualification reservation: code=%s err=%v", remoteErrorCode(err), err)
	}
	redisForward := portForward(t, ctx, kubeconfig, "service/redis", 6379)
	defer redisForward.stop()
	store, err := redisstore.New(redisForward.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	schedules, err := store.ScheduleStore().List(ctx)
	if err != nil {
		t.Fatalf("read persisted schedule: %v", err)
	}
	matched := false
	for _, sched := range schedules {
		if !strings.HasSuffix(sched.Spec.Name, manual) {
			continue
		}
		matched = true
		if sched.Spec.OriginSessionID != session.SessionID(origin) || sched.Spec.EnvironmentRef.ID != ref.ID || sched.Spec.EnvironmentRef.Revision != ref.Revision || sched.Spec.EnvironmentRef.Kind != "kubernetes" || sched.Spec.PlacementScope != "remote-execution" || sched.Spec.Owner == nil || sched.Spec.Owner.Issuer != owner.Issuer || sched.Spec.Owner.Subject != owner.Subject {
			t.Fatalf("persisted manual schedule lost native origin binding: origin_match=%t ref_match=%t scope_match=%t owner_match=%t", sched.Spec.OriginSessionID == session.SessionID(origin), sched.Spec.EnvironmentRef.ID == ref.ID && sched.Spec.EnvironmentRef.Revision == ref.Revision && sched.Spec.EnvironmentRef.Kind == "kubernetes", sched.Spec.PlacementScope == "remote-execution", sched.Spec.Owner != nil && sched.Spec.Owner.Issuer == owner.Issuer && sched.Spec.Owner.Subject == owner.Subject)
		}
	}
	if !matched {
		t.Fatal("in-chat schedule not persisted in Redis")
	}
	originSnapshot, err := store.Load(ctx, session.SessionID(origin))
	if err != nil {
		t.Fatalf("origin snapshot no longer persisted: %v", err)
	}
	if originSnapshot.EnvironmentRef.Kind != "kubernetes" || originSnapshot.EnvironmentRef.ID != ref.ID || originSnapshot.EnvironmentRef.Revision != ref.Revision || originSnapshot.Owner == nil || originSnapshot.Owner.Issuer != owner.Issuer || originSnapshot.Owner.Subject != owner.Subject {
		t.Fatalf("origin snapshot lost exact binding: ref_match=%t owner_match=%t", originSnapshot.EnvironmentRef.Kind == "kubernetes" && originSnapshot.EnvironmentRef.ID == ref.ID && originSnapshot.EnvironmentRef.Revision == ref.Revision, originSnapshot.Owner != nil && originSnapshot.Owner.Issuer == owner.Issuer && originSnapshot.Owner.Subject == owner.Subject)
	}
	if originSnapshot.Owner.GrantType != session.GrantTypeUser && originSnapshot.Owner.GrantType != session.GrantTypeClientCredentials {
		t.Fatalf("origin persisted unsupported execution grant type %q", originSnapshot.Owner.GrantType)
	}
	if _, err := wire.GetSession(ctx, origin); err != nil {
		t.Fatalf("authorized origin session unavailable to agent: %v", err)
	}
	installBorrowedScript(t, ctx, kubeconfig,
		borrowedProofTurn("manual", proofPath("manual")),
		borrowedCall("manual-read", "Read", map[string]any{"path": proofPath("manual")}),
		borrowedScriptTurn{Text: "manual complete"},
		borrowedProofTurn("second", proofPath("second")),
		borrowedCall("second-read", "Read", map[string]any{"path": proofPath("second")}),
		borrowedScriptTurn{Text: "second manual complete"},
	)
	runKubectl(t, ctx, kubeconfig, "rollout", "restart", "deployment/mecak8s", "-n", namespace)
	runKubectl(t, ctx, kubeconfig, "rollout", "status", "deployment/mecak8s", "-n", namespace, "--timeout=240s")
	grpc.stop()
	agent.stop()
	grpc = portForward(t, ctx, kubeconfig, "service/mecak8s", 8080)
	agent = portForward(t, ctx, kubeconfig, "service/mecak8s", 8081)
	if err := wire.Close(); err != nil {
		t.Fatal(err)
	}
	wire, err = tuiClient.Dial(tuiClient.DialConfig{Server: grpc.addr, AuthToken: token})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	var fireID, fireSession string
	leaderDeadline := time.Now().Add(45 * time.Second)
	for {
		fireID, fireSession, err = wire.FireNow(ctx, manual)
		if err == nil || status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "not the scheduler leader") || time.Now().After(leaderDeadline) {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil || fireID == "" || fireSession == "" {
		t.Fatalf("manual fire: id=%q session=%q err=%v", fireID, fireSession, err)
	}
	fireAttach, err := provider.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: ref, Owner: owner, BindingID: fireSession}, Purpose: executionenv.PurposeSession})
	if err != nil || !fireAttach.Ready {
		t.Fatalf("manual fire reference not attachable: ready=%t code=%s", fireAttach.Ready, remoteErrorCode(err))
	}
	fireClaim, release := acquireRun(t, ctx, provider, owner, fireSession, fireAttach, fmt.Sprintf("verify-manual-%d", id))
	file, fileErr := provider.File(ctx, executionenv.FileRequest{Context: fireClaim, Operation: executionenv.OpFileRead, Path: proofPath("manual")})
	release()
	if fileErr != nil || string(file.Data) != "manual\n" {
		t.Fatalf("manual fire did not write remote proof: code=%s", remoteErrorCode(fileErr))
	}
	if got := waitBorrowedFire(t, ctx, wire, manual, started); got != fireSession {
		t.Fatalf("manual fire session=%s want=%s", got, fireSession)
	}
	fireSnapshot, err := store.Load(ctx, session.SessionID(fireSession))
	if err != nil {
		t.Fatalf("load completed manual fire snapshot: %v", err)
	}
	assertBorrowedEvents(t, ctx, agent.addr, token, fireSession, "manual")
	assertBorrowedSnapshot(t, fireSnapshot, "manual")
	if fireSession == origin || environmentForBinding(t, ctx, kubeconfig, fireSession) != ref || readExecutionStatus(t, ctx, kubeconfig, ref.ID).PVCUID != pvc {
		t.Fatal("manual fire did not publish its own binding to the exact origin environment and PVC")
	}
	t.Logf("manual borrowed fire completed session=%s environment=%s", fireSession, ref.ID)

	secondStarted := time.Now()
	_, secondSession, err := wire.FireNow(ctx, manual)
	if err != nil || secondSession == "" || secondSession == fireSession {
		t.Fatalf("second manual fire after released claim: session=%q err=%v", secondSession, err)
	}
	if got := waitBorrowedFire(t, ctx, wire, manual, secondStarted); got != secondSession {
		t.Fatalf("second manual session=%s want=%s", got, secondSession)
	}
	if environmentForBinding(t, ctx, kubeconfig, secondSession) != ref || readExecutionStatus(t, ctx, kubeconfig, ref.ID).PVCUID != pvc {
		t.Fatal("second fire lost the borrowed environment or PVC")
	}
	secondSnapshot, err := store.Load(ctx, session.SessionID(secondSession))
	if err != nil {
		t.Fatalf("load completed second manual fire snapshot: %v", err)
	}
	assertBorrowedSnapshot(t, secondSnapshot, "second")
	assertBorrowedEvents(t, ctx, agent.addr, token, secondSession, "second")
	t.Logf("second borrowed fire completed session=%s environment=%s", secondSession, ref.ID)

	installBorrowedScript(t, ctx, kubeconfig,
		borrowedProofTurn("timer", proofPath("timer")),
		borrowedCall("timer-read", "Read", map[string]any{"path": proofPath("timer")}),
		borrowedScriptTurn{Text: "timer complete"},
	)
	runKubectl(t, ctx, kubeconfig, "rollout", "restart", "deployment/mecatl-execution", "-n", namespace)
	runKubectl(t, ctx, kubeconfig, "rollout", "status", "deployment/mecatl-execution", "-n", namespace, "--timeout=240s")
	runKubectl(t, ctx, kubeconfig, "rollout", "restart", "deployment/mecak8s", "-n", namespace)
	runKubectl(t, ctx, kubeconfig, "rollout", "status", "deployment/mecak8s", "-n", namespace, "--timeout=240s")
	grpc.stop()
	agent.stop()
	grpc = portForward(t, ctx, kubeconfig, "service/mecak8s", 8080)
	agent = portForward(t, ctx, kubeconfig, "service/mecak8s", 8081)
	if err := wire.Close(); err != nil {
		t.Fatal(err)
	}
	wire, err = tuiClient.Dial(tuiClient.DialConfig{Server: grpc.addr, AuthToken: token})
	if err != nil {
		t.Fatal(err)
	}
	if time.Now().After(due) {
		t.Fatalf("restart took past scheduled instant %s; cannot establish a future timer fire", due)
	}
	if got, err := wire.GetSchedule(ctx, timer); err != nil || got.Spec.Name != timer || !got.State.NextFireAt.Equal(due) {
		t.Fatalf("durable one-shot after restart: spec=%+v state=%+v err=%v", got.Spec, got.State, err)
	}
	if got := environmentForBinding(t, ctx, kubeconfig, origin); got != ref || readExecutionStatus(t, ctx, kubeconfig, ref.ID).PVCUID != pvc {
		t.Fatal("provider/agent restart lost exact environment or PVC")
	}
	future := waitBorrowedFire(t, ctx, wire, timer, due)
	if future == fireSession || future == secondSession || future == origin || environmentForBinding(t, ctx, kubeconfig, future) != ref || readExecutionStatus(t, ctx, kubeconfig, ref.ID).PVCUID != pvc {
		t.Fatal("timer fire did not publish distinct binding on original environment and PVC")
	}
	assertBorrowedEvents(t, ctx, agent.addr, token, future, "timer")
	futureSnapshot, err := store.Load(ctx, session.SessionID(future))
	if err != nil {
		t.Fatalf("load completed timer fire snapshot: %v", err)
	}
	assertBorrowedSnapshot(t, futureSnapshot, "timer")
	t.Logf("future borrowed timer completed session=%s environment=%s", future, ref.ID)
}
