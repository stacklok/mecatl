package session

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

// testConfig keeps existing canonical fixture comparisons deterministic.
func testConfig() Config {
	return Config{Policy: sessionPolicy(), InstanceID: 1, Now: func() time.Time { return time.Unix(0, 0) }}
}

func testConfigFor(p Policy) Config {
	c := testConfig()
	c.Policy = p
	return c
}

func TestAuthoringConfig(t *testing.T) {
	h := newSessionHistory(t)
	id := testHistoryID(t)
	p := sessionPolicy()
	for _, construct := range []func(Config) (*Session, error){
		func(c Config) (*Session, error) { return New(h, id, c) },
		func(c Config) (*Session, error) { return Load(t.Context(), h, id, c) },
	} {
		if s, err := construct(Config{Policy: p}); s != nil || !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("zero writer ID: %v, %v", s, err)
		}
		if s, err := construct(Config{InstanceID: 1}); s != nil || !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("zero policy: %v, %v", s, err)
		}
	}
	c := Config{Policy: p, InstanceID: math.MaxUint64, Now: func() time.Time {
		t.Fatal("construction sampled the clock")
		return time.Time{}
	}}
	s, err := New(h, id, c)
	interactionCheck(t, err)
	c.Policy, c.InstanceID, c.Now = Policy{}, 0, nil
	if s.config.Policy != p || s.policy != p || s.config.InstanceID != math.MaxUint64 || s.config.Now == nil {
		t.Fatal("configuration was not copied")
	}
	defaulted, err := New(h, id, Config{Policy: p, InstanceID: 1})
	interactionCheck(t, err)
	if defaulted.config.Now == nil {
		t.Fatal("nil clock was not normalized")
	}
	// A writer config is reusable, not consumed by binding another session.
	other, err := New(h, id, s.config)
	interactionCheck(t, err)
	if other.config.InstanceID != s.config.InstanceID {
		t.Fatal("writer identity changed across sessions")
	}
}

func TestAuthoringTimestampRange(t *testing.T) {
	for _, tc := range []struct {
		name string
		time time.Time
		want int64
		bad  bool
	}{
		{"epoch", time.Unix(0, 0), 0, false},
		{"negative", time.Unix(0, -1), -1, false},
		{"minimum", time.Unix(0, math.MinInt64), math.MinInt64, false},
		{"maximum", time.Unix(0, math.MaxInt64), math.MaxInt64, false},
		{"before minimum", time.Unix(0, math.MinInt64).Add(-time.Nanosecond), 0, true},
		{"after maximum", time.Unix(0, math.MaxInt64).Add(time.Nanosecond), 0, true},
		{"zero Go time", time.Time{}, 0, true},
		{"distant past", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), 0, true},
		{"distant future", time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC), 0, true},
		{"offset", time.Unix(0, -123).In(time.FixedZone("offset", 3600)), -123, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := Config{Policy: sessionPolicy(), InstanceID: 1, Now: func() time.Time { calls++; return tc.time }}
			got, err := c.timestamp()
			if calls != 1 || got != tc.want || (err != nil) != tc.bad || (tc.bad && !errors.Is(err, codec.ErrInvalidData)) {
				t.Fatalf("timestamp: %d, %v, calls=%d", got, err, calls)
			}
		})
	}
}
