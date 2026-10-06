//go:build kind_execution_e2e

package k8s_execution_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/executionclient"
	"github.com/stacklok/mecatl/internal/executionenv"
)

const (
	liveModel         = "anthropic/claude-haiku-4.5"
	liveCredentialKey = "OPENROUTER_API_KEY"
)

func TestKindExecutionLiveQualification(t *testing.T) {
	if os.Getenv("MECATL_EXECUTION_LIVE") != "1" {
		t.Skip("explicit live qualification only")
	}
	state := os.Getenv("MECATL_EXECUTION_QUAL_STATE")
	kubeconfig := filepath.Join(state, "kubeconfig")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	tokenForward := portForward(t, ctx, kubeconfig, "service/oidc-issuer", 8443)
	alice := fixtureToken(t, ctx, tokenForward.addr, filepath.Join(state, "pki"), "alice")
	tokenForward.stop()
	agentForward := portForward(t, ctx, kubeconfig, "service/mecak8s", 8081)
	stopAgentForward := cleanupOnce(agentForward.stop)
	defer stopAgentForward()

	preflightCtx, preflightCancel := context.WithTimeout(ctx, 15*time.Second)
	t.Log("live stage=authenticated_http_preflight reason=begin")
	preflightStatus, _ := request(t, preflightCtx, http.MethodGet, "http://"+agentForward.addr+"/v1/info", alice, nil)
	preflightCancel()
	if preflightStatus != http.StatusOK {
		t.Fatalf("live stage=authenticated_http_preflight reason=rejected status=%d", preflightStatus)
	}
	t.Log("live stage=authenticated_http_preflight reason=ok")
	created := createLiveSession(t, ctx, agentForward.addr, alice)
	secretName := os.Getenv("MECATL_EXECUTION_LIVE_SECRET")
	if !strings.HasPrefix(secretName, "mecak8s-live-") {
		t.Fatal("run-scoped provider Secret name is unavailable")
	}
	assertCredentialScope(t, ctx, kubeconfig, secretName)
	if created.ResolvedModel == nil || created.ResolvedModel.ProviderId != "openrouter" || created.ResolvedModel.ModelId != liveModel {
		t.Fatal("live session resolved an unexpected provider or model")
	}
	nonce := fmt.Sprintf("qual_%d", time.Now().UnixNano())
	promptText := "Create a small Go 1.27 module with arithmetic/sum.go defining Sum(a, b int) int and a table-driven arithmetic/sum_test.go covering positive, negative, and zero inputs. Include the comment // qualification: " + nonce + " in sum.go. Use the Write tool to create the files, then use Shell with the exact command `go test ./...`. Fix any failures and finish with a concise result only after that exact command passes. Do not add external dependencies."
	events := livePrompt(t, ctx, agentForward.addr, created.SessionId, alice, promptText)

	calls := map[string]bool{}
	modelShellPassed := modelRanExactShell(events, "go test ./...")
	var usage *mecatlv1.Usage
	stop := ""
	for _, ev := range events {
		if ev.ToolCall != nil {
			calls[ev.ToolCall.Name] = true
		}
		if ev.ToolResult != nil && ev.ToolResult.IsError {
			t.Fatal("live coding smoke had a tool error")
		}
		if ev.Result != nil {
			stop = ev.Result.Stop
			usage = ev.Result.Usage
		}
	}
	if stop != string(session.StopEndTurn) {
		t.Fatalf("real provider run did not finish successfully (stop=%s)", stop)
	}
	if !calls["Write"] {
		t.Fatal("real provider did not call a filesystem write tool")
	}
	if !calls["Shell"] {
		t.Fatal("real provider did not call Shell")
	}
	if !modelShellPassed {
		t.Fatal("real provider Shell result did not report exit code 0")
	}
	if usage == nil || usage.InputTokens <= 0 || usage.OutputTokens <= 0 {
		t.Fatal("real provider returned no positive token usage")
	}
	t.Logf("live stage=model_coding reason=ok input_tokens=%d output_tokens=%d shell_exit_code=0", usage.InputTokens, usage.OutputTokens)

	t.Log("live stage=independent_typed_verification reason=begin")
	providerForward := portForward(t, ctx, kubeconfig, "service/mecatl-execution", 8443)
	stopProviderForward := cleanupOnce(providerForward.stop)
	defer stopProviderForward()
	client, err := executionclient.New(providerForward.addr, loadTLS(t, filepath.Join(state, "pki"), "mecak8s", "mecatl-execution.execution-qualification.svc.cluster.local"))
	if err != nil {
		t.Fatal("typed execution client setup failed")
	}
	closeClient := cleanupOnce(client.Close)
	defer closeClient()
	owner := executionenv.Owner{Issuer: "https://oidc-issuer.execution-qualification.svc.cluster.local:8443", Subject: "alice"}
	lookup := environmentForBinding(t, ctx, kubeconfig, created.SessionId)
	attached := waitReady(t, ctx, client, owner, created.SessionId, lookup)
	if attached.Environment != lookup {
		t.Fatal("typed execution reattachment returned a different environment")
	}
	rc, release := acquireRun(t, ctx, client, owner, created.SessionId, attached, fmt.Sprintf("live-verify-%d", time.Now().UnixNano()))
	release = cleanupOnce(release)
	defer release()
	before := readQualificationArtifacts(ctx, t, client, rc, nonce)
	installQualificationHelper(ctx, t, client, rc)
	beforeCommand := runQualificationTests(ctx, t, client, rc)
	t.Logf("live stage=independent_typed_verification reason=ok artifact_count=%d command_exit_code=%d", len(before), beforeCommand.Result.ExitCode)

	// No verifier claim or connection may survive either deployment restart.
	release()
	assertRunReleased(ctx, t, client, rc)
	closeClient()
	stopProviderForward()
	stopAgentForward()

	t.Log("live stage=restart_verification reason=begin")
	runKubectl(t, ctx, kubeconfig, "rollout", "restart", "deployment/mecatl-execution", "-n", namespace)
	runKubectl(t, ctx, kubeconfig, "rollout", "status", "deployment/mecatl-execution", "-n", namespace, "--timeout=240s")
	runKubectl(t, ctx, kubeconfig, "rollout", "restart", "deployment/mecak8s", "-n", namespace)
	runKubectl(t, ctx, kubeconfig, "rollout", "status", "deployment/mecak8s", "-n", namespace, "--timeout=240s")

	freshTokenForward := portForward(t, ctx, kubeconfig, "service/oidc-issuer", 8443)
	freshAlice := fixtureToken(t, ctx, freshTokenForward.addr, filepath.Join(state, "pki"), "alice")
	freshTokenForward.stop()
	freshAgentForward := portForward(t, ctx, kubeconfig, "service/mecak8s", 8081)
	defer freshAgentForward.stop()
	if status := getSession(t, ctx, freshAgentForward.addr, created.SessionId, freshAlice); status != http.StatusOK {
		t.Fatalf("authenticated post-restart session load status=%d, want 200", status)
	}

	postRestartLookup := environmentForBinding(t, ctx, kubeconfig, created.SessionId)
	if postRestartLookup != lookup {
		t.Fatal("session binding resolved a different environment after restart")
	}
	freshProviderForward := portForward(t, ctx, kubeconfig, "service/mecatl-execution", 8443)
	defer freshProviderForward.stop()
	freshClient, err := executionclient.New(freshProviderForward.addr, loadTLS(t, filepath.Join(state, "pki"), "mecak8s", "mecatl-execution.execution-qualification.svc.cluster.local"))
	if err != nil {
		t.Fatal("post-restart typed execution client setup failed")
	}
	defer freshClient.Close()
	waitProviderRPCReady(t, ctx, freshClient, freshProviderForward.addr, loadTLS(t, filepath.Join(state, "pki"), "mecak8s", "mecatl-execution.execution-qualification.svc.cluster.local"))
	postRestartAttached := waitReady(t, ctx, freshClient, owner, created.SessionId, postRestartLookup)
	if postRestartAttached.Environment != lookup {
		t.Fatal("typed execution reattachment changed the persisted environment after restart")
	}
	postRestartRC, releasePostRestart := acquireRun(t, ctx, freshClient, owner, created.SessionId, postRestartAttached, fmt.Sprintf("live-restart-verify-%d", time.Now().UnixNano()))
	defer releasePostRestart()
	after := readQualificationArtifacts(ctx, t, freshClient, postRestartRC, nonce)
	if !qualificationArtifactsEqual(before, after) {
		t.Fatal("qualification artifact bytes changed across restart")
	}
	assertQualificationHelper(ctx, t, freshClient, postRestartRC)
	afterCommand := runQualificationTests(ctx, t, freshClient, postRestartRC)
	t.Logf("live stage=restart_verification reason=ok artifact_count=%d before_exit_code=%d after_exit_code=%d", len(after), beforeCommand.Result.ExitCode, afterCommand.Result.ExitCode)

	observedTools := make([]string, 0, 4)
	for _, name := range []string{"Write", "Read", "Edit", "Shell"} {
		if calls[name] {
			observedTools = append(observedTools, name)
		}
	}
	cluster := os.Getenv("MECATL_EXECUTION_LIVE_CLUSTER")
	if cluster == "" {
		t.Fatal("verified qualification cluster identity is unavailable")
	}
	summary := liveSummary{
		Cluster:               cluster,
		Provider:              created.ResolvedModel.ProviderId,
		Model:                 created.ResolvedModel.ModelId,
		InputTokens:           usage.InputTokens,
		OutputTokens:          usage.OutputTokens,
		ToolNames:             observedTools,
		ArtifactCount:         len(after),
		RestartVerified:       true,
		BeforeRestartExitCode: beforeCommand.Result.ExitCode,
		AfterRestartExitCode:  afterCommand.Result.ExitCode,
		Timestamp:             time.Now().UTC().Format(time.RFC3339),
	}
	blob, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		t.Fatal("marshal sanitized summary failed")
	}
	if err := os.WriteFile(filepath.Join(state, "live-summary.json"), append(blob, '\n'), 0o600); err != nil {
		t.Fatal("write sanitized summary failed")
	}
}

