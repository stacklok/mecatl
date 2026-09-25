//go:build kind_execution_e2e

package k8s_execution_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
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

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"

	"github.com/stacklok/mecatl/internal/adapter/executionclient"
	"github.com/stacklok/mecatl/internal/executionenv"
)

// Last in the serial production suite: rotation and migration share this
// throwaway release. Always restore its current authority, never the initial one.
func TestKindExecutionProductionHelmLifetime(t *testing.T) {
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
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
	built, err := client.StartCommand(ctx, executionenv.CommandStartRequest{Context: rc, Command: "go build -o helm-probe helm-probe.go", TimeoutMillis: 30000})
	if err != nil || built.State != executionenv.CommandSucceeded || built.Result.ExitCode != 0 {
		t.Fatal("build lifetime probe failed")
	}
	assertRetiredFixtureKeyDenied(ctx, t, client, state, rc, "helm-sentinel", "retained-helm-data\n")
	release()
	// Occupy the single-slot profile; its reservation must still constrain Ensure
	// after adoption, rather than only matching a ConfigMap annotation.
	quotaBinding := fmt.Sprintf("helm-quota-%d", time.Now().UnixNano())
	quota, err := client.Ensure(ctx, quotaBinding, "quota-cas", owner, "ensure-"+quotaBinding)
	if err != nil {
		t.Fatal("reserve lifetime capacity:", remoteErrorCode(err))
	}
	if err := client.CommitReference(ctx, executionenv.ReferenceRequest{Environment: quota.Environment, Owner: owner, BindingID: quotaBinding, OperationID: "ensure-" + quotaBinding}); err != nil {
		t.Fatal("commit lifetime capacity:", remoteErrorCode(err))
	}
	waitReady(t, ctx, client, owner, quotaBinding, quota.Environment)

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
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 7*time.Minute)
		defer cleanupCancel()
		requireOwnedHelmFixture(t, state, kubeconfig)
		if err := restore(cleanupCtx); err != nil {
			t.Error("restore owned release/current authority failed:", err)
		}
	})
	capacityBefore := lifetimeConfigMap(ctx, t, kubeconfig, "mecatl-execution-profile-allocations")
	authorityBefore := lifetimeConfigMap(ctx, t, kubeconfig, "mecatl-execution-security-authority")
	var priorHistory struct {
		Fingerprints map[string]string `json:"fingerprints"`
	}
	if err := json.Unmarshal([]byte(authorityBefore.Data["state.json"]), &priorHistory); err != nil || len(priorHistory.Fingerprints) == 0 {
		t.Fatal("missing pre-upgrade authority history")
	}
	policiesBefore := lifetimePolicies(ctx, t, kubeconfig)
	liveLedgers := map[string]corev1.ConfigMap{capacityBefore.Name: capacityBefore, authorityBefore.Name: authorityBefore, current.Name: current}
	liveLedgers["mecatl-execution-profiles"] = lifetimeConfigMap(ctx, t, kubeconfig, "mecatl-execution-profiles")
	waitProviderReadyReplicas(t, ctx, kubeconfig, 2)
	if _, err := helmLifetime(ctx, kubeconfig, "upgrade", "mecatl-execution", chart, "-f", valuesPath, "--wait", "--timeout=4m"); !errors.Is(err, errLifetimeNotQuiesced) {
		t.Fatal("live same-release upgrade did not reject provider writers with the quiescence error")
	}
	assertLifetimeRetained(ctx, t, kubeconfig, liveLedgers, policiesBefore)
	if status := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID); status != before || envUID != kubeValue(t, ctx, kubeconfig, "get", "executionenvironment", attached.Environment.ID, "-n", namespace, "-o", "jsonpath={.metadata.uid}") || before.PodUID != kubeValue(t, ctx, kubeconfig, "get", "pod", pod, "-n", namespace, "-o", "jsonpath={.metadata.uid}") || before.PVCUID != kubeValue(t, ctx, kubeconfig, "get", "pvc", "-n", namespace, "-l", "execution.mecatl.dev/environment="+attached.Environment.ID, "-o", "jsonpath={.items[*].metadata.uid}") {
		t.Fatal("rejected live upgrade changed runtime identity or status")
	}
	// Quiesce all writers before lookup snapshots are rendered into the upgrade.
	if err := restore(ctx); err != nil {
		t.Fatal("compatible Helm upgrade failed:", err)
	}
	waitProviderReadyReplicas(t, ctx, kubeconfig, 2)
	ledgers := map[string]corev1.ConfigMap{}
	for _, name := range []string{"mecatl-execution-security-authority", "mecatl-execution-profile-allocations", "mecatl-execution-profiles", "mecatl-execution-security-manifest"} {
		ledgers[name] = lifetimeConfigMap(ctx, t, kubeconfig, name)
	}
	var highWater struct {
		Generation   uint64            `json:"generation"`
		Fingerprints map[string]string `json:"fingerprints"`
	}
	if err := json.Unmarshal([]byte(ledgers["mecatl-execution-security-authority"].Data["state.json"]), &highWater); err != nil || highWater.Generation != uint64(generation+1) || !reflect.DeepEqual(highWater.Fingerprints, priorHistory.Fingerprints) {
		t.Fatal("upgrade did not preserve key history and publish higher authority")
	}
	if capacityAfter := ledgers[capacityBefore.Name]; capacityAfter.UID != capacityBefore.UID || !reflect.DeepEqual(capacityAfter.Data, capacityBefore.Data) {
		t.Fatal("compatible upgrade changed capacity reservations")
	}
	policies := lifetimePolicies(ctx, t, kubeconfig)
	if len(policies) < 2 {
		t.Fatal("workload policies missing before uninstall")
	}
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
	// An incompatible profile is refused before a new release can mutate anything.
	if _, err := helmLifetime(ctx, kubeconfig, "install", "mecatl-execution", chart, "-f", valuesPath, "--set", "profiles.go.maxEnvironments=30", "--dry-run=server"); err == nil {
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
	assertRetiredFixtureKeyDenied(ctx, t, reattachedClient, state, rc, "helm-sentinel", "retained-helm-data\n")
	release()
	if _, err := reattachedClient.Ensure(ctx, quotaBinding+"-excess", "quota-cas", owner, "ensure-"+quotaBinding+"-excess"); !isRemoteCode(err, executionenv.CodeResourceExhausted) {
		t.Fatal("retained capacity did not reject excess allocation:", remoteErrorCode(err))
	}

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
	// Retain the test's data by default, as the chart does; no forced cleanup or
	// finalizer removal. The explicit test-cluster owner controls final disposal.
}

