//go:build kind_execution_e2e

package k8s_execution_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/internal/adapter/executionclient"
	"github.com/stacklok/mecatl/internal/executionenv"
)

const namespace = "execution-qualification"

func TestKindExecutionQualification(t *testing.T) {
	state := os.Getenv("MECATL_EXECUTION_QUAL_STATE")
	if state == "" || os.Getenv("MECATL_KUBE_CONTEXT") == "" {
		t.Fatal("MECATL_EXECUTION_QUAL_STATE and MECATL_KUBE_CONTEXT are required; run task e2e:k8s:execution")
	}
	kubeconfig := filepath.Join(state, "kubeconfig")
	pki := filepath.Join(state, "pki")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	baseline := resourceCount(t, ctx, kubeconfig, "executionenvironments.execution.mecatl.dev")
	providerForward := portForward(t, ctx, kubeconfig, "service/mecatl-execution", 8443)
	providerTLS := loadTLS(t, pki, "mecak8s", "mecatl-execution.execution-qualification.svc.cluster.local")
	providerClient, err := executionclient.New(providerForward.addr, providerTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer providerClient.Close()
	if _, err = providerClient.ValidateProfile(ctx, "go"); err != nil {
		t.Fatalf("validate profile: %v", err)
	}
	if got := resourceCount(t, ctx, kubeconfig, "executionenvironments.execution.mecatl.dev"); got != baseline {
		t.Fatalf("profile validation changed environment count from %d to %d", baseline, got)
	}

	owner := executionenv.Owner{Issuer: "https://oidc-issuer.execution-qualification.svc.cluster.local:8443", Subject: "alice"}
	binding := fmt.Sprintf("direct-binding-%d", time.Now().UnixNano())
	first, err := providerClient.Ensure(ctx, binding, "go", owner)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if first.Environment.ID == "" || first.Environment.Revision == "" {
		t.Fatal("ensure returned an empty exact environment reference")
	}
	ready := waitReady(t, ctx, providerClient, owner, binding, first.Environment)
	if ready.Environment != first.Environment {
		t.Fatalf("ready reference drifted: got %+v want %+v", ready.Environment, first.Environment)
	}
	second, err := providerClient.Ensure(ctx, binding, "go", owner)
	if err != nil {
		t.Fatalf("repeat ensure: %v", err)
	}
	if second.Environment != first.Environment {
		t.Fatalf("repeat ensure changed identity: got %+v want %+v", second.Environment, first.Environment)
	}
	if got := resourceCount(t, ctx, kubeconfig, "executionenvironments.execution.mecatl.dev"); got != baseline+1 {
		t.Fatalf("repeat ensure left %d environments, want baseline + 1 (%d)", got, baseline+1)
	}
	if got := resourceCount(t, ctx, kubeconfig, "pods -l execution.mecatl.dev/environment="+first.Environment.ID); got != 1 {
		t.Fatalf("executor pods = %d, want 1", got)
	}
	if got := resourceCount(t, ctx, kubeconfig, "pvc -l execution.mecatl.dev/environment="+first.Environment.ID); got != 1 {
		t.Fatalf("workspace PVCs = %d, want 1", got)
	}

	intruderTLS := loadTLS(t, pki, "intruder", "mecatl-execution.execution-qualification.svc.cluster.local")
	intruder, err := executionclient.New(providerForward.addr, intruderTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer intruder.Close()
	_, err = intruder.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: first.Environment, Owner: owner, BindingID: binding}, Purpose: executionenv.PurposeSession})
	if err == nil || (!isRemoteCode(err, executionenv.CodePermissionDenied) && !isRemoteCode(err, executionenv.CodeNotFound)) {
		t.Fatalf("different client attach error = %v, want an existence-hiding denial", err)
	}
	rc := executionenv.RequestContext{Environment: first.Environment, Owner: owner, BindingID: binding, Epoch: ready.Epoch, Grant: ready.Grant}
	for name, call := range map[string]func() error{
		"command status": func() error {
			_, err := intruder.CommandStatus(ctx, executionenv.CommandQueryRequest{Context: rc, CommandID: "not-owned"})
			return err
		},
		"command cancel": func() error {
			_, err := intruder.CancelCommand(ctx, executionenv.CommandQueryRequest{Context: rc, CommandID: "not-owned"})
			return err
		},
		"environment retire": func() error {
			return intruder.RetireEnvironment(ctx, executionenv.RetireEnvironmentRequest{Context: rc})
		},
	} {
		if err := call(); err == nil || (!isRemoteCode(err, executionenv.CodePermissionDenied) && !isRemoteCode(err, executionenv.CodeUnauthenticated)) {
			t.Fatalf("different client %s error = %v, want denial", name, err)
		}
	}
	providerForward.stop()
	if _, err := providerClient.ValidateProfile(ctx, "go"); err == nil {
		t.Fatal("provider endpoint loss unexpectedly fell back")
	}

	tokenForward := portForward(t, ctx, kubeconfig, "service/oidc-issuer", 8443)
	alice := fixtureToken(t, ctx, tokenForward.addr, pki, "alice")
	bob := fixtureToken(t, ctx, tokenForward.addr, pki, "bob")
	tokenForward.stop()
	agentForward := portForward(t, ctx, kubeconfig, "service/mecak8s", 8081)
	sessionID := createSession(t, ctx, agentForward.addr, alice)
	body := prompt(t, ctx, agentForward.addr, sessionID, alice, "run the scripted remote qualification")
	if err := os.WriteFile(filepath.Join(state, "mock-journey.sse"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"write-proof", "read-before-edit", "edit-proof", "copy-proof", "move-proof", "write-mod", "write-test", "shell-go-test", "remove-moved", "read-final", "moved.txt", "proof.txt:1:beta", "REMOTE_EXECUTION_QUALIFICATION_COMPLETE", "ok"} {
		if !bytes.Contains(body, []byte(marker)) {
			t.Fatalf("SSE result omitted %q (%s)", marker, mockSSEStatus(body))
		}
	}
	if bytes.Contains(body, []byte(`"is_error":true`)) {
		t.Fatal("scripted remote tool journey contained an error result")
	}
	if status := getSession(t, ctx, agentForward.addr, sessionID, bob); status != http.StatusNotFound {
		t.Fatalf("different OIDC owner read status = %d, want 404", status)
	}
	agentForward.stop()

	applyReattachScript(t, ctx, kubeconfig, state)
	runKubectl(t, ctx, kubeconfig, "rollout", "restart", "deployment/mecatl-execution", "-n", namespace)
	runKubectl(t, ctx, kubeconfig, "rollout", "status", "deployment/mecatl-execution", "-n", namespace, "--timeout=180s")
	runKubectl(t, ctx, kubeconfig, "rollout", "restart", "deployment/mecak8s", "-n", namespace)
	runKubectl(t, ctx, kubeconfig, "rollout", "status", "deployment/mecak8s", "-n", namespace, "--timeout=240s")
	agentForward = portForward(t, ctx, kubeconfig, "service/mecak8s", 8081)
	defer agentForward.stop()
	body = promptEventually(t, ctx, agentForward.addr, sessionID, alice, "verify exact reattachment after both deployments restarted")
	for _, marker := range []string{"reattach-read", "reattach-shell", "beta", "REMOTE_EXECUTION_REATTACH_COMPLETE", "ok"} {
		if !bytes.Contains(body, []byte(marker)) {
			t.Fatalf("reattach SSE omitted %q", marker)
		}
	}
	if bytes.Contains(body, []byte(`"is_error":true`)) {
		t.Fatal("reattached remote operation contained an error result")
	}
	if got := resourceCount(t, ctx, kubeconfig, "executionenvironments.execution.mecatl.dev"); got != baseline+2 {
		t.Fatalf("post-restart environments = %d, want baseline + direct + session (%d)", got, baseline+2)
	}
}