const (
	qualificationHelperPath = "arithmetic/qualification_external_test.go"
	qualificationHelperFile = `package arithmetic

import "testing"

func TestQualificationSemantics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		a, b   int
		result int
	}{
		{name: "positive", a: 2, b: 3, result: 5},
		{name: "negative", a: -2, b: 5, result: 3},
		{name: "zero", a: 0, b: 0, result: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sum(tc.a, tc.b); got != tc.result {
				t.Fatalf("Sum(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.result)
			}
		})
	}
}
`
)

var qualificationArtifactPaths = [...]string{
	"arithmetic/sum.go",
	"arithmetic/sum_test.go",
}

func cleanupOnce(cleanup func()) func() {
	var once sync.Once
	return func() { once.Do(cleanup) }
}

func readQualificationArtifacts(ctx context.Context, t *testing.T, client *executionclient.Client, rc executionenv.RequestContext, nonce string) map[string][]byte {
	t.Helper()
	artifacts := make(map[string][]byte, len(qualificationArtifactPaths))
	for _, path := range qualificationArtifactPaths {
		file, err := client.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileRead, Path: path})
		if err != nil || len(file.Data) == 0 {
			t.Fatalf("typed gRPC artifact verification failed for %s", path)
		}
		artifacts[path] = bytes.Clone(file.Data)
	}
	if !containsExactLine(artifacts["arithmetic/sum.go"], "// qualification: "+nonce) {
		t.Fatal("typed gRPC implementation artifact omitted the exact qualification nonce marker")
	}
	return artifacts
}

