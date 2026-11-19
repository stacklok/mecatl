package executioncontroller

import (
	"context"
	"slices"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

// ExecutorRoute is where an authorized call goes and the template limits it runs under.
type ExecutorRoute struct {
	Pin                ExecutorPin
	MaxFileBytes       int64
	MaxCommandBytes    int64
	MaxCommandDuration time.Duration
}

// ExecutorConnector opens a connection to one pinned executor Pod; release closes it.
type ExecutorConnector interface {
	Connect(
		ctx context.Context,
		pin ExecutorPin,
	) (conn grpc.ClientConnInterface, release func(), err error)
}

type executorRouter interface {
	ExecutorRoute(
		ctx context.Context,
		client string,
		owner string,
		rc executionenv.RequestContext,
		capability string,
	) (ExecutorRoute, error)
}

// ExecutorRoute authorizes one call for capability and returns its executor and limits.
func (s *Store) ExecutorRoute(
	ctx context.Context,
	client string,
	owner string,
	rc executionenv.RequestContext,
	capability string,
) (ExecutorRoute, error) {
	o, err := s.validateRunClaim(ctx, client, owner, rc)
	if err != nil {
		return ExecutorRoute{}, err
	}
	if textNested(o.Object, "status", "lifecycleOperation", "id") != "" {
		return ExecutorRoute{}, &executionenv.Error{
			Code:    executionenv.CodeNotReady,
			Message: transitioningMessage,
		}
	}
	if err := s.requireAdmitted(o, capability); err != nil {
		return ExecutorRoute{}, err
	}
	profile, ok := s.profiles.forEnvironment(o)
	if !ok {
		return ExecutorRoute{}, &executionenv.Error{
			Code:    executionenv.CodeNotReady,
			Message: templateRevokedMessage,
		}
	}
	pin, err := ExecutorPinFromEnvironment(o)
	if err != nil {
		return ExecutorRoute{}, &executionenv.Error{
			Code:      executionenv.CodeNotReady,
			Message:   err.Error(),
			Retryable: true,
		}
	}
	return ExecutorRoute{
		Pin:                pin,
		MaxFileBytes:       profile.Spec.MaxFileBytes,
		MaxCommandBytes:    profile.Spec.MaxCommandBytes,
		MaxCommandDuration: profile.Spec.MaxCommandDuration,
	}, nil
}

func (s *Store) requireAdmitted(o *unstructured.Unstructured, capability string) error {
	admitted, err := s.profiles.admittedCapabilities(o)
	if err != nil {
		return err
	}
	if !slices.Contains(admitted, capability) {
		return &executionenv.Error{
			Code:    executionenv.CodePermissionDenied,
			Message: "the session was not admitted for this service",
		}
	}
	return nil
}

// executorProxy authorizes calls with the Handler's policy and forwards them.
type executorProxy struct {
	handler    *Handler
	connector  ExecutorConnector
	capability string
}

type authorizedCall struct {
	route   ExecutorRoute
	conn    grpc.ClientConnInterface
	release func()
}

// open authorizes rc and connects to the session's executor; nothing is forwarded on error.
func (p *executorProxy) open(
	ctx context.Context,
	rc *executorv1.RequestContext,
) (authorizedCall, error) {
	route, err := p.authorize(ctx, rc)
	if err != nil {
		return authorizedCall{}, notForwarded(err)
	}
	a, err := p.connect(ctx, route)
	return a, notForwarded(err)
}

func (p *executorProxy) authorize(
	ctx context.Context,
	rc *executorv1.RequestContext,
) (ExecutorRoute, error) {
	c, err := p.handler.client(ctx)
	if err != nil {
		return ExecutorRoute{}, err
	}
	erc, ok := requestContextFromProto(executionContext(rc))
	if !ok || !c.policy.MayAttestOwner {
		return ExecutorRoute{}, wireError(executionenv.CodePermissionDenied, false)
	}
	router, ok := p.handler.backend.(executorRouter)
	if !ok || p.connector == nil {
		return ExecutorRoute{}, wireError(executionenv.CodeNotReady, false)
	}
	route, err := router.ExecutorRoute(ctx, c.id, ownerHash(erc.Owner), erc, p.capability)
	if err != nil {
		return ExecutorRoute{}, backendError(err)
	}
	return route, nil
}

func (p *executorProxy) connect(ctx context.Context, route ExecutorRoute) (authorizedCall, error) {
	conn, release, err := p.connector.Connect(ctx, route.Pin)
	if err != nil {
		return authorizedCall{}, wireError(executionenv.CodeNotReady, true)
	}
	return authorizedCall{route: route, conn: conn, release: release}, nil
}

func executionContext(p *executorv1.RequestContext) *executionv1.RequestContext {
	if p == nil {
		return nil
	}
	rc := &executionv1.RequestContext{
		BindingId:       p.GetBindingId(),
		Epoch:           p.GetEpoch(),
		RunId:           p.GetRunId(),
		ClaimId:         p.GetClaimId(),
		GrantGeneration: p.GetGrantGeneration(),
	}
	if env := p.GetEnvironment(); env != nil {
		rc.Environment = &executionv1.EnvironmentRef{Id: env.GetId(), Revision: env.GetRevision()}
	}
	if owner := p.GetOwner(); owner != nil {
		rc.Owner = &executionv1.Owner{Issuer: owner.GetIssuer(), Subject: owner.GetSubject()}
	}
	return rc
}

// executorError hides transport detail, such as Pod addresses, behind NotReady,
// and drops a NOT_FORWARDED claim the executor has no standing to make.
func executorError(err error) error {
	st, ok := status.FromError(err)
	if !ok || st.Code() == codes.Unavailable {
		return wireError(executionenv.CodeNotReady, true)
	}
	if refusedBeforeForwarding(st) {
		return status.Error(st.Code(), st.Message())
	}
	return st.Err()
}

// notForwarded tags a refusal so clients know the executor never saw the call.
func notForwarded(err error) error {
	if err == nil {
		return nil
	}
	st := status.Convert(err)
	with, derr := st.WithDetails(&executorv1.ErrorDetail{
		Reason: executorv1.ErrorReason_ERROR_REASON_NOT_FORWARDED,
	})
	if derr != nil {
		return err
	}
	return with.Err()
}

func refusedBeforeForwarding(st *status.Status) bool {
	for _, d := range st.Details() {
		detail, ok := d.(*executorv1.ErrorDetail)
		if ok && detail.GetReason() == executorv1.ErrorReason_ERROR_REASON_NOT_FORWARDED {
			return true
		}
	}
	return false
}

func limitExceeded(message string) error {
	st := status.New(codes.ResourceExhausted, message)
	with, err := st.WithDetails(&executorv1.ErrorDetail{
		Reason: executorv1.ErrorReason_ERROR_REASON_LIMIT_EXCEEDED,
	})
	if err == nil {
		st = with
	}
	return st.Err()
}
