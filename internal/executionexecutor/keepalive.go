package executionexecutor

import (
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/stacklok/mecatl/internal/executionenv"
)

// ServerKeepalive closes a connection, cancelling its calls, when a ping goes unanswered.
func ServerKeepalive(k executionenv.ExecutorKeepalive) []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: k.Interval, Timeout: k.Timeout}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime: executionenv.ExecutorMinClientPing,
		}),
	}
}
