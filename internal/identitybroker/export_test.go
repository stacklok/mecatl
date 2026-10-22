package identitybroker

import "time"

// SetClock replaces b's clock. Call it before b is used.
func SetClock(b *Broker, now func() time.Time) { b.sessions.now = now }
