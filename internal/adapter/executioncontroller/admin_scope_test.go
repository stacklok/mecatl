package executioncontroller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"net"
	"os"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

const scopeCreator = "spiffe://example/creator"
const scopeAdmin = "spiffe://example/operations"

var scopeOwner = executionenv.Owner{Issuer: "https://issuer.example", Subject: "owner"}

type adminScopeFixture struct {
	client   executionv1.ExecutionProviderServiceClient
	dynamic  *dynamicfake.FakeDynamicClient
	manager  *SecurityManager
	manifest securityManifest
	path     string
}

func newAdminScopeFixture(t *testing.T, route, actor string) *adminScopeFixture {
	t.Helper()
	env := lifecycleAdminEnvironment(2, []any{})
	if route == "data" {
		env = runFixtureEnvironment(4, 4)
	}
	if route == "migrate" {
		_ = unstructured.SetNestedField(env.Object, int64(1), "spec", "schemaVersion")
		_ = unstructured.SetNestedField(env.Object, int64(1), "status", "schemaVersion")
	}
	if route == "recover" {
		_ = unstructured.SetNestedField(env.Object, "FenceUnknown", "status", "fenceState")
	}
	if route == "delete" {
		setConditionObject(env, "Retired", true, "Retired", "retired")
		setConditionObject(env, "ExecutorTerminated", true, "Terminated", "terminated")
	}
	_ = unstructured.SetNestedField(env.Object, hashText(scopeCreator), "spec", "clientHash")
	_ = unstructured.SetNestedField(env.Object, ownerHash(scopeOwner), "spec", "ownerHash")
	if textNested(env.Object, "status", "activeRun", "claimID") != "" {
		_ = unstructured.SetNestedField(env.Object, hashText(scopeCreator), "status", "activeRun", "clientHash")
		_ = unstructured.SetNestedField(env.Object, ownerHash(scopeOwner), "status", "activeRun", "ownerHash")
	}
	_ = unstructured.SetNestedField(env.Object, int64(4), "status", "grantGeneration")
	d := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	k := kubefake.NewSimpleClientset(terminalExecutor(), retainedPVC())
	store := NewStore(d, "ns", testProfiles(), nil).WithKubeClient(k)
	dir, now := t.TempDir(), time.Now().UTC()
	ca, cert, key, clientCert := rpcSecurityPKI(t, now, actor)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	writePKCS8(t, filepath.Join(dir, "grant.pem"), priv)
	for name, data := range map[string][]byte{"server.crt": cert, "server.key": key, "clients.pem": ca} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fp := sha256.Sum256(pub)
	mf := securityManifest{Version: 1, Generation: 1, Issuer: "issuer", Audience: "audience", ActiveKeyID: "k1", GrantTTLText: "1m", ClockSkewText: "5s", Keys: []securityKeyManifest{{ID: "k1", Version: 1, File: "grant.pem", PublicSHA256: hex.EncodeToString(fp[:]), ActivateAt: now.Add(-time.Minute), VerifyUntil: now.Add(time.Hour), State: "active"}}, TLS: securityTLSManifest{CertificateFile: "server.crt", PrivateKeyFile: "server.key", ClientCAFile: "clients.pem"}, Clients: []securityClientManifest{{URI: actor}}}
	path := filepath.Join(dir, "manifest.json")
	writeManifest(t, path, mf)
	manager := NewSecurityManager(path, dir, "ns", "authority", k)
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(manager.TLSConfig())))
	executionv1.RegisterExecutionProviderServiceServer(server, NewHandler(HandlerConfig{Security: manager}, store))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, ServerName: "provider.test", RootCAs: roots, Certificates: []tls.Certificate{clientCert}})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &adminScopeFixture{client: executionv1.NewExecutionProviderServiceClient(conn), dynamic: d, manager: manager, manifest: mf, path: path}
}

