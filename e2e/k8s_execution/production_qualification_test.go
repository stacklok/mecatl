//go:build kind_execution_e2e

package k8s_execution_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/stacklok/mecatl/internal/adapter/executionclient"
	"github.com/stacklok/mecatl/internal/executionenv"
)

func requireProduction(t *testing.T) (string, string, context.Context, context.CancelFunc) {
	t.Helper()
	if os.Getenv("MECATL_EXECUTION_QUAL_PROFILE") != "production" {
		t.Skip("production qualification profile only")
	}
	state := os.Getenv("MECATL_EXECUTION_QUAL_STATE")
	if state == "" {
		t.Fatal("MECATL_EXECUTION_QUAL_STATE is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	return state, filepath.Join(state, "kubeconfig"), ctx, cancel
}

func productionClient(t *testing.T, ctx context.Context, state, kubeconfig string) (*executionclient.Client, *forward) {
	t.Helper()
	f := portForward(t, ctx, kubeconfig, "service/mecatl-execution", 8443)
	client, err := executionclient.New(f.addr, loadTLS(t, filepath.Join(state, "pki"), "mecak8s", "mecatl-execution.execution-qualification.svc.cluster.local"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client, f
}

func productionReplicaClients(t *testing.T, ctx context.Context, state, kubeconfig string) ([2]*executionclient.Client, [2]string) {
	t.Helper()
	var pods corev1.PodList
	raw := runKubectl(t, ctx, kubeconfig, "get", "pods", "-n", namespace, "-l", "app.kubernetes.io/name=mecatl-execution", "-o", "json")
	if err := json.Unmarshal(raw, &pods); err != nil || len(pods.Items) != 2 {
		t.Fatalf("decode two provider replicas: pods=%d err=%v", len(pods.Items), err)
	}
	if pods.Items[0].UID == "" || pods.Items[1].UID == "" || pods.Items[0].UID == pods.Items[1].UID {
		t.Fatal("provider replica Pod UIDs are missing or identical")
	}
	var clients [2]*executionclient.Client
	var podUIDs [2]string
	for i := range clients {
		forward := portForward(t, ctx, kubeconfig, "pod/"+pods.Items[i].Name, 8443)
		client, err := executionclient.New(forward.addr, loadTLS(t, filepath.Join(state, "pki"), "mecak8s", "mecatl-execution.execution-qualification.svc.cluster.local"))
		if err != nil {
			t.Fatal(err)
		}
		clients[i] = client
		podUIDs[i] = string(pods.Items[i].UID)
		t.Cleanup(func() { client.Close() })
	}
	return clients, podUIDs
}

func createProductionEnvironment(t *testing.T, ctx context.Context, c *executionclient.Client, suffix string) (executionenv.Owner, string, executionenv.AttachEnvironmentResponse) {
	t.Helper()
	owner := executionenv.Owner{Issuer: "https://oidc-issuer.execution-qualification.svc.cluster.local:8443", Subject: "production-" + suffix}
	binding := fmt.Sprintf("production-%s-%d", suffix, time.Now().UnixNano())
	op := "ensure-" + binding
	ensured, err := c.Ensure(ctx, binding, "go", owner, op)
	if err != nil {
		t.Fatalf("ensure production environment: %v", err)
	}
	if err := c.CommitReference(ctx, executionenv.ReferenceRequest{Environment: ensured.Environment, Owner: owner, BindingID: binding, OperationID: op}); err != nil {
		t.Fatalf("commit production reference: %v", err)
	}
	return owner, binding, waitReady(t, ctx, c, owner, binding, ensured.Environment)
}

func TestKindExecutionProductionNetworkPolicyEnforced(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	client, _ := productionClient(t, ctx, state, kubeconfig)
	owner, binding, attached := createProductionEnvironment(t, ctx, client, "network")
	rc, release := acquireRun(t, ctx, client, owner, binding, attached, fmt.Sprintf("network-%d", time.Now().UnixNano()))
	defer release()

	assertProductionExecutorPod(t, ctx, kubeconfig, attached.Environment.ID)
	probeSource := []byte("package main\nimport (\"net\";\"os\";\"time\")\nfunc main(){c,e:=net.DialTimeout(\"tcp\",os.Args[1],2*time.Second);if e!=nil{os.Exit(42)};c.Close()}\n")
	if _, err := client.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileCreate, Path: "network_probe.go", Data: probeSource}); err != nil {
		t.Fatalf("install network probe in owned workspace: %v", err)
	}
	built, err := client.StartCommand(ctx, executionenv.CommandStartRequest{Context: rc, Command: "go build -o network-probe network_probe.go", TimeoutMillis: 30000})
	if err != nil || built.State != executionenv.CommandSucceeded || built.Result.ExitCode != 0 {
		t.Fatalf("build network probe: state=%q exit=%d err=%v", built.State, built.Result.ExitCode, err)
	}
	fixtureIP := kubeValue(t, ctx, kubeconfig, "get", "pod/network-fixture", "-n", namespace, "-o", "jsonpath={.status.podIP}")
	if out := runOwnedProbe(t, ctx, client, rc, fixtureIP+":8080"); out.State != executionenv.CommandSucceeded || out.Result.ExitCode != 0 {
		t.Fatal("profile-specific package endpoint was not reachable")
	}
	providerIP := kubeValue(t, ctx, kubeconfig, "get", "service/mecatl-execution", "-n", namespace, "-o", "jsonpath={.spec.clusterIP}")
	peerIP := kubeValue(t, ctx, kubeconfig, "get", "pod/network-intruder", "-n", namespace, "-o", "jsonpath={.status.podIP}")
	for name, endpoint := range map[string]string{
		"provider": providerIP + ":8443", "api": "10.96.0.1:443", "peer": peerIP + ":8080",
		"dns": "10.96.0.10:53", "metadata": "169.254.169.254:80", "internet": "1.1.1.1:443",
	} {
		out := runOwnedProbe(t, ctx, client, rc, endpoint)
		if out.State != executionenv.CommandFailed || out.Result.State != executionenv.CommandFailed || out.Result.ExitCode != 42 {
			t.Fatalf("default-denied executor probe %s state=%q result_state=%q exit=%d, want failed/failed/42", name, out.State, out.Result.State, out.Result.ExitCode)
		}
	}

	out, err := command(ctx, kubeconfig, "exec", "-n", namespace, "pod/network-intruder", "--", "/ko-app/netprobe", "-target", providerIP+":8443", "-timeout", "2s").CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 42 {
		t.Fatalf("unrelated pod denial exit proof=%q err=%v, want 42", strings.TrimSpace(string(out)), err)
	}
	if got := resourceCount(t, ctx, kubeconfig, "pods -l app.kubernetes.io/name=mecatl-execution"); got != 2 {
		t.Fatalf("ready provider replicas=%d, want 2", got)
	}
}

func runOwnedProbe(t *testing.T, ctx context.Context, c *executionclient.Client, rc executionenv.RequestContext, endpoint string) executionenv.CommandStartResponse {
	t.Helper()
	out, err := c.StartCommand(ctx, executionenv.CommandStartRequest{Context: rc, Command: "./network-probe " + endpoint, TimeoutMillis: 15000})
	if err != nil {
		t.Fatalf("network probe RPC failed: %v", err)
	}
	return out
}

func assertProductionExecutorPod(t *testing.T, ctx context.Context, kubeconfig, environmentID string) {
	t.Helper()
	var pods corev1.PodList
	raw := runKubectl(t, ctx, kubeconfig, "get", "pods", "-n", namespace, "-l", "execution.mecatl.dev/environment="+environmentID, "-o", "json")
	if err := json.Unmarshal(raw, &pods); err != nil || len(pods.Items) != 1 {
		t.Fatal("decode exact executor pod")
	}
	pod := pods.Items[0]
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName != "qualification-runc" {
		t.Fatal("executor does not use the qualified RuntimeClass")
	}
	if pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
		t.Fatal("executor received a service-account token")
	}
	if len(pod.Spec.Containers) != 1 {
		t.Fatal("executor container shape drifted")
	}
	c := pod.Spec.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation || c.SecurityContext.Capabilities == nil || len(c.SecurityContext.Capabilities.Drop) != 1 || c.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Fatal("executor capability confinement drifted")
	}
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory, corev1.ResourceEphemeralStorage} {
		request, requestOK := c.Resources.Requests[name]
		limit, limitOK := c.Resources.Limits[name]
		if !requestOK || !limitOK || request.IsZero() || limit.IsZero() {
			t.Fatalf("executor resource %s is unbounded", name)
		}
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == "tmp" && volume.EmptyDir != nil && volume.EmptyDir.SizeLimit != nil && !volume.EmptyDir.SizeLimit.IsZero() {
			return
		}
	}
	t.Fatal("executor /tmp is unbounded")
}

