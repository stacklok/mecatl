package executorclient

import (
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

// Client-side ceilings against a misbehaving executor.
const (
	DefaultMaxFileBytes    = 256 << 20
	DefaultMaxOutputBytes  = 256 << 20
	DefaultFileCallTimeout = time.Minute
)

// ErrLimitExceeded reports a response over a client limit.
var ErrLimitExceeded = errors.New("executor: response exceeds client limit")

// ErrStorageTimeout reports a file call that outlived its timeout. The
// executor may still finish it.
var ErrStorageTimeout = errors.New(
	"workspace storage did not respond in time; a write may still have completed",
)

type limits struct {
	maxFileBytes    int
	maxOutputBytes  int
	fileCallTimeout time.Duration
}

func newLimits(opts []Option) limits {
	l := limits{
		maxFileBytes:    DefaultMaxFileBytes,
		maxOutputBytes:  DefaultMaxOutputBytes,
		fileCallTimeout: DefaultFileCallTimeout,
	}
	for _, opt := range opts {
		opt(&l)
	}
	return l
}

// Option configures a Workspace or Runner.
type Option func(*limits)

// WithMaxFileBytes bounds one file read.
func WithMaxFileBytes(n int) Option {
	return func(l *limits) {
		if n > 0 {
			l.maxFileBytes = n
		}
	}
}

// WithMaxOutputBytes bounds the output a Runner keeps.
func WithMaxOutputBytes(n int) Option {
	return func(l *limits) {
		if n > 0 {
			l.maxOutputBytes = n
		}
	}
}

// WithFileCallTimeout bounds one Workspace call, streams included.
func WithFileCallTimeout(d time.Duration) Option {
	return func(l *limits) {
		if d > 0 {
			l.fileCallTimeout = d
		}
	}
}

// NewClient creates a lazy client with gRPC retries disabled, so no call is sent twice.
func NewClient(
	target string,
	creds credentials.TransportCredentials,
	opts ...grpc.DialOption,
) (*grpc.ClientConn, error) {
	opts = append(opts, grpc.WithTransportCredentials(creds), grpc.WithDisableRetry())
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("executorclient: new client: %w", err)
	}
	return conn, nil
}

// Keepalive fails calls to an executor that stops answering pings.
func Keepalive(interval, timeout time.Duration) grpc.DialOption {
	return grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: interval, Timeout: timeout})
}
