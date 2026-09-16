//go:build kind_execution_e2e

package k8s_execution_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	tokenForward := portForward(t, ctx, kubeconfig, "service/oidc-issuer", 8443)
	alice := fixtureToken(t, ctx, tokenForward.addr, filepath.Join(state, "pki"), "alice")
	tokenForward.stop()
	agentForward := portForward(t, ctx, kubeconfig, "service/mecak8s", 8081)
	defer agentForward.stop()

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
		if ev.ToolResult != nil {
			if ev.ToolResult.IsError {
				t.Fatal("live coding smoke had a tool error")
			}
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

	providerForward := portForward(t, ctx, kubeconfig, "service/mecatl-execution", 8443)
	defer providerForward.stop()
	client, err := executionclient.New(providerForward.addr, loadTLS(t, filepath.Join(state, "pki"), "mecak8s", "mecatl-execution.execution-qualification.svc.cluster.local"))
	if err != nil {
		t.Fatal("typed execution client setup failed")
	}
	defer client.Close()
	owner := executionenv.Owner{Issuer: "https://oidc-issuer.execution-qualification.svc.cluster.local:8443", Subject: "alice"}
	ensured, err := client.Ensure(ctx, created.SessionId, "go", owner)
	if err != nil {
		t.Fatal("typed execution reattachment lookup failed")
	}
	attached := waitReady(t, ctx, client, owner, created.SessionId, ensured.Environment)
	rc := executionenv.RequestContext{Environment: attached.Environment, Owner: owner, BindingID: created.SessionId, Epoch: attached.Epoch, Grant: attached.Grant}
	artifactCount := 0
	file, err := client.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileRead, Path: "arithmetic/sum.go"})
	if err != nil || !strings.Contains(string(file.Data), "return a + b") || !strings.Contains(string(file.Data), nonce) {
		t.Fatal("typed gRPC file verification failed")
	}
	artifactCount++
	testFile, err := client.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileRead, Path: "arithmetic/sum_test.go"})
	if err != nil || len(testFile.Data) == 0 {
		t.Fatal("typed gRPC test-file verification failed")
	}
	artifactCount++
	command, err := client.StartCommand(ctx, executionenv.CommandStartRequest{Context: rc, Command: "go test ./...", TimeoutMillis: 120000})
	if err != nil || command.State != executionenv.CommandSucceeded || command.Result.ExitCode != 0 {
		t.Fatal("typed gRPC independent go test failed")
	}

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
		Cluster:       cluster,
		Provider:      created.ResolvedModel.ProviderId,
		Model:         created.ResolvedModel.ModelId,
		InputTokens:   usage.InputTokens,
		OutputTokens:  usage.OutputTokens,
		ToolNames:     observedTools,
		ArtifactCount: artifactCount,
		ExitCodes:     []int{command.Result.ExitCode},
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
	}
	blob, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		t.Fatal("marshal sanitized summary failed")
	}
	if err := os.WriteFile(filepath.Join(state, "live-summary.json"), append(blob, '\n'), 0o600); err != nil {
		t.Fatal("write sanitized summary failed")
	}
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
	Cluster       string   `json:"cluster"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	InputTokens   int64    `json:"input_tokens"`
	OutputTokens  int64    `json:"output_tokens"`
	ToolNames     []string `json:"tool_names"`
	ArtifactCount int      `json:"artifact_count"`
	ExitCodes     []int    `json:"exit_codes"`
	Timestamp     string   `json:"timestamp"`
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
