package main

import (
	"strings"
	"testing"
)

func TestADR_0330_CLIConfigAndDefaults(t *testing.T) {
	defaults, err := parseFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if defaults.redisFollowPoolSize != 32 || defaults.redisMaxFollowers != 32 {
		t.Fatalf("Redis follow defaults = pool %d, followers %d; want 32, 32", defaults.redisFollowPoolSize, defaults.redisMaxFollowers)
	}
	defaultApp := appConfig(defaults, nil, observability{})
	if defaultApp.RedisFollowPoolSize != 32 || defaultApp.RedisMaxFollowers != 32 {
		t.Fatalf("app.Config Redis follow defaults = pool %d, followers %d; want 32, 32", defaultApp.RedisFollowPoolSize, defaultApp.RedisMaxFollowers)
	}

	overrides, err := parseFlags([]string{"--redis-follow-pool-size=64", "--redis-max-followers=17"})
	if err != nil {
		t.Fatal(err)
	}
	overrideApp := appConfig(overrides, nil, observability{})
	if overrideApp.RedisFollowPoolSize != 64 || overrideApp.RedisMaxFollowers != 17 {
		t.Fatalf("app.Config Redis follow overrides = pool %d, followers %d; want 64, 17", overrideApp.RedisFollowPoolSize, overrideApp.RedisMaxFollowers)
	}
}

func TestRedisFollowCapacity_Scenario4_InvalidBoundsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "zero pool", args: []string{"--redis-follow-pool-size=0"}},
		{name: "negative pool", args: []string{"--redis-follow-pool-size=-1"}},
		{name: "zero followers", args: []string{"--redis-max-followers=0"}},
		{name: "negative followers", args: []string{"--redis-max-followers=-1"}},
		{name: "followers exceed pool", args: []string{"--redis-follow-pool-size=8", "--redis-max-followers=9"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseFlags(tc.args)
			if err == nil {
				t.Fatalf("parseFlags(%v) succeeded", tc.args)
			}
			if !strings.Contains(err.Error(), "redis") {
				t.Fatalf("parseFlags(%v) error = %q, want Redis-specific error", tc.args, err)
			}
		})
	}
}
