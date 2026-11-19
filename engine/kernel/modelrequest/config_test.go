package modelrequest

import (
	"time"

	"github.com/stacklok/mecatl/engine/kernel/session"
)

func testConfig() session.Config {
	limits := testLimits()
	return session.Config{Policy: session.Policy{CommitLimits: limits, PayloadLimits: limits, MaxRecordsPerCommit: 1024, SoftMaxRetainedRecords: 32768, MaxRetainedRecords: 32768, SoftMaxPayloadBytes: 1 << 30, MaxPayloadBytes: 1 << 30, SoftMaxDecodedWork: 1 << 40, MaxDecodedWork: 1 << 40}, InstanceID: 1, Now: func() time.Time { return time.Unix(0, 0) }}
}

func testConfigFor(p session.Policy) session.Config {
	c := testConfig()
	c.Policy = p
	return c
}