func TestKindExecutionProductionSecurityRotation(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	oldClient, forward := productionClient(t, ctx, state, kubeconfig)
	owner, binding, attached := createProductionEnvironment(t, ctx, oldClient, "rotation")
	oldClaim, err := oldClient.AcquireRun(ctx, executionenv.RunClaimRequest{Environment: attached.Environment, Owner: owner, BindingID: binding, RunID: "rotation-old-grant", OperationID: "acquire-rotation-old-grant", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	oldRun := executionenv.RequestContext{Environment: oldClaim.Environment, Owner: owner, BindingID: binding, RunID: oldClaim.RunID, ClaimID: oldClaim.ClaimID, Epoch: oldClaim.Epoch, GrantGeneration: oldClaim.GrantGeneration, Grant: oldClaim.Grant}
	if _, err := oldClient.File(ctx, executionenv.FileRequest{Context: oldRun, Operation: executionenv.OpFileCreate, Path: "rotation-sentinel.txt", Data: []byte("old-authority\n")}); err != nil {
		t.Fatal(err)
	}

	rotationDir := filepath.Join(state, "rotation")
	generator := exec.CommandContext(ctx, "go", "run", "-tags", "kind_execution_e2e", "./e2e/k8s_execution/fixture/rotation", filepath.Join(state, "pki"), rotationDir)
	generator.Dir = repoRoot(t)
	generator.Env = cleanEnv()
	if out, err := generator.CombinedOutput(); err != nil {
		t.Fatalf("generate synthetic rotation material: %v: %s", err, out)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		applySecurityCandidate(t, cleanupCtx, kubeconfig, rotationDir, filepath.Join(rotationDir, "restore-fixture-clients.json"), "bridge")
		waitProviderReadyReplicas(t, cleanupCtx, kubeconfig, 2)
	})

	// A changed policy at the already-authoritative generation is rejected by
	// both replicas. The old snapshot cannot continue authorizing RPCs while the
	// mounted candidate disagrees with the durable authority ledger.
	applySecurityCandidate(t, ctx, kubeconfig, filepath.Join(state, "pki"), filepath.Join(rotationDir, "invalid-same-generation.json"), "initial")
	waitProviderReadyReplicas(t, ctx, kubeconfig, 0)
	if _, err := oldClient.File(ctx, executionenv.FileRequest{Context: oldRun, Operation: executionenv.OpFileRead, Path: "rotation-sentinel.txt"}); err == nil {
		t.Fatal("same-generation changed policy continued authorizing an existing gRPC connection")
	}
	applySecurityCandidate(t, ctx, kubeconfig, filepath.Join(state, "pki"), filepath.Join(state, "pki", "manifest.json"), "initial")
	waitProviderReadyReplicas(t, ctx, kubeconfig, 2)
	waitFileContent(t, ctx, oldClient, oldRun, "rotation-sentinel.txt", "old-authority\n", "valid authority recovery")

	// Generation 2 bridges client trust before changing the server certificate.
	// The old connection remains usable, while a new-CA client can establish its
	// own connection and receives grants from k2.
	applySecurityCandidate(t, ctx, kubeconfig, rotationDir, filepath.Join(rotationDir, "bridge.json"), "bridge")
	waitProviderReadyReplicas(t, ctx, kubeconfig, 2)
	newTLS, err := executionclient.LoadTLSConfig(executionclient.TLSFiles{CA: filepath.Join(rotationDir, "client-roots.pem"), Cert: filepath.Join(rotationDir, "mecak8s-new.crt"), Key: filepath.Join(rotationDir, "mecak8s-new.key")})
	if err != nil {
		t.Fatal(err)
	}
	newTLS.ServerName = "mecatl-execution.execution-qualification.svc.cluster.local"
	newClient, err := executionclient.New(forward.addr, newTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer newClient.Close()
	newAttached := waitReady(t, ctx, newClient, owner, binding, attached.Environment)
	renewed, err := newClient.RenewRun(ctx, executionenv.RunClaimRequest{Environment: oldRun.Environment, Owner: owner, BindingID: binding, RunID: oldRun.RunID, ClaimID: oldRun.ClaimID, Epoch: oldRun.Epoch, GrantGeneration: oldRun.GrantGeneration, OperationID: "rotation-renew-k2", TTL: time.Minute})
	if err != nil {
		t.Fatalf("renew old claim under bridge authority: %v", err)
	}
	newRun := executionenv.RequestContext{Environment: renewed.Environment, Owner: owner, BindingID: binding, RunID: renewed.RunID, ClaimID: renewed.ClaimID, Epoch: renewed.Epoch, GrantGeneration: renewed.GrantGeneration, Grant: renewed.Grant}

	// Generation 3 switches server TLS, removes the old client CA, and revokes
	// k1. Authorization is checked on every RPC, so the established old-client
	// HTTP/2 connection is denied rather than passing until reconnect.
	applySecurityCandidate(t, ctx, kubeconfig, rotationDir, filepath.Join(rotationDir, "final.json"), "final")
	waitProviderReadyReplicas(t, ctx, kubeconfig, 2)
	if _, err := oldClient.File(ctx, executionenv.FileRequest{Context: oldRun, Operation: executionenv.OpFileRead, Path: "rotation-sentinel.txt"}); err == nil {
		t.Fatal("established client signed by the removed CA remained authorized")
	}
	if _, err := newClient.File(ctx, executionenv.FileRequest{Context: oldRun, Operation: executionenv.OpFileRead, Path: "rotation-sentinel.txt"}); err == nil {
		t.Fatal("grant signed by revoked k1 remained authorized through the new client")
	}
	if got, err := newClient.File(ctx, executionenv.FileRequest{Context: newRun, Operation: executionenv.OpFileRead, Path: "rotation-sentinel.txt"}); err != nil || string(got.Data) != "old-authority\n" {
		t.Fatalf("new authority did not authorize preserved data: %v", err)
	}
	freshClient, err := executionclient.New(forward.addr, newTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer freshClient.Close()
	if _, err := freshClient.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: attached.Environment, Owner: owner, BindingID: binding}, Purpose: executionenv.PurposeSession}); err != nil {
		t.Fatalf("fresh new-CA client failed after TLS switch: %v", err)
	}

	if err := newClient.ReleaseRun(ctx, executionenv.RunClaimRequest{Environment: renewed.Environment, Owner: owner, BindingID: binding, RunID: renewed.RunID, ClaimID: renewed.ClaimID, Epoch: renewed.Epoch, GrantGeneration: renewed.GrantGeneration, OperationID: "rotation-release-k2"}); err != nil {
		t.Fatal(err)
	}
	generation, err := newClient.RevokeEnvironment(ctx, attached.Environment, owner, newAttached.GrantGeneration, "rotation-revoke-replay")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := freshClient.RevokeEnvironment(ctx, attached.Environment, owner, newAttached.GrantGeneration, "rotation-revoke-replay")
	if err != nil || replayed != generation {
		t.Fatalf("durable revoke receipt replay generation=%d want=%d err=%v", replayed, generation, err)
	}
	runKubectl(t, ctx, kubeconfig, "rollout", "restart", "deployment/mecatl-execution", "-n", namespace)
	runKubectl(t, ctx, kubeconfig, "rollout", "status", "deployment/mecatl-execution", "-n", namespace, "--timeout=240s")
	postRestartForward := portForward(t, ctx, kubeconfig, "service/mecatl-execution", 8443)
	postRestartClient, err := executionclient.New(postRestartForward.addr, newTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer postRestartClient.Close()
	if _, err := postRestartClient.File(ctx, executionenv.FileRequest{Context: newRun, Operation: executionenv.OpFileRead, Path: "rotation-sentinel.txt"}); err == nil {
		t.Fatal("revoked generation became usable after provider restart")
	}
	// Restore fixture client compatibility through a higher generation; this is
	// another forward rotation, never a high-water-mark rollback.
	applySecurityCandidate(t, ctx, kubeconfig, rotationDir, filepath.Join(rotationDir, "restore-fixture-clients.json"), "bridge")
	waitProviderReadyReplicas(t, ctx, kubeconfig, 2)
}

func waitFileContent(t *testing.T, ctx context.Context, client *executionclient.Client, rc executionenv.RequestContext, path, want, proof string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		got, err := client.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileRead, Path: path})
		if err == nil && string(got.Data) == want {
			return
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("%s did not authorize preserved data: %v", proof, lastErr)
}

