package executioncontroller

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"sync/atomic"
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
	kubeFake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestOperationOriginalPeerWithdrawal(t *testing.T) {
	for _, mode := range []string{"expired", "CA removed", "policy generation", "API unavailable", "ordinary renewal"} {
		for _, phase := range []string{"admission", "command", "completion"} {
			admission, completion := phase == "admission", phase == "completion"
			if mode == "ordinary renewal" && phase != "command" {
				continue
			}
			t.Run(mode+" at "+phase, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					now := time.Now().UTC()
					caPEM, _, _, issued := rpcSecurityPKI(t, now, "spiffe://example/client")
					leaf, err := x509.ParseCertificate(issued.Certificate[0])
					if err != nil {
						t.Fatal(err)
					}
					roots := x509.NewCertPool()
					if !roots.AppendCertsFromPEM(caPEM) {
						t.Fatal("bad CA")
					}
					policy := map[string]ClientPolicy{"spiffe://example/client": {MayAttestOwner: true, ExecutionTemplates: []string{"go"}}}
					ledger, err := json.Marshal(securityLedger{Generation: 1, Digest: "policy-v1"})
					if err != nil {
						t.Fatal(err)
					}
					kube := kubeFake.NewSimpleClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "authority", Namespace: "ns"}, Data: map[string]string{securityStateDataKey: string(ledger)}})
					var unavailable atomic.Bool
					kube.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
						if unavailable.Load() {
							return true, nil, errors.New("test authority API unavailable")
						}
						return false, nil, nil
					})
					security := NewSecurityManager("", "", "ns", "authority", kube)
					var clock atomic.Int64
					clock.Store(now.UnixNano())
					security.now = func() time.Time { return time.Unix(0, clock.Load()) }
					security.state.Store(&securityState{snapshot: &securitySnapshot{generation: 1, digest: "policy-v1", validUntil: now.Add(3 * time.Hour), clientCAs: roots, clients: policy}})
					withdraw := func() {
						switch mode {
						case "expired":
							clock.Store(leaf.NotAfter.Add(time.Second).UnixNano())
						case "CA removed":
							security.state.Store(&securityState{snapshot: &securitySnapshot{generation: 1, digest: "policy-v1", validUntil: now.Add(3 * time.Hour), clientCAs: x509.NewCertPool(), clients: policy}})
						case "policy generation":
							cm, err := kube.CoreV1().ConfigMaps("ns").Get(t.Context(), "authority", metav1.GetOptions{})
							if err != nil {
								t.Fatal(err)
							}
							cm.Data[securityStateDataKey] = `{"generation":2,"digest":"policy-v2"}`
							if _, err := kube.CoreV1().ConfigMaps("ns").Update(t.Context(), cm, metav1.UpdateOptions{}); err != nil {
								t.Fatal(err)
							}
						case "API unavailable":
							unavailable.Store(true)
						default:
							security.state.Store(&securityState{snapshot: &securitySnapshot{generation: 1, digest: "policy-v1", validUntil: now.Add(3 * time.Hour), clientCAs: roots, clients: policy}})
						}
					}
					env := runFixtureEnvironment(1, 1)
					_ = unstructured.SetNestedField(env.Object, hashText("spiffe://example/client"), "spec", "clientHash")
					_ = unstructured.SetNestedField(env.Object, hashText("spiffe://example/client"), "status", "activeRun", "clientHash")
					resource := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
					blocked := make(chan struct{})
					proceed := make(chan struct{})
					if admission || completion {
						blockAt := int32(2)
						if completion {
							blockAt = 3
						}
						var gets atomic.Int32
						resource.PrependReactor("get", "executionenvironments", func(_ k8stesting.Action) (bool, runtime.Object, error) {
							if gets.Add(1) == blockAt {
								close(blocked)
								<-proceed
							}
							return false, nil, nil
						})
					}
					executor := newLeaseBlockingExecutor()
					defer executor.unblock()
					store := NewStore(resource, "ns", testProfiles(), executor).WithSecurityManager(security)
					store.opTTL = 60 * time.Millisecond
					if completion {
						store.opTTL = time.Hour
					}
					ctx := peer.NewContext(t.Context(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}}})
					done := make(chan error, 1)
					go func() {
						_, e := store.execute(ctx, "spiffe://example/client", "owner", operationRequestContext(), executionenv.ExecutorRequest{Operation: executionenv.OpCommandStart, Command: "true"})
						done <- e
					}()
					if admission {
						select {
						case <-blocked:
						case <-time.After(time.Second):
							t.Fatal("admission did not block")
						}
					} else {
						select {
						case <-executor.started:
						case e := <-done:
							t.Fatalf("dispatch failed: %v", e)
						case <-time.After(time.Second):
							t.Fatal("command did not dispatch")
						}
					}
					if completion {
						executor.unblock()
						<-blocked // Completion read, after the lease goroutine has stopped.
						withdraw()
						close(proceed)
						err := <-done
						var fenced *executionenv.Error
						if !errors.As(err, &fenced) || fenced.Code != executionenv.CodeFenceUnknown || fenced.Message != "operation authority changed before completion" {
							t.Fatalf("completion-only withdrawal: %v", err)
						}
						current, err := resource.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
						if err != nil || textNested(current.Object, "status", "fenceState") != "FenceUnknown" || textNested(current.Object, "status", "activeOperation", "id") == "" {
							t.Fatalf("completion withdrawal did not retain the fenced operation: %v", err)
						}
						return
					}
					withdraw()
					if mode == "ordinary renewal" {
						select {
						case <-executor.cancelled:
							t.Fatal("ordinary renewal cancelled the active command")
						case <-time.After(3 * store.opTTL):
						}
						executor.unblock()
						select {
						case err := <-done:
							if err != nil {
								t.Fatalf("terminal receipt lost at renewal: %v", err)
							}
						case <-time.After(time.Second):
							t.Fatal("renewed command did not finish")
						}
						current, err := resource.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
						if err != nil || textNested(current.Object, "status", "fenceState") != fenceHealthy {
							t.Fatalf("renewed command fenced: %v %v", current, err)
						}
						return
					}
					if admission {
						close(proceed)
						select {
						case <-executor.started:
							t.Fatal("withdrawn peer dispatched")
						case e := <-done:
							var fenced *executionenv.Error
							if !errors.As(e, &fenced) || fenced.Code != executionenv.CodeFenceUnknown {
								t.Fatalf("withdrawal: %v", e)
							}
						case <-time.After(time.Second):
							t.Fatal("admission did not stop")
						}
					} else {
						select {
						case <-executor.cancelled:
						case <-time.After(500 * time.Millisecond):
							t.Fatal("withdrawn peer continued")
						}
						executor.unblock()
						select {
						case e := <-done:
							var fenced *executionenv.Error
							if !errors.As(e, &fenced) || fenced.Code != executionenv.CodeFenceUnknown {
								t.Fatalf("withdrawal: %v", e)
							}
						case <-time.After(time.Second):
							t.Fatal("command did not stop")
						}
					}
				})
			})
		}
	}
}