func mockSSEStatus(body []byte) string {
	var names []string
	errorClasses := map[string]int{}
	stop := "missing"
	for _, line := range bytes.Split(body, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("data: ")) {
			continue
		}
		var ev mecatlv1.Event
		if json.Unmarshal(bytes.TrimPrefix(line, []byte("data: ")), &ev) != nil {
			continue
		}
		if ev.ToolCall != nil {
			names = append(names, ev.ToolCall.Name)
		}
		if ev.ToolResult != nil && ev.ToolResult.IsError {
			errorClasses[classifyMockToolError(ev.ToolResult.Content)]++
		}
		if ev.Result != nil {
			stop = ev.Result.Stop
		}
	}
	return fmt.Sprintf("tools=%s error_classes=%v stop=%s", strings.Join(names, ","), errorClasses, stop)
}

func classifyMockToolError(content string) string {
	content = strings.ToLower(content)
	for _, class := range []string{"grant", "permission", "owner", "stale", "unavailable", "not found", "version", "placement", "environment", "connection", "timeout", "binding", "argument", "request", "profile", "reference", "invalid", "read before", "already exists"} {
		if strings.Contains(content, class) {
			return strings.ReplaceAll(class, " ", "_")
		}
	}
	return "other_redacted"
}

func waitReady(t *testing.T, ctx context.Context, c *executionclient.Client, owner executionenv.Owner, binding string, ref executionenv.EnvironmentRef) executionenv.AttachEnvironmentResponse {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		out, err := c.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: ref, Owner: owner, BindingID: binding}, Purpose: executionenv.PurposeSession})
		if err == nil && out.Ready {
			return out
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("execution environment did not become ready")
	return executionenv.AttachEnvironmentResponse{}
}
func isRemoteCode(err error, code executionenv.ErrorCode) bool {
	var remote *executionenv.Error
	return errors.As(err, &remote) && remote.Code == code
}

func loadTLS(t *testing.T, dir, name, serverName string) *tls.Config {
	t.Helper()
	cfg, err := executionclient.LoadTLSConfig(executionclient.TLSFiles{CA: filepath.Join(dir, "ca.crt"), Cert: filepath.Join(dir, name+".crt"), Key: filepath.Join(dir, name+".key")})
	if err != nil {
		t.Fatal(err)
	}
	cfg.ServerName = serverName
	return cfg
}

