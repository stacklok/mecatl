package executioncontroller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type fakeBackend struct {
	ensureCalls, created int
	allocations          map[string]Allocation
	commandState         executionenv.CommandState
	intents              []executionenv.ReferenceIntent
}

func newFakeBackend() *fakeBackend { return &fakeBackend{allocations: map[string]Allocation{}} }
func (*fakeBackend) ValidateProfile(context.Context, string) (Profile, error) {
	return Profile{Name: "go", Digest: "sha256:test", MaxFileBytes: 1024, MaxCommandBytes: 1024, MaxCommandDuration: time.Minute}, nil
}
func (f *fakeBackend) Ensure(_ context.Context, client, owner, binding, _, fp string) (Allocation, error) {
	f.ensureCalls++
	if a, ok := f.allocations[fp]; ok {
		return a, nil
	}
	f.created++
	a := Allocation{Environment: executionenv.EnvironmentRef{ID: "env-1", Revision: "rev-1"}, Epoch: 1, OwnerHash: owner, BindingID: binding, Client: client, Ready: true}
	f.allocations[fp] = a
	return a, nil
}
func (*fakeBackend) Attach(context.Context, executionenv.EnvironmentRef, string, string, string) (Allocation, error) {
	panic("unused")
}
func (*fakeBackend) ReleaseReference(context.Context, executionenv.EnvironmentRef, string, string, string) error {
	return nil
}
func (*fakeBackend) Retire(context.Context, executionenv.EnvironmentRef, string) error { return nil }
func (*fakeBackend) File(context.Context, string, string, executionenv.FileRequest) (executionenv.FileResponse, error) {
	return executionenv.FileResponse{}, nil
}
func (f *fakeBackend) StartCommand(context.Context, string, string, executionenv.CommandStartRequest) (executionenv.CommandStartResponse, error) {
	state := f.commandState
	if state == "" {
		state = executionenv.CommandSucceeded
	}
	return executionenv.CommandStartResponse{CommandID: "command", State: state, Result: executionenv.CommandStatusResponse{CommandID: "command", State: state}}, nil
}
func (*fakeBackend) CommandStatus(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error) {
	return executionenv.CommandStatusResponse{}, nil
}
func (*fakeBackend) CancelCommand(context.Context, string, string, executionenv.CommandQueryRequest) (executionenv.CommandStatusResponse, error) {
	return executionenv.CommandStatusResponse{}, nil
}

func (f *fakeBackend) EnsurePending(ctx context.Context, client, owner, binding, profile, fp, _ string) (Allocation, error) {
	return f.Ensure(ctx, client, owner, binding, profile, fp)
}
func (*fakeBackend) AcquireRun(_ context.Context, ref executionenv.EnvironmentRef, _ string, _ string, binding, run, _ string, ttl time.Duration) (executionenv.RunClaim, error) {
	return executionenv.RunClaim{Environment: ref, BindingID: binding, RunID: run, ClaimID: "claim", Epoch: 2, GrantGeneration: 1, ExpiresAt: time.Now().Add(ttl)}, nil
}
func (*fakeBackend) RenewRun(_ context.Context, _ executionenv.EnvironmentRef, _ string, _ string, req executionenv.RunClaimRequest) (executionenv.RunClaim, error) {
	return executionenv.RunClaim{Environment: req.Environment, BindingID: req.BindingID, RunID: req.RunID, ClaimID: req.ClaimID, Epoch: req.Epoch, GrantGeneration: 1, ExpiresAt: time.Now().Add(req.TTL)}, nil
}
func (*fakeBackend) ReleaseRun(context.Context, executionenv.EnvironmentRef, string, string, executionenv.RunClaimRequest) error {
	return nil
}
func (*fakeBackend) CommitReference(context.Context, executionenv.EnvironmentRef, string, string, string, string) error {
	return nil
}
func (*fakeBackend) AbortReference(context.Context, executionenv.EnvironmentRef, string, string, string, string) error {
	return nil
}
func (*fakeBackend) ReserveSuccessor(context.Context, executionenv.EnvironmentRef, string, string, string, string, string) error {
	return nil
}
func (*fakeBackend) PrepareReferenceDelete(context.Context, executionenv.EnvironmentRef, string, string, string, string) error {
	return nil
}
func (*fakeBackend) ConfirmReferenceDelete(context.Context, executionenv.EnvironmentRef, string, string, string, string) error {
	return nil
}
func (*fakeBackend) CancelReferenceDelete(context.Context, executionenv.EnvironmentRef, string, string, string, string) error {
	return nil
}
func (f *fakeBackend) ListReferenceIntents(context.Context, string, string, int) ([]executionenv.ReferenceIntent, error) {
	return f.intents, nil
}
func (*fakeBackend) FindReferenceIntent(context.Context, executionenv.EnvironmentRef, string, string, string) (executionenv.ReferenceIntent, error) {
	return executionenv.ReferenceIntent{}, nil
}

