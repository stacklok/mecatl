//go:build kind_execution_e2e

package k8s_execution_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/stacklok/mecatl/internal/adapter/executionclient"
	"github.com/stacklok/mecatl/internal/executionenv"
)

func inflightHandshake(endpoint string, files executionclient.TLSFiles) (string, error) {
	cfg, err := executionclient.LoadTLSConfig(files)
	if err != nil {
		return "", err
	}
	return inflightTLSHandshake(endpoint, cfg)
}

func inflightTLSHandshake(endpoint string, cfg *tls.Config) (string, error) {
	cfg.NextProtos = []string{"h2"}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", endpoint, cfg)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	serial := conn.ConnectionState().PeerCertificates[0].SerialNumber.String()
	// A rejected ALPN handshake or closing over unread HTTP/2 settings can
	// reset TCP and tear down the shared kubectl tunnel, including active RPCs.
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", err
	}
	if err := conn.CloseWrite(); err != nil {
		return "", err
	}
	if _, err := io.Copy(io.Discard, conn); err != nil {
		return "", err
	}
	return serial, nil
}

func inflightIssuedHandshake(endpoint string, ca []byte, pair tls.Certificate) (string, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return "", fmt.Errorf("invalid fixture public trust")
	}
	return inflightTLSHandshake(endpoint, &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "mecatl-execution.execution-qualification.svc.cluster.local", RootCAs: pool, Certificates: []tls.Certificate{pair}})
}

const inflightBarrierCommand = `set -eu; test ! -e renewal-release; printf 'started\n' > renewal-started; i=0; while [ ! -f renewal-release ]; do if [ "$i" -ge 210 ]; then printf 'renewal barrier timeout\n' >&2; exit 124; fi; i=$((i+1)); sleep 1; done; printf 'after\n' > renewal-writer; printf 'renewal-terminal\n'`

const inflightReleaseCommand = `set -eu; test -f /workspace/renewal-started; test ! -e /workspace/renewal-release; printf 'release\n' > /workspace/renewal-release`

func inflightExecutorPod(pods []corev1.Pod, environmentID, uid string) (corev1.Pod, error) {
	if len(pods) != 1 || uid == "" {
		return corev1.Pod{}, fmt.Errorf("expected one executor with a durable Pod UID")
	}
	pod := pods[0]
	if pod.Name == "" || string(pod.UID) != uid || pod.Labels["execution.mecatl.dev/environment"] != environmentID || pod.DeletionTimestamp != nil || len(pod.Spec.Containers) != 1 || pod.Spec.Containers[0].Name != "executor" {
		return corev1.Pod{}, fmt.Errorf("executor Pod does not match the active environment")
	}
	return pod, nil
}

func inflightExecutorCommand(ctx context.Context, kubeconfig, pod, script string) *exec.Cmd {
	return command(ctx, kubeconfig, "--request-timeout=10s", "exec", "-n", namespace, "pod/"+pod, "-c", "executor", "--", "/bin/sh", "-c", script)
}

func inflightRenewCommand(ctx context.Context, state, kubeconfig, name string) *exec.Cmd {
	cmd := fixtureCommand(ctx, filepath.Join(state, "tools", "cmctl"), "renew", "--kubeconfig", kubeconfig, "--context", os.Getenv("MECATL_KUBE_CONTEXT"), "--namespace", namespace, name)
	cmd.Env = cleanEnv()
	cmd.WaitDelay = time.Second
	return cmd
}

