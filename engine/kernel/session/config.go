package session

import (
	"errors"
	"math"
	"time"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

// ErrInvalidConfig identifies a missing writer instance identity.
var ErrInvalidConfig = errors.New("invalid session authoring config")

// Config supplies diagnostic metadata for newly authored history. It is separate
// from resource Policy and is copied by New and Load. Reuse the same nonzero
// InstanceID across sessions belonging to one running writer instance; it proves
// neither authority nor global uniqueness. A new writer loading history supplies
// its own identity without changing historical metadata.
//
// Session calls remain caller-serialized. If Now closes over state shared across
// sessions, the caller is responsible for synchronizing that state.
type Config struct {
	// InstanceID is the required nonzero diagnostic writer identity.
	InstanceID uint64
	// Now supplies wall-clock time, defaulting to time.Now when nil. Each
	// successful record admission and first submission preparation samples once.
	// Values outside the signed Unix-nanosecond range fail with codec.ErrInvalidData.
	Now func() time.Time
}

// normalized copies authoring configuration without sampling the clock.
func (c Config) normalized() (Config, error) {
	if c.InstanceID == 0 {
		return Config{}, ErrInvalidConfig
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c, nil
}

// timestamp samples once and checks the representable wall-time interval before
// converting, because UnixNano silently wraps outside it. Monotonic data is not stored.
func (c Config) timestamp() (int64, error) {
	t := c.Now()
	if t.Before(time.Unix(0, math.MinInt64)) || t.After(time.Unix(0, math.MaxInt64)) {
		return 0, codec.ErrInvalidData
	}
	return t.UnixNano(), nil
}
