//go:build kind_execution_e2e

package k8s_execution_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
	"github.com/stacklok/mecatl/internal/executionenv"
)

// Last in the serial production suite: rotation and migration share this
// throwaway release. Always restore its current authority, never the initial one.
func TestKindExecutionProductionHelmLifetime(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	stageStarted := time.Now()
	t.Log("stage=provision_and_probe starting")
	requireOwnedHelmFixture(t, state, kubeconfig)
	client, _ := productionClient(t, ctx, state, kubeconfig)
	owner, binding, attached := createProductionEnvironment(t, ctx, client, "helm-lifetime")
	rc, release := acquireRun(t, ctx, client, owner, binding, attached, "helm-lifetime-write")
	probe := []byte("package main\nimport (\"net\";\"os\";\"time\")\nfunc main(){c,e:=net.DialTimeout(\"tcp\",os.Args[1],2*time.Second);if e!=nil{os.Exit(42)};c.Close()}\n")
	for path, data := range map[string][]byte{"helm-sentinel": []byte("retained-helm-data\n"), "helm-probe.go": probe} {
		if _, err := client.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileCreate, Path: path, Data: data}); err != nil {
			t.Fatal("create lifetime workspace data:", remoteErrorCode(err))
		}
	}
	built, err := client.StartCommand(ctx, executionenv.CommandStartRequest{Context: rc, Command: "go build -o helm-probe helm-probe.go", TimeoutMillis: 120000})
	if err != nil || built.State != executionenv.CommandSucceeded || built.Result.ExitCode != 0 {
		t.Fatal("build lifetime probe failed")
	}
	waitFileContent(t, ctx, client, rc, "helm-sentinel", "retained-helm-data\n", "current run after provisioning")
	release()
	// Occupy the single-slot profile; its reservation must still constrain Ensure
	// after adoption, rather than only matching a ConfigMap annotation.
	quotaBinding := fmt.Sprintf("helm-quota-%d", time.Now().UnixNano())
	quota, err := client.EnsureTemplate(ctx, quotaBinding, "quota-cas", kindTemplateRevision(t, ctx, client, "quota-cas"), owner, "ensure-"+quotaBinding)
	if err != nil {
		t.Fatal("reserve lifetime capacity:", remoteErrorCode(err))
	}
	if err := client.CommitReference(ctx, executionenv.ReferenceRequest{Environment: quota.Environment, Owner: owner, BindingID: quotaBinding, OperationID: "ensure-" + quotaBinding}); err != nil {
		t.Fatal("commit lifetime capacity:", remoteErrorCode(err))
	}
	waitReady(t, ctx, client, owner, quotaBinding, quota.Environment)
	logQualificationStage(t, &stageStarted, "provision_and_probe", "prepare_upgrade_fixture")

	before := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID)
	envUID := kubeValue(t, ctx, kubeconfig, "get", "executionenvironment", attached.Environment.ID, "-n", namespace, "-o", "jsonpath={.metadata.uid}")
	pod := kubeValue(t, ctx, kubeconfig, "get", "pods", "-n", namespace, "-l", "execution.mecatl.dev/environment="+attached.Environment.ID, "-o", "jsonpath={.items[0].metadata.name}")
	if before.PVCUID != kubeValue(t, ctx, kubeconfig, "get", "pvc", "-n", namespace, "-l", "execution.mecatl.dev/environment="+attached.Environment.ID, "-o", "jsonpath={.items[*].metadata.uid}") || before.PodUID != kubeValue(t, ctx, kubeconfig, "get", "pod", pod, "-n", namespace, "-o", "jsonpath={.metadata.uid}") {
		t.Fatal("runtime status does not name the actual PVC/Pod UIDs")
	}
	fixtureIP := kubeValue(t, ctx, kubeconfig, "get", "pod/network-fixture", "-n", namespace, "-o", "jsonpath={.status.podIP}")
	peerIP := kubeValue(t, ctx, kubeconfig, "get", "pod/network-intruder", "-n", namespace, "-o", "jsonpath={.status.podIP}")

	// Only nonsecret chart values and manifests are captured. No Helm manifest or
	// Secret dump is written to artifacts; command errors report status only.
	raw, err := helmLifetime(ctx, kubeconfig, "get", "values", "mecatl-execution", "-o", "json")
	if err != nil {
		t.Fatal("read owned release values:", err)
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatal("decode release values")
	}
	current := lifetimeConfigMap(ctx, t, kubeconfig, "mecatl-execution-security-manifest")
	oldManifest := current.Data["manifest.json"]
	var manifest map[string]any
	if err := json.Unmarshal([]byte(oldManifest), &manifest); err != nil {
		t.Fatal("decode nonsecret authority manifest")
	}
	generation, ok := manifest["generation"].(float64)
	if !ok || generation < 1 {
		t.Fatal("missing authority generation")
	}
	manifest["generation"] = generation + 1
	newManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := values["provider"].(map[string]any)
	if !ok {
		t.Fatal("missing provider values")
	}
	provider["securityManifest"] = string(newManifest)
	dir, err := os.MkdirTemp(state, "helm-lifetime-")
	if err != nil {
		t.Fatal(err)
	}
	valuesPath := filepath.Join(dir, "values.json")
	raw, err = json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(valuesPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	chart := filepath.Join(repoRoot(t), "deploy/helm/mecatl-execution")
	restore := func(ctx context.Context) error {
		if err := quiesceLifetimeProvider(ctx, kubeconfig); err != nil {
			return err
		}
		_, err := helmLifetime(ctx, kubeconfig, "upgrade", "--install", "mecatl-execution", chart, "-f", valuesPath, "--wait", "--timeout=4m")
		return err
	}
	var restoreDefaultChoice func() error
	t.Cleanup(func() {
		cleanupStarted := time.Now()
		t.Log("stage=helm_cleanup starting")
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 7*time.Minute)
		defer cleanupCancel()
		requireOwnedHelmFixture(t, state, kubeconfig)
		if restoreDefaultChoice != nil {
			if err := restoreDefaultChoice(); err != nil {
				t.Error("restore default template selection failed:", err)
				return
			}
		}
		if err := restore(cleanupCtx); err != nil {
			t.Error("restore owned release/current authority failed:", err)
		}
		t.Logf("stage=helm_cleanup elapsed=%s", time.Since(cleanupStarted).Round(time.Millisecond))
	})
	capacityBefore := lifetimeConfigMap(ctx, t, kubeconfig, "mecatl-execution-profile-allocations")
	authorityBefore := lifetimeConfigMap(ctx, t, kubeconfig, "mecatl-execution-security-authority")
	var beforePolicy struct {
		Generation uint64 `json:"generation"`
		Digest     string `json:"digest"`
	}
	if err := json.Unmarshal([]byte(authorityBefore.Data["state.json"]), &beforePolicy); err != nil || beforePolicy.Generation == 0 || beforePolicy.Digest == "" {
		t.Fatal("missing pre-upgrade policy authority")
	}
	policiesBefore := lifetimePolicies(ctx, t, kubeconfig)
	liveLedgers := map[string]corev1.ConfigMap{capacityBefore.Name: capacityBefore, authorityBefore.Name: authorityBefore, current.Name: current}
	liveLedgers["mecatl-execution-templates"] = lifetimeConfigMap(ctx, t, kubeconfig, "mecatl-execution-templates")
	waitProviderReadyReplicas(t, ctx, kubeconfig, 2)
	logQualificationStage(t, &stageStarted, "prepare_upgrade_fixture", "live_upgrade_rejection")
	if _, err := helmLifetime(ctx, kubeconfig, "upgrade", "mecatl-execution", chart, "-f", valuesPath, "--wait", "--timeout=4m"); !errors.Is(err, errLifetimeNotQuiesced) {
		t.Fatal("live same-release upgrade did not reject provider writers with the quiescence error")
	}
	assertLifetimeRetained(ctx, t, kubeconfig, liveLedgers, policiesBefore)
	if status := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID); status != before || envUID != kubeValue(t, ctx, kubeconfig, "get", "executionenvironment", attached.Environment.ID, "-n", namespace, "-o", "jsonpath={.metadata.uid}") || before.PodUID != kubeValue(t, ctx, kubeconfig, "get", "pod", pod, "-n", namespace, "-o", "jsonpath={.metadata.uid}") || before.PVCUID != kubeValue(t, ctx, kubeconfig, "get", "pvc", "-n", namespace, "-l", "execution.mecatl.dev/environment="+attached.Environment.ID, "-o", "jsonpath={.items[*].metadata.uid}") {
		t.Fatal("rejected live upgrade changed runtime identity or status")
	}
	logQualificationStage(t, &stageStarted, "live_upgrade_rejection", "compatible_upgrade")
	// Quiesce all writers before lookup snapshots are rendered into the upgrade.
	if err := restore(ctx); err != nil {
		t.Fatal("compatible Helm upgrade failed:", err)
	}
	waitSecurityGeneration(ctx, t, kubeconfig, uint64(generation+1))
	waitProviderReadyReplicas(t, ctx, kubeconfig, 2)
	ledgers := map[string]corev1.ConfigMap{}
	for _, name := range []string{"mecatl-execution-security-authority", "mecatl-execution-profile-allocations", "mecatl-execution-templates", "mecatl-execution-security-manifest"} {
		ledgers[name] = lifetimeConfigMap(ctx, t, kubeconfig, name)
	}
	var highWater struct {
		Generation uint64 `json:"generation"`
		Digest     string `json:"digest"`
	}
	if err := json.Unmarshal([]byte(ledgers["mecatl-execution-security-authority"].Data["state.json"]), &highWater); err != nil || highWater.Generation != uint64(generation+1) || highWater.Digest == "" || highWater.Digest == beforePolicy.Digest {
		t.Fatal("upgrade did not advance the generation-bound client policy authority")
	}
	if capacityAfter := ledgers[capacityBefore.Name]; capacityAfter.UID != capacityBefore.UID || !reflect.DeepEqual(capacityAfter.Data, capacityBefore.Data) {
		t.Fatal("compatible upgrade changed capacity reservations")
	}
	policies := lifetimePolicies(ctx, t, kubeconfig)
	if len(policies) < 2 {
		t.Fatal("workload policies missing before uninstall")
	}
	logQualificationStage(t, &stageStarted, "compatible_upgrade", "uninstall_and_retained_workload")
	if _, err := helmLifetime(ctx, kubeconfig, "uninstall", "mecatl-execution", "--wait", "--timeout=2m"); err != nil {
		t.Fatal("uninstall owned release failed:", err)
	}
	waitResourceAbsent(t, ctx, kubeconfig, "deployment", "mecatl-execution")
	assertLifetimeRetained(ctx, t, kubeconfig, ledgers, policies)
	after := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID)
	if after.PVCUID != before.PVCUID || after.PodUID != before.PodUID || before.PVCUID != kubeValue(t, ctx, kubeconfig, "get", "pvc", "-n", namespace, "-l", "execution.mecatl.dev/environment="+attached.Environment.ID, "-o", "jsonpath={.items[*].metadata.uid}") || before.PodUID != kubeValue(t, ctx, kubeconfig, "get", "pod", pod, "-n", namespace, "-o", "jsonpath={.metadata.uid}") || envUID != kubeValue(t, ctx, kubeconfig, "get", "executionenvironment", attached.Environment.ID, "-n", namespace, "-o", "jsonpath={.metadata.uid}") {
		t.Fatal("uninstall replaced retained runtime identity")
	}
	// Bypass the absent provider only to observe the *same executor*. Positive
	// reachability prevents an all-network-down result masquerading as isolation.
	lifetimeProbe(ctx, t, kubeconfig, pod, fixtureIP+":8080", 0)
	lifetimeProbe(ctx, t, kubeconfig, pod, peerIP+":8080", 42)
	lifetimeProbe(ctx, t, kubeconfig, pod, "10.96.0.1:443", 42)
	lifetimeProbe(ctx, t, kubeconfig, pod, "1.1.1.1:443", 42)
	data := runKubectl(t, ctx, kubeconfig, "exec", "-n", namespace, pod, "--", "cat", "/workspace/helm-sentinel")
	if string(data) != "retained-helm-data\n" {
		t.Fatal("retained workspace data changed during provider absence")
	}
	logQualificationStage(t, &stageStarted, "uninstall_and_retained_workload", "negative_adoption")
	// A changed historical recipe is refused before any resource can be adopted.
	goTemplate := values["templates"].(map[string]any)["go"].(map[string]any)
	oldRevision := goTemplate["default"].(string)
	if _, err := helmLifetime(ctx, kubeconfig, "install", "mecatl-execution", chart, "-f", valuesPath, "--set", "templates.go.revisions."+oldRevision+".execution.maxEnvironments=30", "--dry-run=server"); err == nil {
		t.Fatal("incompatible reinstall was accepted")
	}
	// Refusal must happen without adopting another release or bootstrapping a
	// new authority name over this namespace's retained allocations.
	for _, args := range [][]string{
		{"install", "foreign-execution", chart, "-f", valuesPath, "--dry-run=server"},
		{"install", "mecatl-execution", chart, "-f", valuesPath, "--set", "fullnameOverride=missing-history", "--dry-run=server"},
	} {
		if _, err := helmLifetime(ctx, kubeconfig, args...); err == nil {
			t.Fatal("foreign/missing-history reinstall was accepted")
		}
	}
	assertLifetimeRetained(ctx, t, kubeconfig, ledgers, policies)
	logQualificationStage(t, &stageStarted, "negative_adoption", "reinstall_and_reattach")
	if _, err := helmLifetime(ctx, kubeconfig, "install", "mecatl-execution", chart, "-f", valuesPath, "--wait", "--timeout=4m"); err != nil {
		t.Fatal("same-identity Helm reinstall/adoption failed:", err)
	}
	assertLifetimeRetained(ctx, t, kubeconfig, ledgers, policies)
	reattachedClient, _ := productionClient(t, ctx, state, kubeconfig)
	reattached := waitReady(t, ctx, reattachedClient, owner, binding, attached.Environment)
	if reattached.Environment != attached.Environment {
		t.Fatal("reattach changed exact environment revision")
	}
	after = readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID)
	if after.PVCUID != before.PVCUID || after.PodUID != before.PodUID || before.PVCUID != kubeValue(t, ctx, kubeconfig, "get", "pvc", "-n", namespace, "-l", "execution.mecatl.dev/environment="+attached.Environment.ID, "-o", "jsonpath={.items[*].metadata.uid}") || before.PodUID != kubeValue(t, ctx, kubeconfig, "get", "pod", pod, "-n", namespace, "-o", "jsonpath={.metadata.uid}") || envUID != kubeValue(t, ctx, kubeconfig, "get", "executionenvironment", attached.Environment.ID, "-n", namespace, "-o", "jsonpath={.metadata.uid}") {
		t.Fatal("adoption replaced retained runtime identity")
	}
	rc, release = acquireRun(t, ctx, reattachedClient, owner, binding, reattached, "helm-lifetime-read")
	waitFileContent(t, ctx, reattachedClient, rc, "helm-sentinel", "retained-helm-data\n", "same-release Helm adoption")
	waitFileContent(t, ctx, reattachedClient, rc, "helm-sentinel", "retained-helm-data\n", "current run after reattach")
	release()
	if _, err := reattachedClient.EnsureTemplate(ctx, quotaBinding+"-excess", "quota-cas", kindTemplateRevision(t, ctx, reattachedClient, "quota-cas"), owner, "ensure-"+quotaBinding+"-excess"); !isRemoteCode(err, executionenv.CodeResourceExhausted) {
		t.Fatal("retained capacity did not reject excess allocation:", remoteErrorCode(err))
	}
	logQualificationStage(t, &stageStarted, "reinstall_and_reattach", "stale_authority_restore")

	// The old manifest still has valid key material, windows, and clients. Only
	// its generation is older. An existing connection must fail after reload.
	patch, err := json.Marshal(map[string]any{"data": map[string]string{"manifest.json": oldManifest}})
	if err != nil {
		t.Fatal(err)
	}
	runKubectl(t, ctx, kubeconfig, "patch", "configmap/mecatl-execution-security-manifest", "-n", namespace, "--type=merge", "-p", string(patch))
	waitProviderReadyReplicas(t, ctx, kubeconfig, 0)
	if _, err := reattachedClient.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: attached.Environment, Owner: owner, BindingID: binding}, Purpose: executionenv.PurposeSession}); !isRemoteCode(err, executionenv.CodeNotReady) {
		t.Fatal("older still-valid authority did not fail closed with not_ready after reinstall:", remoteErrorCode(err))
	}
	authority := lifetimeConfigMap(ctx, t, kubeconfig, "mecatl-execution-security-authority")
	if !reflect.DeepEqual(authority.Data, ledgers[authority.Name].Data) {
		t.Fatal("stale candidate reset authority history")
	}
	if err := restore(ctx); err != nil {
		t.Fatal("restore current authority:", err)
	}
	waitProviderReadyReplicas(t, ctx, kubeconfig, 2)
	restoredClient, _ := productionClient(t, ctx, state, kubeconfig)
	waitReady(t, ctx, restoredClient, owner, binding, attached.Environment)
	logQualificationStage(t, &stageStarted, "stale_authority_restore", "publish_new_revision")

	definitions := goTemplate["revisions"].(map[string]any)
	oldExecution := definitions[oldRevision].(map[string]any)["execution"].(map[string]any)
	newExecution := make(map[string]any, len(oldExecution))
	for key, value := range oldExecution {
		newExecution[key] = value
	}
	newExecution["maxCommandDuration"] = "4m"
	canonicalInput, err := json.Marshal(newExecution)
	if err != nil {
		t.Fatal(err)
	}
	var spec executioncontroller.ProfileSpec
	if err := yaml.Unmarshal(canonicalInput, &spec); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(append([]byte("mecatl/execution-template/v1\x00"), canonical...))
	newRevision := "v1-" + hex.EncodeToString(sum[:])
	if newRevision == oldRevision {
		t.Fatal("new recipe did not change revision")
	}
	definitions[newRevision] = map[string]any{"execution": newExecution}
	goTemplate["default"] = newRevision
	writeValues := func() {
		t.Helper()
		content, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(valuesPath, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	restoreDefaultChoice = func() error {
		goTemplate["default"] = oldRevision
		delete(definitions[oldRevision].(map[string]any), "deprecated")
		content, err := json.Marshal(values)
		if err != nil {
			return err
		}
		return os.WriteFile(valuesPath, content, 0o600)
	}
	writeValues()
	if err := restore(ctx); err != nil {
		t.Fatal("publish additive execution revision:", err)
	}
	newClient, _ := productionClient(t, ctx, state, kubeconfig)
	if _, err := newClient.ValidateTemplate(ctx, "go", newRevision); err != nil {
		t.Fatal("new published revision unavailable:", err)
	}
	runKubectl(t, ctx, kubeconfig, "rollout", "restart", "deployment/mecak8s", "-n", namespace)
	runKubectl(t, ctx, kubeconfig, "rollout", "status", "deployment/mecak8s", "-n", namespace, "--timeout=240s")
	issuerForward := portForward(t, ctx, kubeconfig, "service/oidc-issuer", 8443)
	aliceToken := fixtureToken(t, ctx, issuerForward.addr, filepath.Join(state, "pki"), "alice")
	issuerForward.stop()
	agentForward := portForward(t, ctx, kubeconfig, "service/mecak8s", 8081)
	defer agentForward.stop()
	status, publicCatalog := request(t, ctx, "GET", "http://"+agentForward.addr+"/v1/execution-templates", aliceToken, nil)
	if status != 200 || !bytes.Contains(publicCatalog, []byte(newRevision)) || !bytes.Contains(publicCatalog, []byte(oldRevision)) {
		t.Fatal("authenticated public catalog did not publish both retained revisions")
	}
	newSession := createSession(t, ctx, agentForward.addr, aliceToken, newRevision)
	newRef := environmentForBinding(t, ctx, kubeconfig, newSession)
	if newRef == attached.Environment {
		t.Fatal("new public revision reused the old allocation")
	}
	newOwner := executionenv.Owner{Issuer: owner.Issuer, Subject: "alice"}
	if got := waitReady(t, ctx, newClient, newOwner, newSession, newRef); got.Environment != newRef {
		t.Fatal("new public revision failed to bind")
	}
	oldReady := waitReady(t, ctx, newClient, owner, binding, attached.Environment)
	oldRun, done := acquireRun(t, ctx, newClient, owner, binding, oldReady, "historical-revision-shell")
	waitFileContent(t, ctx, newClient, oldRun, "helm-sentinel", "retained-helm-data\n", "historical template after provider restart")
	shell, err := newClient.StartCommand(ctx, executionenv.CommandStartRequest{Context: oldRun, Command: "go version", TimeoutMillis: 30000})
	if err != nil || shell.State != executionenv.CommandSucceeded || shell.Result.ExitCode != 0 {
		t.Fatal("historical revision lost its bound Shell")
	}
	done()

	// Only new binds are blocked by deprecation; old exact references and the
	// PVC remain readable after another provider restart.
	definitions[oldRevision].(map[string]any)["deprecated"] = true
	writeValues()
	if err := restore(ctx); err != nil {
		t.Fatal("deprecate historical revision:", err)
	}
	deprecatedClient, _ := productionClient(t, ctx, state, kubeconfig)
	if _, err := deprecatedClient.ValidateTemplate(ctx, "go", oldRevision); err == nil {
		t.Fatal("deprecated revision remained eligible for new binding")
	}
	if _, err := deprecatedClient.EnsureTemplate(ctx, "deprecated-new-binding", "go", oldRevision, owner, "deprecated-ensure"); err == nil {
		t.Fatal("deprecated revision accepted a new allocation")
	}
	deprecatedRequest, err := json.Marshal(map[string]any{"execution": map[string]any{"template": map[string]string{"id": "go", "revision": oldRevision}}})
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := request(t, ctx, "POST", "http://"+agentForward.addr+"/v1/sessions", aliceToken, deprecatedRequest); status == 201 || status/100 != 4 {
		t.Fatalf("deprecated public template bind status=%d", status)
	}
	if got := waitReady(t, ctx, deprecatedClient, owner, binding, attached.Environment); got.Environment != attached.Environment {
		t.Fatal("deprecation broke an existing exact reference")
	}
	if uid := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID).PVCUID; uid != before.PVCUID {
		t.Fatal("revision publication/deprecation replaced retained PVC")
	}
	// Exercise revocation only on the disposable allocation of the newly
	// published revision. Make the old revision eligible again as the default,
	// then revoke the active non-default revision across a Helm/provider restart.
	goTemplate["default"] = oldRevision
	delete(definitions[oldRevision].(map[string]any), "deprecated")
	newReady := waitReady(t, ctx, deprecatedClient, newOwner, newSession, newRef)
	claim, err := deprecatedClient.AcquireRun(ctx, executionenv.RunClaimRequest{Environment: newReady.Environment, Owner: newOwner, BindingID: newSession, RunID: "helm-revoked-active", OperationID: "helm-revoked-active", TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal("acquire revocation proof run:", remoteErrorCode(err))
	}
	active := executionenv.RequestContext{Environment: claim.Environment, Owner: newOwner, BindingID: newSession, RunID: claim.RunID, ClaimID: claim.ClaimID, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration}
	commandResult := make(chan executionenv.CommandStartResponse, 1)
	commandError := make(chan error, 1)
	go func() {
		result, err := deprecatedClient.StartCommand(ctx, executionenv.CommandStartRequest{Context: active, Command: "printf 'running\\n' > revoke-running; sleep 220", TimeoutMillis: 240000})
		commandResult <- result
		commandError <- err
	}()
	newPod := kubeValue(t, ctx, kubeconfig, "get", "pods", "-n", namespace, "-l", "execution.mecatl.dev/environment="+newRef.ID, "-o", "jsonpath={.items[0].metadata.name}")
	startDeadline := time.Now().Add(30 * time.Second)
	for {
		marker, err := command(ctx, kubeconfig, "exec", "-n", namespace, newPod, "--", "cat", "/workspace/revoke-running").CombinedOutput()
		if err == nil && string(marker) == "running\n" {
			break
		}
		if time.Now().After(startDeadline) || ctx.Err() != nil {
			t.Fatal("active command did not start before revocation")
		}
		time.Sleep(time.Second)
	}
	newStatus := readExecutionStatus(t, ctx, kubeconfig, newRef.ID)
	definitions[newRevision].(map[string]any)["revoked"] = true
	writeValues()
	if err := restore(ctx); err != nil {
		t.Fatal("revoke actively used revision through Helm:", err)
	}
	revokedClient, _ := productionClient(t, ctx, state, kubeconfig)
	// Revocation retains the Pod finalizer as evidence; require kubelet termination, not deletion.
	waitExecutorTerminal(ctx, t, kubeconfig, newRef.ID, newStatus.PodUID)
	select {
	case result := <-commandResult:
		if result.State == executionenv.CommandSucceeded {
			t.Fatal("revoked active command completed successfully")
		}
		<-commandError
	case <-time.After(30 * time.Second):
		t.Fatal("revoked active command was not canceled after executor termination")
	}
	if uid := readExecutionStatus(t, ctx, kubeconfig, newRef.ID).PVCUID; uid != newStatus.PVCUID || uid == "" {
		t.Fatal("revocation changed retained workspace PVC")
	}
	if _, err := revokedClient.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: newRef, Owner: newOwner, BindingID: newSession}, Purpose: executionenv.PurposeSession}); err == nil {
		t.Fatal("revoked allocation reattached")
	}
	if _, err := revokedClient.AcquireRun(ctx, executionenv.RunClaimRequest{Environment: newRef, Owner: newOwner, BindingID: newSession, RunID: "revoked-new-run", OperationID: "revoked-new-run", TTL: time.Minute}); err == nil {
		t.Fatal("revoked allocation admitted a new run")
	}
	if _, err := revokedClient.File(ctx, executionenv.FileRequest{Context: active, Operation: executionenv.OpFileRead, Path: "revoke-running"}); err == nil {
		t.Fatal("revoked allocation admitted file access")
	}
	// The derivative has only one published revision. Remove its default and
	// revoke that last revision through the real Helm ConfigMap/provider restart.
	utility := values["templates"].(map[string]any)["operator-utility"].(map[string]any)
	utilityRevision := utility["default"].(string)
	utilityBinding := "helm-sole-revocation"
	utilityAllocation, err := revokedClient.EnsureTemplate(ctx, utilityBinding, "operator-utility", utilityRevision, owner, "helm-sole-ensure")
	if err != nil {
		t.Fatal("create retained sole-revision allocation:", err)
	}
	utilityRef := utilityAllocation.Environment
	utilityReady := waitReady(t, ctx, revokedClient, owner, utilityBinding, utilityRef)
	utilityPVC := readExecutionStatus(t, ctx, kubeconfig, utilityReady.Environment.ID).PVCUID
	delete(utility, "default")
	utility["revisions"].(map[string]any)[utilityRevision].(map[string]any)["revoked"] = true
	writeValues()
	if err := restore(ctx); err != nil {
		t.Fatal("revoke sole revision through Helm:", err)
	}
	soleClient, _ := productionClient(t, ctx, state, kubeconfig)
	if _, err := soleClient.ValidateTemplate(ctx, "operator-utility", utilityRevision); err == nil {
		t.Fatal("revoked sole revision remained selectable")
	}
	if _, err := soleClient.Attach(ctx, executionenv.AttachEnvironmentRequest{Context: executionenv.RequestContext{Environment: utilityRef, Owner: owner, BindingID: utilityBinding}, Purpose: executionenv.PurposeSession}); err == nil {
		t.Fatal("revoked sole revision reattached")
	}
	if got := readExecutionStatus(t, ctx, kubeconfig, utilityRef.ID).PVCUID; got != utilityPVC || got == "" {
		t.Fatal("sole revision revocation lost retained PVC")
	}
	logQualificationStage(t, &stageStarted, "publish_new_revision", "cleanup")
	// Retain the test's data by default, as the chart does; no forced cleanup or
	// finalizer removal. The explicit test-cluster owner controls final disposal.
}