func containsExactLine(content []byte, want string) bool {
	for _, line := range bytes.Split(content, []byte("\n")) {
		if string(bytes.TrimSpace(line)) == want {
			return true
		}
	}
	return false
}

func installQualificationHelper(ctx context.Context, t *testing.T, client *executionclient.Client, rc executionenv.RequestContext) {
	t.Helper()
	if _, err := client.File(ctx, executionenv.FileRequest{
		Context:   rc,
		Operation: executionenv.OpFileCreate,
		Path:      qualificationHelperPath,
		Data:      []byte(qualificationHelperFile),
	}); err != nil {
		t.Fatal("create fixed qualification semantics test failed")
	}
	assertQualificationHelper(ctx, t, client, rc)
}

func assertQualificationHelper(ctx context.Context, t *testing.T, client *executionclient.Client, rc executionenv.RequestContext) {
	t.Helper()
	file, err := client.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileRead, Path: qualificationHelperPath})
	if err != nil || !bytes.Equal(file.Data, []byte(qualificationHelperFile)) {
		t.Fatal("fixed qualification semantics test changed or was unavailable")
	}
}

func runQualificationTests(ctx context.Context, t *testing.T, client *executionclient.Client, rc executionenv.RequestContext) executionenv.CommandStartResponse {
	t.Helper()
	command, err := client.StartCommand(ctx, executionenv.CommandStartRequest{Context: rc, Command: "go test ./...", TimeoutMillis: 120000})
	if err != nil || command.State != executionenv.CommandSucceeded || command.Result.ExitCode != 0 {
		t.Fatal("typed gRPC independent go test failed")
	}
	return command
}