type forward struct {
	addr string
	cmd  *exec.Cmd
}

func (f *forward) stop() {
	if f == nil || f.cmd == nil || f.cmd.Process == nil {
		return
	}
	_ = f.cmd.Process.Signal(os.Interrupt)
	_, _ = f.cmd.Process.Wait()
}
func portForward(t *testing.T, ctx context.Context, kubeconfig, target string, remote int) *forward {
	t.Helper()
	local := freePort(t)
	cmd := command(ctx, kubeconfig, "port-forward", "-n", namespace, target, fmt.Sprintf("%d:%d", local, remote))
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f := &forward{addr: fmt.Sprintf("127.0.0.1:%d", local), cmd: cmd}
	t.Cleanup(f.stop)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", f.addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return f
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("port-forward %s did not start: %s", target, stderr.String())
	return nil
}
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func cleanEnv() []string {
	return []string{"HOME=" + os.Getenv("HOME"), "PATH=" + os.Getenv("PATH")}
}
func command(ctx context.Context, kubeconfig string, args ...string) *exec.Cmd {
	contextName := os.Getenv("MECATL_KUBE_CONTEXT")
	fullArgs := append([]string{"--kubeconfig", kubeconfig, "--context", contextName}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", fullArgs...)
	cmd.Env = cleanEnv()
	return cmd
}
func runKubectl(t *testing.T, ctx context.Context, kubeconfig string, args ...string) []byte {
	t.Helper()
	out, err := command(ctx, kubeconfig, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %s failed: %v: %s", args[0], err, out)
	}
	return out
}
func resourceCount(t *testing.T, ctx context.Context, kubeconfig, spec string) int {
	t.Helper()
	args := append([]string{"get"}, strings.Fields(spec)...)
	args = append(args, "-n", namespace, "-o", "name")
	out := runKubectl(t, ctx, kubeconfig, args...)
	if len(bytes.TrimSpace(out)) == 0 {
		return 0
	}
	return len(bytes.Split(bytes.TrimSpace(out), []byte{'\n'}))
}

func fixtureToken(t *testing.T, ctx context.Context, addr, pki, sub string) string {
	t.Helper()
	ca, err := os.ReadFile(filepath.Join(pki, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatal("fixture CA invalid")
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, ServerName: "oidc-issuer.execution-qualification.svc.cluster.local"}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+"/token?sub="+sub, nil)
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		IDToken string `json:"id_token"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out) != nil || out.IDToken == "" {
		t.Fatal("fixture token issuance failed")
	}
	return out.IDToken
}
func createSession(t *testing.T, ctx context.Context, addr, token string) string {
	t.Helper()
	status, body := request(t, ctx, http.MethodPost, "http://"+addr+"/v1/sessions", token, []byte(`{"mode":"default","limits":{"max_turns":16,"max_tool_calls":24,"max_consecutive_failures":3}}`))
	if status != http.StatusCreated {
		t.Fatalf("create session status=%d body=%s", status, body)
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(body, &out) != nil || out.SessionID == "" {
		t.Fatal("create session returned no id")
	}
	return out.SessionID
}
func prompt(t *testing.T, ctx context.Context, addr, id, token, text string) []byte {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"text": text})
	status, body := request(t, ctx, http.MethodPost, "http://"+addr+"/v1/sessions/"+id+"/prompt", token, raw)
	if status/100 != 2 {
		t.Fatalf("prompt status=%d body=%s", status, body)
	}
	return body
}
func promptEventually(t *testing.T, ctx context.Context, addr, id, token, text string) []byte {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"text": text})
	deadline := time.Now().Add(90 * time.Second)
	for {
		status, body := request(t, ctx, http.MethodPost, "http://"+addr+"/v1/sessions/"+id+"/prompt", token, raw)
		if status/100 == 2 {
			return body
		}
		if status != http.StatusConflict || time.Now().After(deadline) {
			t.Fatalf("reattach prompt status=%d body=%s", status, body)
		}
		time.Sleep(time.Second)
	}
}
func getSession(t *testing.T, ctx context.Context, addr, id, token string) int {
	t.Helper()
	status, _ := request(t, ctx, http.MethodGet, "http://"+addr+"/v1/sessions/"+id, token, nil)
	return status
}
func request(t *testing.T, ctx context.Context, method, url, token string, body []byte) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, raw
}
func applyReattachScript(t *testing.T, ctx context.Context, kubeconfig, state string) {
	t.Helper()
	root := filepath.Clean(filepath.Join(state, "..", "..", ".."))
	_ = root
	source := filepath.Join(repoRoot(t), "deploy", "mecatl-execution-kind", "mock-script-reattach.json")
	cmd := command(ctx, kubeconfig, "create", "configmap", "execution-mock", "-n", namespace, "--from-file=mock-script.json="+source, "--dry-run=client", "-o", "yaml")
	rendered, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	apply := command(ctx, kubeconfig, "apply", "-f", "-")
	apply.Stdin = bytes.NewReader(rendered)
	if out, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("update mock script: %v: %s", err, out)
	}
}
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}
