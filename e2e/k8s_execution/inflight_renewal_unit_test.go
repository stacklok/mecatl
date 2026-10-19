//go:build kind_execution_e2e

package k8s_execution_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/stacklok/mecatl/internal/adapter/executionclient"
)

func TestControlledRenewalLiveOrdering(t *testing.T) {
	data, err := os.ReadFile("inflight_renewal_test.go")
	if err != nil {
		t.Fatal(err)
	}
	_, body, ok := strings.Cut(string(data), "func TestKindExecutionInFlightAutomaticRenewal(t *testing.T) {")
	if !ok {
		t.Fatal("live renewal test missing")
	}
	for _, step := range []string{
		"active = statusAt(renewCtx)",
		"inflightExecutorPod(pods.Items",
		"executorExec(`set -eu; if [ -f /workspace/renewal-started ]",
		"inflightRenewRequest(renewCtx",
		`server, serverErr := strconv.Atoi(certificate("execution-provider-tls", "revision"))`,
		"inflightIssuedHandshake(forward.addr, ca, pair)",
		"inflightReloadAndRelease(renewCtx",
		"return client.ReloadTLS()",
		"executorExec(inflightReleaseCommand)",
		"case <-done:",
		`Path: "after-renewal"`,
	} {
		_, remaining, found := strings.Cut(body, step)
		if !found {
			t.Fatalf("missing or reordered controlled-renewal step: %s", step)
		}
		body = remaining
	}
}

func TestControlledRenewalCommandsAreExplicitAndIsolated(t *testing.T) {
	t.Setenv("MECATL_EXECUTION_K8S_TOOLBOX", "sre")
	t.Setenv("MECATL_KUBE_CONTEXT", "kind-owned-fixture")
	t.Setenv("KUBECONFIG", "/poison/ambient")
	t.Setenv("AWS_ACCESS_KEY_ID", "poison")
	t.Setenv("HOME", t.TempDir())
	state := t.TempDir()
	kubeconfig := filepath.Join(state, "kubeconfig")
	for _, name := range []string{"execution-provider-tls", "execution-client-tls"} {
		cmd := inflightRenewCommand(t.Context(), state, kubeconfig, name)
		want := []string{"toolbox", "run", "-c", "sre", filepath.Join(state, "tools", "cmctl"), "renew", "--kubeconfig", kubeconfig, "--context", "kind-owned-fixture", "--namespace", namespace, name}
		if !slices.Equal(cmd.Args, want) || !slices.Equal(cmd.Env, cleanEnv()) || cmd.WaitDelay <= 0 {
			t.Fatalf("renew command does not use isolated explicit fixture arguments: %v", cmd.Args)
		}
		for _, entry := range cmd.Env {
			if strings.HasPrefix(entry, "AWS_") || strings.HasPrefix(entry, "KUBECONFIG=") {
				t.Fatal("ambient credentials or kubeconfig reached cmctl")
			}
		}
	}
	cmd := inflightExecutorCommand(t.Context(), kubeconfig, "exact-executor", inflightReleaseCommand)
	want := []string{"toolbox", "run", "-c", "sre", "kubectl", "--kubeconfig", kubeconfig, "--context", "kind-owned-fixture", "--request-timeout=10s", "exec", "-n", namespace, "pod/exact-executor", "-c", "executor", "--", "/bin/sh", "-c", inflightReleaseCommand}
	if !slices.Equal(cmd.Args, want) || !slices.Equal(cmd.Env, cleanEnv()) {
		t.Fatalf("release command is not narrowly scoped: %v", cmd.Args)
	}
}

func TestControlledRenewalExecutorIdentity(t *testing.T) {
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "exact", UID: "uid", Labels: map[string]string{"execution.mecatl.dev/environment": "environment"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "executor"}}}}
	if _, err := inflightExecutorPod([]corev1.Pod{pod}, "environment", "uid"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name             string
		pods             []corev1.Pod
		environment, uid string
	}{
		{"missing", nil, "environment", "uid"},
		{"duplicate", []corev1.Pod{pod, pod}, "environment", "uid"},
		{"other-environment", []corev1.Pod{pod}, "other", "uid"},
		{"replacement", []corev1.Pod{pod}, "environment", "replacement-uid"},
		{"missing-durable-uid", []corev1.Pod{pod}, "environment", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := inflightExecutorPod(tc.pods, tc.environment, tc.uid); err == nil {
				t.Fatal("accepted an unverified executor")
			}
		})
	}
}

