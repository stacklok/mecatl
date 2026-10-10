package executioncontroller

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestDurableRunAuthorizationOverMTLS(t *testing.T) {
	for _, tc := range []struct {
		name      string
		client    string
		request   func(*executionv1.RequestContext)
		field     []string
		value     any
		reference executionenv.ReferenceState
		policy    string
		want      codes.Code
	}{
		{name: "current published reference", want: codes.OK},
		{name: "foreign client", client: "spiffe://example/other", want: codes.NotFound},
		{name: "foreign owner subject", request: func(r *executionv1.RequestContext) { r.Owner.Subject = "other" }, want: codes.NotFound},
		{name: "foreign owner issuer", request: func(r *executionv1.RequestContext) { r.Owner.Issuer = "https://other.example" }, want: codes.NotFound},
		{name: "foreign environment", request: func(r *executionv1.RequestContext) { r.Environment.Id = "other" }, want: codes.NotFound},
		{name: "foreign revision", request: func(r *executionv1.RequestContext) { r.Environment.Revision = "other" }, want: codes.Aborted},
		{name: "foreign binding", request: func(r *executionv1.RequestContext) { r.BindingId = "other" }, want: codes.Aborted},
		{name: "foreign run", request: func(r *executionv1.RequestContext) { r.RunId = "other" }, want: codes.Aborted},
		{name: "foreign claim", request: func(r *executionv1.RequestContext) { r.ClaimId = "other" }, want: codes.Aborted},
		{name: "stale epoch", request: func(r *executionv1.RequestContext) { r.Epoch++ }, want: codes.Aborted},
		{name: "stale generation", request: func(r *executionv1.RequestContext) { r.GrantGeneration++ }, want: codes.Aborted},
		{name: "revoked generation", field: []string{"status", "grantGeneration"}, value: int64(2), want: codes.Aborted},
		{name: "expired lease", field: []string{"status", "activeRun", "expiresAt"}, value: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), want: codes.Aborted},
		{name: "reference removed", field: []string{"status", "references"}, value: []any{}, want: codes.Unavailable},
		{name: "unpublished pending create", reference: executionenv.ReferencePendingCreate, want: codes.Unavailable},
		{name: "unpublished pending delete", reference: executionenv.ReferencePendingDelete, want: codes.Unavailable},
		{name: "template revoked", field: []string{"spec", "templateDigest"}, value: "sha256:revoked", want: codes.Unavailable},
		{name: "template permission removed", policy: "template removed", want: codes.Unavailable},
		{name: "superseded policy ledger", policy: "superseded", want: codes.Unavailable},
		{name: "fence uncertain", field: []string{"status", "fenceState"}, value: "FenceUnknown", want: codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := runFixtureEnvironment(1, 1)
			for _, prefix := range [][]string{{"spec"}, {"status", "activeRun"}} {
				if err := unstructured.SetNestedField(env.Object, hashText(scopeCreator), append(prefix, "clientHash")...); err != nil {
					t.Fatal(err)
				}
				if err := unstructured.SetNestedField(env.Object, ownerHash(scopeOwner), append(prefix, "ownerHash")...); err != nil {
					t.Fatal(err)
				}
			}
			if tc.field != nil {
				if err := unstructured.SetNestedField(env.Object, tc.value, tc.field...); err != nil {
					t.Fatal(err)
				}
			}
			if tc.reference != "" {
				refs, err := referenceRecords(env)
				if err != nil {
					t.Fatal(err)
				}
				refs[0].State = tc.reference
				if err := setReferenceRecords(env, refs); err != nil {
					t.Fatal(err)
				}
			}
			clientID := scopeCreator
			if tc.client != "" {
				clientID = tc.client
			}
			dir := t.TempDir()
			ca, cert, key, clientCert := rpcSecurityPKI(t, time.Now(), clientID)
			writeRotationMaterial(t, dir, map[string][]byte{"server.crt": cert, "server.key": key, "clients.pem": ca})
			manifest := securityManifest{Version: 1, Generation: 1, TLS: securityTLSManifest{CertificateFile: "server.crt", PrivateKeyFile: "server.key", ClientCAFile: "clients.pem"}, Clients: []securityClientManifest{{URI: clientID, MayAttestOwner: true, ExecutionTemplates: []string{"go"}}}}
			path := filepath.Join(dir, "manifest.json")
			writeManifest(t, path, manifest)
			kube := kubefake.NewSimpleClientset()
			manager := NewSecurityManager(path, dir, "ns", "authority", kube)
			if err := manager.Reload(t.Context()); err != nil {
				t.Fatal(err)
			}
			switch tc.policy {
			case "template removed":
				manifest.Generation++
				manifest.Clients[0].ExecutionTemplates = nil
				writeManifest(t, path, manifest)
				if err := manager.Reload(t.Context()); err != nil {
					t.Fatal(err)
				}
			case "superseded":
				cm, err := kube.CoreV1().ConfigMaps("ns").Get(t.Context(), "authority", metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				cm.Data[securityStateDataKey] = `{"generation":2,"digest":"revoked"}`
				if _, err := kube.CoreV1().ConfigMaps("ns").Update(t.Context(), cm, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
			executor := newLeaseBlockingExecutor()
			executor.unblock()
			store := NewStore(dynamic, "ns", testProfiles(), executor).WithSecurityManager(manager)
			server := grpc.NewServer(grpc.Creds(credentials.NewTLS(manager.TLSConfig())))
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
			request := &executionv1.RequestContext{Environment: &executionv1.EnvironmentRef{Id: "env", Revision: "rev"}, Owner: &executionv1.Owner{Issuer: scopeOwner.Issuer, Subject: scopeOwner.Subject}, BindingId: "binding", RunId: "run", ClaimId: "claim", Epoch: 1, GrantGeneration: 1}
			if tc.request != nil {
				tc.request(request)
			}

			// Detached control has no Store dispatch fallback: removing the handler's
			// mandatory validator must fail this matrix, not merely hit execute's checks.
			_, err = client.CommandStatus(t.Context(), &executionv1.CommandQueryRequest{Context: request, CommandId: "command"})
			wantQuery := tc.want
			if wantQuery == codes.OK {
				wantQuery = codes.Unimplemented
			}
			if status.Code(err) != wantQuery {
				t.Fatalf("command authorization: got %v, want %v", err, wantQuery)
			}
			response, err := client.Files(t.Context(), &executionv1.FileRequest{Context: request, Operation: executionv1.FileOperation_FILE_OPERATION_READ, Path: "sentinel"})
			if status.Code(err) != tc.want {
				t.Fatalf("file authorization: got %v, want %v", err, tc.want)
			}
			select {
			case <-executor.started:
				if tc.want != codes.OK {
					t.Fatal("invalid request reached executor dispatch")
				}
			default:
				if tc.want == codes.OK {
					t.Fatal("authorized request did not dispatch")
				}
			}
			if tc.want == codes.OK {
				if string(response.GetData()) != "ok" || string(response.GetVersion()) != "v1" {
					t.Fatalf("executor result was not returned: %v", response)
				}
			} else {
				for _, action := range dynamic.Actions() {
					if action.GetVerb() != "get" {
						t.Fatalf("invalid request mutated durable state: %v", action)
					}
				}
			}
		})
	}
}
