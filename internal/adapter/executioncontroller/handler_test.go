package executioncontroller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/url"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type fakeBackend struct {
	ensureCalls, created int
	allocations          map[string]Allocation
	commandState         executionenv.CommandState
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
func (*fakeBackend) ListReferenceIntents(context.Context, string, string, int) ([]executionenv.ReferenceIntent, error) {
	return nil, nil
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
}