type adminFakeBackend struct {
	*fakeBackend
	migratedSchema int64
	deleteErr      error
	deletedRequest adminLifecycleRequest
}

func (*adminFakeBackend) ReplaceExecutor(context.Context, adminLifecycleRequest) error { return nil }
func (*adminFakeBackend) RetireExact(context.Context, adminLifecycleRequest) error     { return nil }
func (*adminFakeBackend) RecoverEnvironment(context.Context, adminLifecycleRequest) error {
	return nil
}
func (b *adminFakeBackend) DeleteRetiredEnvironment(_ context.Context, req adminLifecycleRequest) error {
	b.deletedRequest = req
	return b.deleteErr
}
func (b *adminFakeBackend) MigrateEnvironment(_ context.Context, req adminLifecycleRequest) error {
	b.migratedSchema = req.ExpectedSchema
	return nil
}
func (*adminFakeBackend) RevokeEnvironment(context.Context, adminLifecycleRequest, uint64) (uint64, error) {
	return 2, nil
}

func authenticatedContext(id string) context.Context {
	u, _ := url.Parse(id)
	cert := &x509.Certificate{URIs: []*url.URL{u}}
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: structTLSState(cert)}})
}
func structTLSState(cert *x509.Certificate) (s tls.ConnectionState) {
	s.PeerCertificates = []*x509.Certificate{cert}
	s.VerifiedChains = [][]*x509.Certificate{{cert}}
	return s
}

type handlerDiagnosticRecord struct {
	level port.Level
	msg   string
	attrs []any
}

type handlerDiagnosticRecorder struct {
	records []handlerDiagnosticRecord
}

func (d *handlerDiagnosticRecorder) Log(_ context.Context, level port.Level, msg string, attrs ...any) {
	d.records = append(d.records, handlerDiagnosticRecord{level: level, msg: msg, attrs: append([]any(nil), attrs...)})
}

func (d *handlerDiagnosticRecorder) With(...any) port.Diagnostics { return d }

func TestDeleteRetiredBackendFailureDiagnosticIsBounded(t *testing.T) {
	const (
		id       = "spiffe://cluster/ns/admin"
		sentinel = "sentinel-private-operation-delete-retired"
	)
	backend := &adminFakeBackend{fakeBackend: newFakeBackend(), deleteErr: errors.New(sentinel + " env=env operation=delete-op")}
	diagnostics := &handlerDiagnosticRecorder{}
	h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{id: {Administrator: true}}, Diagnostics: diagnostics}, backend)
	_, err := h.DeleteRetiredEnvironment(authenticatedContext(id), &executionv1.DeleteRetiredEnvironmentRequest{
		Environment:    &executionv1.EnvironmentRef{Id: "env", Revision: "rev"},
		Owner:          &executionv1.Owner{Issuer: "issuer", Subject: "alice"},
		ExpectedPvcUid: "pvc-uid",
		OperationId:    "delete-op",
	})
	if status.Code(err) != codes.Internal {
		t.Fatalf("code=%v, want %v", status.Code(err), codes.Internal)
	}
	if backend.deletedRequest.OperationID != "delete-op" || backend.deletedRequest.Environment.ID != "env" {
		t.Fatalf("backend request=%+v", backend.deletedRequest)
	}
	if len(diagnostics.records) != 1 {
		t.Fatalf("diagnostic records=%d, want 1: %+v", len(diagnostics.records), diagnostics.records)
	}
	record := diagnostics.records[0]
	if record.level != port.LevelWarn || record.msg != "execution backend failure" {
		t.Fatalf("diagnostic=%+v", record)
	}
	if len(record.attrs) != 4 || record.attrs[0] != "operation" || record.attrs[1] != "delete_retired" || record.attrs[2] != "reason" || record.attrs[3] != "other" {
		t.Fatalf("diagnostic attrs=%#v", record.attrs)
	}
	if strings.Contains(fmt.Sprint(record), sentinel) {
		t.Fatalf("diagnostic leaked backend error: %+v", record)
	}
}

