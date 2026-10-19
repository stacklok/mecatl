package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// APIMajor is a wire-contract major version.
type APIMajor int32

// SupportedAPIMajor is intentionally client-owned, independent of the server.
const SupportedAPIMajor APIMajor = 1

const compatibilityCheckTimeout = 10 * time.Second

type compatibilityErrorKind uint8

const (
	compatibilityServerNewer compatibilityErrorKind = iota + 1
	compatibilityServerOlder
	compatibilityMissing
	compatibilityInvalid
)

type compatibilityContextError struct {
	cause      error
	contextErr error
}

func (e *compatibilityContextError) Error() string { return e.cause.Error() }

func (e *compatibilityContextError) Unwrap() []error { return []error{e.cause, e.contextErr} }

func (e *compatibilityContextError) GRPCStatus() *status.Status { return status.Convert(e.cause) }

// CompatibilityError is a client-authored, safe-to-display compatibility
// failure. It never includes text supplied by the remote server.
type CompatibilityError struct {
	kind     compatibilityErrorKind
	cause    error
	Expected APIMajor
	Observed APIMajor
}

func (e *CompatibilityError) Error() string {
	switch e.kind {
	case compatibilityServerNewer:
		return fmt.Sprintf("server API is newer than this mecatui (expected major %d, observed %d); update mecatui", e.Expected, e.Observed)
	case compatibilityServerOlder:
		return fmt.Sprintf("server API is older than this mecatui (expected major %d, observed %d); update server", e.Expected, e.Observed)
	case compatibilityMissing:
		return "server does not support compatibility checking; update server"
	default:
		return "server returned invalid compatibility information"
	}
}

func (e *CompatibilityError) Unwrap() error { return e.cause }

// SafeCompatibilityError returns a client-authored compatibility message that
// may be displayed without exposing remote error text.
func SafeCompatibilityError(err error) (string, bool) {
	var compatibilityErr *CompatibilityError
	if !errors.As(err, &compatibilityErr) {
		return "", false
	}
	return compatibilityErr.Error(), true
}

// CheckCompatibility verifies that the connected server implements this
// client's API major. It is deliberately a standalone preflight: callers opt
// into it before startup, and it does not change legacy capability discovery.
func (c *Client) CheckCompatibility(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, compatibilityCheckTimeout)
	defer cancel()

	info, err := c.svc.GetCompatibilityInfo(withoutSessionAffinity(ctx), &mecatlv1.GetCompatibilityInfoRequest{})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			// Token acquisition can return a closed, secret-safe sentinel that
			// masks cancellation. Retain both gRPC status and errors.Is semantics.
			return errors.Join(status.FromContextError(contextErr).Err(), contextErr)
		}
		if status.Code(err) == codes.DeadlineExceeded {
			// gRPC can report the deadline before the bounded context exposes
			// its error. Preserve the remote status while retaining timeout
			// classification for the startup boundary.
			return &compatibilityContextError{cause: err, contextErr: context.DeadlineExceeded}
		}
		if status.Code(err) == codes.Unimplemented {
			return &CompatibilityError{kind: compatibilityMissing, cause: err}
		}
		return err
	}
	return compareAPIMajor(info, SupportedAPIMajor)
}

func compareAPIMajor(info *mecatlv1.GetCompatibilityInfoResponse, supportedMajor APIMajor) error {
	if info == nil || info.GetApiMajor() <= 0 {
		return &CompatibilityError{kind: compatibilityInvalid}
	}
	observed := APIMajor(info.GetApiMajor())
	switch {
	case observed > supportedMajor:
		return &CompatibilityError{kind: compatibilityServerNewer, Expected: supportedMajor, Observed: observed}
	case observed < supportedMajor:
		return &CompatibilityError{kind: compatibilityServerOlder, Expected: supportedMajor, Observed: observed}
	default:
		return nil
	}
}
