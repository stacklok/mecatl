//go:build kind_execution_e2e

package k8s_execution_test

import (
	"context"
	"encoding/json"
	"go/format"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestLivePromptWaitsForLeaseBeforeAcceptedStream(t *testing.T) {
	calls := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || r.Header.Get("Authorization") != "Bearer synthetic" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(calls) == 0 {
			calls <- body.Text
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"session_leased_elsewhere"}`))
			return
		}
		calls <- body.Text
		_, _ = w.Write([]byte("data: {\"result\":{\"stop\":\"end_turn\"}}\n\n"))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	events := livePrompt(t, ctx, strings.TrimPrefix(server.URL, "http://"), "owned", "synthetic", "verify")
	if len(events) != 1 || events[0].Result == nil || len(calls) != 2 || <-calls != "verify" || <-calls != "verify" {
		t.Fatal("lease recovery did not preserve the request and accepted stream")
	}
}

func TestFixtureKubectlUsesSREToolboxWithExactContext(t *testing.T) {
	t.Setenv("MECATL_EXECUTION_K8S_TOOLBOX", "sre")
	t.Setenv("MECATL_KUBE_CONTEXT", "kind-owned-fixture")
	cmd := command(context.Background(), "/owned/state/kubeconfig", "get", "pods")
	if !slices.Equal(cmd.Args, []string{"toolbox", "run", "-c", "sre", "kubectl", "--kubeconfig", "/owned/state/kubeconfig", "--context", "kind-owned-fixture", "get", "pods"}) {
		t.Fatalf("kubectl command lost explicit SRE context: %v", cmd.Args)
	}
}

func TestReplacementProofRequestsUsePostClaimEpochForPositiveRequest(t *testing.T) {
	ref := executionenv.EnvironmentRef{ID: "fixture", Revision: "revision"}
	owner := executionenv.Owner{Issuer: "issuer", Subject: "subject"}
	staleStatus := executionStatus{Epoch: 4, PodUID: "pod", PVCUID: "pvc"}
	current := executionStatus{Epoch: 6, PodUID: "pod", PVCUID: "pvc"}
	stale, wrong, exact := replacementProofRequests(ref, owner, staleStatus, current)
	if stale.ExpectedEpoch != staleStatus.Epoch || stale.ExpectedPodUID != staleStatus.PodUID {
		t.Fatal("stale negative request did not preserve the pre-claim authority")
	}
	if wrong.ExpectedEpoch != current.Epoch || wrong.ExpectedPodUID == current.PodUID {
		t.Fatal("wrong-UID negative request did not use current authority")
	}
	if exact.ExpectedEpoch != current.Epoch || exact.ExpectedPodUID != current.PodUID || exact.ExpectedPVCUID != current.PVCUID {
		t.Fatal("positive exact request did not use post-release authoritative state")
	}
}

func TestModelRanExactShellRequiresDecodedCommandAndMatchingCallID(t *testing.T) {
	call := func(id, args string) *mecatlv1.Event {
		return &mecatlv1.Event{ToolCall: &mecatlv1.ToolCall{Id: id, Name: "Shell", Args: args}}
	}
	result := func(id string) *mecatlv1.Event {
		return &mecatlv1.Event{ToolResult: &mecatlv1.ToolResult{CallId: id, Content: "[exit code: 0]"}}
	}
	for name, events := range map[string][]*mecatlv1.Event{
		"echo":              {call("shell", `{"command":"echo ok"}`), result("shell")},
		"invalid JSON":      {call("shell", `{"command":`), result("shell")},
		"wrong result ID":   {call("shell", `{"command":"go test ./..."}`), result("other")},
		"unrelated success": {call("echo", `{"command":"echo ok"}`), result("echo"), call("test", `{"command":"go test ./..."}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if modelRanExactShell(events, "go test ./...") {
				t.Fatal("unqualified Shell call counted")
			}
		})
	}
	if !modelRanExactShell([]*mecatlv1.Event{call("test", `{"command":"  go test ./...  "}`), result("test")}, "go test ./...") {
		t.Fatal("matching successful Shell call was not counted")
	}
}

