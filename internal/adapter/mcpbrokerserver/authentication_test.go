package mcpbrokerserver

import (
	"context"
	"errors"
	"testing"
	"time"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
)

type completeVerifierFake struct {
	readyErr error
	closed   bool
}

func (*completeVerifierFake) Validate(context.Context, string) (*session.Principal, error) {
	return nil, errors.New("not used")
}
func (f *completeVerifierFake) Ready(context.Context) error { return f.readyErr }
func (f *completeVerifierFake) Close() error                { f.closed = true; return nil }

func TestWorkloadJWTVerifierKubernetesBootstrap(t *testing.T) {
	fixture := newIdentityFixture(t)
	projected := fixture.token(t, fixture.server.URL, testAudience, time.Now().Add(time.Minute), nil)
	cfg := WorkloadJWTConfig{
		Audience:         testAudience,
		AllowedSubjects:  []string{"workload-secret-identity"},
		TrustedCAPEM:     fixture.caPEM(),
		MaxJWKSStaleness: time.Minute,
		KubernetesBootstrap: &KubernetesBootstrapConfig{
			DiscoveryURL: fixture.server.URL + "/.well-known/openid-configuration",
			JWKSURI:      fixture.server.URL + "/keys",
			TokenSource:  func() ([]byte, error) { return []byte(projected), nil },
		},
	}
	verifier, err := newWorkloadJWTVerifier(t.Context(), cfg)
	if err != nil {
		t.Fatalf("newWorkloadJWTVerifier: %v", err)
	}
	t.Cleanup(func() { _ = verifier.Close() })

	principal, err := verifier.Validate(t.Context(), projected)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if principal.Subject != "workload-secret-identity" {
		t.Fatalf("subject = %q, want workload-secret-identity", principal.Subject)
	}

	cfg.KubernetesBootstrap.TokenSource = func() ([]byte, error) {
		return []byte(fixture.token(t, "https://wrong-issuer.example", testAudience, time.Now().Add(time.Minute), nil)), nil
	}
	if verifier, err := newWorkloadJWTVerifier(t.Context(), cfg); err == nil {
		_ = verifier.Close()
		t.Fatal("newWorkloadJWTVerifier accepted a projected token whose issuer differs from discovery")
	}
}

func TestBrokerHostUsesCompleteVerifierForReadinessAndClose(t *testing.T) {
	verifier := &completeVerifierFake{}
	host, err := newBrokerHost(t.Context(), hostConfig{Service: &countingService{}, Verifier: verifier})
	if err != nil {
		t.Fatal(err)
	}
	if !host.ready(t.Context()) {
		t.Fatal("verifier-ready host is not ready")
	}
	verifier.readyErr = errors.New("unavailable")
	if host.ready(t.Context()) {
		t.Fatal("unready verifier left host ready")
	}
	if err := host.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !verifier.closed {
		t.Fatal("host did not close verifier")
	}
}

func TestOperationNameUsesGeneratedFullMethodNames(t *testing.T) {
	for method, want := range map[string]string{
		brokerv1.BrokerService_Attach_FullMethodName:                     "attach",
		brokerv1.BrokerService_Commit_FullMethodName:                     "commit",
		brokerv1.BrokerService_Abort_FullMethodName:                      "abort",
		brokerv1.BrokerService_Close_FullMethodName:                      "close",
		brokerv1.BrokerService_Delete_FullMethodName:                     "delete",
		brokerv1.BrokerService_Execute_FullMethodName:                    "execute",
		brokerv1.BrokerService_RequestAuthorization_FullMethodName:       "request_authorization",
		brokerv1.BrokerService_AbortAuthorization_FullMethodName:         "abort_authorization",
		brokerv1.BrokerService_PresentAuthorization_FullMethodName:       "present_authorization",
		brokerv1.BrokerService_AuthorizationStatus_FullMethodName:        "authorization_status",
		brokerv1.BrokerService_CancelAuthorization_FullMethodName:        "cancel_authorization",
		brokerv1.BrokerService_BeginWorkspaceEnrollment_FullMethodName:   "begin_workspace_enrollment",
		brokerv1.BrokerService_ObserveWorkspaceEnrollment_FullMethodName: "observe_workspace_enrollment",
		brokerv1.BrokerService_CancelWorkspaceEnrollment_FullMethodName:  "cancel_workspace_enrollment",
	} {
		if got := operationName(method); got != want {
			t.Errorf("operationName(%q) = %q, want %q", method, got, want)
		}
	}
	if got := operationName("/unknown"); got != "unknown" {
		t.Fatalf("unknown operation = %q", got)
	}
}