func requireOwnedHelmFixture(t *testing.T, state, kubeconfig string) {
	t.Helper()
	ownership, err := os.ReadFile(filepath.Join(state, "ownership"))
	if err != nil {
		t.Fatal("lifetime test requires fixture ownership record")
	}
	fields := map[string]string{}
	for line := range strings.SplitSeq(string(ownership), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			fields[key] = value
		}
	}
	cluster := fields["cluster"]
	if !strings.HasPrefix(cluster, "mecatl-execution-qual-") || fields["context"] != "kind-"+cluster || fields["context"] != os.Getenv("MECATL_KUBE_CONTEXT") || fields["kubeconfig"] != kubeconfig || fields["namespace"] != namespace || fields["profile"] != "production" {
		t.Fatal("lifetime test refuses non-owned cluster/context/namespace")
	}
}

func lifetimeConfigMap(ctx context.Context, t *testing.T, kubeconfig, name string) corev1.ConfigMap {
	t.Helper()
	var cm corev1.ConfigMap
	if err := json.Unmarshal(runKubectl(t, ctx, kubeconfig, "get", "configmap", name, "-n", namespace, "-o", "json"), &cm); err != nil {
		t.Fatal("decode nonsecret lifetime ConfigMap")
	}
	if cm.UID == "" || cm.Labels["app.kubernetes.io/managed-by"] != "Helm" || cm.Annotations["meta.helm.sh/release-name"] != "mecatl-execution" || cm.Annotations["meta.helm.sh/release-namespace"] != namespace {
		t.Fatal("refuse foreign lifetime ConfigMap", name)
	}
	return cm
}

