// Package cronparse is the parser-only cron → next-fire helper for the
// scheduled-tasks feature (issue #189). It wraps robfig/cron/v3's parser +
// schedule (NOT the cron.New() daemon — the durable ScheduleStore is ground
// truth, the in-memory timer is a derived lookahead, per decision #6).
//
// It is a pure stdlib + robfig/cron/v3 leaf: NO internal/ import, NO
// engine/port import (so the conformance suite and any adapter can use it
// without pulling the port surface). The composition layer calls NextFire at
// Save time to fail-closed on a bad cron expression (a schedule with a bad cron
// is rejected, never silently never-fires) and at tick time to compute the
// next fire instant it hands to ScheduleStore.Claim.
package cronparse

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// NextFire returns the next instant strictly AFTER from at which the cron
// expression expr next fires, in the given location. expr is a 5-field cron
// expression OR an @-macro (@every <duration>, @daily, @hourly, @weekly,
// @monthly, @yearly/@annually). An invalid expression returns an error
// (fail-closed at Save time — a schedule with a bad cron is rejected, never
// silently never-fires).
//
// The location is the IANA timezone the schedule fires in (per-schedule tz;
// default UTC). Pass time.UTC for a UTC schedule. NextFire does NOT mutate the
// location: it views `from` in `loc` and returns the result in `loc`.
//
// robfig's Schedule.Next returns the next instant STRICTLY AFTER `from` (a fire
// at exactly `from` is the current slot, already claimed; the next is strictly
// after), which is the semantics the claim-before-fire contract wants — a
// claimed slot's NextFireAt advances past `now`, so a peer replica's Due does
// not re-return it.
func NextFire(expr string, from time.Time, loc *time.Location) (time.Time, error) {
	if loc == nil {
		loc = time.UTC
	}
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("cronparse: invalid cron expression %q: %w", expr, err)
	}
	next := sched.Next(from.In(loc))
	if next.IsZero() {
		// robfig's Schedule.Next caps its forward search at ~5 years and returns
		// the ZERO time (with no error) for a parseable-but-impossible expression
		// that can never fire — e.g. "0 0 30 2 *" (Feb 30). ParseStandard accepts
		// these, so without this guard NextFire would return (zeroTime, nil), which
		// the claim-before-fire contract interprets as "no further fire → disable
		// the schedule" — silently turning an impossible cron into a completed one
		// and defeating this package's fail-closed Save-time promise (a schedule
		// with a bad cron is rejected, never silently never-fires). Surface it as
		// an error so composition rejects it at Save time.
		return time.Time{}, fmt.Errorf("cronparse: cron expression %q never fires (impossible schedule)", expr)
	}
	return next, nil
}