func TestLiveCompletedCodingRetainsOnlySuccessfulMarkerRun(t *testing.T) {
	edit := &mecatlv1.Event{ToolCall: &mecatlv1.ToolCall{Name: "Edit", Args: `{"path":"arithmetic/sum.go","new_string":"// qualification: qual_123"}`}}
	success := &mecatlv1.Event{Result: &mecatlv1.Result{Stop: "end_turn"}}
	budget := &mecatlv1.Event{Result: &mecatlv1.Result{Stop: "budget"}}
	if got, _ := completedLiveCoding([]*mecatlv1.Event{edit, budget}); len(got) != 0 {
		t.Fatal("budget-stopped run accepted as completed coding")
	}
	got, nonce := completedLiveCoding([]*mecatlv1.Event{budget, edit, success, budget, success})
	if nonce != "qual_123" || len(got) != 2 || got[0] != edit || got[1] != success {
		t.Fatal("later empty or failed run displaced original coding evidence")
	}
}

func TestLiveRestartProofHelpersAcceptExactArtifactsAndCleanupOnce(t *testing.T) {
	before := map[string][]byte{
		"arithmetic/sum.go":      []byte("package arithmetic\n// qualification: qual_fixture\nfunc Sum(a, b int) int { return a + b }\n"),
		"arithmetic/sum_test.go": []byte("package arithmetic\nfunc ExampleSum() { _ = Sum(1, 2) }\n"),
	}
	after := map[string][]byte{
		"arithmetic/sum.go":      append([]byte(nil), before["arithmetic/sum.go"]...),
		"arithmetic/sum_test.go": append([]byte(nil), before["arithmetic/sum_test.go"]...),
	}
	if !qualificationArtifactsEqual(before, after) {
		t.Fatal("byte-identical fixed qualification artifacts were rejected")
	}
	if !containsExactLine(before["arithmetic/sum.go"], "// qualification: qual_fixture") {
		t.Fatal("exact nonce marker line was rejected")
	}

	calls := 0
	cleanup := cleanupOnce(func() { calls++ })
	cleanup()
	cleanup()
	if calls != 1 {
		t.Fatalf("cleanup calls=%d, want 1", calls)
	}
}

func TestLiveRestartArtifactDiagnosticsClassifyAndRedact(t *testing.T) {
	const private = "PRIVATE-ARTIFACT-DETAIL"
	for _, tc := range []struct {
		name      string
		responses []struct {
			file executionenv.FileResponse
			err  error
		}
		wantCalls   int
		wantOutcome string
		wantClass   string
		wantRetry   bool
		wantBytes   int
	}{
		{
			name: "retryable not ready replays read only",
			responses: []struct {
				file executionenv.FileResponse
				err  error
			}{{err: &executionenv.Error{Code: executionenv.CodeNotReady, Retryable: true, Message: private}}, {file: executionenv.FileResponse{Data: []byte("artifact")}}},
			wantCalls: 2,
		},
		{
			name: "nonretryable not ready fails immediately",
			responses: []struct {
				file executionenv.FileResponse
				err  error
			}{{err: &executionenv.Error{Code: executionenv.CodeNotReady, Message: private}}},
			wantCalls: 1, wantOutcome: "read_error", wantClass: "not_ready",
		},
		{
			name: "stale authority fails immediately",
			responses: []struct {
				file executionenv.FileResponse
				err  error
			}{{err: &executionenv.Error{Code: executionenv.CodeConflict, Retryable: true, Message: private}}},
			wantCalls: 1, wantOutcome: "read_error", wantClass: "conflict",
		},
		{
			name: "remote error",
			responses: []struct {
				file executionenv.FileResponse
				err  error
			}{{err: &executionenv.Error{Code: executionenv.CodeInternal, Message: private}}},
			wantCalls: 1, wantOutcome: "read_error", wantClass: "internal",
		},
		{
			name: "empty success",
			responses: []struct {
				file executionenv.FileResponse
				err  error
			}{{file: executionenv.FileResponse{}}},
			wantCalls: 1, wantOutcome: "empty_response", wantClass: "none",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			data, failure := readQualificationArtifact(t.Context(), "post_restart", "arithmetic/sum_test.go", func(context.Context) (executionenv.FileResponse, error) {
				calls++
				response := tc.responses[calls-1]
				return response.file, response.err
			})
			if calls != tc.wantCalls {
				t.Fatalf("calls=%d want=%d", calls, tc.wantCalls)
			}
			if tc.wantOutcome == "" {
				if failure != nil || string(data) != "artifact" {
					t.Fatalf("data=%q failure=%v", data, failure)
				}
				return
			}
			if failure == nil || failure.Outcome != tc.wantOutcome || failure.ErrorClass != tc.wantClass || failure.Retryable != tc.wantRetry || failure.ByteCount != tc.wantBytes {
				t.Fatalf("failure=%+v", failure)
			}
			if strings.Contains(failure.Error(), private) {
				t.Fatal("artifact diagnostics exposed a private remote error message")
			}
		})
	}
}