func inflightRunning(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return fmt.Errorf("command completed before controlled renewal/reload/release")
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func inflightWait(ctx context.Context, done <-chan struct{}, ready func() (bool, error)) error {
	for {
		if err := inflightRunning(ctx, done); err != nil {
			return err
		}
		ok, err := ready()
		if err != nil {
			return err
		}
		if err := inflightRunning(ctx, done); err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-done:
			return fmt.Errorf("command completed while waiting for controlled renewal")
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Only an explicit API update conflict is retryable. A successful or ambiguous
// request is never repeated, and an already-issuing certificate is left alone.
func inflightRenewRequest(ctx context.Context, request func() error, pending func() (bool, error)) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := request()
		if err == nil || !strings.Contains(err.Error(), "failed to trigger issuance of Certificate ") || !strings.Contains(err.Error(), "the object has been modified") || attempt == 2 {
			return err
		}
		if issuing, checkErr := pending(); checkErr != nil || issuing {
			return checkErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func inflightReloadAndRelease(ctx context.Context, done <-chan struct{}, reload, release func() error) error {
	if err := inflightRunning(ctx, done); err != nil {
		return err
	}
	if err := reload(); err != nil {
		return err
	}
	if err := inflightRunning(ctx, done); err != nil {
		return err
	}
	return release()
}

// Exercise an active provider lease across controlled cert-manager leaf renewal.
// Keep the historical test name for existing selectors.
func TestKindExecutionInFlightAutomaticRenewal(t *testing.T) {
	if os.Getenv("MECATL_EXECUTION_QUAL_RENEWAL") != "1" {
		t.Skip("controlled renewal qualification only")
	}
	state, kubeconfig, parent, parentCancel := requireProduction(t)
	defer parentCancel()
	ctx, cancel := context.WithTimeout(parent, 7*time.Minute)
	defer cancel()
	ioCtx := ctx
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}, &clientcmd.ConfigOverrides{CurrentContext: os.Getenv("MECATL_KUBE_CONTEXT")}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Timeout = 10 * time.Second
	k8s, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	readIssued := func() *corev1.Secret {
		secret, err := k8s.CoreV1().Secrets(namespace).Get(ioCtx, "execution-client-tls", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return secret
	}
	projection := t.TempDir()
	files := executionclient.TLSFiles{CA: filepath.Join(projection, "ca.crt"), Cert: filepath.Join(projection, "tls.crt"), Key: filepath.Join(projection, "tls.key"), ServerName: "mecatl-execution.execution-qualification.svc.cluster.local"}
	ca, err := os.ReadFile(filepath.Join(state, "pki", "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files.CA, ca, 0600); err != nil {
		t.Fatal(err)
	}
	// Mirror the cert-manager Secret into an atomic local projection for the
	// host-side production reloadable client; no Secret is edited in Kubernetes.
	publish := func(secret *corev1.Secret, generation int) string {
		t.Helper()
		pair, err := tls.X509KeyPair(secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey])
		if err != nil {
			t.Fatal("cert-manager returned an invalid client pair")
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			t.Fatal("cert-manager returned an invalid client leaf")
		}
		name := fmt.Sprintf("..revision-%d", generation)
		dir := filepath.Join(projection, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for key, data := range map[string][]byte{"tls.crt": secret.Data[corev1.TLSCertKey], "tls.key": secret.Data[corev1.TLSPrivateKeyKey]} {
			if err := os.WriteFile(filepath.Join(dir, key), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Symlink(name, filepath.Join(projection, "..next")); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(projection, "..next"), filepath.Join(projection, "..data")); err != nil {
			t.Fatal(err)
		}
		return leaf.SerialNumber.String()
	}
	for _, name := range []string{"tls.crt", "tls.key"} {
		if err := os.Symlink(filepath.Join("..data", name), filepath.Join(projection, name)); err != nil {
			t.Fatal(err)
		}
	}
	oldClientSerial := publish(readIssued(), 0)
	forward := portForward(t, ctx, kubeconfig, "service/mecatl-execution", 8443)
	client, err := executionclient.NewWithTLSFiles(forward.addr, files)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	oldServerSerial, err := inflightHandshake(forward.addr, files)
	if err != nil {
		t.Fatalf("initial provider handshake: %v", err)
	}
	owner, binding, attached := createProductionEnvironment(t, ctx, client, "inflight-renewal")
	statusAt := func(parent context.Context) executionStatus {
		t.Helper()
		poll, stop := context.WithTimeout(parent, 10*time.Second)
		defer stop()
		return readExecutionStatus(t, poll, kubeconfig, attached.Environment.ID)
	}
	before := statusAt(ctx)
	if before.PVCUID == "" || before.PodUID == "" {
		t.Fatal("missing retained workspace or executor identity")
	}
	certificate := func(name, field string) string {
		poll, stop := context.WithTimeout(ioCtx, 10*time.Second)
		defer stop()
		return kubeValue(t, poll, kubeconfig, "get", "certificate/"+name, "-n", namespace, "-o", "jsonpath={.status."+field+"}")
	}
	serverRevision, err := strconv.Atoi(certificate("execution-provider-tls", "revision"))
	if err != nil {
		t.Fatal(err)
	}
	clientRevision, err := strconv.Atoi(certificate("execution-client-tls", "revision"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("controlled_renewal_baseline server_revision=%d client_revision=%d", serverRevision, clientRevision)
	runID := fmt.Sprintf("inflight-%d", time.Now().UnixNano())
	claim, err := client.AcquireRun(ctx, executionenv.RunClaimRequest{Environment: attached.Environment, Owner: owner, BindingID: binding, RunID: runID, OperationID: "acquire-" + runID, TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	rc := executionenv.RequestContext{Environment: claim.Environment, Owner: owner, BindingID: binding, RunID: claim.RunID, ClaimID: claim.ClaimID, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if err := client.ReleaseRun(cleanup, executionenv.RunClaimRequest{Environment: claim.Environment, Owner: owner, BindingID: binding, RunID: claim.RunID, ClaimID: claim.ClaimID, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration, OperationID: "release-" + runID}); err != nil {
			t.Errorf("release in-flight run: %v", err)
		}
	}()
	if _, err := client.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileCreate, Path: "renewal-writer", Data: []byte("before\n")}); err != nil {
		t.Fatal(err)
	}
	type result struct {
		response executionenv.CommandStartResponse
		err      error
	}
	var r result
	done := make(chan struct{})
	commandCtx, stopCommand := context.WithCancel(ctx)
	go func() {
		r.response, r.err = client.StartCommand(commandCtx, executionenv.CommandStartRequest{Context: rc, Command: inflightBarrierCommand, TimeoutMillis: 240000})
		close(done)
	}()
	defer func() { stopCommand(); <-done }()
	renewCtx, stopRenewal := context.WithTimeout(ctx, 180*time.Second)
	defer stopRenewal()
	ioCtx = renewCtx
	var active executionStatus
	if err := inflightWait(renewCtx, done, func() (bool, error) {
		active = statusAt(renewCtx)
		return active.ActiveOperationID != "", nil
	}); err != nil {
		t.Fatalf("active operation publication: %v", err)
	}
	pods, err := k8s.CoreV1().Pods(namespace).List(renewCtx, metav1.ListOptions{LabelSelector: "execution.mecatl.dev/environment=" + attached.Environment.ID})
	if err != nil {
		t.Fatal(err)
	}
	pod, err := inflightExecutorPod(pods.Items, attached.Environment.ID, active.PodUID)
	if err != nil {
		t.Fatal(err)
	}
	assertActive := func() error {
		if err := inflightRunning(renewCtx, done); err != nil {
			return err
		}
		status := statusAt(renewCtx)
		if status.ActiveOperationID != active.ActiveOperationID || status.PodUID != before.PodUID || status.PVCUID != before.PVCUID || status.Epoch != claim.Epoch || status.ActiveGrantGeneration != claim.GrantGeneration || status.FenceState == "FenceUnknown" {
			return fmt.Errorf("controlled renewal changed active operation, Pod, PVC, epoch, or grant")
		}
		return inflightRunning(renewCtx, done)
	}
	// Fixture-admin exec only checks/releases the barrier; client.File cannot run
	// concurrently with StartCommand. Never select a replacement Pod by label.
	executorExec := func(script string) ([]byte, error) {
		if err := assertActive(); err != nil {
			return nil, err
		}
		poll, stop := context.WithTimeout(renewCtx, 10*time.Second)
		defer stop()
		verify := func() error {
			current, err := k8s.CoreV1().Pods(namespace).Get(poll, pod.Name, metav1.GetOptions{})
			if err != nil {
				return err
			}
			_, err = inflightExecutorPod([]corev1.Pod{*current}, attached.Environment.ID, string(pod.UID))
			return err
		}
		if err := verify(); err != nil {
			return nil, err
		}
		out, err := inflightExecutorCommand(poll, kubeconfig, pod.Name, script).CombinedOutput()
		if verifyErr := verify(); verifyErr != nil {
			return nil, verifyErr
		}
		return out, err
	}
	if err := inflightWait(renewCtx, done, func() (bool, error) {
		out, err := executorExec(`set -eu; if [ -f /workspace/renewal-started ]; then printf 'started'; fi`)
		return string(out) == "started", err
	}); err != nil {
		t.Fatalf("command started marker: %v", err)
	}
	t.Logf("controlled_renewal_active operation=%s pod=%s pod_uid=%s epoch=%d grant=%d", active.ActiveOperationID, pod.Name, pod.UID, claim.Epoch, claim.GrantGeneration)
	for _, cert := range []struct {
		name     string
		revision int
	}{{"execution-provider-tls", serverRevision}, {"execution-client-tls", clientRevision}} {
		err := inflightRenewRequest(renewCtx, func() error {
			if err := assertActive(); err != nil {
				return err
			}
			requestCtx, stop := context.WithTimeout(renewCtx, 15*time.Second)
			defer stop()
			out, err := inflightRenewCommand(requestCtx, state, kubeconfig, cert.name).CombinedOutput()
			if err != nil {
				return fmt.Errorf("cmctl renew %s: %w: %s", cert.name, err, out)
			}
			return assertActive()
		}, func() (bool, error) {
			if err := assertActive(); err != nil {
				return false, err
			}
			revision, err := strconv.Atoi(certificate(cert.name, "revision"))
			if err != nil {
				return false, err
			}
			return revision > cert.revision || certificate(cert.name, `conditions[?(@.type=="Issuing")].status`) == "True", nil
		})
		if err != nil {
			t.Fatalf("controlled renewal request: %v", err)
		}
	}
	if err := inflightWait(renewCtx, done, func() (bool, error) {
		if err := assertActive(); err != nil {
			return false, err
		}
		server, serverErr := strconv.Atoi(certificate("execution-provider-tls", "revision"))
		peer, peerErr := strconv.Atoi(certificate("execution-client-tls", "revision"))
		if serverErr != nil || peerErr != nil {
			return false, fmt.Errorf("certificate revision unavailable: server=%v client=%v", serverErr, peerErr)
		}
		return server > serverRevision && peer > clientRevision, nil
	}); err != nil {
		t.Fatalf("controlled issuance revisions: %v", err)
	}
	// The active RPC is pinned to the original client transport. Publish the
	// cert-manager-issued projection and reload the *same* client while it drains.
	newClientSerial := oldClientSerial
	newServerSerial := oldServerSerial
	if err := inflightWait(renewCtx, done, func() (bool, error) {
		if err := assertActive(); err != nil {
			return false, err
		}
		secret := readIssued()
		pair, pairErr := tls.X509KeyPair(secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey])
		if pairErr != nil {
			return false, fmt.Errorf("renewed client Secret has invalid pair")
		}
		leaf, parseErr := x509.ParseCertificate(pair.Certificate[0])
		if parseErr != nil {
			return false, fmt.Errorf("renewed client Secret has invalid leaf")
		}
		newClientSerial = leaf.SerialNumber.String()
		if newClientSerial != oldClientSerial {
			// Handshake with the issued new pair before switching the active client.
			newServerSerial, err = inflightIssuedHandshake(forward.addr, ca, pair)
			if err != nil {
				newServerSerial = oldServerSerial
			}
		}
		return newClientSerial != oldClientSerial && newServerSerial != oldServerSerial, nil
	}); err != nil {
		t.Fatalf("renewed projected identities during active RPC: %v", err)
	}
	if err := inflightReloadAndRelease(renewCtx, done, func() error {
		if err := assertActive(); err != nil {
			return err
		}
		if got := publish(readIssued(), 1); got != newClientSerial {
			return fmt.Errorf("client Secret changed between handshake and projection; refusing ambiguous reload")
		}
		return client.ReloadTLS()
	}, func() error {
		if err := assertActive(); err != nil {
			return err
		}
		t.Logf("transport_reloaded_during_rpc=true old_client_serial=%s new_client_serial=%s old_server_serial=%s new_server_serial=%s", oldClientSerial, newClientSerial, oldServerSerial, newServerSerial)
		_, err := executorExec(inflightReleaseCommand)
		return err
	}); err != nil {
		t.Fatalf("reload and release controlled renewal barrier: %v", err)
	}
	select {
	case <-done:
		if r.err != nil || r.response.CommandID == "" || r.response.CommandID != r.response.Result.CommandID || r.response.State != executionenv.CommandSucceeded || r.response.Result.State != executionenv.CommandSucceeded || r.response.Result.ExitCode != 0 || r.response.Result.TerminalReceipt == "" || !strings.Contains(string(r.response.Result.Stdout), "renewal-terminal") {
			t.Fatalf("in-flight command terminal proof: state=%s result=%s code=%d receipt=%t err=%v", r.response.State, r.response.Result.State, r.response.Result.ExitCode, r.response.Result.TerminalReceipt != "", r.err)
		}
	case <-ctx.Done():
		t.Fatal("in-flight operation did not return a terminal result")
	}
	written, err := client.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileRead, Path: "renewal-writer"})
	if err != nil || strings.TrimSpace(string(written.Data)) != "after" {
		t.Fatalf("file mutation across renewal: value=%q err=%v", written.Data, err)
	}
	status := statusAt(ctx)
	if status.FenceState == "FenceUnknown" || status.ActiveOperationID != "" || status.PVCUID != before.PVCUID || status.PodUID != before.PodUID || status.Epoch != claim.Epoch || status.ActiveGrantGeneration != claim.GrantGeneration {
		t.Fatalf("renewal changed durable operation/workspace: fence=%q active=%t pvc_preserved=%t", status.FenceState, status.ActiveOperationID != "", status.PVCUID == before.PVCUID)
	}
	if _, err := client.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileCreate, Path: "after-renewal", Data: []byte("new-call\n")}); err != nil {
		t.Fatalf("post-renewal file mutation: %v", err)
	}
}
