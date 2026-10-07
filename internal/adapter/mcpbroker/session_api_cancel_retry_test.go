package mcpbroker

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// Metadata returns just as the native cancellation deadline expires.
type cancelDeadlineRedis struct{ redis.UniversalClient }

func (r *cancelDeadlineRedis) Get(ctx context.Context, key string) *redis.StringCmd {
	cmd := r.UniversalClient.Get(ctx, key)
	if cmd.Err() == nil {
		<-ctx.Done()
	}
	return cmd
}
