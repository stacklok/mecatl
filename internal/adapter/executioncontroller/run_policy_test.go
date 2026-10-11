package executioncontroller

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
)

func TestRunTemplatePolicyWithdrawalOverMTLS(t *testing.T) {
	for _, route := range []string{"acquire", "acquire replay", "renew", "renew replay"} {
		for _, phase := range []string{"before request", "after authentication", "CAS retry", "stale store replica"} {
			t.Run(route+"/"+phase, func(t *testing.T) {
				now := time.Now().UTC()
				dir := t.TempDir()
				ca, cert, key, clientCert := rpcSecurityPKI(t, now, scopeCreator)
				writeRotationMaterial(t, dir, map[string][]byte{"server.crt": cert, "server.key": key, "clients.pem": ca})
				manifest := securityManifest{Version: 1, Generation: 1, TLS: securityTLSManifest{CertificateFile: "server.crt", PrivateKeyFile: "server.key", ClientCAFile: "clients.pem"}, Clients: []securityClientManifest{{URI: scopeCreator, MayAttestOwner: true, ExecutionTemplates: []string{"go"}}}}
				path := filepath.Join(dir, "manifest.json")
				writeManifest(t, path, manifest)
				kube := kubefake.NewSimpleClientset()
				manager := NewSecurityManager(path, dir, "ns", "authority", kube)
				if err := manager.Reload(t.Context()); err != nil {
					t.Fatal(err)
				}
				storeManager := NewSecurityManager(path, dir, "ns", "authority", kube)
				if err := storeManager.Reload(t.Context()); err != nil {
					t.Fatal(err)
				}
				env := runFixtureEnvironment(1, 1)
				unstructured.RemoveNestedField(env.Object, "status", "activeRun")
				_ = unstructured.SetNestedField(env.Object, hashText(scopeCreator), "spec", "clientHash")
				_ = unstructured.SetNestedField(env.Object, ownerHash(scopeOwner), "spec", "ownerHash")
				dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
				store := NewStore(dynamic, "ns", testProfiles(), nil).WithSecurityManager(storeManager)
				store.now = func() time.Time { return now }
				limiter, err := NewRPCLimiter(manager, 4, 2)
				if err != nil {
					t.Fatal(err)
				}
				server := grpc.NewServer(grpc.Creds(credentials.NewTLS(manager.TLSConfig())), grpc.UnaryInterceptor(limiter.UnaryInterceptor))
				executionv1.RegisterExecutionProviderServiceServer(server, NewHandler(HandlerConfig{Security: manager}, store))
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				go func() { _ = server.Serve(listener) }()
				t.Cleanup(server.Stop)
				roots := x509.NewCertPool()
				if !roots.AppendCertsFromPEM(ca) {
					t.Fatal("invalid test CA")
				}
				conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, ServerName: "provider.test", RootCAs: roots, Certificates: []tls.Certificate{clientCert}})))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Close() })
				client := executionv1.NewExecutionProviderServiceClient(conn)
				ref := &executionv1.EnvironmentRef{Id: "env", Revision: "rev"}
				owner := &executionv1.Owner{Issuer: scopeOwner.Issuer, Subject: scopeOwner.Subject}
				acquire := &executionv1.AcquireRunRequest{Environment: ref, Owner: owner, BindingId: "binding", RunId: "run", OperationId: "acquire", TtlMillis: time.Minute.Milliseconds()}
				claim, err := client.AcquireRun(t.Context(), acquire)
				if err != nil {
					t.Fatalf("allowed acquisition: %v", err)
				}
				if replay, err := client.AcquireRun(t.Context(), acquire); err != nil || replay.GetClaimId() != claim.ClaimId || !replay.GetExpiresAt().AsTime().Equal(claim.ExpiresAt.AsTime()) {
					t.Fatalf("allowed acquisition replay: %v, %v", replay, err)
				}
				renew := &executionv1.RenewRunRequest{Environment: ref, Owner: owner, BindingId: claim.BindingId, RunId: claim.RunId, ClaimId: claim.ClaimId, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration, OperationId: "renew", TtlMillis: (2 * time.Minute).Milliseconds()}
				renewed, err := client.RenewRun(t.Context(), renew)
				if err != nil || !renewed.GetExpiresAt().AsTime().After(claim.ExpiresAt.AsTime()) {
					t.Fatalf("allowed renewal: %v, %v", renewed, err)
				}
				if replay, err := client.RenewRun(t.Context(), renew); err != nil || !replay.GetExpiresAt().AsTime().Equal(renewed.ExpiresAt.AsTime()) {
					t.Fatalf("allowed renewal replay: %v, %v", replay, err)
				}
				release := &executionv1.ReleaseRunRequest{Environment: ref, Owner: owner, BindingId: claim.BindingId, RunId: claim.RunId, ClaimId: claim.ClaimId, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration, OperationId: "release"}
				if route == "acquire" {
					if _, err := client.ReleaseRun(t.Context(), release); err != nil {
						t.Fatal(err)
					}
					acquire.RunId, acquire.OperationId = "next-run", "next-acquire"
				}
				if route == "renew" {
					renew.OperationId, renew.TtlMillis = "next-renew", (3 * time.Minute).Milliseconds()
				}

				before, err := dynamic.Tracker().Get(ExecutionEnvironmentGVR, "ns", "env")
				if err != nil {
					t.Fatal(err)
				}
				dynamic.ClearActions()
				// Keep owner attestation and authentication valid; withdraw only the
				// template grant, including after handler admission or a failed CAS.
				manifest.Generation++
				manifest.Clients[0].ExecutionTemplates = nil
				writeManifest(t, path, manifest)
				withdraw := func() error {
					if err := manager.Reload(t.Context()); err != nil {
						return err
					}
					if phase == "stale store replica" {
						return nil
					}
					return storeManager.Reload(t.Context())
				}
				var changed atomic.Bool
				switch phase {
				case "after authentication", "CAS retry":
					verb := "get"
					if phase == "CAS retry" {
						verb = "update"
					}
					dynamic.PrependReactor(verb, "executionenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
						if changed.Swap(true) {
							return false, nil, nil
						}
						if err := withdraw(); err != nil {
							return true, nil, err
						}
						if phase == "CAS retry" {
							return true, nil, apierrors.NewConflict(ExecutionEnvironmentGVR.GroupResource(), "env", nil)
						}
						return false, nil, nil
					})
				default:
					if err := withdraw(); err != nil {
						t.Fatal(err)
					}
				}
				var response *executionv1.RunClaimResponse
				if route == "acquire" || route == "acquire replay" {
					response, err = client.AcquireRun(t.Context(), acquire)
				} else {
					response, err = client.RenewRun(t.Context(), renew)
				}
				if status.Code(err) != codes.Unavailable || response != nil {
					t.Fatalf("withdrawn template returned claim: %v, %v", response, err)
				}
				if (phase == "after authentication" || phase == "CAS retry") && !changed.Load() {
					t.Fatal("did not exercise policy withdrawal during admission")
				}
				after, err := dynamic.Tracker().Get(ExecutionEnvironmentGVR, "ns", "env")
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("denied request changed durable state: %v", err)
				}
				updates := 0
				for _, action := range dynamic.Actions() {
					if action.GetVerb() == "update" {
						updates++
					}
				}
				wantUpdates := 0
				if phase == "CAS retry" {
					wantUpdates = 1 // Only the planted conflict, never a successful write.
				}
				if updates != wantUpdates {
					t.Fatalf("denied request attempted %d writes, want %d", updates, wantUpdates)
				}
				if _, err := client.ReleaseRun(t.Context(), release); err != nil {
					t.Fatalf("withdrawal blocked run cleanup: %v", err)
				}
				if _, err := client.ReleaseReference(t.Context(), &executionv1.ReleaseReferenceRequest{Context: &executionv1.RequestContext{Environment: ref, Owner: owner, BindingId: "binding"}}); err != nil {
					t.Fatalf("withdrawal blocked reference cleanup: %v", err)
				}
				after, err = dynamic.Tracker().Get(ExecutionEnvironmentGVR, "ns", "env")
				if err != nil {
					t.Fatal(err)
				}
				cleaned := after.(*unstructured.Unstructured)
				refs, err := referenceRecords(cleaned)
				if err != nil || len(refs) != 0 || textNested(cleaned.Object, "status", "activeRun", "claimID") != "" {
					t.Fatalf("cleanup retained run or references: %v", err)
				}
			})
		}
	}
}