func TestLiveRestartArtifactRetryTerminationRetainsLastDiagnostic(t *testing.T) {
	notReady := func() (executionenv.FileResponse, error) {
		return executionenv.FileResponse{Data: []byte("partial")}, &executionenv.Error{Code: executionenv.CodeNotReady, Retryable: true, Message: "PRIVATE-REMOTE-DETAIL"}
	}
	for _, tc := range []struct {
		name         string
		ctx          func() (context.Context, context.CancelFunc)
		read         func(context.Context) (executionenv.FileResponse, error)
		maxAttempts  int
		wait         func(context.Context, context.CancelFunc) error
		wantCalls    int
		wantOutcome  string
		wantAttempts int
	}{
		{
			name: "cancel before read",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				return ctx, func() {}
			},
			read: func(context.Context) (executionenv.FileResponse, error) {
				t.Fatal("read called after cancellation")
				return executionenv.FileResponse{}, nil
			},
			maxAttempts: 2, wait: func(context.Context, context.CancelFunc) error { return nil },
			wantOutcome: "context_canceled",
		},
		{
			name:        "timeout during backoff",
			ctx:         func() (context.Context, context.CancelFunc) { return context.WithCancel(t.Context()) },
			read:        func(context.Context) (executionenv.FileResponse, error) { return notReady() },
			maxAttempts: 2, wait: func(context.Context, context.CancelFunc) error { return context.DeadlineExceeded },
			wantCalls: 1, wantOutcome: "retry_timeout", wantAttempts: 1,
		},
		{
			name:        "cancel during backoff",
			ctx:         func() (context.Context, context.CancelFunc) { return context.WithCancel(t.Context()) },
			read:        func(context.Context) (executionenv.FileResponse, error) { return notReady() },
			maxAttempts: 2,
			wait: func(ctx context.Context, cancel context.CancelFunc) error {
				cancel()
				return ctx.Err()
			},
			wantCalls: 1, wantOutcome: "context_canceled", wantAttempts: 1,
		},
		{
			name: "deadline returned by final in flight read",
			ctx:  func() (context.Context, context.CancelFunc) { return context.WithCancel(t.Context()) },
			read: func(ctx context.Context) (executionenv.FileResponse, error) {
				if ctx.Err() == nil {
					return notReady()
				}
				return executionenv.FileResponse{}, ctx.Err()
			},
			maxAttempts: 2,
			wait: func(context.Context, context.CancelFunc) error {
				return nil
			},
			wantCalls: 2, wantOutcome: "retry_timeout", wantAttempts: 2,
		},
		{
			name:        "retry exhaustion",
			ctx:         func() (context.Context, context.CancelFunc) { return context.WithCancel(t.Context()) },
			read:        func(context.Context) (executionenv.FileResponse, error) { return notReady() },
			maxAttempts: 2, wait: func(context.Context, context.CancelFunc) error { return nil },
			wantCalls: 2, wantOutcome: "retry_exhausted", wantAttempts: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.ctx()
			defer cancel()
			calls := 0
			_, failure := readQualificationArtifactWithRetry(ctx, "post_restart", "arithmetic/sum.go", func(ctx context.Context) (executionenv.FileResponse, error) {
				calls++
				if tc.name == "deadline returned by final in flight read" && calls == 2 {
					return executionenv.FileResponse{}, context.DeadlineExceeded
				}
				return tc.read(ctx)
			}, time.Hour, tc.maxAttempts, func(ctx context.Context) error { return tc.wait(ctx, cancel) })
			if calls != tc.wantCalls || failure == nil || failure.Outcome != tc.wantOutcome || failure.Attempts != tc.wantAttempts {
				t.Fatalf("calls=%d failure=%+v", calls, failure)
			}
			if tc.wantCalls > 0 && (failure.ErrorClass != "not_ready" || !failure.Retryable || failure.ByteCount != len("partial")) {
				t.Fatalf("last RPC diagnostic was not retained: %+v", failure)
			}
		})
	}
}