func applySecurityCandidate(t *testing.T, ctx context.Context, kubeconfig, materialDir, manifest, mode string) {
	t.Helper()
	pki := filepath.Join(os.Getenv("MECATL_EXECUTION_QUAL_STATE"), "pki")
	secretArgs := []string{"create", "secret", "generic", "execution-security", "-n", namespace, "--from-file=grant-k1.pem=" + filepath.Join(pki, "grant-key.pem")}
	switch mode {
	case "initial":
		secretArgs = append(secretArgs, "--from-file=tls.crt="+filepath.Join(pki, "provider.crt"), "--from-file=tls.key="+filepath.Join(pki, "provider.key"), "--from-file=clients.pem="+filepath.Join(pki, "ca.crt"))
	case "bridge":
		secretArgs = append(secretArgs, "--from-file=grant-k2.pem="+filepath.Join(materialDir, "grant-k2.pem"), "--from-file=tls.crt="+filepath.Join(pki, "provider.crt"), "--from-file=tls.key="+filepath.Join(pki, "provider.key"), "--from-file=clients.pem="+filepath.Join(materialDir, "bridge-clients.pem"))
	case "final":
		secretArgs = append(secretArgs, "--from-file=grant-k2.pem="+filepath.Join(materialDir, "grant-k2.pem"), "--from-file=tls.crt="+filepath.Join(materialDir, "provider-new.crt"), "--from-file=tls.key="+filepath.Join(materialDir, "provider-new.key"), "--from-file=clients.pem="+filepath.Join(materialDir, "final-clients.pem"))
	default:
		t.Fatalf("unknown security candidate mode %q", mode)
	}
	secretArgs = append(secretArgs, "--dry-run=client", "-o", "yaml")
	applyKubectlInput(t, ctx, kubeconfig, runKubectl(t, ctx, kubeconfig, secretArgs...))
	config := runKubectl(t, ctx, kubeconfig, "create", "configmap", "mecatl-execution-security-manifest", "-n", namespace, "--from-file=manifest.json="+manifest, "--dry-run=client", "-o", "yaml")
	applyKubectlInput(t, ctx, kubeconfig, config)
}

func applyKubectlInput(t *testing.T, ctx context.Context, kubeconfig string, input []byte) {
	t.Helper()
	cmd := command(ctx, kubeconfig, "apply", "-f", "-")
	cmd.Stdin = bytes.NewReader(input)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl apply synthetic fixture: %v: %s", err, out)
	}
}

