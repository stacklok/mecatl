package identitybroker

import (
	"context"
	"errors"
)

// Workload is the service making a call, as the broker's authenticator
// determined it. It is never a request field: Mecatl presents no identity of
// its own, and the broker decides who is calling.
type Workload struct {
	// Name identifies the calling service, such as a Kubernetes
	// ServiceAccount.
	Name string
	// Verified reports that an authenticator verified the workload's
	// credential. The zero value is false, so only an authenticator that
	// verifies a credential sets it. When false, Name is a claim nobody
	// proved: implementations must never treat it as the verified workload of
	// the same name, and records must not present it as verified.
	Verified bool
}

// Validate reports whether w has a name. The error is an *Error with
// CodeUnauthenticated.
func (w Workload) Validate() error {
	if !isToken(w.Name) {
		return NewError(CodeUnauthenticated, "", "the calling workload has no name")
	}
	return nil
}

// Core is the broker side of the contract: Service's calls, each with the
// workload the authenticator determined. Implementations provide the behavior
// documented on Service, and also:
//
//   - A workload that fails Validate is refused with CodeUnauthenticated,
//     before any other check.
//   - A broker session is bound to the whole Workload that opened it, Verified
//     included, so a verified and an unverified workload with the same name
//     are different workloads.
//   - Another workload cannot tell another workload's broker session from
//     none: closing, registering into, or calling through it fails exactly
//     as for an unknown session ID (CodePermissionDenied, before any closing
//     reason), and FindSession reports none open. Opening its host session is
//     refused with CodePermissionDenied.
//
// VerifyCredential is the one call that is not Service's. AS-1 is a module of
// the broker and calls it in process to check the agent credential on each
// grant, so it takes no workload, and Mecatl never calls it.
type Core interface {
	OpenSession(ctx context.Context, workload Workload, req OpenSessionRequest) (Session, error)
	FindSession(ctx context.Context, workload Workload, req FindSessionRequest) (Session, bool, error)
	CloseSession(ctx context.Context, workload Workload, req CloseSessionRequest) error
	RegisterAgent(ctx context.Context, workload Workload, req RegisterAgentRequest) (Registration, error)
	AuthorizeCall(ctx context.Context, workload Workload, req AuthorizeCallRequest) (Authorization, error)
	// VerifyCredential checks an agent credential for use in a broker
	// session: the design's credential checks, in order, then that the agent
	// is registered to that session and the session is open. It returns the
	// agent, its ag_ identifier, and the credential's expiry. An expired
	// credential fails with CodeUnauthenticated and ReasonSVIDExpired; every
	// other failure with CodePermissionDenied and ReasonDenied.
	VerifyCredential(ctx context.Context, req VerifyCredentialRequest) (VerifiedAgent, error)
}

// Authenticator decides which workload is calling. A failure is an *Error,
// normally with CodeUnauthenticated or CodePermissionDenied.
type Authenticator interface {
	Authenticate(ctx context.Context) (Workload, error)
}

// UnverifiedAuthenticator returns the same unverified workload for every call.
// It stands in until the broker authenticates the workload's credential,
// which will be addressed later.
type UnverifiedAuthenticator struct {
	Name string
}

// Authenticate implements Authenticator. The workload is never verified.
func (a UnverifiedAuthenticator) Authenticate(context.Context) (Workload, error) {
	return Workload{Name: a.Name}, nil
}

// NewInProcess returns the Service Mecatl calls when the broker runs in its
// process. Each call asks authn which workload is calling, then passes it to
// core, so the in-process path takes the same authentication step as a
// remote one.
func NewInProcess(authn Authenticator, core Core) Service {
	return inProcess{authn: authn, core: core}
}

type inProcess struct {
	authn Authenticator
	core  Core
}

func (p inProcess) authenticate(ctx context.Context) (Workload, error) {
	workload, err := p.authn.Authenticate(ctx)
	if err == nil {
		return workload, nil
	}
	var brokerErr *Error
	if errors.As(err, &brokerErr) {
		return Workload{}, err
	}
	return Workload{}, NewError(CodeUnauthenticated, "", "the calling workload could not be authenticated")
}

func (p inProcess) OpenSession(ctx context.Context, req OpenSessionRequest) (Session, error) {
	workload, err := p.authenticate(ctx)
	if err != nil {
		return Session{}, err
	}
	return p.core.OpenSession(ctx, workload, req)
}

func (p inProcess) FindSession(ctx context.Context, req FindSessionRequest) (Session, bool, error) {
	workload, err := p.authenticate(ctx)
	if err != nil {
		return Session{}, false, err
	}
	return p.core.FindSession(ctx, workload, req)
}

func (p inProcess) CloseSession(ctx context.Context, req CloseSessionRequest) error {
	workload, err := p.authenticate(ctx)
	if err != nil {
		return err
	}
	return p.core.CloseSession(ctx, workload, req)
}

func (p inProcess) RegisterAgent(ctx context.Context, req RegisterAgentRequest) (Registration, error) {
	workload, err := p.authenticate(ctx)
	if err != nil {
		return Registration{}, err
	}
	return p.core.RegisterAgent(ctx, workload, req)
}

func (p inProcess) AuthorizeCall(ctx context.Context, req AuthorizeCallRequest) (Authorization, error) {
	workload, err := p.authenticate(ctx)
	if err != nil {
		return Authorization{}, err
	}
	return p.core.AuthorizeCall(ctx, workload, req)
}
