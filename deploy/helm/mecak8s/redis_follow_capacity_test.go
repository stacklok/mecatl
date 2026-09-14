package mecak8s_test

import (
	"slices"
	"testing"
)

func TestRedisFollowCapacity_Scenario4_HelmValuesRenderFlags(t *testing.T) {
	for _, tc := range []struct {
		name          string
		extra         []string
		wantPool      string
		wantFollowers string
	}{
		{name: "defaults", wantPool: "--redis-follow-pool-size=32", wantFollowers: "--redis-max-followers=32"},
		{name: "overrides", extra: []string{"--set", "redis.follow.poolSize=64,redis.follow.maxFollowers=17"}, wantPool: "--redis-follow-pool-size=64", wantFollowers: "--redis-max-followers=17"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append(productionArgs(), tc.extra...)
			rendered, err := helm(t, args...)
			if err != nil {
				t.Fatalf("render: %v\n%s", err, rendered)
			}
			got := deploymentFromRender(t, rendered).Spec.Template.Spec.Containers[0].Args
			for _, want := range []string{tc.wantPool, tc.wantFollowers} {
				if !slices.Contains(got, want) {
					t.Fatalf("rendered args missing %q: %v", want, got)
				}
			}
		})
	}
}

func TestRedisFollowCapacity_Scenario4_InvalidBoundsFailClosed(t *testing.T) {
	for _, values := range []string{
		"redis.follow.poolSize=0",
		"redis.follow.poolSize=-1",
		"redis.follow.poolSize=1.5",
		"redis.follow.maxFollowers=0",
		"redis.follow.maxFollowers=-1",
		"redis.follow.maxFollowers=1.5",
		"redis.follow.poolSize=8,redis.follow.maxFollowers=9",
	} {
		t.Run(values, func(t *testing.T) {
			args := append(productionArgs(), "--set", values)
			if rendered, err := helm(t, args...); err == nil {
				t.Fatalf("invalid bounds %q rendered successfully:\n%s", values, rendered)
			}
		})
	}
}