func waitProviderReadyReplicas(t *testing.T, ctx context.Context, kubeconfig string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var pods corev1.PodList
		raw := runKubectl(t, ctx, kubeconfig, "get", "pods", "-n", namespace, "-l", "app.kubernetes.io/name=mecatl-execution", "-o", "json")
		if json.Unmarshal(raw, &pods) == nil {
			ready := 0
			for _, pod := range pods.Items {
				for _, condition := range pod.Status.Conditions {
					if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
						ready++
					}
				}
			}
			if ready == want {
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("provider Ready replicas did not converge to %d", want)
}

func TestKindExecutionProductionReplicaLifecycle(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	replicas, replicaUIDs := productionReplicaClients(t, ctx, state, kubeconfig)
	client := replicas[0]
	owner, binding, attached := createProductionEnvironment(t, ctx, client, "lifecycle")
	first := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID)
	if first.SpecSchema != 2 || first.StatusSchema != 2 || first.PodUID == "" || first.PVCUID == "" {
		t.Fatal("schema-v2 exact runtime identities were not persisted")
	}

	type acquireResult struct {
		claim executionenv.RunClaim
		err   error
	}
	startAcquire := make(chan struct{})
	acquired := make(chan acquireResult, 2)
	for i := range replicas {
		i := i
		go func() {
			<-startAcquire
			claim, err := replicas[i].AcquireRun(ctx, executionenv.RunClaimRequest{Environment: attached.Environment, Owner: owner, BindingID: binding, RunID: fmt.Sprintf("run-%d", i), OperationID: fmt.Sprintf("acquire-%d", i), TTL: time.Minute})
			acquired <- acquireResult{claim: claim, err: err}
		}()
	}
	close(startAcquire)
	var claim1 executionenv.RunClaim
	conflicts := 0
	for range replicas {
		result := <-acquired
		if result.err == nil {
			claim1 = result.claim
		} else if isRemoteCode(result.err, executionenv.CodeConflict) {
			conflicts++
		} else {
			t.Fatalf("cross-replica run acquisition returned unexpected error: %v", result.err)
		}
	}
	if claim1.ClaimID == "" || conflicts != 1 {
		t.Fatalf("cross-replica run acquisition pod_uids=%v winner=%t conflicts=%d, want one each", replicaUIDs, claim1.ClaimID != "", conflicts)
	}
	statusWithClaim := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID)
	if statusWithClaim.ActiveGrantGeneration != claim1.GrantGeneration {
		t.Fatal("CRD pruned activeRun.grantGeneration")
	}
	if _, err := client.RenewRun(ctx, executionenv.RunClaimRequest{Environment: attached.Environment, Owner: owner, BindingID: binding, RunID: claim1.RunID, ClaimID: "stale", Epoch: claim1.Epoch, GrantGeneration: claim1.GrantGeneration, OperationID: "renew-stale", TTL: time.Minute}); err == nil {
		t.Fatal("stale renewal unexpectedly succeeded")
	}
	rc := executionenv.RequestContext{Environment: claim1.Environment, Owner: owner, BindingID: binding, RunID: claim1.RunID, ClaimID: claim1.ClaimID, Epoch: claim1.Epoch, GrantGeneration: claim1.GrantGeneration, Grant: claim1.Grant}
	if _, err := client.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileCreate, Path: "lifecycle-proof.txt", Data: []byte("preserved\n")}); err != nil {
		t.Fatal(err)
	}
	if err := client.ReleaseRun(ctx, executionenv.RunClaimRequest{Environment: claim1.Environment, Owner: owner, BindingID: binding, RunID: claim1.RunID, ClaimID: claim1.ClaimID, Epoch: claim1.Epoch, GrantGeneration: claim1.GrantGeneration, OperationID: "release-a"}); err != nil {
		t.Fatal(err)
	}

	wrong := executionenv.RetireEnvironmentRequest{Environment: attached.Environment, Owner: owner, ExpectedEpoch: first.Epoch, ExpectedPodUID: "wrong-pod-uid", ExpectedPVCUID: first.PVCUID, OperationID: "replace-wrong"}
	if err := client.ReplaceExecutor(ctx, wrong); err == nil {
		t.Fatal("wrong Pod UID replacement unexpectedly succeeded")
	}
	if got := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID).PodUID; got != first.PodUID {
		t.Fatal("wrong-UID replacement changed executor")
	}
	exact := wrong
	exact.ExpectedPodUID = first.PodUID
	exact.OperationID = "replace-exact"
	if err := client.ReplaceExecutor(ctx, exact); err != nil {
		t.Fatalf("replace exact executor: %v", err)
	}
	second := waitExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID, func(s executionStatus) bool { return s.Ready && s.PodUID != "" && s.PodUID != first.PodUID })
	if second.PVCUID != first.PVCUID || second.Epoch <= first.Epoch {
		t.Fatal("replacement did not preserve exact PVC and advance epoch")
	}
	reattached := waitReady(t, ctx, client, owner, binding, attached.Environment)
	rc2, release2 := acquireRun(t, ctx, client, owner, binding, reattached, "verify-replacement")
	proof, err := client.File(ctx, executionenv.FileRequest{Context: rc2, Operation: executionenv.OpFileRead, Path: "lifecycle-proof.txt"})
	if err != nil || string(proof.Data) != "preserved\n" {
		t.Fatal("replacement lost PVC data")
	}
	release2()

	revokeResults := make(chan struct {
		generation uint64
		err        error
	}, 2)
	startRevoke := make(chan struct{})
	for i := range replicas {
		i := i
		go func() {
			<-startRevoke
			generation, err := replicas[i].RevokeEnvironment(ctx, attached.Environment, owner, reattached.GrantGeneration, "revoke-lifecycle")
			revokeResults <- struct {
				generation uint64
				err        error
			}{generation: generation, err: err}
		}()
	}
	close(startRevoke)
	var newGeneration uint64
	for range replicas {
		result := <-revokeResults
		if result.err != nil || result.generation <= reattached.GrantGeneration {
			t.Fatalf("cross-replica revoke failed: generation=%d err=%v", result.generation, result.err)
		}
		if newGeneration != 0 && result.generation != newGeneration {
			t.Fatalf("cross-replica revoke replay diverged: got=%d want=%d", result.generation, newGeneration)
		}
		newGeneration = result.generation
	}
	if _, err := client.File(ctx, executionenv.FileRequest{Context: rc2, Operation: executionenv.OpFileRead, Path: "lifecycle-proof.txt"}); err == nil {
		t.Fatal("revoked grant remained usable")
	}
	runKubectl(t, ctx, kubeconfig, "rollout", "restart", "deployment/mecatl-execution", "-n", namespace)
	runKubectl(t, ctx, kubeconfig, "rollout", "status", "deployment/mecatl-execution", "-n", namespace, "--timeout=240s")
	if refreshed := waitReady(t, ctx, client, owner, binding, attached.Environment); refreshed.GrantGeneration != newGeneration {
		t.Fatal("revocation generation was not durable across provider restart")
	}

	refReq := executionenv.ReferenceRequest{Environment: attached.Environment, Owner: owner, BindingID: binding, OperationID: "delete-ref-lifecycle"}
	if err := client.PrepareReferenceDelete(ctx, refReq); err != nil {
		t.Fatal(err)
	}
	if err := client.ConfirmReferenceDelete(ctx, refReq); err != nil {
		t.Fatal(err)
	}
	retireStatus := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID)
	retire := executionenv.RetireEnvironmentRequest{Environment: attached.Environment, Owner: owner, ExpectedEpoch: retireStatus.Epoch, ExpectedPodUID: retireStatus.PodUID, ExpectedPVCUID: retireStatus.PVCUID, OperationID: "retire-lifecycle"}
	if err := client.RetireEnvironment(ctx, retire); err != nil {
		t.Fatalf("retire exact environment: %v", err)
	}
	retired := waitExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID, func(s executionStatus) bool { return s.Retired && s.ExecutorTerminated })
	if resourceCount(t, ctx, kubeconfig, "pvc -l execution.mecatl.dev/environment="+attached.Environment.ID) != 1 || resourceCount(t, ctx, kubeconfig, "pods -l execution.mecatl.dev/environment="+attached.Environment.ID) != 0 {
		t.Fatal("retirement did not stop executor while retaining PVC")
	}
	if err := client.DeleteRetiredEnvironment(ctx, attached.Environment, owner, retired.PVCUID, "delete-retired-lifecycle"); err != nil {
		t.Fatalf("delete retired environment: %v", err)
	}
	waitResourceAbsent(t, ctx, kubeconfig, "executionenvironment", attached.Environment.ID)
	if resourceCount(t, ctx, kubeconfig, "pvc -l execution.mecatl.dev/environment="+attached.Environment.ID) != 0 {
		t.Fatal("explicit retired deletion retained its owned synthetic PVC")
	}
}

type executionStatus struct {
	SpecSchema, StatusSchema           int
	Epoch                              uint64
	PodUID, PVCUID                     string
	ActiveOperationID, FenceState      string
	ActiveGrantGeneration              uint64
	Ready, Retired, ExecutorTerminated bool
	ReadyReason                        string
}

func readExecutionStatus(t *testing.T, ctx context.Context, kubeconfig, name string) executionStatus {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(runKubectl(t, ctx, kubeconfig, "get", "executionenvironment", name, "-n", namespace, "-o", "json"), &object); err != nil {
		t.Fatal(err)
	}
	nested := func(path ...string) any {
		var v any = object
		for _, p := range path {
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[p]
		}
		return v
	}
	number := func(path ...string) uint64 {
		if v, ok := nested(path...).(float64); ok {
			return uint64(v)
		}
		return 0
	}
	text := func(path ...string) string { v, _ := nested(path...).(string); return v }
	condition := func(kind string) bool {
		items, _ := nested("status", "conditions").([]any)
		for _, item := range items {
			m, _ := item.(map[string]any)
			if m["type"] == kind && m["status"] == "True" {
				return true
			}
		}
		return false
	}
	conditionReason := func(kind string) string {
		items, _ := nested("status", "conditions").([]any)
		for _, item := range items {
			m, _ := item.(map[string]any)
			if m["type"] == kind {
				reason, _ := m["reason"].(string)
				return reason
			}
		}
		return ""
	}
	return executionStatus{SpecSchema: int(number("spec", "schemaVersion")), StatusSchema: int(number("status", "schemaVersion")), Epoch: number("status", "epoch"), PodUID: text("status", "pod", "uid"), PVCUID: text("status", "pvc", "uid"), ActiveOperationID: text("status", "activeOperation", "id"), FenceState: text("status", "fenceState"), ActiveGrantGeneration: number("status", "activeRun", "grantGeneration"), Ready: condition("Ready"), Retired: condition("Retired"), ExecutorTerminated: condition("ExecutorTerminated"), ReadyReason: conditionReason("Ready")}
}

func waitExecutionStatus(t *testing.T, ctx context.Context, kubeconfig, name string, accept func(executionStatus) bool) executionStatus {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		s := readExecutionStatus(t, ctx, kubeconfig, name)
		if accept(s) {
			return s
		}
		time.Sleep(time.Second)
	}
	t.Fatal("execution status did not reach required lifecycle state")
	return executionStatus{}
}