// Quiesced upgrades can outlive a grant/claim. Re-sign the CURRENT claim with
// the fixture's retired k1, before and after adoption: expiry, released claims,
// and stale epochs cannot masquerade as durable authority rejection.
func assertRetiredFixtureKeyDenied(ctx context.Context, t *testing.T, client *executionclient.Client, state string, rc executionenv.RequestContext, path, want string) {
	t.Helper()
	old := resignFixtureGrant(t, rc, filepath.Join(state, "pki", "grant-key.pem"), "k1", rc.GrantGeneration)
	waitFileContent(t, ctx, client, rc, path, want, "current authority before retired-key probe")
	if _, err := client.File(ctx, executionenv.FileRequest{Context: old, Operation: executionenv.OpFileRead, Path: path}); !isRemoteCode(err, executionenv.CodePermissionDenied) {
		var remote *executionenv.Error
		t.Fatalf("retired signing authority rejection: errorCode=%s retryable=%t", remoteErrorCode(err), errors.As(err, &remote) && remote.Retryable)
	}
	waitFileContent(t, ctx, client, rc, path, want, "current authority after retired-key probe")
}

func resignFixtureGrant(t *testing.T, rc executionenv.RequestContext, keyPath, keyID string, generation uint64) executionenv.RequestContext {
	t.Helper()
	parts := strings.Split(rc.Grant, ".")
	if len(parts) != 3 {
		t.Fatal("invalid current fixture grant")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal("decode current fixture grant")
	}
	var claims executionenv.GrantClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal("decode current fixture claims")
	}
	material, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal("read test-owned signing key")
	}
	block, _ := pem.Decode(material)
	if block == nil {
		t.Fatal("decode test-owned signing key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal("parse test-owned signing key")
	}
	private, ok := key.(ed25519.PrivateKey)
	if !ok {
		t.Fatal("fixture signing key is not Ed25519")
	}
	claims.KeyID = keyID
	claims.GrantGeneration = generation
	probe := rc
	probe.GrantGeneration = generation
	probe.Grant, err = executionenv.SignGrant(private, claims)
	if err != nil {
		t.Fatal("sign fixture grant")
	}
	verifier := executionenv.GrantVerifier{Keys: map[string]ed25519.PublicKey{keyID: private.Public().(ed25519.PublicKey)}, Issuer: claims.Issuer, Audience: claims.Audience, MaxLifetime: time.Minute}
	if _, err := verifier.Verify(probe.Grant, executionenv.GrantExpectation{Client: claims.Client, OwnerHash: claims.OwnerHash, BindingID: rc.BindingID, RunID: rc.RunID, ClaimID: rc.ClaimID, Environment: rc.Environment, Epoch: rc.Epoch, GrantGeneration: generation, Operation: executionenv.OpFileRead}); err != nil {
		t.Fatal("fixture probe is not cryptographically valid and current")
	}
	return probe
}

