package sessiongroup_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/lease"
	"github.com/stacklok/mecatl/engine/kernel/session"
	"github.com/stacklok/mecatl/engine/kernel/sessiongroup"
	groupid "github.com/stacklok/mecatl/engine/kernel/sessiongroup/id"
)

type testProvider struct {
	called bool
}

func (p *testProvider) Begin(groupid.GroupID) (lease.Attempt, error) {
	p.called = true
	return nil, nil
}

type testClock struct {
	nowCalled  bool
	waitCalled bool
}

func (c *testClock) Now() time.Time {
	c.nowCalled = true
	return time.Time{}
}

func (c *testClock) Wait(context.Context, time.Time) error {
	c.waitCalled = true
	return nil
}

func minimalLimits() codec.Limits {
	return codec.Limits{
		MaxBytes:              1,
		MaxStringBytes:        1,
		MaxByteStringBytes:    1,
		MaxArrayElements:      1,
		MaxMapPairs:           1,
		MaxDepth:              1,
		MaxNodes:              1,
		MaxCollectionElements: 1,
	}
}

func validConfig(provider lease.Provider, clock sessiongroup.Clock) sessiongroup.Config {
	limits := minimalLimits()
	return sessiongroup.Config{
		Provider: provider,
		Clock:    clock,
		HistoryPolicy: history.Policy{
			CommitLimits:   limits,
			RetryWindow:    1,
			MaxPageBytes:   1,
			MaxPageCommits: 1,
		},
		SessionPolicy: session.Policy{
			CommitLimits:           limits,
			PayloadLimits:          limits,
			MaxRecordsPerCommit:    1,
			SoftMaxRetainedRecords: 1,
			SoftMaxPayloadBytes:    1,
			SoftMaxDecodedWork:     1,
			MaxRetainedRecords:     1,
			MaxPayloadBytes:        1,
			MaxDecodedWork:         1,
		},
		InstanceID:        1,
		OpeningTimeout:    time.Second,
		AttemptTimeout:    time.Nanosecond,
		RetryInterval:     time.Nanosecond,
		RenewInterval:     time.Nanosecond,
		DrainTimeout:      time.Nanosecond,
		ShutdownTimeout:   2 * time.Nanosecond,
		MaxLoadedSessions: 1,
	}
}

func TestConfigValidate(t *testing.T) {
	provider := &testProvider{}
	clock := &testClock{}
	if err := validConfig(provider, clock).Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if provider.called || clock.nowCalled || clock.waitCalled {
		t.Fatal("validation executed an injected dependency")
	}

	for name, mutate := range map[string]func(*sessiongroup.Config){
		"nil provider":                     func(c *sessiongroup.Config) { c.Provider = nil },
		"nil clock":                        func(c *sessiongroup.Config) { c.Clock = nil },
		"zero history policy":              func(c *sessiongroup.Config) { c.HistoryPolicy = history.Policy{} },
		"invalid history policy":           func(c *sessiongroup.Config) { c.HistoryPolicy.RetryWindow = 0 },
		"zero session policy":              func(c *sessiongroup.Config) { c.SessionPolicy = session.Policy{} },
		"invalid session policy":           func(c *sessiongroup.Config) { c.SessionPolicy.MaxRecordsPerCommit = 0 },
		"zero instance ID":                 func(c *sessiongroup.Config) { c.InstanceID = 0 },
		"zero opening timeout":             func(c *sessiongroup.Config) { c.OpeningTimeout = 0 },
		"negative opening timeout":         func(c *sessiongroup.Config) { c.OpeningTimeout = -time.Nanosecond },
		"zero attempt timeout":             func(c *sessiongroup.Config) { c.AttemptTimeout = 0 },
		"negative attempt timeout":         func(c *sessiongroup.Config) { c.AttemptTimeout = -time.Nanosecond },
		"zero retry interval":              func(c *sessiongroup.Config) { c.RetryInterval = 0 },
		"negative retry interval":          func(c *sessiongroup.Config) { c.RetryInterval = -time.Nanosecond },
		"zero renewal interval":            func(c *sessiongroup.Config) { c.RenewInterval = 0 },
		"negative renewal interval":        func(c *sessiongroup.Config) { c.RenewInterval = -time.Nanosecond },
		"zero drain timeout":               func(c *sessiongroup.Config) { c.DrainTimeout = 0 },
		"negative drain timeout":           func(c *sessiongroup.Config) { c.DrainTimeout = -time.Nanosecond },
		"zero shutdown timeout":            func(c *sessiongroup.Config) { c.ShutdownTimeout = 0 },
		"negative shutdown timeout":        func(c *sessiongroup.Config) { c.ShutdownTimeout = -time.Nanosecond },
		"equal shutdown and drain timeout": func(c *sessiongroup.Config) { c.ShutdownTimeout = c.DrainTimeout },
		"shorter shutdown timeout":         func(c *sessiongroup.Config) { c.ShutdownTimeout = c.DrainTimeout - time.Nanosecond },
		"zero loaded sessions":             func(c *sessiongroup.Config) { c.MaxLoadedSessions = 0 },
		"negative loaded sessions":         func(c *sessiongroup.Config) { c.MaxLoadedSessions = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			provider := &testProvider{}
			clock := &testClock{}
			config := validConfig(provider, clock)
			mutate(&config)
			if err := config.Validate(); !errors.Is(err, sessiongroup.ErrInvalidConfig) {
				t.Fatalf("Validate() = %v, want ErrInvalidConfig", err)
			}
			if provider.called || clock.nowCalled || clock.waitCalled {
				t.Fatal("invalid validation executed an injected dependency")
			}
		})
	}
}

func TestConfigValidateBoundaries(t *testing.T) {
	provider := &testProvider{}
	clock := &testClock{}
	config := validConfig(provider, clock)
	config.OpeningTimeout = time.Nanosecond
	config.AttemptTimeout = time.Hour
	config.RetryInterval = time.Hour
	config.RenewInterval = time.Hour
	config.DrainTimeout = time.Nanosecond
	config.ShutdownTimeout = 2 * time.Nanosecond
	config.MaxLoadedSessions = 1
	if err := config.Validate(); err != nil {
		t.Fatalf("independent positive bounds must remain valid: %v", err)
	}
}