func waitResourceAbsent(t *testing.T, ctx context.Context, kubeconfig, resource, name string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		cmd := command(ctx, kubeconfig, "get", resource, name, "-n", namespace, "-o", "name")
		if err := cmd.Run(); err != nil {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("owned synthetic resource was not deleted")
}

func kubeValue(t *testing.T, ctx context.Context, kubeconfig string, args ...string) string {
	t.Helper()
	return strings.TrimSpace(string(runKubectl(t, ctx, kubeconfig, args...)))
}

func TestKindExecutionProductionHolderLossFencesActiveOperation(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	replicas, replicaUIDs := productionReplicaClients(t, ctx, state, kubeconfig)
	holderClient, survivingClient := replicas[0], replicas[1]
	owner, binding, attached := createProductionEnvironment(t, ctx, holderClient, "holder-loss")
	rc, _ := acquireRun(t, ctx, holderClient, owner, binding, attached, "holder-loss-run")
	initial := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID)

	var providers corev1.PodList
	if err := json.Unmarshal(runKubectl(t, ctx, kubeconfig, "get", "pods", "-n", namespace, "-l", "app.kubernetes.io/name=mecatl-execution", "-o", "json"), &providers); err != nil || len(providers.Items) != 2 {
		t.Fatal("holder-loss proof requires two provider replicas")
	}
	holderPod := ""
	for i := range providers.Items {
		if string(providers.Items[i].UID) == replicaUIDs[0] {
			holderPod = providers.Items[i].Name
		}
	}
	if holderPod == "" {
		t.Fatalf("holder replica UID %s no longer identifies a provider Pod", replicaUIDs[0])
	}

	commandDone := make(chan error, 1)
	go func() {
		_, commandErr := holderClient.StartCommand(ctx, executionenv.CommandStartRequest{Context: rc, Command: `nohup sh -c 'sleep 45; kill 1' >/dev/null 2>&1 & i=0; trap '' HUP TERM; while [ $i -lt 90 ]; do i=$((i+1)); printf '%s\n' "$i" > holder-loss-nonce; sleep 1; done`, TimeoutMillis: 110000})
		commandDone <- commandErr
	}()
	waitExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID, func(s executionStatus) bool { return s.ActiveOperationID != "" })
	runKubectl(t, ctx, kubeconfig, "delete", "pod/"+holderPod, "-n", namespace, "--wait=true", "--timeout=90s")
	runKubectl(t, ctx, kubeconfig, "rollout", "status", "deployment/mecatl-execution", "-n", namespace, "--timeout=180s")

	if _, err := survivingClient.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileCreate, Path: "second-writer", Data: []byte("must-not-run")}); err == nil {
		t.Fatal("surviving replica admitted an overlapping writer after holder loss")
	}
	if _, err := survivingClient.AcquireRun(ctx, executionenv.RunClaimRequest{Environment: attached.Environment, Owner: owner, BindingID: binding, RunID: "overlap-run", OperationID: "overlap-run-acquire", TTL: time.Minute}); err == nil {
		t.Fatal("surviving replica admitted a new run while old operation ownership was unresolved")
	}
	fenced := waitExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID, func(s executionStatus) bool {
		return s.FenceState == "FenceUnknown" && s.ActiveOperationID != "" && !s.Ready
	})
	if fenced.ReadyReason != "FenceUnknown" {
		t.Fatalf("holder loss reason=%q, want bounded FenceUnknown", fenced.ReadyReason)
	}
	select {
	case <-commandDone:
	case <-time.After(5 * time.Second):
		// A disconnected exec stream is permitted to remain locally blocked; the
		// durable operation identity above, not RPC completion, is the proof.
	}

	waitExecutorTerminal(t, ctx, kubeconfig, attached.Environment.ID)
	recovery := executionenv.RetireEnvironmentRequest{Environment: attached.Environment, Owner: owner, ExpectedEpoch: initial.Epoch, ExpectedPodUID: initial.PodUID, ExpectedPVCUID: initial.PVCUID, OperationID: "recover-holder-loss"}
	if err := survivingClient.RecoverEnvironment(ctx, recovery); err != nil {
		t.Fatalf("recover exact terminal executor: %v", err)
	}
	if err := survivingClient.ReplaceExecutor(ctx, executionenv.RetireEnvironmentRequest{Environment: attached.Environment, Owner: owner, ExpectedEpoch: initial.Epoch, ExpectedPodUID: initial.PodUID, ExpectedPVCUID: initial.PVCUID, OperationID: "replace-holder-loss"}); err != nil {
		t.Fatalf("replace recovered executor: %v", err)
	}
	replaced := waitExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID, func(s executionStatus) bool { return s.Ready && s.PodUID != "" && s.PodUID != initial.PodUID })
	if replaced.PVCUID != initial.PVCUID {
		t.Fatal("holder-loss recovery replaced the retained workspace")
	}
	reattached := waitReady(t, ctx, survivingClient, owner, binding, attached.Environment)
	verify, release := acquireRun(t, ctx, survivingClient, owner, binding, reattached, "holder-loss-verify")
	if _, err := survivingClient.File(ctx, executionenv.FileRequest{Context: verify, Operation: executionenv.OpFileRead, Path: "holder-loss-nonce"}); err != nil {
		t.Fatalf("post-recovery sentinel unavailable: %v", err)
	}
	if _, err := survivingClient.File(ctx, executionenv.FileRequest{Context: verify, Operation: executionenv.OpFileRead, Path: "second-writer"}); err == nil {
		t.Fatal("denied overlapping writer nevertheless reached the executor")
	}
	release()
}