func lifetimePolicies(ctx context.Context, t *testing.T, kubeconfig string) map[string]networkingv1.NetworkPolicy {
	t.Helper()
	var list networkingv1.NetworkPolicyList
	if err := json.Unmarshal(runKubectl(t, ctx, kubeconfig, "get", "networkpolicy", "-n", namespace, "-o", "json"), &list); err != nil {
		t.Fatal("decode lifetime policies")
	}
	result := map[string]networkingv1.NetworkPolicy{}
	for _, p := range list.Items {
		if p.Name == "mecatl-execution-workload-default-deny" || strings.HasPrefix(p.Name, "mecatl-execution-profile-") {
			result[p.Name] = p
		}
	}
	return result
}

func assertLifetimeRetained(ctx context.Context, t *testing.T, kubeconfig string, cms map[string]corev1.ConfigMap, policies map[string]networkingv1.NetworkPolicy) {
	t.Helper()
	for name, before := range cms {
		after := lifetimeConfigMap(ctx, t, kubeconfig, name)
		if before.UID != after.UID || !reflect.DeepEqual(before.Data, after.Data) {
			t.Fatal("retained ConfigMap identity/data changed", name)
		}
	}
	live := lifetimePolicies(ctx, t, kubeconfig)
	if len(live) != len(policies) {
		t.Fatal("retained policy set changed")
	}
	for name, before := range policies {
		after := live[name]
		if before.UID != after.UID || !reflect.DeepEqual(before.Spec, after.Spec) {
			t.Fatal("retained policy identity/confinement changed", name)
		}
	}
}