func TestControlledRenewalWaitDetectsEarlyCompletion(t *testing.T) {
	for _, when := range []string{"before-poll", "during-poll"} {
		t.Run(when, func(t *testing.T) {
			done := make(chan struct{})
			if when == "before-poll" {
				close(done)
			}
			called := false
			err := inflightWait(t.Context(), done, func() (bool, error) {
				called = true
				close(done)
				return true, nil
			})
			if err == nil || called != (when == "during-poll") {
				t.Fatalf("early completion not detected: called=%t err=%v", called, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := inflightWait(ctx, make(chan struct{}), func() (bool, error) { t.Fatal("polled after cancellation"); return false, nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("unbounded cancelled poll: %v", err)
	}
}

func TestControlledRenewalReleaseOnlyAfterReload(t *testing.T) {
	for _, mode := range []string{"success", "reload-error", "early-completion"} {
		t.Run(mode, func(t *testing.T) {
			done := make(chan struct{})
			var order []string
			err := inflightReloadAndRelease(t.Context(), done, func() error {
				order = append(order, "reload")
				if mode == "reload-error" {
					return errors.New("invalid projection")
				}
				if mode == "early-completion" {
					close(done)
				}
				return nil
			}, func() error { order = append(order, "release"); return nil })
			want := []string{"reload"}
			if mode == "success" {
				want = append(want, "release")
			}
			if !slices.Equal(order, want) || (err == nil) != (mode == "success") {
				t.Fatalf("release order=%v err=%v", order, err)
			}
		})
	}
}

func TestControlledRenewalRetriesOnlyUnambiguousPerCertificateConflict(t *testing.T) {
	conflict := errors.New("failed to trigger issuance of Certificate execution-qualification/execution-client-tls: Operation cannot be fulfilled: the object has been modified")
	for _, mode := range []string{"success", "ambiguous", "conflict-retry", "already-issuing", "persistent-conflict"} {
		t.Run(mode, func(t *testing.T) {
			calls, checks := 0, 0
			err := inflightRenewRequest(t.Context(), func() error {
				calls++
				switch mode {
				case "ambiguous":
					return errors.New("request timed out after update")
				case "already-issuing", "persistent-conflict":
					return conflict
				case "conflict-retry":
					if calls == 1 {
						return conflict
					}
				}
				return nil
			}, func() (bool, error) { checks++; return mode == "already-issuing", nil })
			wantCalls, wantChecks := 1, 0
			if mode == "already-issuing" {
				wantChecks = 1
			}
			if mode == "conflict-retry" {
				wantCalls, wantChecks = 2, 1
			}
			if mode == "persistent-conflict" {
				wantCalls, wantChecks = 3, 2
			}
			if calls != wantCalls || checks != wantChecks || (err != nil) != (mode == "ambiguous" || mode == "persistent-conflict") {
				t.Fatalf("requests=%d checks=%d err=%v", calls, checks, err)
			}
		})
	}
}

func TestControlledRenewalBarrierTimeoutAndRelease(t *testing.T) {
	for _, release := range []bool{false, true} {
		t.Run(fmt.Sprint(release), func(t *testing.T) {
			dir := t.TempDir()
			// Keep the production shell, but shorten its self-timeout for offline CI.
			script := strings.Replace(inflightBarrierCommand, `-ge 210`, `-ge 2`, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
			cmd.Dir = dir
			cmd.Env = []string{"HOME=" + dir, "PATH=" + os.Getenv("PATH")}
			if release {
				markerDone := make(chan struct{})
				var markerErr error
				defer func() {
					cancel()
					<-markerDone
					if markerErr != nil && !errors.Is(markerErr, context.Canceled) {
						t.Errorf("release marker: %v", markerErr)
					}
				}()
				go func() {
					defer close(markerDone)
					for {
						if _, err := os.Stat(filepath.Join(dir, "renewal-started")); err == nil {
							markerErr = os.WriteFile(filepath.Join(dir, "renewal-release"), []byte("release\n"), 0600)
							return
						}
						select {
						case <-ctx.Done():
							markerErr = ctx.Err()
							return
						case <-time.After(10 * time.Millisecond):
						}
					}
				}()
			}
			out, err := cmd.CombinedOutput()
			if release {
				data, readErr := os.ReadFile(filepath.Join(dir, "renewal-writer"))
				if err != nil || readErr != nil || string(data) != "after\n" || string(out) != "renewal-terminal\n" {
					t.Fatalf("released barrier: %q %q %v %v", out, data, err, readErr)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 124 || !strings.Contains(string(out), "renewal barrier timeout") {
					t.Fatalf("barrier did not fail closed: %q %v", out, err)
				}
				if _, err := os.Stat(filepath.Join(dir, "renewal-writer")); !os.IsNotExist(err) {
					t.Fatal("timed-out barrier performed terminal mutation")
				}
			}
		})
	}
}

func TestInFlightHandshakeNegotiatesGRPCAndDrainsBeforeClose(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const serverName = "mecatl-execution.execution-qualification.svc.cluster.local"
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{serverName}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	dir := t.TempDir()
	files := executionclient.TLSFiles{CA: filepath.Join(dir, "ca.crt"), Cert: filepath.Join(dir, "tls.crt"), Key: filepath.Join(dir, "tls.key"), ServerName: serverName}
	for path, data := range map[string][]byte{files.CA: certPEM, files.Cert: certPEM, files.Key: keyPEM} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, probe := range map[string]func(string) (string, error){
		"projected": func(endpoint string) (string, error) { return inflightHandshake(endpoint, files) },
		"issued":    func(endpoint string) (string, error) { return inflightIssuedHandshake(endpoint, certPEM, pair) },
	} {
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			serverResult := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					serverResult <- err
					return
				}
				defer conn.Close()
				if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					serverResult <- err
					return
				}
				creds := credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots})
				secure, auth, err := creds.ServerHandshake(conn)
				if err != nil {
					serverResult <- err
					return
				}
				defer secure.Close()
				if auth.(credentials.TLSInfo).State.NegotiatedProtocol != "h2" {
					serverResult <- fmt.Errorf("probe did not negotiate gRPC ALPN")
					return
				}
				// Force post-handshake records to be consumed, rather than relying
				// on the timing of a small HTTP/2 settings frame in a real tunnel.
				if _, err := secure.Write(make([]byte, 1<<20)); err != nil {
					serverResult <- err
					return
				}
				_, err = io.Copy(io.Discard, secure)
				serverResult <- err
			}()
			serial, probeErr := probe(listener.Addr().String())
			listener.Close()
			serverErr := <-serverResult
			if probeErr != nil || serverErr != nil || serial != "1" {
				t.Fatalf("serial=%q probe=%v server=%v", serial, probeErr, serverErr)
			}
		})
	}
}
