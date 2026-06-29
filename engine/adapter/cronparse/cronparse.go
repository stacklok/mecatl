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
	return sched.Next(from.In(loc)), nil
}
