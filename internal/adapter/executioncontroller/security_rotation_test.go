package executioncontroller

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestSecurityReloadTLSWithoutPolicyGenerationChange(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	ca, cert, key := syntheticPKI(t, now)
	manifest := securityManifest{Version: 1, Generation: 1, TLS: securityTLSManifest{CertificateFile: "tls.crt", PrivateKeyFile: "tls.key", ClientCAFile: "clients.pem"}, Clients: []securityClientManifest{{URI: "spiffe://example/client", MayAttestOwner: true}}}
	writeRotationMaterial(t, dir, map[string][]byte{"tls.crt": cert, "tls.key": key, "clients.pem": ca})
	path := filepath.Join(dir, "manifest.json")
	writeManifest(t, path, manifest)
	kube := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "authority", Namespace: "ns"}})
	manager := NewSecurityManager(path, dir, "ns", "authority", kube)
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := manager.state.Load().snapshot
	newCA, newCert, newKey := syntheticPKI(t, now)
	writeRotationMaterial(t, dir, map[string][]byte{"tls.crt": newCert, "tls.key": newKey, "clients.pem": newCA})
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	second := manager.state.Load().snapshot
	if second == first || second.generation != first.generation || second.digest != first.digest || !manager.CheckReady(t.Context()) {
		t.Fatal("renewed TLS material changed policy publication or became unavailable")
	}
	writeRotationMaterial(t, dir, map[string][]byte{"tls.key": []byte("broken")})
	if err := manager.Reload(t.Context()); err == nil || manager.Ready() {
		t.Fatal("incomplete TLS rotation did not fail closed")
	}
}