func waitExecutorTerminal(t *testing.T, ctx context.Context, kubeconfig, environmentID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		var pods corev1.PodList
		raw := runKubectl(t, ctx, kubeconfig, "get", "pods", "-n", namespace, "-l", "execution.mecatl.dev/environment="+environmentID, "-o", "json")
		if json.Unmarshal(raw, &pods) == nil && len(pods.Items) == 1 && (pods.Items[0].Status.Phase == corev1.PodFailed || pods.Items[0].Status.Phase == corev1.PodSucceeded) {
			terminated := len(pods.Items[0].Status.ContainerStatuses) == 1 && pods.Items[0].Status.ContainerStatuses[0].State.Terminated != nil
			if terminated {
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatal("test-owned executor did not produce terminal kubelet evidence")
}

func TestKindExecutionProductionQuotaSaturation(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	replicas, replicaUIDs := productionReplicaClients(t, ctx, state, kubeconfig)
	client := replicas[0]

	// First prove the provider's profile-wide CAS authority under concurrent
	// requests. Exactly one distinct allocation may consume quota-cas's sole slot.
	type ensureResult struct {
		owner   executionenv.Owner
		binding string
		out     executionenv.EnsureEnvironmentResponse
		err     error
	}
	start := make(chan struct{})
	results := make(chan ensureResult, 2)
	for i := 0; i < 2; i++ {
		i := i
		go func() {
			<-start
			owner := executionenv.Owner{Issuer: "https://oidc-issuer.execution-qualification.svc.cluster.local:8443", Subject: fmt.Sprintf("quota-cas-%d", i)}
			binding := fmt.Sprintf("quota-cas-%d-%d", i, time.Now().UnixNano())
			out, err := replicas[i].Ensure(ctx, binding, "quota-cas", owner, "ensure-"+binding)
			results <- ensureResult{owner: owner, binding: binding, out: out, err: err}
		}()
	}
	close(start)
	var accepted *ensureResult
	denied := 0
	for range 2 {
		result := <-results
		if result.err == nil {
			copy := result
			accepted = &copy
		} else if isRemoteCode(result.err, executionenv.CodeResourceExhausted) {
			denied++
		} else {
			t.Fatalf("quota CAS returned unexpected error: %v", result.err)
		}
	}
	if accepted == nil || denied != 1 {
		t.Fatalf("quota CAS pod_uids=%v accepted=%v resource_exhausted=%d, want one each", replicaUIDs, accepted != nil, denied)
	}
	if err := client.CommitReference(ctx, executionenv.ReferenceRequest{Environment: accepted.out.Environment, Owner: accepted.owner, BindingID: accepted.binding, OperationID: "ensure-" + accepted.binding}); err != nil {
		t.Fatal(err)
	}
	waitReady(t, ctx, client, accepted.owner, accepted.binding, accepted.out.Environment)

	// Then exercise Kubernetes admission itself. Tighten only the fixture-owned
	// namespace's PVC count to one additional claim and restore the known chart
	// value before converging and explicitly deleting these synthetic allocations.
	pvcBaseline := resourceCount(t, ctx, kubeconfig, "pvc")
	runKubectl(t, ctx, kubeconfig, "patch", "resourcequota/mecatl-execution", "-n", namespace, "--type=merge", "-p", fmt.Sprintf(`{"spec":{"hard":{"persistentvolumeclaims":"%d"}}}`, pvcBaseline+1))
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), time.Minute)
		defer cleanupCancel()
		_ = command(cleanupCtx, kubeconfig, "patch", "resourcequota/mecatl-execution", "-n", namespace, "--type=merge", "-p", `{"spec":{"hard":{"persistentvolumeclaims":"100"}}}`).Run()
	})

	allocations := make([]ensureResult, 0, 2)
	for i := 0; i < 2; i++ {
		owner := executionenv.Owner{Issuer: "https://oidc-issuer.execution-qualification.svc.cluster.local:8443", Subject: fmt.Sprintf("quota-kube-%d", i)}
		binding := fmt.Sprintf("quota-kube-%d-%d", i, time.Now().UnixNano())
		out, err := client.Ensure(ctx, binding, "quota-kube", owner, "ensure-"+binding)
		if err != nil {
			t.Fatalf("create quota admission candidate: %v", err)
		}
		allocations = append(allocations, ensureResult{owner: owner, binding: binding, out: out})
	}
	deadline := time.Now().Add(2 * time.Minute)
	var blocked ensureResult
	for time.Now().Before(deadline) {
		for _, allocation := range allocations {
			status := readExecutionStatus(t, ctx, kubeconfig, allocation.out.Environment.ID)
			if !status.Ready && status.ReadyReason == "PVCUnavailable" && resourceCount(t, ctx, kubeconfig, "pvc -l execution.mecatl.dev/environment="+allocation.out.Environment.ID) == 0 {
				blocked = allocation
			}
		}
		if blocked.binding != "" {
			break
		}
		time.Sleep(time.Second)
	}
	if blocked.binding == "" {
		t.Fatal("Kubernetes PVC quota did not leave one allocation NotReady with reason PVCUnavailable")
	}
	if resourceCount(t, ctx, kubeconfig, "pvc") != pvcBaseline+1 {
		t.Fatal("PVC quota admitted more than one additional workspace")
	}
	runKubectl(t, ctx, kubeconfig, "patch", "resourcequota/mecatl-execution", "-n", namespace, "--type=merge", "-p", `{"spec":{"hard":{"persistentvolumeclaims":"100"}}}`)

	all := append(allocations, *accepted)
	for _, allocation := range all {
		if err := client.CommitReference(ctx, executionenv.ReferenceRequest{Environment: allocation.out.Environment, Owner: allocation.owner, BindingID: allocation.binding, OperationID: "ensure-" + allocation.binding}); err != nil {
			t.Fatal(err)
		}
		waitReady(t, ctx, client, allocation.owner, allocation.binding, allocation.out.Environment)
		retireSyntheticEnvironment(t, ctx, client, kubeconfig, allocation.owner, allocation.binding, allocation.out.Environment)
	}
}

func retireSyntheticEnvironment(t *testing.T, ctx context.Context, client *executionclient.Client, kubeconfig string, owner executionenv.Owner, binding string, ref executionenv.EnvironmentRef) {
	t.Helper()
	deleteRef := executionenv.ReferenceRequest{Environment: ref, Owner: owner, BindingID: binding, OperationID: "delete-ref-" + binding}
	if err := client.PrepareReferenceDelete(ctx, deleteRef); err != nil {
		t.Fatal(err)
	}
	if err := client.ConfirmReferenceDelete(ctx, deleteRef); err != nil {
		t.Fatal(err)
	}
	status := readExecutionStatus(t, ctx, kubeconfig, ref.ID)
	request := executionenv.RetireEnvironmentRequest{Environment: ref, Owner: owner, ExpectedEpoch: status.Epoch, ExpectedPodUID: status.PodUID, ExpectedPVCUID: status.PVCUID, OperationID: "retire-" + binding}
	if err := client.RetireEnvironment(ctx, request); err != nil {
		t.Fatal(err)
	}
	retired := waitExecutionStatus(t, ctx, kubeconfig, ref.ID, func(s executionStatus) bool { return s.Retired && s.ExecutorTerminated })
	if err := client.DeleteRetiredEnvironment(ctx, ref, owner, retired.PVCUID, "delete-retired-"+binding); err != nil {
		t.Fatal(err)
	}
	waitResourceAbsent(t, ctx, kubeconfig, "executionenvironment", ref.ID)
}

