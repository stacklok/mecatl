package executioncontroller

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
)

type contextStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s contextStream) Context() context.Context { return s.ctx }

func TestRPCLimiterTagsOnlyExecutorRefusalsAsNotForwarded(t *testing.T) {
	limiter, err := NewRPCLimiter(&SecurityManager{}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	unary := func(context.Context, any) (any, error) { return nil, nil }
	stream := func(any, grpc.ServerStream) error { return nil }
	for method, want := range map[string]bool{
		executionv1.ExecutionProviderService_AcquireRun_FullMethodName: false,
		healthpb.Health_Check_FullMethodName:                           false,
		executorv1.FileService_StatFile_FullMethodName:                 true,
		executorv1.FileService_ReadFile_FullMethodName:                 true,
		executorv1.CommandService_RunCommand_FullMethodName:            true,
	} {
		_, unaryErr := limiter.UnaryInterceptor(
			t.Context(),
			nil,
			&grpc.UnaryServerInfo{FullMethod: method},
			unary,
		)
		streamErr := limiter.StreamInterceptor(
			nil,
			contextStream{ctx: t.Context()},
			&grpc.StreamServerInfo{FullMethod: method},
			stream,
		)
		for _, err := range []error{unaryErr, streamErr} {
			tagged := reasonOf(err) == executorv1.ErrorReason_ERROR_REASON_NOT_FORWARDED
			if status.Code(err) != codes.Unauthenticated || tagged != want {
				t.Errorf("%s without a peer = %v, want not forwarded %t", method, err, want)
			}
		}
	}
}

func TestRPCLimiterBoundsGlobalPerClientAndIdentityMap(t *testing.T) {
	limiter, err := NewRPCLimiter(&SecurityManager{}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !limiter.acquire("client-a") || limiter.acquire("client-a") {
		t.Fatal("per-client bound was not enforced")
	}
	if !limiter.acquire("client-b") || limiter.acquire("client-c") {
		t.Fatal("global bound was not enforced")
	}
	limiter.release("client-a")
	limiter.release("client-b")
	if len(limiter.clients) != 0 || limiter.global != 0 {
		t.Fatalf("released limiter retained cardinality: global=%d clients=%v", limiter.global, limiter.clients)
	}
}

func TestRPCLimiterRejectsInvalidBounds(t *testing.T) {
	for _, limits := range [][2]int{{0, 1}, {1, 0}, {1, 2}} {
		if _, err := NewRPCLimiter(&SecurityManager{}, limits[0], limits[1]); err == nil {
			t.Fatalf("accepted limits %v", limits)
		}
	}
}
