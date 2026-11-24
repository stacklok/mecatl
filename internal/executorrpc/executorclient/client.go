package executorclient

import (
	"errors"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Client-side ceilings against a misbehaving executor.
const (
	DefaultMaxFileBytes   = 256 << 20
	DefaultMaxOutputBytes = 256 << 20
)

// ErrLimitExceeded reports a response over a client limit.
var ErrLimitExceeded = errors.New("executor: response exceeds client limit")

type limits struct {
	maxFileBytes   int
	maxOutputBytes int
}

func newLimits(opts []Option) limits {
	l := limits{maxFileBytes: DefaultMaxFileBytes, maxOutputBytes: DefaultMaxOutputBytes}
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