func TestKindExecutionProductionPendingDeleteOutageRecovery(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	client, _ := productionClient(t, ctx, state, kubeconfig)
	owner, binding, attached := createProductionEnvironment(t, ctx, client, "pending-delete")
	request := executionenv.ReferenceRequest{Environment: attached.Environment, Owner: owner, BindingID: binding, OperationID: "pending-delete-outage"}
	if err := client.PrepareReferenceDelete(ctx, request); err != nil {
		t.Fatal(err)
	}
	if got := executionReferences(t, ctx, kubeconfig, attached.Environment.ID)[binding]; got != string(executionenv.ReferencePendingDelete) {
		t.Fatalf("prepared reference state=%q", got)
	}
	runKubectl(t, ctx, kubeconfig, "rollout", "restart", "deployment/mecatl-execution", "-n", namespace)
	runKubectl(t, ctx, kubeconfig, "rollout", "status", "deployment/mecatl-execution", "-n", namespace, "--timeout=240s")
	client, _ = productionClient(t, ctx, state, kubeconfig)
	intents, err := client.ListReferenceIntents(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, intent := range intents {
		if intent.Environment == attached.Environment && intent.BindingID == binding && intent.State == executionenv.ReferencePendingDelete && intent.OperationID == request.OperationID {
			found = true
		}
	}
	if !found {
		t.Fatal("pending-delete intent was not durable across provider outage")
	}
	intruderTLS := loadTLS(t, filepath.Join(state, "pki"), "intruder", "mecatl-execution.execution-qualification.svc.cluster.local")
	forward := portForward(t, ctx, kubeconfig, "service/mecatl-execution", 8443)
	intruder, err := executionclient.New(forward.addr, intruderTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer intruder.Close()
	private, err := intruder.ListReferenceIntents(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(private) != 0 {
		t.Fatal("pending reference intent crossed the mTLS client privacy boundary")
	}
	if err := client.ConfirmReferenceDelete(ctx, request); err != nil {
		t.Fatal(err)
	}
	waitReferenceSet(t, ctx, kubeconfig, attached.Environment.ID)
	if resourceCount(t, ctx, kubeconfig, "pvc -l execution.mecatl.dev/environment="+attached.Environment.ID) != 1 {
		t.Fatal("pending-delete recovery removed retained storage")
	}
	status := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID)
	retire := executionenv.RetireEnvironmentRequest{Environment: attached.Environment, Owner: owner, ExpectedEpoch: status.Epoch, ExpectedPodUID: status.PodUID, ExpectedPVCUID: status.PVCUID, OperationID: "retire-pending-delete"}
	if err := client.RetireEnvironment(ctx, retire); err != nil {
		t.Fatal(err)
	}
	retired := waitExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID, func(s executionStatus) bool { return s.Retired && s.ExecutorTerminated })
	if err := client.DeleteRetiredEnvironment(ctx, attached.Environment, owner, retired.PVCUID, "delete-pending-delete"); err != nil {
		t.Fatal(err)
	}
}

type legacyMigrationFixture struct {
	Environment     executionenv.EnvironmentRef `json:"environment"`
	Owner           executionenv.Owner          `json:"owner"`
	Binding         string                      `json:"binding"`
	PodUID          string                      `json:"pod_uid"`
	PVCUID          string                      `json:"pvc_uid"`
	Malformed       executionenv.EnvironmentRef `json:"malformed_environment"`
	MalformedPodUID string                      `json:"malformed_pod_uid"`
	MalformedPVCUID string                      `json:"malformed_pvc_uid"`
	Insecure        executionenv.EnvironmentRef `json:"insecure_environment"`
	InsecurePodUID  string                      `json:"insecure_pod_uid"`
	InsecurePVCUID  string                      `json:"insecure_pvc_uid"`
}

func runKindExecutionProductionCompatiblePrototypeMigration(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	client, _ := productionClient(t, ctx, state, kubeconfig)
	var fixture legacyMigrationFixture
	raw, err := os.ReadFile(filepath.Join(state, "legacy-migration.json"))
	if err != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatalf("load pre-upgrade legacy API fixture: %v", err)
	}
	if fixture.Environment.ID == "" || fixture.Environment.Revision == "" || fixture.PodUID == "" || fixture.PVCUID == "" || fixture.Binding == "" {
		t.Fatal("pre-upgrade legacy API fixture identities are incomplete")
	}

	var stored map[string]any
	if err := json.Unmarshal(runKubectl(t, ctx, kubeconfig, "get", "executionenvironment", fixture.Environment.ID, "-n", namespace, "-o", "json"), &stored); err != nil {
		t.Fatal(err)
	}
	status, _ := stored["status"].(map[string]any)
	references, _ := status["references"].([]any)
	if len(references) != 1 || references[0] != fixture.Binding {
		t.Fatalf("legacy string references were not persisted through the real API upgrade: %#v", references)
	}
	before := readExecutionStatus(t, ctx, kubeconfig, fixture.Environment.ID)
	if before.SpecSchema != 1 || before.StatusSchema != 1 || before.PodUID != fixture.PodUID || before.PVCUID != fixture.PVCUID {
		t.Fatal("pre-upgrade fixture did not preserve schema-1 exact runtime identity")
	}
	if _, err := client.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: fixture.Environment, Owner: fixture.Owner, BindingID: fixture.Binding}, Purpose: executionenv.PurposeSession}); err == nil {
		t.Fatal("prototype schema attached before explicit migration")
	}
	if err := client.MigrateEnvironment(ctx, fixture.Environment, fixture.Owner, 1, "foreign-pod-uid", before.PVCUID, "migration-wrong-uid"); err == nil {
		t.Fatal("migration adopted a foreign Pod UID")
	}
	if err := client.MigrateEnvironment(ctx, fixture.Environment, fixture.Owner, 1, "", before.PVCUID, "migration-missing-uid"); err == nil {
		t.Fatal("migration accepted a missing runtime UID")
	}
	if err := client.MigrateEnvironment(ctx, fixture.Environment, fixture.Owner, 2, before.PodUID, before.PVCUID, "migration-unknown-version"); err == nil {
		t.Fatal("migration accepted an unrecognized source schema")
	}
	if err := client.MigrateEnvironment(ctx, fixture.Malformed, fixture.Owner, 1, fixture.MalformedPodUID, fixture.MalformedPVCUID, "migration-malformed-shape"); err == nil || !isRemoteCode(err, executionenv.CodeConflict) {
		t.Fatalf("migration accepted a legacy reference shape outside the recognized prototype: %v", err)
	}
	if err := client.MigrateEnvironment(ctx, fixture.Insecure, fixture.Owner, 1, fixture.InsecurePodUID, fixture.InsecurePVCUID, "migration-insecure-runtime"); err == nil || !isRemoteCode(err, executionenv.CodeConflict) {
		t.Fatalf("migration accepted an insecure legacy executor: %v", err)
	}
	if got := readExecutionStatus(t, ctx, kubeconfig, fixture.Insecure.ID); got.SpecSchema != 1 || got.StatusSchema != 1 {
		t.Fatal("rejected insecure legacy executor was rewritten")
	}
	// Normalize the deliberately rejected test-owned object through the API so it
	// cannot poison later client-scoped reference reconciliation in this retained cluster.
	normalized := fmt.Sprintf(`[{"bindingID":"legacy-malformed-binding","state":"Published","operationID":"migration-fixture-normalize","createdAt":%q}]`, time.Now().UTC().Format(time.RFC3339Nano))
	runKubectl(t, ctx, kubeconfig, "patch", "executionenvironment/"+fixture.Malformed.ID, "-n", namespace, "--subresource=status", "--type=merge", "-p", `{"status":{"references":`+normalized+`}}`)
	insecureNormalized := fmt.Sprintf(`[{"bindingID":"legacy-insecure-binding","state":"Published","operationID":"migration-insecure-fixture-normalize","createdAt":%q}]`, time.Now().UTC().Format(time.RFC3339Nano))
	runKubectl(t, ctx, kubeconfig, "patch", "executionenvironment/"+fixture.Insecure.ID, "-n", namespace, "--subresource=status", "--type=merge", "-p", `{"status":{"references":`+insecureNormalized+`}}`)
	if err := client.MigrateEnvironment(ctx, fixture.Malformed, fixture.Owner, 1, fixture.MalformedPodUID, fixture.MalformedPVCUID, "migration-normalized-shape"); err != nil {
		t.Fatalf("normalize rejected migration fixture: %v", err)
	}
	if err := client.MigrateEnvironment(ctx, fixture.Environment, fixture.Owner, 1, before.PodUID, before.PVCUID, "migration-compatible-v1"); err != nil {
		t.Fatalf("migrate compatible prototype: %v", err)
	}
	after := waitExecutionStatus(t, ctx, kubeconfig, fixture.Environment.ID, func(s executionStatus) bool { return s.Ready && s.SpecSchema == 2 && s.StatusSchema == 2 })
	if after.PodUID != before.PodUID || after.PVCUID != before.PVCUID || after.Epoch != before.Epoch+1 {
		t.Fatal("migration changed runtime identity or failed to advance only the fence epoch")
	}
	if got := executionReferences(t, ctx, kubeconfig, fixture.Environment.ID); got[fixture.Binding] != string(executionenv.ReferencePublished) {
		t.Fatal("migration did not convert the persisted legacy string reference")
	}
	reattached := waitReady(t, ctx, client, fixture.Owner, fixture.Binding, fixture.Environment)
	verify, verifyRelease := acquireRun(t, ctx, client, fixture.Owner, fixture.Binding, reattached, "migration-verify")
	got, err := client.File(ctx, executionenv.FileRequest{Context: verify, Operation: executionenv.OpFileRead, Path: "migration-sentinel"})
	if err != nil || string(got.Data) != "prototype-data\n" {
		t.Fatal("migration lost prototype workspace data")
	}
	verifyRelease()
}