func TestListReferenceIntentsProjectsAttestedOwner(t *testing.T) {
	backend := newFakeBackend()
	backend.intents = []executionenv.ReferenceIntent{{Environment: executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, BindingID: "binding", State: executionenv.ReferencePendingDelete, OperationID: "delete", CreatedAt: time.Now().UTC()}}
	id := "spiffe://cluster/ns/mecak8s"
	h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{id: {MayAttestOwner: true}}}, backend)
	owner := &executionv1.Owner{Issuer: "issuer", Subject: "alice"}
	out, err := h.ListReferenceIntents(authenticatedContext(id), &executionv1.ListReferenceIntentsRequest{Owner: owner, Limit: 64})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.GetIntents()) != 1 || out.GetIntents()[0].GetOwner().GetIssuer() != owner.GetIssuer() || out.GetIntents()[0].GetOwner().GetSubject() != owner.GetSubject() {
		t.Fatal("owner-scoped reference intent omitted its attested owner")
	}
}

func TestMigrateEnvironmentRequiresExpectedSchemaPresence(t *testing.T) {
	backend := &adminFakeBackend{fakeBackend: newFakeBackend()}
	id := "spiffe://cluster/ns/admin"
	h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{id: {Administrator: true}}}, backend)
	ctx := authenticatedContext(id)
	base := &executionv1.MigrateEnvironmentRequest{Environment: &executionv1.EnvironmentRef{Id: "env", Revision: "rev"}, Owner: &executionv1.Owner{Issuer: "issuer", Subject: "alice"}, ExpectedPodUid: "pod", ExpectedPvcUid: "pvc", OperationId: "migrate"}
	if _, err := h.MigrateEnvironment(ctx, base); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("omitted schema code=%v", status.Code(err))
	}
	zero := uint32(0)
	base.ExpectedSchemaVersion = &zero
	if _, err := h.MigrateEnvironment(ctx, base); err != nil || backend.migratedSchema != 0 {
		t.Fatalf("explicit zero rejected: schema=%d err=%v", backend.migratedSchema, err)
	}
	unknown := uint32(2)
	base.ExpectedSchemaVersion = &unknown
	if _, err := h.MigrateEnvironment(ctx, base); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown schema code=%v", status.Code(err))
	}
}

func TestHandlerBlocksAllRequestsUntilStartupReady(t *testing.T) {
	backend := newFakeBackend()
	h := NewHandler(HandlerConfig{Ready: func() bool { return false }}, backend)
	_, err := h.EnsureEnvironment(authenticatedContext("spiffe://cluster/ns/mecak8s"), &executionv1.EnsureEnvironmentRequest{})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("code=%v", status.Code(err))
	}
	if backend.ensureCalls != 0 {
		t.Fatalf("backend calls before readiness=%d", backend.ensureCalls)
	}
}

func TestHandlerRequiresAllowlistedCanonicalURISAN(t *testing.T) {
	h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{"spiffe://cluster/ns/mecak8s": {}}}, newFakeBackend())
	_, err := h.ValidateProfile(authenticatedContext("spiffe://cluster/ns/other"), &executionv1.ValidateProfileRequest{Profile: "go"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code=%v", status.Code(err))
	}
}