func TestWriteLiveQualificationFailureSummaryRedactsArtifactData(t *testing.T) {
	const sentinel = "PRIVATE-ARTIFACT-DETAIL"
	t.Setenv("MECATL_EXECUTION_TEMPLATE_REVISION", "fixture-revision")
	state := t.TempDir()
	rc := executionenv.RequestContext{Environment: executionenv.EnvironmentRef{ID: "environment"}}
	_, failure := readQualificationArtifactWithRetry(t.Context(), "post_restart", "arithmetic/sum.go", func(context.Context) (executionenv.FileResponse, error) {
		return executionenv.FileResponse{Data: []byte(sentinel)}, &executionenv.Error{Code: executionenv.CodeNotReady, Retryable: true, Message: sentinel}
	}, time.Hour, 2, func(context.Context) error { return context.DeadlineExceeded })
	if failure == nil {
		t.Fatal("expected retry failure")
	}
	if failure.Outcome != "retry_timeout" || failure.ErrorClass != "not_ready" || !failure.Retryable || failure.ByteCount != len(sentinel) || failure.Attempts != 1 {
		t.Fatalf("unexpected retry failure: %+v", failure)
	}
	writeLiveQualificationFailureSummary(t, state, rc, *failure)

	blob, err := os.ReadFile(filepath.Join(state, "live-summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), sentinel) {
		t.Fatal("persisted summary exposed artifact data")
	}
	var got liveSummary
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatal(err)
	}
	if got.EnvironmentID != "environment" || got.TemplateRevision != "fixture-revision" || got.QualificationPhase != failure.Phase || got.QualificationOutcome != failure.Outcome || got.QualificationArtifactPath != failure.Path || got.QualificationErrorClass != failure.ErrorClass || got.QualificationRetryable != failure.Retryable || got.QualificationByteCount != failure.ByteCount || got.QualificationExpectedSize != failure.ExpectedByteCount || got.QualificationAttempts != failure.Attempts || got.Timestamp == "" {
		t.Fatalf("persisted summary=%+v failure=%+v", got, failure)
	}
}

func TestLiveRestartArtifactMismatchDiagnosticsAreBounded(t *testing.T) {
	const private = "PRIVATE-ARTIFACT-CONTENT"
	before := map[string][]byte{
		"arithmetic/sum.go":      []byte(private),
		"arithmetic/sum_test.go": []byte("unchanged"),
	}
	after := map[string][]byte{
		"arithmetic/sum.go":      []byte("different"),
		"arithmetic/sum_test.go": []byte("unchanged"),
	}
	failure := qualificationArtifactMismatch(before, after)
	if failure == nil || failure.Outcome != "content_mismatch" || failure.Path != "arithmetic/sum.go" || failure.ByteCount != len(after["arithmetic/sum.go"]) || failure.ExpectedByteCount != len(before["arithmetic/sum.go"]) {
		t.Fatalf("failure=%+v", failure)
	}
	if strings.Contains(failure.Error(), private) {
		t.Fatal("artifact mismatch diagnostics exposed artifact content")
	}
}

func TestQualificationHelperIsValidCanonicalGo(t *testing.T) {
	formatted, err := format.Source([]byte(qualificationHelperFile))
	if err != nil {
		t.Fatalf("fixed qualification helper is invalid Go: %v", err)
	}
	if string(formatted) != qualificationHelperFile {
		t.Fatal("fixed qualification helper is not gofmt-canonical")
	}
}

