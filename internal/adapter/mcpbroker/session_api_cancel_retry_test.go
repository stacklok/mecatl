package mcpbroker

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// The durable terminal write succeeds just as the native cleanup deadline expires.
type cancelDeadlineRedis struct{ redis.UniversalClient }

func (r *cancelDeadlineRedis) Set(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd {
	cmd := r.UniversalClient.Set(ctx, key, value, ttl)
	if cmd.Err() == nil {
		<-ctx.Done()
	}
	return cmd
}
