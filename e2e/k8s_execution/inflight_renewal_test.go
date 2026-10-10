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

// Exercise an active provider lease across cert-manager's scheduled leaf rotation.
func TestKindExecutionInFlightAutomaticRenewal(t *testing.T) {
	if os.Getenv("MECATL_EXECUTION_QUAL_RENEWAL") != "1" {
		t.Skip("automatic renewal qualification only")
	}
	state, kubeconfig, ctx, cancel := requireProduction(t)
	defer cancel()
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}, &clientcmd.ConfigOverrides{CurrentContext: os.Getenv("MECATL_KUBE_CONTEXT")}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	k8s, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	readIssued := func() *corev1.Secret {
		secret, err := k8s.CoreV1().Secrets(namespace).Get(ctx, "execution-client-tls", metav1.GetOptions{})
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
	before := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID)
	if before.PVCUID == "" {
		t.Fatal("missing retained workspace")
	}
	certificate := func(name, field string) string {
		return kubeValue(t, ctx, kubeconfig, "get", "certificate/"+name, "-n", namespace, "-o", "jsonpath={.status."+field+"}")
	}
	var renewal time.Time
	for {
		var err error
		renewal, err = time.Parse(time.RFC3339, certificate("execution-provider-tls", "renewalTime"))
		if err != nil {
			t.Fatal(err)
		}
		if time.Until(renewal) > 40*time.Second {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("no future automatic renewal in qualification window")
		case <-time.After(10 * time.Second):
		}
	}
	serverRevision, err := strconv.Atoi(certificate("execution-provider-tls", "revision"))
	if err != nil {
		t.Fatal(err)
	}
	clientRevision, err := strconv.Atoi(certificate("execution-client-tls", "revision"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("scheduled_renewal=%s server_revision=%d client_revision=%d", renewal.UTC().Format(time.RFC3339), serverRevision, clientRevision)
	select {
	case <-ctx.Done():
		t.Fatal("renewal wait exceeded qualification deadline")
	case <-time.After(time.Until(renewal.Add(-20 * time.Second))):
	}
	runID := fmt.Sprintf("inflight-%d", time.Now().UnixNano())
	claim, err := client.AcquireRun(ctx, executionenv.RunClaimRequest{Environment: attached.Environment, Owner: owner, BindingID: binding, RunID: runID, OperationID: "acquire-" + runID, TTL: 3 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	rc := executionenv.RequestContext{Environment: claim.Environment, Owner: owner, BindingID: binding, RunID: claim.RunID, ClaimID: claim.ClaimID, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration}
	defer func() {
		if err := client.ReleaseRun(context.WithoutCancel(ctx), executionenv.RunClaimRequest{Environment: claim.Environment, Owner: owner, BindingID: binding, RunID: claim.RunID, ClaimID: claim.ClaimID, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration, OperationID: "release-" + runID}); err != nil {
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
	done := make(chan result, 1)
	go func() {
		response, err := client.StartCommand(ctx, executionenv.CommandStartRequest{Context: rc, Command: `i=0; while [ "$i" -lt 90 ]; do i=$((i+1)); printf '%s\n' "$i" > renewal-writer; sleep 1; done; printf 'renewal-terminal\n'`, TimeoutMillis: 115000})
		done <- result{response, err}
	}()
	for readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID).ActiveOperationID == "" {
		select {
		case r := <-done:
			t.Fatalf("command ended before active-operation publication: state=%s code=%s", r.response.State, remoteErrorCode(r.err))
		case <-ctx.Done():
			t.Fatal("active operation was not published")
		case <-time.After(time.Second):
		}
	}
	observed := false
	for !observed {
		select {
		case r := <-done:
			t.Fatalf("command ended before both scheduled renewals: state=%s code=%s", r.response.State, remoteErrorCode(r.err))
		case <-ctx.Done():
			t.Fatal("automatic renewal deadline during active operation")
		case <-time.After(2 * time.Second):
			server, serverErr := strconv.Atoi(certificate("execution-provider-tls", "revision"))
			peer, peerErr := strconv.Atoi(certificate("execution-client-tls", "revision"))
			if serverErr != nil || peerErr != nil {
				t.Fatalf("certificate revision unavailable: server=%v client=%v", serverErr, peerErr)
			}
			observed = server > serverRevision && peer > clientRevision
			if observed {
				t.Logf("in_flight_renewal=%s server_revision=%d client_revision=%d", time.Now().UTC().Format(time.RFC3339), server, peer)
			}
		}
	}
	// The active RPC is pinned to the original client transport. Publish the
	// cert-manager-issued projection and reload the *same* client while it drains.
	newClientSerial := oldClientSerial
	newServerSerial := oldServerSerial
	for newClientSerial == oldClientSerial || newServerSerial == oldServerSerial {
		select {
		case r := <-done:
			t.Fatalf("command ended before client transport switched: state=%s code=%s", r.response.State, remoteErrorCode(r.err))
		case <-ctx.Done():
			t.Fatal("renewed projected identities did not become available during active RPC")
		default:
		}
		secret := readIssued()
		pair, pairErr := tls.X509KeyPair(secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey])
		if pairErr != nil {
			t.Fatal("renewed client Secret has invalid pair")
		}
		leaf, parseErr := x509.ParseCertificate(pair.Certificate[0])
		if parseErr != nil {
			t.Fatal("renewed client Secret has invalid leaf")
		}
		newClientSerial = leaf.SerialNumber.String()
		if newClientSerial != oldClientSerial {
			// Handshake with the issued new pair before switching the active client.
			newServerSerial, err = inflightIssuedHandshake(forward.addr, ca, pair)
			if err != nil {
				newServerSerial = oldServerSerial
			}
		}
		if newClientSerial == oldClientSerial || newServerSerial == oldServerSerial {
			time.Sleep(time.Second)
		}
	}
	if got := publish(readIssued(), 1); got != newClientSerial {
		t.Fatal("client Secret changed between handshake and projection; refusing ambiguous reload")
	}
	if err := client.ReloadTLS(); err != nil {
		t.Fatalf("reload issued client projection during active command: %v", err)
	}
	if status := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID); status.ActiveOperationID == "" {
		t.Fatal("active command finished before client transport reload")
	}
	t.Logf("transport_reloaded_during_rpc=true old_client_serial=%s new_client_serial=%s old_server_serial=%s new_server_serial=%s", oldClientSerial, newClientSerial, oldServerSerial, newServerSerial)
	select {
	case r := <-done:
		if r.err != nil || r.response.State != executionenv.CommandSucceeded || r.response.Result.State != executionenv.CommandSucceeded || r.response.Result.ExitCode != 0 || r.response.Result.TerminalReceipt == "" || !strings.Contains(string(r.response.Result.Stdout), "renewal-terminal") {
			t.Fatalf("in-flight command terminal proof: state=%s result=%s code=%d receipt=%t err=%v", r.response.State, r.response.Result.State, r.response.Result.ExitCode, r.response.Result.TerminalReceipt != "", r.err)
		}
	case <-ctx.Done():
		t.Fatal("in-flight operation did not return a terminal result")
	}
	written, err := client.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileRead, Path: "renewal-writer"})
	if err != nil || strings.TrimSpace(string(written.Data)) != "90" {
		t.Fatalf("file mutation across renewal: value=%q err=%v", written.Data, err)
	}
	status := readExecutionStatus(t, ctx, kubeconfig, attached.Environment.ID)
	if status.FenceState == "FenceUnknown" || status.ActiveOperationID != "" || status.PVCUID != before.PVCUID {
		t.Fatalf("renewal changed durable operation/workspace: fence=%q active=%t pvc_preserved=%t", status.FenceState, status.ActiveOperationID != "", status.PVCUID == before.PVCUID)
	}
	if _, err := client.File(ctx, executionenv.FileRequest{Context: rc, Operation: executionenv.OpFileCreate, Path: "after-renewal", Data: []byte("new-call\n")}); err != nil {
		t.Fatalf("post-renewal file mutation: %v", err)
	}
}