func (f *adminScopeFixture) policy(t *testing.T, admin bool, scope []string) {
	t.Helper()
	f.manifest.Generation++
	f.manifest.Clients[0] = scopedManifestClient(t, f.manifest.Clients[0].URI, admin, scope)
	writeManifest(t, f.path, f.manifest)
	if err := f.manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func callScopedAdmin(ctx context.Context, c executionv1.ExecutionProviderServiceClient, route string, q adminLifecycleRequest, owner executionenv.Owner) error {
	ref := &executionv1.EnvironmentRef{Id: q.Environment.ID, Revision: q.Environment.Revision}
	o := &executionv1.Owner{Issuer: owner.Issuer, Subject: owner.Subject}
	var err error
	switch route {
	case "retire":
		_, err = c.RetireEnvironment(ctx, &executionv1.RetireEnvironmentRequest{Environment: ref, Owner: o, ExpectedExecutionEpoch: q.ExpectedEpoch, ExpectedPodUid: q.ExpectedPodUID, ExpectedPvcUid: q.ExpectedPVCUID, OperationId: q.OperationID})
	case "replace":
		_, err = c.ReplaceExecutor(ctx, &executionv1.ReplaceExecutorRequest{Environment: ref, Owner: o, ExpectedExecutionEpoch: q.ExpectedEpoch, ExpectedPodUid: q.ExpectedPodUID, ExpectedPvcUid: q.ExpectedPVCUID, OperationId: q.OperationID})
	case "recover":
		_, err = c.RecoverEnvironment(ctx, &executionv1.RecoverEnvironmentRequest{Environment: ref, Owner: o, ExpectedExecutionEpoch: q.ExpectedEpoch, ExpectedPodUid: q.ExpectedPodUID, ExpectedPvcUid: q.ExpectedPVCUID, OperationId: q.OperationID})
	case "delete":
		_, err = c.DeleteRetiredEnvironment(ctx, &executionv1.DeleteRetiredEnvironmentRequest{Environment: ref, Owner: o, ExpectedPvcUid: q.ExpectedPVCUID, OperationId: q.OperationID})
	case "migrate":
		schema := uint32(q.ExpectedSchema)
		_, err = c.MigrateEnvironment(ctx, &executionv1.MigrateEnvironmentRequest{Environment: ref, Owner: o, ExpectedSchemaVersion: &schema, ExpectedPodUid: q.ExpectedPodUID, ExpectedPvcUid: q.ExpectedPVCUID, OperationId: q.OperationID})
	case "revoke":
		_, err = c.RevokeEnvironment(ctx, &executionv1.RevokeEnvironmentRequest{Environment: ref, Owner: o, ExpectedGrantGeneration: q.ExpectedEpoch, OperationId: q.OperationID})
	default:
		panic("unknown test route")
	}
	return err
}

func TestScopedAdminAllRoutesOverMTLS(t *testing.T) {
	for _, route := range []string{"retire", "replace", "recover", "delete", "migrate", "revoke"} {
		t.Run(route, func(t *testing.T) {
			f := newAdminScopeFixture(t, route, scopeAdmin)
			q := adminRequestFixture()
			q.ExpectedSchema = 1
			call := func(q adminLifecycleRequest, owner executionenv.Owner) error {
				return callScopedAdmin(t.Context(), f.client, route, q, owner)
			}
			if err := call(q, scopeOwner); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("nonadmin: %v", err)
			}
			for _, scope := range [][]string{nil, {"spiffe://example/unrelated"}} {
				f.policy(t, true, scope)
				denied := call(q, scopeOwner)
				missing := q
				missing.Environment.ID = "absent"
				absent := call(missing, scopeOwner)
				if status.Code(denied) != codes.NotFound || status.Convert(denied).Message() != status.Convert(absent).Message() || status.Code(absent) != codes.NotFound {
					t.Fatalf("scope/missing disclosure: denied=%v absent=%v", denied, absent)
				}
			}
			// Creator login is deliberately absent from the allowlist.
			f.policy(t, true, []string{scopeCreator})
			wrongOwner := scopeOwner
			wrongOwner.Subject = "another-owner"
			if err := call(q, wrongOwner); status.Code(err) != codes.NotFound {
				t.Fatalf("owner mismatch: %v", err)
			}
			stale := q
			stale.Environment.Revision = "stale"
			if err := call(stale, scopeOwner); status.Code(err) != codes.NotFound {
				t.Fatalf("revision mismatch: %v", err)
			}
			stale = q
			if route == "revoke" {
				stale.ExpectedEpoch++
			} else {
				stale.ExpectedPVCUID = "stale"
			}
			if err := call(stale, scopeOwner); status.Code(err) != codes.Aborted {
				t.Fatalf("stale runtime identity: %v", err)
			}
			if route == "retire" || route == "replace" || route == "recover" {
				stale = q
				stale.ExpectedEpoch++
				if err := call(stale, scopeOwner); status.Code(err) != codes.Aborted {
					t.Fatalf("stale epoch: %v", err)
				}
			}
			if route != "delete" && route != "revoke" {
				stale = q
				stale.ExpectedPodUID = "stale"
				if err := call(stale, scopeOwner); status.Code(err) != codes.Aborted {
					t.Fatalf("stale Pod UID: %v", err)
				}
			}
			if route == "migrate" {
				stale = q
				stale.ExpectedSchema = 0
				if err := call(stale, scopeOwner); status.Code(err) != codes.Aborted {
					t.Fatalf("wrong schema: %v", err)
				}
			}
			before, err := f.dynamic.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err := call(q, scopeOwner); err != nil {
				t.Fatalf("scoped admin rejected: %v", err)
			}
			if err := call(q, scopeOwner); err != nil {
				t.Fatalf("exact replay: %v", err)
			}
			after, err := f.dynamic.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if reflect.DeepEqual(before.Object["status"], after.Object["status"]) {
				t.Fatal("successful administration did not persist its transition")
			}
			if route == "revoke" {
				receipts, _, err := unstructured.NestedSlice(after.Object, "status", "revocationReceipts")
				if err != nil || len(receipts) != 1 || intNested(after.Object, "status", "grantGeneration") != 5 {
					t.Fatalf("revocation replay was not idempotent: %v", after.Object["status"])
				}
				if text(receipts[0].(map[string]any), "fingerprint") != fingerprint("env", "rev", scopeAdmin, ownerHash(scopeOwner), "4") {
					t.Fatal("revocation receipt replaced the authenticated actor with the creator")
				}
			}
			for _, field := range []string{"clientHash", "ownerHash", "revision"} {
				if textNested(before.Object, "spec", field) != textNested(after.Object, "spec", field) {
					t.Fatalf("admin rewrote immutable %s", field)
				}
			}
			// Even receipt replay is a new admission on the established connection.
			f.policy(t, true, nil)
			if err := call(q, scopeOwner); status.Code(err) != codes.NotFound {
				t.Fatalf("scope removal did not revoke replay: %v", err)
			}
		})
		t.Run(route+"/creator", func(t *testing.T) {
			f := newAdminScopeFixture(t, route, scopeCreator)
			q := adminRequestFixture()
			q.ExpectedSchema = 1
			if err := callScopedAdmin(t.Context(), f.client, route, q, scopeOwner); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("normal creator is admin: %v", err)
			}
			f.policy(t, true, nil)
			if err := callScopedAdmin(t.Context(), f.client, route, q, scopeOwner); err != nil {
				t.Fatalf("legacy self-admin: %v", err)
			}
		})
	}
}

