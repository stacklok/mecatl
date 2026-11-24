package resourcegroup_test

import (
	"context"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/kernel/groupstorage"
	"github.com/stacklok/mecatl/engine/kernel/resourcegroup"
	groupid "github.com/stacklok/mecatl/engine/kernel/resourcegroup/id"
)

type poisonClock struct{}

func (poisonClock) Now() time.Time                        { panic("unexpected Clock.Now") }
func (poisonClock) Wait(context.Context, time.Time) error { panic("unexpected Clock.Wait") }

func validConfig() resourcegroup.ManagerConfig {
	return resourcegroup.ManagerConfig{
		ResolveStorage: func(context.Context, groupid.GroupID) (groupstorage.Backend, error) {
			panic("unexpected ResolveStorage")
		},
		Clock:           poisonClock{},
		LeaseDuration:   2 * time.Nanosecond,
		OpeningTimeout:  time.Nanosecond,
		AttemptTimeout:  time.Hour,
		RetryInterval:   time.Hour,
		RenewInterval:   time.Nanosecond,
		DrainTimeout:    time.Nanosecond,
		ShutdownTimeout: 2 * time.Nanosecond,
	}
}

func TestManagerConfigValidate(t *testing.T) {
	// Attempt/retry bounds deliberately exceed opening, lease, and shutdown
	// bounds: only the two documented relationships belong to validation.
	if err := validConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*resourcegroup.ManagerConfig){
		"nil resolver":     func(c *resourcegroup.ManagerConfig) { c.ResolveStorage = nil },
		"nil clock":        func(c *resourcegroup.ManagerConfig) { c.Clock = nil },
		"equal renewal":    func(c *resourcegroup.ManagerConfig) { c.RenewInterval = c.LeaseDuration },
		"longer renewal":   func(c *resourcegroup.ManagerConfig) { c.RenewInterval = c.LeaseDuration + 1 },
		"equal shutdown":   func(c *resourcegroup.ManagerConfig) { c.ShutdownTimeout = c.DrainTimeout },
		"shorter shutdown": func(c *resourcegroup.ManagerConfig) { c.ShutdownTimeout = c.DrainTimeout - 1 },
	} {
		t.Run(name, func(t *testing.T) {
			c := validConfig()
			mutate(&c)
			if err := c.Validate(); err != resourcegroup.ErrInvalidConfig {
				t.Fatalf("Validate() = %v, want ErrInvalidConfig", err)
			}
		})
	}
	for name, field := range map[string]func(*resourcegroup.ManagerConfig) *time.Duration{
		"lease":    func(c *resourcegroup.ManagerConfig) *time.Duration { return &c.LeaseDuration },
		"opening":  func(c *resourcegroup.ManagerConfig) *time.Duration { return &c.OpeningTimeout },
		"attempt":  func(c *resourcegroup.ManagerConfig) *time.Duration { return &c.AttemptTimeout },
		"retry":    func(c *resourcegroup.ManagerConfig) *time.Duration { return &c.RetryInterval },
		"renew":    func(c *resourcegroup.ManagerConfig) *time.Duration { return &c.RenewInterval },
		"drain":    func(c *resourcegroup.ManagerConfig) *time.Duration { return &c.DrainTimeout },
		"shutdown": func(c *resourcegroup.ManagerConfig) *time.Duration { return &c.ShutdownTimeout },
	} {
		t.Run(name, func(t *testing.T) {
			for _, value := range []time.Duration{0, -1} {
				c := validConfig()
				*field(&c) = value
				if err := c.Validate(); err != resourcegroup.ErrInvalidConfig {
					t.Fatalf("Validate() with %v = %v, want ErrInvalidConfig", value, err)
				}
			}
		})
	}
	if err := (resourcegroup.ManagerConfig{}).Validate(); err != resourcegroup.ErrInvalidConfig {
		t.Fatalf("zero Validate() = %v", err)
	}
}
