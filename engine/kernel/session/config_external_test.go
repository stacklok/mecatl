package session_test

import (
	"time"

	"github.com/stacklok/mecatl/engine/kernel/session"
)

func testConfig() session.Config {
	return session.Config{Policy: sessionPolicy(), InstanceID: 1, Now: func() time.Time { return time.Unix(0, 0) }}
}

func testConfigFor(p session.Policy) session.Config {
	c := testConfig()
	c.Policy = p
	return c
}