func TestScopedAdminCASRetryRechecksSubject(t *testing.T) {
	for _, route := range []string{"retire", "replace", "recover", "delete", "migrate", "revoke"} {
		t.Run(route, func(t *testing.T) {
			f := newAdminScopeFixture(t, route, scopeAdmin)
			f.policy(t, true, []string{scopeCreator})
			var conflicted atomic.Bool
			f.dynamic.PrependReactor("update", "executionenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
				if conflicted.Swap(true) {
					return false, nil, nil
				}
				current, err := f.dynamic.Tracker().Get(ExecutionEnvironmentGVR, "ns", "env")
				if err != nil {
					return true, nil, err
				}
				o := current.(*unstructured.Unstructured)
				_ = unstructured.SetNestedField(o.Object, hashText("spiffe://example/other"), "spec", "clientHash")
				if err := f.dynamic.Tracker().Update(ExecutionEnvironmentGVR, o, "ns"); err != nil {
					return true, nil, err
				}
				return true, nil, apierrors.NewConflict(ExecutionEnvironmentGVR.GroupResource(), "env", nil)
			})
			q := adminRequestFixture()
			q.ExpectedSchema = 1
			if err := callScopedAdmin(t.Context(), f.client, route, q, scopeOwner); status.Code(err) != codes.NotFound {
				t.Fatalf("CAS retry escaped scope: %v", err)
			}
			if !conflicted.Load() {
				t.Fatal("did not exercise CAS retry")
			}
		})
	}
}