func assertRunReleased(ctx context.Context, t *testing.T, client *executionclient.Client, rc executionenv.RequestContext) {
	t.Helper()
	_, err := client.RenewRun(ctx, executionenv.RunClaimRequest{
		Environment:     rc.Environment,
		Owner:           rc.Owner,
		BindingID:       rc.BindingID,
		RunID:           rc.RunID,
		ClaimID:         rc.ClaimID,
		Epoch:           rc.Epoch,
		GrantGeneration: rc.GrantGeneration,
		OperationID:     "verify-released-" + rc.RunID,
		TTL:             time.Minute,
	})
	if !isRemoteCode(err, executionenv.CodeConflict) {
		t.Fatalf("released verifier claim remained renewable: code=%s", remoteErrorCode(err))
	}
}

func qualificationArtifactsEqual(before, after map[string][]byte) bool {
	if len(before) != len(qualificationArtifactPaths) || len(after) != len(qualificationArtifactPaths) {
		return false
	}
	for _, path := range qualificationArtifactPaths {
		if !bytes.Equal(before[path], after[path]) {
			return false
		}
	}
	return true
}

func modelRanExactShell(events []*mecatlv1.Event, command string) bool {
	matching := map[string]struct{}{}
	for _, ev := range events {
		if ev.ToolCall != nil && ev.ToolCall.Name == "Shell" {
			var args map[string]json.RawMessage
			if json.Unmarshal([]byte(ev.ToolCall.Args), &args) != nil {
				continue
			}
			var requested string
			if json.Unmarshal(args["command"], &requested) == nil && strings.TrimSpace(requested) == command {
				matching[ev.ToolCall.Id] = struct{}{}
			}
		}
		if ev.ToolResult != nil {
			if _, ok := matching[ev.ToolResult.CallId]; ok && !ev.ToolResult.IsError && strings.Contains(ev.ToolResult.Content, "[exit code: 0]") {
				return true
			}
		}
	}
	return false
}

type liveSummary struct {
	Cluster               string   `json:"cluster"`
	Provider              string   `json:"provider"`
	Model                 string   `json:"model"`
	InputTokens           int64    `json:"input_tokens"`
	OutputTokens          int64    `json:"output_tokens"`
	ToolNames             []string `json:"tool_names"`
	ArtifactCount         int      `json:"artifact_count"`
	RestartVerified       bool     `json:"restart_verified"`
	BeforeRestartExitCode int      `json:"before_restart_exit_code"`
	AfterRestartExitCode  int      `json:"after_restart_exit_code"`
	Timestamp             string   `json:"timestamp"`
}

func assertCredentialScope(t *testing.T, ctx context.Context, kubeconfig, secretName string) {
	t.Helper()
	var agent appsv1.Deployment
	if err := json.Unmarshal(runKubectl(t, ctx, kubeconfig, "get", "deployment/mecak8s", "-n", namespace, "-o", "json"), &agent); err != nil {
		t.Fatal("decode mecak8s credential-scope metadata failed")
	}
	assertRealProviderArgs(t, agent.Spec.Template.Spec)
	want := secretReference{Container: "agent", Source: "env", Name: secretName, Key: liveCredentialKey, EnvName: liveCredentialKey}
	refs := referencesNamed(secretReferences(agent.Spec.Template.Spec), secretName)
	if len(refs) != 1 || refs[0] != want {
		t.Fatal("run-scoped provider credential is not confined to the mecak8s agent environment")
	}

	var provider appsv1.Deployment
	if err := json.Unmarshal(runKubectl(t, ctx, kubeconfig, "get", "deployment/mecatl-execution", "-n", namespace, "-o", "json"), &provider); err != nil {
		t.Fatal("decode provider credential-scope metadata failed")
	}
	var executors corev1.PodList
	if err := json.Unmarshal(runKubectl(t, ctx, kubeconfig, "get", "pods", "-n", namespace, "-l", "execution.mecatl.dev/environment", "-o", "json"), &executors); err != nil {
		t.Fatal("decode executor credential-scope metadata failed")
	}
	if containsSecretReference(secretReferences(provider.Spec.Template.Spec), secretName) {
		t.Fatal("provider credential escaped into the execution provider")
	}
	for _, pod := range executors.Items {
		if containsSecretReference(secretReferences(pod.Spec), secretName) {
			t.Fatal("provider credential escaped into an executor pod")
		}
	}
}

func assertRealProviderArgs(t *testing.T, spec corev1.PodSpec) {
	t.Helper()
	if err := realProviderConfigError(spec); err != nil {
		t.Fatal(err)
	}
}

