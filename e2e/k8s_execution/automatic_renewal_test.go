//go:build kind_execution_e2e

package k8s_execution_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func renewalNeedsIntermediateJourney(cycle int) bool { return cycle >= 1 && cycle <= 2 }

func TestRenewalJourneyBudget(t *testing.T) {
	for _, tc := range []struct {
		cycles int
		want   int
	}{{1, 1}, {2, 2}, {3, 2}, {12, 2}} {
		used := 0
		for cycle := 1; cycle <= tc.cycles; cycle++ {
			if renewalNeedsIntermediateJourney(cycle) {
				used++
			}
		}
		if used != tc.want || used+1 > 3 {
			t.Fatalf("cycles=%d: intermediate=%d, want=%d with one post-expiry journey reserved", tc.cycles, used, tc.want)
		}
	}
}

// Run separately after the serial qualification: no clock changes, Secret edits,
// cmctl renewals, or workload restarts occur during the observed renewals.
func TestKindExecutionAutomaticCertificateRenewal(t *testing.T) {
	if os.Getenv("MECATL_EXECUTION_QUAL_RENEWAL") != "1" {
		t.Skip("long-running automatic renewal qualification only")
	}
	state, kubeconfig, _, cancel := requireProduction(t)
	cancel()
	ctx, stop := context.WithTimeout(context.Background(), 78*time.Minute)
	defer stop()
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}, &clientcmd.ConfigOverrides{CurrentContext: os.Getenv("MECATL_KUBE_CONTEXT")}).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	k8s, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	initialPEM, err := os.ReadFile(filepath.Join(state, "pki", "provider.crt"))
	if err != nil {
		t.Fatal(err)
	}
	initialBlock, _ := pem.Decode(initialPEM)
	if initialBlock == nil {
		t.Fatal("missing initial server certificate")
	}
	initial, err := x509.ParseCertificate(initialBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("initial_server_serial=%s initial_not_after=%s", initial.SerialNumber, initial.NotAfter.UTC().Format(time.RFC3339))
	if !time.Now().Before(initial.NotAfter) {
		t.Fatal("initial certificate expired before renewal qualification began")
	}
	cm := lifetimeConfigMap(ctx, t, kubeconfig, "mecatl-execution-security-authority")
	ledger := cm.Data["state.json"]
	tokenForward := portForward(t, ctx, kubeconfig, "service/oidc-issuer", 8443)
	defer tokenForward.stop()
	pki := filepath.Join(state, "pki")
	token := func() string { return fixtureToken(t, ctx, tokenForward.addr, pki, "alice") }
	applyMockScript(t, ctx, kubeconfig, "mock-script.json")
	runKubectl(t, ctx, kubeconfig, "rollout", "restart", "deployment/mecak8s", "-n", namespace)
	runKubectl(t, ctx, kubeconfig, "rollout", "status", "deployment/mecak8s", "-n", namespace, "--timeout=240s")
	agentForward := portForward(t, ctx, kubeconfig, "service/mecak8s", 8081)
	defer agentForward.stop()
	session := createSession(t, ctx, agentForward.addr, token(), os.Getenv("MECATL_EXECUTION_TEMPLATE_REVISION"))
	assertMockJourney(t, prompt(t, ctx, agentForward.addr, session, token(), "run scripted qualification before certificate renewal"))
	ref := environmentForBinding(t, ctx, kubeconfig, session)
	pvc := readExecutionStatus(t, ctx, kubeconfig, ref.ID).PVCUID
	if pvc == "" {
		t.Fatal("missing preserved workspace PVC")
	}
	applyReattachScript(t, ctx, kubeconfig, state)
	runKubectl(t, ctx, kubeconfig, "rollout", "restart", "deployment/mecak8s", "-n", namespace)
	runKubectl(t, ctx, kubeconfig, "rollout", "status", "deployment/mecak8s", "-n", namespace, "--timeout=240s")
	agentForward.stop()
	agentForward = portForward(t, ctx, kubeconfig, "service/mecak8s", 8081)
	defer agentForward.stop()
	previousServer := ""
	previousClient := ""
	changes := 0
	for changes < 2 || time.Now().Before(initial.NotAfter.Add(time.Minute)) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("automatic renewal deadline: %v; observed=%d", err, changes)
		}
		secret, err := k8s.CoreV1().Secrets(namespace).Get(ctx, "execution-client-tls", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		pair, err := tls.X509KeyPair(secret.Data[corev1.TLSCertKey], secret.Data[corev1.TLSPrivateKeyKey])
		if err != nil {
			t.Fatal("issued client Secret has invalid TLS pair")
		}
		clientCert, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		pool := x509.NewCertPool()
		ca, err := os.ReadFile(filepath.Join(pki, "ca.crt"))
		if err != nil || !pool.AppendCertsFromPEM(ca) {
			t.Fatal("fixture public CA invalid")
		}
		providerForward := portForward(t, ctx, kubeconfig, "service/mecatl-execution", 8443)
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", providerForward.addr, &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "mecatl-execution.execution-qualification.svc.cluster.local", RootCAs: pool, Certificates: []tls.Certificate{pair}})
		if err != nil {
			providerForward.stop()
			t.Fatalf("provider handshake after renewal: %v", err)
		}
		peer := conn.ConnectionState().PeerCertificates[0]
		conn.Close()
		providerForward.stop()
		serverSerial := peer.SerialNumber.String()
		clientSerial := clientCert.SerialNumber.String()
		if previousServer == "" {
			previousServer = serverSerial
			previousClient = clientSerial
			t.Logf("baseline_server_serial=%s baseline_client_serial=%s", serverSerial, clientSerial)
		}
		if serverSerial != previousServer && clientSerial != previousClient {
			changes++
			t.Logf("automatic_renewal=%d server_serial=%s server_not_after=%s client_serial=%s client_not_after=%s elapsed_since_initial_expiry=%s", changes, serverSerial, peer.NotAfter.UTC().Format(time.RFC3339), clientSerial, clientCert.NotAfter.UTC().Format(time.RFC3339), time.Since(initial.NotAfter).Round(time.Second))
			previousServer, previousClient = serverSerial, clientSerial
			if renewalNeedsIntermediateJourney(changes) {
				agentForward.stop()
				agentForward = portForward(t, ctx, kubeconfig, "service/mecak8s", 8081)
				tokenForward.stop()
				tokenForward = portForward(t, ctx, kubeconfig, "service/oidc-issuer", 8443)
				body := promptEventually(t, ctx, agentForward.addr, session, token(), fmt.Sprintf("verify exact reattachment after renewal %d", changes))
				for _, marker := range []string{"reattach-read", "reattach-shell", "beta", "REMOTE_EXECUTION_REATTACH_COMPLETE"} {
					if !bytes.Contains(body, []byte(marker)) {
						t.Fatalf("renewal %d missing tool proof %s", changes, marker)
					}
				}
			}
			if got := readExecutionStatus(t, ctx, kubeconfig, ref.ID).PVCUID; got != pvc {
				t.Fatal("workspace PVC changed during automatic renewal")
			}
			if current := lifetimeConfigMap(ctx, t, kubeconfig, "mecatl-execution-security-authority").Data["state.json"]; current != ledger {
				t.Fatal("policy generation changed during ordinary leaf renewal")
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("automatic renewal timeout")
		case <-time.After(20 * time.Second):
		}
	}
	agentForward.stop()
	agentForward = portForward(t, ctx, kubeconfig, "service/mecak8s", 8081)
	tokenForward.stop()
	tokenForward = portForward(t, ctx, kubeconfig, "service/oidc-issuer", 8443)
	body := promptEventually(t, ctx, agentForward.addr, session, token(), "verify remote tools after initial certificate expiry")
	for _, marker := range []string{"reattach-read", "reattach-shell", "REMOTE_EXECUTION_REATTACH_COMPLETE"} {
		if !bytes.Contains(body, []byte(marker)) {
			t.Fatalf("post-expiry session omitted tool proof %s", marker)
		}
	}
	if got := readExecutionStatus(t, ctx, kubeconfig, ref.ID).PVCUID; got != pvc {
		t.Fatal("post-expiry workspace PVC changed")
	}
	t.Logf("PASS automatic renewals=%d initial_expiry_passed=true unchanged_policy=true unchanged_workspace=true", changes)
}