func TestScopedAdminUsesAdmittedPolicySnapshot(t *testing.T) {
	f := newAdminScopeFixture(t, "revoke", scopeAdmin)
	f.policy(t, true, []string{scopeCreator})
	var changed atomic.Bool
	f.manifest.Generation++
	f.manifest.Clients[0] = scopedManifestClient(t, scopeAdmin, true, nil)
	writeManifest(t, f.path, f.manifest)
	f.dynamic.PrependReactor("get", "executionenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
		if !changed.Swap(true) {
			// Authentication has already admitted this request; change the live
			// snapshot before the store makes its subject decision.
			if err := f.manager.Reload(t.Context()); err != nil {
				return true, nil, err
			}
		}
		return false, nil, nil
	})
	q := adminRequestFixture()
	if err := callScopedAdmin(t.Context(), f.client, "revoke", q, scopeOwner); err != nil {
		t.Fatalf("admitted snapshot was replaced mid-request: %v", err)
	}
	if !changed.Load() {
		t.Fatal("policy did not change during admission")
	}
	if err := callScopedAdmin(t.Context(), f.client, "revoke", q, scopeOwner); status.Code(err) != codes.NotFound {
		t.Fatalf("next request reused old scope: %v", err)
	}
}

func TestScopedAdminCompletedReplacementReceiptReauthorizes(t *testing.T) {
	f := newAdminScopeFixture(t, "replace", scopeAdmin)
	f.policy(t, true, []string{scopeCreator})
	q := adminRequestFixture()
	env, err := f.dynamic.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedMap(env.Object, map[string]any{"operationID": q.OperationID, "previousPodUID": q.ExpectedPodUID, "replacementPodUID": "new-pod", "pvcUID": q.ExpectedPVCUID, "previousEpoch": int64(4), "replacementEpoch": int64(5)}, "status", "lastReplacement")
	_ = unstructured.SetNestedField(env.Object, int64(5), "status", "epoch")
	_ = unstructured.SetNestedField(env.Object, "new-pod", "status", "pod", "uid")
	if _, err := f.dynamic.Resource(ExecutionEnvironmentGVR).Namespace("ns").UpdateStatus(t.Context(), env, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := callScopedAdmin(t.Context(), f.client, "replace", q, scopeOwner); err != nil {
		t.Fatal(err)
	}
	wrongOwner := scopeOwner
	wrongOwner.Subject = "other"
	if err := callScopedAdmin(t.Context(), f.client, "replace", q, wrongOwner); status.Code(err) != codes.NotFound {
		t.Fatalf("receipt bypassed owner check: %v", err)
	}
	f.policy(t, true, nil)
	if err := callScopedAdmin(t.Context(), f.client, "replace", q, scopeOwner); status.Code(err) != codes.NotFound {
		t.Fatalf("receipt bypassed removed scope: %v", err)
	}
}

func TestScopedAdminMigrationReceiptRetainsUIDPreconditions(t *testing.T) {
	f := newAdminScopeFixture(t, "migrate", scopeAdmin)
	f.policy(t, true, []string{scopeCreator})
	q := adminRequestFixture()
	q.ExpectedSchema = 1
	if err := callScopedAdmin(t.Context(), f.client, "migrate", q, scopeOwner); err != nil {
		t.Fatal(err)
	}
	if err := callScopedAdmin(t.Context(), f.client, "migrate", q, scopeOwner); err != nil {
		t.Fatalf("exact migration replay failed: %v", err)
	}
	q.ExpectedSchema = 0
	if err := callScopedAdmin(t.Context(), f.client, "migrate", q, scopeOwner); status.Code(err) != codes.Aborted {
		t.Fatalf("migration receipt bypassed source schema: %v", err)
	}
	q.ExpectedSchema = 1
	q.ExpectedPVCUID = "stale"
	if err := callScopedAdmin(t.Context(), f.client, "migrate", q, scopeOwner); status.Code(err) != codes.Aborted {
		t.Fatalf("migration receipt bypassed UID: %v", err)
	}
}

func TestMigrationCompletedCASReplayRequiresExactSourceSchema(t *testing.T) {
	for _, receipt := range []string{"exact", "changed", "absent", "expired"} {
		t.Run(receipt, func(t *testing.T) {
			f := newAdminScopeFixture(t, "migrate", scopeAdmin)
			f.policy(t, true, []string{scopeCreator})
			var raced bool
			f.dynamic.PrependReactor("update", "executionenvironments", func(action k8stesting.Action) (bool, runtime.Object, error) {
				u := action.(k8stesting.UpdateAction).GetObject().(*unstructured.Unstructured)
				if raced || textNested(u.Object, "status", "lastMigrationOperationID") == "" {
					return false, nil, nil
				}
				raced = true
				completed := u.DeepCopy()
				switch receipt {
				case "changed":
					_ = unstructured.SetNestedField(completed.Object, int64(0), "status", "lastMigrationFromSchema")
				case "absent":
					unstructured.RemoveNestedField(completed.Object, "status", "lastMigrationFromSchema")
				case "expired":
					unstructured.RemoveNestedField(completed.Object, "status", "lastMigrationOperationID")
					unstructured.RemoveNestedField(completed.Object, "status", "lastMigrationFromSchema")
				}
				if err := f.dynamic.Tracker().Update(ExecutionEnvironmentGVR, completed, "ns"); err != nil {
					t.Fatal(err)
				}
				return true, nil, apierrors.NewConflict(ExecutionEnvironmentGVR.GroupResource(), "env", nil)
			})
			q := adminRequestFixture()
			q.ExpectedSchema = 1
			err := callScopedAdmin(t.Context(), f.client, "migrate", q, scopeOwner)
			want := codes.Aborted
			if receipt == "exact" {
				want = codes.OK
			}
			if !raced || status.Code(err) != want {
				t.Fatalf("CAS replay raced=%t code=%v, want %v", raced, status.Code(err), want)
			}
		})
	}
}

func TestScopedAdminEqualGenerationDriftFailsClosed(t *testing.T) {
	f := newAdminScopeFixture(t, "revoke", scopeAdmin)
	f.policy(t, true, []string{scopeCreator})
	q := adminRequestFixture()
	if err := callScopedAdmin(t.Context(), f.client, "revoke", q, scopeOwner); err != nil {
		t.Fatal(err)
	}
	f.manifest.Clients[0] = scopedManifestClient(t, scopeAdmin, true, nil)
	writeManifest(t, f.path, f.manifest)
	if err := f.manager.Reload(t.Context()); err == nil {
		t.Fatal("equal-generation scope drift admitted")
	}
	if err := callScopedAdmin(t.Context(), f.client, "revoke", q, scopeOwner); status.Code(err) != codes.Unavailable {
		t.Fatalf("stale snapshot admitted RPC: %v", err)
	}
}

func TestScopedAdminWithOwnerAttestationCannotUseAnotherCreatorsDataPlane(t *testing.T) {
	f := newAdminScopeFixture(t, "data", scopeAdmin)
	f.policy(t, true, []string{scopeCreator})
	f.manifest.Generation++
	f.manifest.Clients[0].MayAttestOwner = true
	writeManifest(t, f.path, f.manifest)
	if err := f.manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	ref := &executionv1.EnvironmentRef{Id: "env", Revision: "rev"}
	owner := &executionv1.Owner{Issuer: scopeOwner.Issuer, Subject: scopeOwner.Subject}
	before, err := f.dynamic.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := referenceRecords(before)
	if err != nil {
		t.Fatal(err)
	}
	refs = append(refs, referenceRecord{BindingID: "pending", State: executionenv.ReferencePendingCreate, OperationID: "pending-op", CreatedAt: time.Now().UTC()})
	if err := setReferenceRecords(before, refs); err != nil {
		t.Fatal(err)
	}
	if _, err := f.dynamic.Resource(ExecutionEnvironmentGVR).Namespace("ns").UpdateStatus(t.Context(), before, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"attach": func() error {
			_, err := f.client.AttachEnvironment(t.Context(), &executionv1.AttachEnvironmentRequest{Context: &executionv1.RequestContext{Environment: ref, Owner: owner, BindingId: "binding"}, Purpose: "session"})
			return err
		},
		"acquire": func() error {
			_, err := f.client.AcquireRun(t.Context(), &executionv1.AcquireRunRequest{Environment: ref, Owner: owner, BindingId: "binding", RunId: "run", OperationId: "acquire", TtlMillis: 60000})
			return err
		},
		"reference": func() error {
			_, err := f.client.CommitReference(t.Context(), &executionv1.ReferenceMutationRequest{Environment: ref, Owner: owner, BindingId: "pending", OperationId: "pending-op"})
			return err
		},
		"exact discovery": func() error {
			_, err := f.client.ListReferenceIntents(t.Context(), &executionv1.ListReferenceIntentsRequest{Environment: ref, Owner: owner, BindingId: "pending"})
			return err
		},
	} {
		if err := call(); status.Code(err) != codes.NotFound {
			t.Errorf("%s: expected creator-bound not found, got %v", name, err)
		}
	}
	for _, query := range []*executionv1.ListReferenceIntentsRequest{{Owner: owner}, {}} {
		list, err := f.client.ListReferenceIntents(t.Context(), query)
		if err != nil || len(list.GetIntents()) != 0 {
			t.Fatal("discovery exposed another creator's pending reference")
		}
	}
	after, err := f.dynamic.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil || !reflect.DeepEqual(before.Object, after.Object) {
		t.Fatal("creator-bound denials changed foreign environment")
	}
	own, err := f.client.EnsureEnvironment(t.Context(), &executionv1.EnsureEnvironmentRequest{BindingId: "own", Profile: "go", Owner: owner, OperationId: "own-create"})
	if err != nil || own.GetEnvironment().GetId() == "env" {
		t.Fatalf("explicit owner attestation did not permit own allocation: %v", err)
	}
	list, err := f.client.ListReferenceIntents(t.Context(), &executionv1.ListReferenceIntentsRequest{Owner: owner})
	if err != nil || len(list.GetIntents()) != 1 || list.Intents[0].GetEnvironment().GetId() != own.GetEnvironment().GetId() {
		t.Fatal("discovery did not isolate the actor's own pending allocation")
	}
}

func TestScopedAdminDoesNotGrantAttestationOrDataPlane(t *testing.T) {
	f := newAdminScopeFixture(t, "data", scopeAdmin)
	f.policy(t, true, []string{scopeCreator})
	ref := &executionv1.EnvironmentRef{Id: "env", Revision: "rev"}
	owner := &executionv1.Owner{Issuer: scopeOwner.Issuer, Subject: scopeOwner.Subject}
	request := &executionv1.RequestContext{Environment: ref, Owner: owner, BindingId: "binding", Epoch: 4, RunId: "run", ClaimId: "claim", GrantGeneration: 4}
	// A correctly signed actor-bound grant still cannot cross creator ownership.
	h := NewHandler(HandlerConfig{Security: f.manager}, nil)
	claim := executionenv.RunClaim{Environment: executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, BindingID: "binding", RunID: "run", ClaimID: "claim", Epoch: 4, GrantGeneration: 4}
	grant, _, err := h.signClaim(t.Context(), claim, scopeAdmin, ownerHash(scopeOwner))
	if err != nil {
		t.Fatal(err)
	}
	request.Grant = grant
	calls := map[string]func() error{
		"ensure attestation": func() error {
			_, err := f.client.EnsureEnvironment(t.Context(), &executionv1.EnsureEnvironmentRequest{BindingId: "new", Profile: "go", Owner: owner, OperationId: "ensure"})
			return err
		},
		"attach": func() error {
			_, err := f.client.AttachEnvironment(t.Context(), &executionv1.AttachEnvironmentRequest{Context: request, Purpose: "session"})
			return err
		},
		"run": func() error {
			_, err := f.client.AcquireRun(t.Context(), &executionv1.AcquireRunRequest{Environment: ref, Owner: owner, BindingId: "binding", RunId: "run", OperationId: "acquire", TtlMillis: 60000})
			return err
		},
		"reference": func() error {
			_, err := f.client.CommitReference(t.Context(), &executionv1.ReferenceMutationRequest{Environment: ref, Owner: owner, BindingId: "binding", OperationId: "commit"})
			return err
		},
		"reference discovery": func() error {
			_, err := f.client.ListReferenceIntents(t.Context(), &executionv1.ListReferenceIntentsRequest{Owner: owner})
			return err
		},
		"files": func() error {
			_, err := f.client.Files(t.Context(), &executionv1.FileRequest{Context: request, Operation: executionv1.FileOperation_FILE_OPERATION_READ, Path: "file"})
			return err
		},
		"shell": func() error {
			_, err := f.client.StartCommand(t.Context(), &executionv1.CommandStartRequest{Context: request, Command: "true"})
			return err
		},
	}
	before, err := f.dynamic.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for name, call := range calls {
		want := codes.PermissionDenied
		switch name {
		case "run", "reference", "reference discovery":
			want = codes.InvalidArgument
		case "files", "shell":
			want = codes.NotFound
		}
		if err := call(); status.Code(err) != want {
			t.Errorf("%s: want %v, got %v", name, want, err)
		}
	}
	after, err := f.dynamic.Resource(ExecutionEnvironmentGVR).Namespace("ns").Get(t.Context(), "env", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Object, after.Object) {
		t.Fatal("denied data-plane calls changed environment")
	}
}