func lifetimeProbe(ctx context.Context, t *testing.T, kubeconfig, pod, endpoint string, want int) {
	t.Helper()
	cmd := command(ctx, kubeconfig, "exec", "-n", namespace, pod, "--", "/workspace/helm-probe", endpoint)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if want == 0 && err == nil {
		return
	}
	var exit *exec.ExitError
	if want != 0 && errors.As(err, &exit) && exit.ExitCode() == want {
		return
	}
	t.Fatalf("retained executor network probe did not produce exit %d", want)
}

func quiesceLifetimeProvider(ctx context.Context, kubeconfig string) error {
	cmd := command(ctx, kubeconfig, "get", "deployment/mecatl-execution", "-n", namespace, "--ignore-not-found", "-o", "json")
	var out lifetimeOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("read owned provider deployment: %w", err)
	}
	if out.Len() == 0 {
		return nil
	}
	var deployment struct {
		Metadata struct{ Labels, Annotations map[string]string }
	}
	if err := json.Unmarshal(out.Bytes(), &deployment); err != nil {
		return errors.New("decode owned provider deployment")
	}
	if deployment.Metadata.Labels["app.kubernetes.io/managed-by"] != "Helm" || deployment.Metadata.Annotations["meta.helm.sh/release-name"] != "mecatl-execution" || deployment.Metadata.Annotations["meta.helm.sh/release-namespace"] != namespace {
		return errors.New("refuse to quiesce a foreign provider deployment")
	}
	if err := command(ctx, kubeconfig, "scale", "deployment/mecatl-execution", "-n", namespace, "--replicas=0").Run(); err != nil {
		return fmt.Errorf("quiesce owned provider: %w", err)
	}
	if err := command(ctx, kubeconfig, "wait", "--for=delete", "pod", "-n", namespace, "-l", "app.kubernetes.io/name=mecatl-execution", "--timeout=2m").Run(); err != nil {
		return fmt.Errorf("wait for provider quiescence: %w", err)
	}
	return nil
}