func TestKindExecutionProductionClearForkLifecycle(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	provider, _ := productionClient(t, ctx, state, kubeconfig)

	tokenForward := portForward(t, ctx, kubeconfig, "service/oidc-issuer", 8443)
	alice := fixtureToken(t, ctx, tokenForward.addr, filepath.Join(state, "pki"), "alice")
	bob := fixtureToken(t, ctx, tokenForward.addr, filepath.Join(state, "pki"), "bob")
	tokenForward.stop()
	agent := portForward(t, ctx, kubeconfig, "service/mecak8s", 8081)
	defer agent.stop()

	source := createSession(t, ctx, agent.addr, alice)
	assertMockJourney(t, prompt(t, ctx, agent.addr, source, alice, "run the scripted remote qualification"))
	owner := executionenv.Owner{Issuer: "https://oidc-issuer.execution-qualification.svc.cluster.local:8443", Subject: "alice"}
	allocation := executionenv.EnsureEnvironmentResponse{Environment: environmentForBinding(t, ctx, kubeconfig, source)}
	attached := waitReady(t, ctx, provider, owner, source, allocation.Environment)
	before := readExecutionStatus(t, ctx, kubeconfig, allocation.Environment.ID)

	forkID := successorSession(t, ctx, agent.addr, source, alice, "fork")
	clearID := successorSession(t, ctx, agent.addr, source, alice, "clear")
	if forkID == source || clearID == source || forkID == clearID {
		t.Fatal("Clear/Fork did not publish distinct session identities")
	}
	if got := getSession(t, ctx, agent.addr, forkID, bob); got != http.StatusNotFound {
		t.Fatalf("unknown owner observed fork: status=%d", got)
	}
	if got := resourceCount(t, ctx, kubeconfig, "executionenvironments.execution.mecatl.dev"); got < 1 {
		t.Fatal("successor publication lost its source environment")
	}
	waitReferenceSet(t, ctx, kubeconfig, allocation.Environment.ID, source, forkID, clearID)
	after := readExecutionStatus(t, ctx, kubeconfig, allocation.Environment.ID)
	if after.PVCUID != before.PVCUID || after.PodUID != before.PodUID || after.Epoch != before.Epoch {
		t.Fatal("Clear/Fork changed the exact remote execution identity")
	}
	for _, binding := range []string{forkID, clearID} {
		if _, err := provider.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: allocation.Environment, Owner: owner, BindingID: binding}, Purpose: executionenv.PurposeSession}); err != nil {
			t.Fatalf("successor %s did not attach to source environment: %v", binding, err)
		}
	}

	claim, err := provider.AcquireRun(ctx, executionenv.RunClaimRequest{Environment: attached.Environment, Owner: owner, BindingID: source, RunID: "source-blocker", OperationID: "source-blocker-acquire", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.AcquireRun(ctx, executionenv.RunClaimRequest{Environment: attached.Environment, Owner: owner, BindingID: clearID, RunID: "clear-overlap", OperationID: "clear-overlap-acquire", TTL: time.Minute}); err == nil || !isRemoteCode(err, executionenv.CodeConflict) {
		t.Fatalf("second session admitted an overlapping run: %v", err)
	}
	if err := provider.ReleaseRun(ctx, executionenv.RunClaimRequest{Environment: attached.Environment, Owner: owner, BindingID: source, RunID: claim.RunID, ClaimID: claim.ClaimID, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration, OperationID: "source-blocker-release"}); err != nil {
		t.Fatal(err)
	}
	clearAttached := waitReady(t, ctx, provider, owner, clearID, allocation.Environment)
	_, release := acquireRun(t, ctx, provider, owner, clearID, clearAttached, "clear-after-release")
	release()

	for _, id := range []string{forkID, clearID, source} {
		status, body := request(t, ctx, http.MethodPost, "http://"+agent.addr+"/v1/sessions/"+id+"/delete", alice, nil)
		if status != http.StatusNoContent {
			t.Fatalf("delete session %s status=%d body=%s", id, status, body)
		}
	}
	waitReferenceSet(t, ctx, kubeconfig, allocation.Environment.ID)
	final := readExecutionStatus(t, ctx, kubeconfig, allocation.Environment.ID)
	if final.PVCUID != before.PVCUID || resourceCount(t, ctx, kubeconfig, "pvc -l execution.mecatl.dev/environment="+allocation.Environment.ID) != 1 {
		t.Fatal("session deletion removed or replaced the retained workspace")
	}
}

func environmentForBinding(t *testing.T, ctx context.Context, kubeconfig, binding string) executionenv.EnvironmentRef {
	t.Helper()
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				Revision string `json:"revision"`
			} `json:"spec"`
			Status struct {
				References []struct {
					BindingID string `json:"bindingID"`
				} `json:"references"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(runKubectl(t, ctx, kubeconfig, "get", "executionenvironments", "-n", namespace, "-o", "json"), &list); err != nil {
		t.Fatal(err)
	}
	for _, item := range list.Items {
		for _, ref := range item.Status.References {
			if ref.BindingID == binding && item.Metadata.Name != "" && item.Spec.Revision != "" {
				return executionenv.EnvironmentRef{ID: item.Metadata.Name, Revision: item.Spec.Revision}
			}
		}
	}
	t.Fatalf("no exact execution environment owns binding %s", binding)
	return executionenv.EnvironmentRef{}
}

func successorSession(t *testing.T, ctx context.Context, addr, source, token, operation string) string {
	t.Helper()
	status, body := request(t, ctx, http.MethodPost, "http://"+addr+"/v1/sessions/"+source+"/"+operation, token, []byte(`{}`))
	if status != http.StatusCreated {
		t.Fatalf("%s session status=%d body=%s", operation, status, body)
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(body, &out) != nil || out.SessionID == "" {
		t.Fatalf("%s session returned no identity", operation)
	}
	return out.SessionID
}

func waitReferenceSet(t *testing.T, ctx context.Context, kubeconfig, environmentID string, want ...string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		got := executionReferences(t, ctx, kubeconfig, environmentID)
		if len(got) == len(want) {
			matched := true
			for _, binding := range want {
				if got[binding] != string(executionenv.ReferencePublished) {
					matched = false
				}
			}
			if matched {
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("environment references did not converge to published set %v", want)
}

func executionReferences(t *testing.T, ctx context.Context, kubeconfig, environmentID string) map[string]string {
	t.Helper()
	var object struct {
		Status struct {
			References []struct {
				BindingID string `json:"bindingID"`
				State     string `json:"state"`
			} `json:"references"`
		} `json:"status"`
	}
	if err := json.Unmarshal(runKubectl(t, ctx, kubeconfig, "get", "executionenvironment", environmentID, "-n", namespace, "-o", "json"), &object); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]string, len(object.Status.References))
	for _, ref := range object.Status.References {
		out[ref.BindingID] = ref.State
	}
	return out
}

func TestKindExecutionProductionFailureArtifactBoundary(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	artifact := runKubectl(t, ctx, kubeconfig, "get", "events", "-n", namespace, "-o", "custom-columns=OBJECT:.involvedObject.name,REASON:.reason", "--no-headers")
	if len(artifact) > 1<<20 {
		t.Fatal("sanitized failure artifact exceeds one MiB")
	}
	for _, forbidden := range []string{"kubeconfig", "BEGIN PRIVATE KEY", "Bearer ", "grant-key", "ownerSubject", "clientHash", "Secret/data"} {
		if strings.Contains(string(artifact), forbidden) {
			t.Fatalf("sanitized failure artifact contains forbidden class %q", forbidden)
		}
	}
	if err := os.WriteFile(filepath.Join(state, "production-failure-artifact.txt"), artifact, 0o600); err != nil {
		t.Fatal(err)
	}
}