func realProviderConfigError(spec corev1.PodSpec) error {
	for _, volume := range spec.Volumes {
		if volume.Name == "execution-mock" || (volume.ConfigMap != nil && volume.ConfigMap.Name == "execution-mock") {
			return errors.New("live deployment retained the execution-mock volume")
		}
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.ConfigMap != nil && source.ConfigMap.Name == "execution-mock" {
					return errors.New("live deployment retained a projected execution-mock source")
				}
			}
		}
	}
	for _, container := range spec.Containers {
		for _, mount := range container.VolumeMounts {
			if mount.Name == "execution-mock" {
				return errors.New("live deployment retained the execution-mock mount")
			}
		}
		if container.Name != "agent" {
			continue
		}
		seen := map[string]bool{}
		for _, arg := range container.Args {
			seen[arg] = true
			if arg == "--mock" || strings.HasPrefix(arg, "--mock-script=") {
				return errors.New("live deployment retained mock-provider arguments")
			}
			if strings.Contains(arg, "base-url") {
				return errors.New("live deployment overrides the provider endpoint")
			}
		}
		for _, required := range []string{"--default-provider=openrouter", "--model=" + liveModel, "--max-run-tokens=32000"} {
			if !seen[required] {
				return fmt.Errorf("live deployment is missing required argument %s", required)
			}
		}
		return nil
	}
	return errors.New("live deployment has no agent container")
}

type secretReference struct {
	Container string
	Source    string
	Name      string
	Key       string
	EnvName   string
}

func secretReferences(spec corev1.PodSpec) []secretReference {
	var refs []secretReference
	collectContainer := func(container corev1.Container, kind string) {
		for _, env := range container.Env {
			if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
				refs = append(refs, secretReference{Container: kind + container.Name, Source: "env", Name: env.ValueFrom.SecretKeyRef.Name, Key: env.ValueFrom.SecretKeyRef.Key, EnvName: env.Name})
			}
		}
		for _, envFrom := range container.EnvFrom {
			if envFrom.SecretRef != nil {
				refs = append(refs, secretReference{Container: kind + container.Name, Source: "envFrom", Name: envFrom.SecretRef.Name})
			}
		}
	}
	for _, container := range spec.Containers {
		collectContainer(container, "")
	}
	for _, container := range spec.InitContainers {
		collectContainer(container, "init:")
	}
	for _, volume := range spec.Volumes {
		if volume.Secret != nil {
			refs = append(refs, secretReference{Source: "volume", Name: volume.Secret.SecretName})
		}
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.Secret != nil {
					refs = append(refs, secretReference{Source: "projected", Name: source.Secret.Name})
				}
			}
		}
	}
	for _, pullSecret := range spec.ImagePullSecrets {
		refs = append(refs, secretReference{Source: "imagePullSecret", Name: pullSecret.Name})
	}
	return refs
}

func referencesNamed(refs []secretReference, name string) []secretReference {
	matched := make([]secretReference, 0, 1)
	for _, ref := range refs {
		if ref.Name == name {
			matched = append(matched, ref)
		}
	}
	return matched
}

func containsSecretReference(refs []secretReference, name string) bool {
	return len(referencesNamed(refs, name)) != 0
}

type liveCreateResponse struct {
	SessionId     string                  `json:"session_id"`
	ResolvedModel *mecatlv1.ResolvedModel `json:"resolved_model"`
}

func createLiveSession(t *testing.T, ctx context.Context, addr, token string) liveCreateResponse {
	t.Helper()
	t.Log("live stage=http_create reason=begin")
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteHeaders:         func() { t.Log("live stage=http_create reason=headers_sent") },
		GotFirstResponseByte: func() { t.Log("live stage=http_create reason=response_started") },
	})
	status, body := request(t, ctx, http.MethodPost, "http://"+addr+"/v1/sessions", token, []byte(`{"mode":"default","limits":{"max_turns":8,"max_tool_calls":20,"max_consecutive_failures":3}}`))
	if status != http.StatusCreated {
		t.Fatalf("live session creation failed (HTTP %d)", status)
	}
	var out liveCreateResponse
	if json.Unmarshal(body, &out) != nil || out.SessionId == "" {
		t.Fatal("live session response invalid")
	}
	return out
}

func livePrompt(t *testing.T, ctx context.Context, addr, id, token, text string) []*mecatlv1.Event {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"text": text})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/v1/sessions/"+id+"/prompt", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal("live prompt transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2<<20))
		t.Fatalf("live prompt rejected (HTTP %d; body redacted)", resp.StatusCode)
	}
	var events []*mecatlv1.Event
	s := bufio.NewScanner(io.LimitReader(resp.Body, 4<<20))
	s.Buffer(make([]byte, 64<<10), 1<<20)
	for s.Scan() {
		line := s.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev mecatlv1.Event
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev) != nil {
			t.Fatal("invalid live SSE event")
		}
		events = append(events, &ev)
	}
	if s.Err() != nil {
		t.Fatal("live SSE stream failed")
	}
	return events
}
