package executioncontroller

import (
	"context"
	"slices"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

// MaxCommandDuration is the longest command any template may allow.
const MaxCommandDuration = 30 * time.Minute

// RegisterExecutorServices serves the authorizing FileService and
// CommandService on server and reports both as serving through gRPC health,
// which clients check before placing work on this provider.
func RegisterExecutorServices(server *grpc.Server, h *Handler, connect ExecutorConnectFunc) {
	executorv1.RegisterFileServiceServer(server, NewFileProxy(h, connect))
	executorv1.RegisterCommandServiceServer(server, NewCommandProxy(h, connect))
	health := grpchealth.NewServer()
	for _, service := range executorServiceNames() {
		health.SetServingStatus(service, healthpb.HealthCheckResponse_SERVING)
	}
	healthpb.RegisterHealthServer(server, health)
}

// executorServiceNames names the services file and command calls need.
func executorServiceNames() []string {
	return []string{
		executorv1.FileService_ServiceDesc.ServiceName,
		executorv1.CommandService_ServiceDesc.ServiceName,
	}
}

// errRunClaimNotCurrent is the definitive denial of a stale or expired run
// claim: UNAUTHENTICATED, retryable, and never forwarded, so the caller may
// renew its claim and retry.
var errRunClaimNotCurrent = &executionenv.Error{
	Code:      executionenv.CodeUnauthenticated,
	Message:   "run claim is not current",
	Retryable: true,
}

// ExecutorRoute is where an authorized call goes and the template limits it runs under.
type ExecutorRoute struct {
	Pin                ExecutorPin
	MaxFileBytes       int64
	MaxCommandBytes    int64
	MaxCommandDuration time.Duration
}

// ExecutorConnectFunc opens a connection to one pinned executor Pod; release closes it.
type ExecutorConnectFunc func(
	pin ExecutorPin,
) (conn grpc.ClientConnInterface, release func(), err error)

type authorityRechecker interface {
	RecheckAuthority(
		ctx context.Context,
		client string,
		owner string,
		rc executionenv.RequestContext,
	) error
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
	connector  ExecutorConnectFunc
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
) (context.Context, authorizedCall, error) {
	ctx, f := p.track(ctx, rc)
	route, err := p.authorize(ctx, rc)
	if err != nil {
		f.release()
		return ctx, authorizedCall{}, notForwarded(withdrawnOr(ctx, err))
	}
	a, err := p.connect(ctx, route, f)
	return ctx, a, notForwarded(err)
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

// recheck reports whether a forwarded call's authority ended, and whether a
// refusal is unconfirmed because authorization state could not be read.
func (p *executorProxy) recheck(ctx context.Context, rc *executorv1.RequestContext) (bool, error) {
	c, err := p.handler.client(ctx)
	if err != nil {
		// The client check refuses as Unavailable only while not ready.
		return status.Code(err) == codes.Unavailable, err
	}
	erc, ok := requestContextFromProto(executionContext(rc))
	if !ok || !c.policy.MayAttestOwner {
		return false, wireError(executionenv.CodePermissionDenied, false)
	}
	owner := ownerHash(erc.Owner)
	if rechecker, ok := p.handler.backend.(authorityRechecker); ok {
		err = rechecker.RecheckAuthority(ctx, c.id, owner, erc)
	} else if router, ok := p.handler.backend.(executorRouter); ok {
		_, err = router.ExecutorRoute(ctx, c.id, owner, erc, p.capability)
	} else {
		return false, wireError(executionenv.CodeNotReady, false)
	}
	if err != nil {
		return unconfirmed(err), backendError(err)
	}
	return false, nil
}

// connect dials route's executor for a call tracked since before admission,
// refusing it if it was withdrawn meanwhile; release ends the tracking.
func (p *executorProxy) connect(
	ctx context.Context,
	route ExecutorRoute,
	f *flight,
) (authorizedCall, error) {
	conn, release, err := p.connector(route.Pin)
	if err != nil {
		f.release()
		return authorizedCall{}, withdrawnOr(ctx, wireError(executionenv.CodeNotReady, true))
	}
	if !p.handler.flights.admit(f) {
		release()
		f.release()
		return authorizedCall{}, withdrawnOr(ctx, status.FromContextError(ctx.Err()).Err())
	}
	return authorizedCall{route: route, conn: conn, release: func() {
		f.release()
		release()
	}}, nil
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