var errLifetimeNotQuiesced = errors.New("execution provider must be quiesced")

func helmLifetime(ctx context.Context, kubeconfig string, args ...string) ([]byte, error) {
	if helmLifecycleMutation(args) {
		versionArgs := []string{"version", "--template", "{{.Version}}"}
		cmd := fixtureCommand(ctx, "helm", versionArgs...)
		cmd.Env = cleanEnv()
		version, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("detect Helm version: %w", err)
		}
		major, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(string(version)), "v"), ".")
		if major == "4" {
			// Helm 4 defaults chart lifecycle writes to server-side apply. Use
			// client-side mode so reviewed external ConfigMap rotation and the
			// following quiesced chart update do not contend for field ownership.
			args = append(args, "--server-side=false")
		}
	}
	full := append([]string{"--kubeconfig", kubeconfig, "--kube-context", os.Getenv("MECATL_KUBE_CONTEXT"), "--namespace", namespace}, args...)
	cmd := fixtureCommand(ctx, "helm", full...)
	cmd.Env = cleanEnv()
	var out, stderr lifetimeOutput
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if bytes.Contains(stderr.Bytes(), []byte("quiesce the execution provider (scale to zero and wait for its Pods to disappear) before upgrading")) {
			return nil, errLifetimeNotQuiesced
		}
		return nil, fmt.Errorf("Helm %s failed: %w (output suppressed)", args[0], err)
	}
	return out.Bytes(), nil
}