func TestHandlerValidateIsReadOnlyAndEnsureIdempotent(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	backend := newFakeBackend()
	h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{"spiffe://cluster/ns/mecak8s": {MayAttestOwner: true}}, Signer: GrantSigner{KeyID: "k1", PrivateKey: priv, Issuer: "provider", Audience: "execution", Lifetime: time.Minute}, Verifier: executionenv.GrantVerifier{Keys: map[string]ed25519.PublicKey{"k1": pub}, Issuer: "provider", Audience: "execution"}}, backend)
	ctx := authenticatedContext("spiffe://cluster/ns/mecak8s")
	if _, err := h.ValidateProfile(ctx, &executionv1.ValidateProfileRequest{Profile: "go"}); err != nil {
		t.Fatal(err)
	}
	if backend.ensureCalls != 0 {
		t.Fatal("validation allocated")
	}
	q := &executionv1.EnsureEnvironmentRequest{BindingId: "session-1", Profile: "go", Owner: &executionv1.Owner{Issuer: "issuer", Subject: "alice"}, OperationId: "create"}
	first, err := h.EnsureEnvironment(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.EnsureEnvironment(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if backend.created != 1 || first.Environment.Id != second.Environment.Id || first.Epoch != second.Epoch {
		t.Fatalf("created=%d", backend.created)
	}
}

func TestHandlerWithNilBackendRejectsEveryRPCWithoutPanicking(t *testing.T) {
	h := NewHandler(HandlerConfig{}, nil)
	ctx := context.Background()
	calls := []func(bool) error{
		func(valid bool) error {
			var q *executionv1.ValidateProfileRequest
			if valid {
				q = &executionv1.ValidateProfileRequest{Profile: "go"}
			}
			_, err := h.ValidateProfile(ctx, q)
			return err
		},
		func(valid bool) error {
			var q *executionv1.EnsureEnvironmentRequest
			if valid {
				q = &executionv1.EnsureEnvironmentRequest{BindingId: "binding", Profile: "go", Owner: &executionv1.Owner{Issuer: "issuer", Subject: "alice"}, OperationId: "create"}
			}
			_, err := h.EnsureEnvironment(ctx, q)
			return err
		},
		func(valid bool) error {
			var q *executionv1.AttachEnvironmentRequest
			if valid {
				q = &executionv1.AttachEnvironmentRequest{Purpose: executionenv.PurposeSession}
			}
			_, err := h.AttachEnvironment(ctx, q)
			return err
		},
		func(valid bool) error {
			var q *executionv1.ReleaseReferenceRequest
			if valid {
				q = &executionv1.ReleaseReferenceRequest{Context: &executionv1.RequestContext{}}
			}
			_, err := h.ReleaseReference(ctx, q)
			return err
		},
		func(valid bool) error {
			var q *executionv1.RetireEnvironmentRequest
			if valid {
				q = &executionv1.RetireEnvironmentRequest{Environment: &executionv1.EnvironmentRef{}, Owner: &executionv1.Owner{}}
			}
			_, err := h.RetireEnvironment(ctx, q)
			return err
		},
		func(valid bool) error {
			var q *executionv1.FileRequest
			if valid {
				q = &executionv1.FileRequest{Operation: executionv1.FileOperation_FILE_OPERATION_READ, Path: "x"}
			}
			_, err := h.Files(ctx, q)
			return err
		},
		func(valid bool) error {
			var q *executionv1.CommandStartRequest
			if valid {
				q = &executionv1.CommandStartRequest{Command: "true"}
			}
			_, err := h.StartCommand(ctx, q)
			return err
		},
		func(valid bool) error {
			var q *executionv1.CommandQueryRequest
			if valid {
				q = &executionv1.CommandQueryRequest{CommandId: "command"}
			}
			_, err := h.CommandStatus(ctx, q)
			return err
		},
		func(valid bool) error {
			var q *executionv1.CommandQueryRequest
			if valid {
				q = &executionv1.CommandQueryRequest{CommandId: "command"}
			}
			_, err := h.CancelCommand(ctx, q)
			return err
		},
	}
	for i, call := range calls {
		for _, valid := range []bool{false, true} {
			if err := call(valid); status.Code(err) != codes.Unavailable {
				t.Fatalf("RPC %d valid=%t code=%v", i, valid, status.Code(err))
			}
		}
	}
}

func TestHandlerRejectsUnknownBackendCommandState(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	backend := newFakeBackend()
	backend.commandState = executionenv.CommandState("future")
	h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{"spiffe://cluster/ns/mecak8s": {MayAttestOwner: true}}, Signer: GrantSigner{KeyID: "k1", PrivateKey: priv, Issuer: "provider", Audience: "execution", Lifetime: time.Minute}, Verifier: executionenv.GrantVerifier{Keys: map[string]ed25519.PublicKey{"k1": pub}, Issuer: "provider", Audience: "execution"}}, backend)
	ctx := authenticatedContext("spiffe://cluster/ns/mecak8s")
	ensured, err := h.EnsureEnvironment(ctx, &executionv1.EnsureEnvironmentRequest{BindingId: "session-1", Profile: "go", Owner: &executionv1.Owner{Issuer: "issuer", Subject: "alice"}, OperationId: "create"})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := h.AcquireRun(ctx, &executionv1.AcquireRunRequest{Environment: ensured.Environment, Owner: &executionv1.Owner{Issuer: "issuer", Subject: "alice"}, BindingId: "session-1", RunId: "run", OperationId: "acquire", TtlMillis: time.Minute.Milliseconds()})
	if err != nil {
		t.Fatal(err)
	}
	q := &executionv1.CommandStartRequest{Context: &executionv1.RequestContext{Environment: claim.Environment, Owner: &executionv1.Owner{Issuer: "issuer", Subject: "alice"}, BindingId: "session-1", RunId: claim.RunId, ClaimId: claim.ClaimId, Epoch: claim.Epoch, GrantGeneration: claim.GrantGeneration, Grant: claim.Grant}, Command: "true"}
	if _, err := h.StartCommand(ctx, q); status.Code(err) != codes.Internal || status.Convert(err).Message() != "execution provider request failed" {
		t.Fatalf("unexpected invalid-state result: code=%v message=%q", status.Code(err), status.Convert(err).Message())
	}
}

