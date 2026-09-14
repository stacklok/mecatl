package app

import "testing"

func TestRedisFollowCapacityConfigReachesStoreUnchanged(t *testing.T) {
	cfg := Config{RedisURL: "redis.example:6379", RedisFollowPoolSize: 64, RedisMaxFollowers: 17}
	got := redisStoreConfig(cfg)
	if got.FollowPoolSize != 64 || got.MaxFollowers != 17 {
		t.Fatalf("redisstore.Config follow limits = pool %d, followers %d; want 64, 17", got.FollowPoolSize, got.MaxFollowers)
	}
}