func helmLifecycleMutation(args []string) bool {
	if len(args) == 0 || args[0] != "install" && args[0] != "upgrade" {
		return false
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "--dry-run") {
			return false
		}
	}
	return true
}

func TestHelmLifetimeSelectsClientSideWritesOnlyForHelm4(t *testing.T) {
	for _, tc := range []struct {
		name        string
		version     string
		args        []string
		wantVersion bool
		wantFlag    bool
	}{
		{name: "Helm 4 upgrade", version: "v4.0.0", args: []string{"upgrade", "mecatl-execution", "chart"}, wantVersion: true, wantFlag: true},
		{name: "Helm 3 install", version: "v3.16.4", args: []string{"install", "mecatl-execution", "chart"}, wantVersion: true},
		{name: "Helm 4 server dry run", version: "v4.0.0", args: []string{"install", "mecatl-execution", "chart", "--dry-run=server"}},
		{name: "Helm 4 read", version: "v4.0.0", args: []string{"get", "values", "mecatl-execution"}},
		{name: "Helm 4 uninstall", version: "v4.0.0", args: []string{"uninstall", "mecatl-execution"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "calls")
			script := fmt.Sprintf("#!/bin/sh\nset -eu\nprintf '%%s\\n' \"$*\" >> %q\nif [ \"$1\" = version ]; then printf '%%s\\n' %q; fi\n", marker, tc.version)
			if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MECATL_EXECUTION_K8S_TOOLBOX", "")
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			if _, err := helmLifetime(t.Context(), "fixture-kubeconfig", tc.args...); err != nil {
				t.Fatal(err)
			}
			calls, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			got := string(calls)
			if strings.Contains(got, "version --template {{.Version}}") != tc.wantVersion {
				t.Fatalf("version probe mismatch: %q", got)
			}
			if strings.Contains(got, "--server-side=false") != tc.wantFlag {
				t.Fatalf("client-side mode mismatch: %q", got)
			}
		})
	}
}

func TestLifetimeOutputRejectsOversizedArtifacts(t *testing.T) {
	var out lifetimeOutput
	if _, err := io.Copy(&out, strings.NewReader(strings.Repeat("x", 1<<20))); err != nil || out.Len() != 1<<20 {
		t.Fatal("bounded output did not accept its exact limit")
	}
	if _, err := io.Copy(&out, strings.NewReader("overflow")); err == nil || out.Len() != 1<<20 {
		t.Fatal("output exceeded its bound through io.Copy")
	}
}

type lifetimeOutput struct{ buffer bytes.Buffer }

func (b *lifetimeOutput) Len() int      { return b.buffer.Len() }
func (b *lifetimeOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *lifetimeOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("lifetime output exceeds 1 MiB")
	}
	return b.buffer.Write(p)
}
