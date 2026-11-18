package modelrequest

import (
	"time"

	"github.com/stacklok/mecatl/engine/kernel/session"
)

func testConfig() session.Config {
	return session.Config{InstanceID: 1, Now: func() time.Time { return time.Unix(0, 0) }}
}