func TestLiveSummaryIncludesFixedRestartEvidence(t *testing.T) {
	blob, err := json.Marshal(liveSummary{
		Cluster:                   "fixture",
		Provider:                  "openrouter",
		Model:                     liveModel,
		InputTokens:               1,
		OutputTokens:              2,
		ToolNames:                 []string{"Write", "Shell"},
		ArtifactCount:             2,
		RestartVerified:           true,
		BeforeRestartExitCode:     0,
		AfterRestartExitCode:      0,
		QualificationPhase:        "post_restart",
		QualificationOutcome:      "read_error",
		QualificationArtifactPath: "arithmetic/sum_test.go",
		QualificationErrorClass:   "not_ready",
		QualificationRetryable:    true,
		QualificationByteCount:    0,
		QualificationExpectedSize: 17,
		QualificationAttempts:     2,
		Timestamp:                 "2026-09-23T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(blob, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"cluster", "provider", "model", "input_tokens", "output_tokens", "tool_names", "artifact_count", "timestamp", "restart_verified", "before_restart_exit_code", "after_restart_exit_code", "session_id", "environment_id", "template_revision", "pvc_uid", "completed_model_calls", "initial_successful_tool_counts", "qualification_phase", "qualification_outcome", "qualification_artifact_path", "qualification_error_class", "qualification_retryable", "qualification_byte_count", "qualification_expected_byte_count", "qualification_attempts"} {
		if _, ok := fields[name]; !ok {
			t.Fatalf("summary omitted %s", name)
		}
	}
	if len(fields) != 25 {
		t.Fatalf("summary field count=%d, want fixed allowlist of 25", len(fields))
	}
	var verified bool
	if err := json.Unmarshal(fields["restart_verified"], &verified); err != nil || !verified {
		t.Fatal("summary did not preserve positive restart verdict")
	}
}

func TestRealProviderArgsRejectExecutionMockSources(t *testing.T) {
	tests := []corev1.PodSpec{
		{Volumes: []corev1.Volume{{Name: "execution-mock"}}},
		{Volumes: []corev1.Volume{{Name: "other", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "execution-mock"}}}}}},
		{
			Volumes: []corev1.Volume{{
				Name: "other",
				VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{
					ConfigMap: &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "execution-mock"}},
				}}}},
			}},
		},
		{Containers: []corev1.Container{{Name: "sidecar", VolumeMounts: []corev1.VolumeMount{{Name: "execution-mock"}}}}},
	}
	for i, spec := range tests {
		if err := realProviderConfigError(spec); err == nil {
			t.Fatalf("case %d did not reject execution-mock source", i)
		}
	}
}

func TestSecretReferencesFindEveryCredentialEscape(t *testing.T) {
	const sentinel = "synthetic-live-secret-sentinel"
	tests := map[string]corev1.PodSpec{
		"sidecar env": {
			Containers: []corev1.Container{{Name: "sidecar", Env: []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: sentinel}, Key: "token"}}}}}},
		},
		"sidecar envFrom": {
			Containers: []corev1.Container{{Name: "sidecar", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: sentinel}}}}}},
		},
		"init container env": {
			InitContainers: []corev1.Container{{Name: "setup", Env: []corev1.EnvVar{{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: sentinel}, Key: "token"}}}}}},
		},
		"secret volume": {
			Volumes: []corev1.Volume{{Name: "credentials", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: sentinel}}}},
		},
		"projected secret volume": {
			Volumes: []corev1.Volume{{Name: "credentials", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: sentinel}}}}}}}},
		},
	}
	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			if !containsSecretReference(secretReferences(spec), sentinel) {
				t.Fatal("credential escape was not detected")
			}
		})
	}
}

func TestExpectedHarnessCredentialReferenceIsExact(t *testing.T) {
	const sentinel = "synthetic-live-secret-sentinel"
	spec := corev1.PodSpec{Containers: []corev1.Container{{
		Name: "agent",
		Env: []corev1.EnvVar{{Name: liveCredentialKey, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: sentinel},
			Key:                  liveCredentialKey,
		}}}},
	}}}
	want := secretReference{Container: "agent", Source: "env", Name: sentinel, Key: liveCredentialKey, EnvName: liveCredentialKey}
	refs := secretReferences(spec)
	if len(refs) != 1 || refs[0] != want {
		t.Fatal("expected harness credential reference was not projected exactly")
	}
}