func TestResignFixtureGrantChangesOnlyRequestedAuthority(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal("marshal synthetic signing key")
	}
	keyPath := filepath.Join(t.TempDir(), "synthetic.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal("write synthetic signing key")
	}
	now := time.Now().UTC().Truncate(time.Second)
	claims := executionenv.GrantClaims{KeyID: "k2", Issuer: "fixture", Audience: "fixture", Client: "fixture-client", OwnerHash: "fixture-owner", BindingID: "binding", RunID: "run", ClaimID: "claim", Environment: executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, Epoch: 3, GrantGeneration: 2, Operations: []executionenv.Operation{executionenv.OpFileRead}, NotBefore: now.Add(-time.Second), ExpiresAt: now.Add(30 * time.Second), Nonce: "fixture-nonce"}
	grant, err := executionenv.SignGrant(key, claims)
	if err != nil {
		t.Fatal("sign synthetic control")
	}
	rc := executionenv.RequestContext{Environment: claims.Environment, Owner: executionenv.Owner{Issuer: "issuer", Subject: "subject"}, BindingID: claims.BindingID, RunID: claims.RunID, ClaimID: claims.ClaimID, Epoch: claims.Epoch, GrantGeneration: claims.GrantGeneration, Grant: grant}
	for _, probe := range []struct {
		keyID      string
		generation uint64
	}{{"k2", 1}, {"k1", 2}} {
		resigned := resignFixtureGrant(t, rc, keyPath, probe.keyID, probe.generation)
		verifier := executionenv.GrantVerifier{Keys: map[string]ed25519.PublicKey{probe.keyID: key.Public().(ed25519.PublicKey)}, Issuer: claims.Issuer, Audience: claims.Audience, MaxLifetime: time.Minute}
		got, err := verifier.Verify(resigned.Grant, executionenv.GrantExpectation{Client: claims.Client, OwnerHash: claims.OwnerHash, BindingID: rc.BindingID, RunID: rc.RunID, ClaimID: rc.ClaimID, Environment: rc.Environment, Epoch: rc.Epoch, GrantGeneration: probe.generation, Operation: executionenv.OpFileRead})
		want := claims
		want.KeyID, want.GrantGeneration = probe.keyID, probe.generation
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("probe changed claims beyond key ID and generation")
		}
		resigned.Grant, resigned.GrantGeneration = rc.Grant, rc.GrantGeneration
		if resigned != rc {
			t.Fatal("probe changed request identity")
		}
	}
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
	full := append([]string{"--kubeconfig", kubeconfig, "--kube-context", os.Getenv("MECATL_KUBE_CONTEXT"), "--namespace", namespace}, args...)
	cmd := exec.CommandContext(ctx, "helm", full...)
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