func TestCanonicalClientIdentityRejectsCNAndAmbiguousURIs(t *testing.T) {
	if _, err := canonicalClientIdentity(&x509.Certificate{Subject: pkix.Name{CommonName: "trusted"}}); err == nil {
		t.Fatal("CN accepted")
	}
	u1, _ := url.Parse("spiffe://b/x")
	u2, _ := url.Parse("spiffe://a/x")
	if _, err := canonicalClientIdentity(&x509.Certificate{URIs: []*url.URL{u1, u2}}); err == nil {
		t.Fatal("ambiguous URI SANs accepted")
	}
}

func TestRunClaimResponseKeepsNonAuthoritySigningFailureInternal(t *testing.T) {
	h := NewHandler(HandlerConfig{Signer: GrantSigner{KeyID: "k1", Issuer: "issuer", Audience: "audience", Lifetime: time.Minute}}, newFakeBackend())
	claim := executionenv.RunClaim{Environment: executionenv.EnvironmentRef{ID: "env", Revision: "rev"}, BindingID: "binding", RunID: "run", ClaimID: "claim", Epoch: 1, GrantGeneration: 1}
	_, err := h.runClaimResponse(t.Context(), claim, "spiffe://example/client", "owner")
	st := status.Convert(err)
	if st.Code() != codes.Internal {
		t.Fatalf("code=%v", st.Code())
	}
	details := st.Details()
	if len(details) != 1 || details[0].(*executionv1.ErrorDetail).Code != string(executionenv.CodeInternal) || details[0].(*executionv1.ErrorDetail).Retryable {
		t.Fatalf("details=%v", details)
	}
}

func TestBackendReasonClassIsBounded(t *testing.T) {
	invalid := apierrors.NewInvalid(schema.GroupKind{Group: "example.test", Kind: "Fixture"}, "private-resource-name", field.ErrorList{field.Invalid(field.NewPath("status"), "private-value", "private-detail")})
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "Kubernetes invalid", err: invalid, want: "invalid"},
		{name: "Kubernetes conflict", err: apierrors.NewConflict(schema.GroupResource{Group: "example.test", Resource: "fixtures"}, "private-resource-name", context.Canceled), want: "conflict"},
		{name: "controlled", err: &executionenv.Error{Code: executionenv.CodeNotReady, Message: "private-detail"}, want: "not_ready"},
		{name: "unknown", err: errors.New("private-detail"), want: "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := backendReasonClass(tc.err); got != tc.want {
				t.Fatalf("reason=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestWireErrorDoesNotExposeBackendMessage(t *testing.T) {
	err := backendError(&executionenv.Error{Code: executionenv.CodeInternal, Message: "kubectl exec --token=secret"})
	if got := status.Convert(err).Message(); got != "execution provider request failed" {
		t.Fatalf("message=%q", got)
	}
	unknown := backendError(&executionenv.Error{Code: executionenv.ErrorCode("future"), Message: "sensitive path"})
	st := status.Convert(unknown)
	if st.Code() != codes.Internal || st.Message() != "execution provider request failed" {
		t.Fatalf("unknown error escaped: code=%v message=%q", st.Code(), st.Message())
	}
	if details := st.Details(); len(details) != 1 || details[0].(*executionv1.ErrorDetail).Code != string(executionenv.CodeInternal) {
		t.Fatalf("unknown error detail=%v", details)
	}
	fenced := status.Convert(backendError(&executionenv.Error{Code: executionenv.CodeFenceUnknown, Message: "/var/run/provider/private-key"}))
	if fenced.Code() != codes.FailedPrecondition || strings.Contains(fenced.Message(), "/var/") {
		t.Fatalf("fence error leaked or mapped incorrectly: code=%v message=%q", fenced.Code(), fenced.Message())
	}
	if details := fenced.Details(); len(details) != 1 || details[0].(*executionv1.ErrorDetail).Code != string(executionenv.CodeFenceUnknown) {
		t.Fatalf("fence error detail=%v", details)
	}
}