func TestSecurityProjectedSwitchDuringActiveOperation(t *testing.T) {
	for _, mode := range []string{"renewal", "policy and TLS", "separate policy", "stable mismatched key", "switch to mismatched key", "continuous switches"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				dir := t.TempDir()
				now := time.Now().UTC()
				ca, cert, key, issued := rpcSecurityPKI(t, now, "spiffe://example/client")
				_, nextCert, nextKey := syntheticPKI(t, now)
				manifest := securityManifest{Version: 1, Generation: 1, TLS: securityTLSManifest{CertificateFile: "tls.crt", PrivateKeyFile: "tls.key", ClientCAFile: "clients.pem"}, Clients: []securityClientManifest{{URI: "spiffe://example/client", MayAttestOwner: true, ExecutionTemplates: []string{"go"}}}}
				policyDir := dir
				if mode == "separate policy" {
					policyDir = t.TempDir()
				}
				for _, generation := range []string{"..first", "..second"} {
					if err := os.Mkdir(filepath.Join(dir, generation), 0700); err != nil {
						t.Fatal(err)
					}
					if policyDir != dir {
						if err := os.Mkdir(filepath.Join(policyDir, generation), 0700); err != nil {
							t.Fatal(err)
						}
					}
					writeRotationMaterial(t, filepath.Join(dir, generation), map[string][]byte{"tls.crt": cert, "tls.key": key, "clients.pem": ca})
					writeManifest(t, filepath.Join(policyDir, generation, "manifest.json"), manifest)
				}
				writeRotationMaterial(t, filepath.Join(dir, "..second"), map[string][]byte{"tls.crt": nextCert, "tls.key": nextKey})
				if mode == "policy and TLS" || mode == "separate policy" {
					manifest.Generation++
					manifest.Clients[0].Administrator = true
					writeManifest(t, filepath.Join(policyDir, "..second", "manifest.json"), manifest)
				}
				for _, name := range []string{"tls.crt", "tls.key", "clients.pem"} {
					if err := os.Symlink(filepath.Join("..data", name), filepath.Join(dir, name)); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(filepath.Join("..data", "manifest.json"), filepath.Join(policyDir, "manifest.json")); err != nil {
					t.Fatal(err)
				}
				switchSecurityProjection(t, dir, "..first")
				if policyDir != dir {
					switchSecurityProjection(t, policyDir, "..first")
				}
				kube := fake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "authority", Namespace: "ns"}})
				manager := NewSecurityManager(filepath.Join(policyDir, "manifest.json"), dir, "ns", "authority", kube)
				if err := manager.Reload(t.Context()); err != nil {
					t.Fatal(err)
				}
				leaf, err := x509.ParseCertificate(issued.Certificate[0])
				if err != nil {
					t.Fatal(err)
				}
				env := runFixtureEnvironment(1, 1)
				_ = unstructured.SetNestedField(env.Object, hashText("spiffe://example/client"), "spec", "clientHash")
				_ = unstructured.SetNestedField(env.Object, hashText("spiffe://example/client"), "status", "activeRun", "clientHash")
				resource := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
				executor := newLeaseBlockingExecutor()
				defer executor.unblock()
				store := NewStore(resource, "ns", testProfiles(), executor).WithSecurityManager(manager)
				ctx := peer.NewContext(t.Context(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}}})
				done := make(chan error, 1)
				go func() {
					resp, err := store.execute(ctx, "spiffe://example/client", "owner", operationRequestContext(), executionenv.ExecutorRequest{Operation: executionenv.OpCommandStart, Command: "true"})
					if err == nil && (resp.Command == nil || resp.Command.State != executionenv.CommandSucceeded || resp.Command.TerminalReceipt != "confirmed") {
						err = errors.New("missing successful terminal receipt")
					}
					done <- err
				}()
				select {
				case <-executor.started:
				case err := <-done:
					t.Fatalf("operation failed before dispatch: %v", err)
				}
				reads := 0
				manager.readFile = func(root *os.Root, name string) ([]byte, error) {
					data, err := readRootFile(root, name)
					if name == "tls.crt" {
						reads++
						if mode == "continuous switches" {
							switchSecurityProjection(t, dir, []string{"..first", "..second"}[reads%2])
						} else if reads == 1 && mode != "stable mismatched key" {
							switchSecurityProjection(t, policyDir, "..second")
						}
					}
					return data, err
				}
				invalid := mode == "stable mismatched key" || mode == "switch to mismatched key" || mode == "continuous switches"
				switch mode {
				case "stable mismatched key":
					writeRotationMaterial(t, filepath.Join(dir, "..first"), map[string][]byte{"tls.key": nextKey})
				case "switch to mismatched key":
					writeRotationMaterial(t, filepath.Join(dir, "..second"), map[string][]byte{"tls.key": key})
				}
				err = manager.Reload(t.Context())
				if invalid {
					if err == nil || manager.Ready() || manager.CheckReady(t.Context()) {
						t.Fatal("invalid projection retained authority")
					}
				} else {
					if err != nil || !manager.CheckReady(t.Context()) {
						t.Fatalf("coherent reload failed: %v", err)
					}
					snapshot := manager.state.Load().snapshot
					if snapshot.generation != manifest.Generation || snapshot.clients["spiffe://example/client"].Administrator != manifest.Clients[0].Administrator {
						t.Fatal("reload mixed policy and TLS generations")
					}
					wantCert, wantKey := nextCert, nextKey
					if mode == "separate policy" {
						wantCert, wantKey = cert, key
					}
					pair, err := tls.X509KeyPair(wantCert, wantKey)
					if err != nil || !bytes.Equal(snapshot.tlsConfig.Certificates[0].Certificate[0], pair.Certificate[0]) {
						t.Fatal("reload did not publish the current TLS generation")
					}
				}
				wantReads := 2
				switch mode {
				case "stable mismatched key":
					wantReads = 1
				case "continuous switches":
					wantReads = 3
				}
				if reads != wantReads {
					t.Fatalf("candidate attempts = %d, want %d", reads, wantReads)
				}
				executor.unblock()
				err = <-done
				wantFence := fenceHealthy
				if invalid {
					wantFence = "FenceUnknown"
					var fenced *executionenv.Error
					if !errors.As(err, &fenced) || fenced.Code != executionenv.CodeFenceUnknown {
						t.Fatalf("invalid authority completion: %v", err)
					}
				} else if err != nil {
					t.Fatalf("renewal lost active operation: %v", err)
				}
				current, err := resource.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
				if err != nil || textNested(current.Object, "status", "fenceState") != wantFence {
					t.Fatalf("unexpected durable fence: %v", err)
				}
				if !invalid && textNested(current.Object, "status", "activeOperation", "id") != "" {
					t.Fatal("successful operation was not cleared")
				}
			})
		})
	}
}

func TestSecurityTLSReadsStayRootConfined(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	writeRotationMaterial(t, outside, map[string][]byte{"tls.key": []byte("outside sentinel")})
	if err := os.Symlink(filepath.Join(outside, "tls.key"), filepath.Join(dir, "escape.key")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..data", "tls.key"), filepath.Join(dir, "tls.key")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"../tls.key", filepath.Join(outside, "tls.key"), "nested/tls.key", ".", "..", "escape.key", "tls.key"} {
		if _, err := readRootFile(root, name); err == nil {
			t.Errorf("security read accepted an escaping or non-basename path: %q", name)
		}
	}
}

func switchSecurityProjection(t *testing.T, dir, generation string) {
	t.Helper()
	if err := os.Symlink(generation, filepath.Join(dir, "..data_tmp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "..data_tmp"), filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
}

func writeRotationMaterial(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
