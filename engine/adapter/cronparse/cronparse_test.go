package cronparse

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNextFire(t *testing.T) {
	utc := time.UTC
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("failed to load America/New_York: %v", err)
	}
	// A fixed summer date (July) so NY is unambiguously on EDT (UTC-4).
	summerFrom := time.Date(2026, 7, 15, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name string
		expr string
		from time.Time
		loc  *time.Location
		want time.Time
	}{
		{
			name: "@daily next midnight UTC",
			expr: "@daily",
			from: time.Date(2026, 1, 15, 10, 30, 45, 0, utc),
			loc:  utc,
			want: time.Date(2026, 1, 16, 0, 0, 0, 0, utc),
		},
		{
			name: "0 9 daily from after 09:00 fires next day UTC",
			expr: "0 9 * * *",
			from: time.Date(2026, 1, 15, 10, 30, 0, 0, utc),
			loc:  utc,
			want: time.Date(2026, 1, 16, 9, 0, 0, 0, utc),
		},
		{
			name: "0 9 daily from before 09:00 fires today UTC",
			expr: "0 9 * * *",
			from: time.Date(2026, 1, 15, 8, 0, 0, 0, utc),
			loc:  utc,
			want: time.Date(2026, 1, 15, 9, 0, 0, 0, utc),
		},
		{
			name: "@every 30m is exactly from + 30m on a second boundary",
			expr: "@every 30m",
			from: time.Date(2026, 1, 15, 10, 30, 0, 0, utc),
			loc:  utc,
			want: time.Date(2026, 1, 15, 11, 0, 0, 0, utc),
		},
		{
			name: "@every 1h is exactly from + 1h",
			expr: "@every 1h",
			from: time.Date(2026, 1, 15, 10, 30, 0, 0, utc),
			loc:  utc,
			want: time.Date(2026, 1, 15, 11, 30, 0, 0, utc),
		},
		{
			name: "*/5 next 5-min boundary strictly after from",
			expr: "*/5 * * * *",
			from: time.Date(2026, 1, 15, 10, 31, 0, 0, utc),
			loc:  utc,
			want: time.Date(2026, 1, 15, 10, 35, 0, 0, utc),
		},
		{
			name: "boundary: from exactly on a fire yields the NEXT fire, not from",
			expr: "*/5 * * * *",
			from: time.Date(2026, 1, 15, 10, 30, 0, 0, utc),
			loc:  utc,
			want: time.Date(2026, 1, 15, 10, 35, 0, 0, utc),
		},
		{
			name: "boundary: @daily from exactly midnight yields next midnight",
			expr: "@daily",
			from: time.Date(2026, 1, 16, 0, 0, 0, 0, utc),
			loc:  utc,
			want: time.Date(2026, 1, 17, 0, 0, 0, 0, utc),
		},
		{
			name: "0 9 in America/New_York fires 09:00 EDT (13:00 UTC) on a summer day",
			expr: "0 9 * * *",
			from: summerFrom,
			loc:  ny,
			want: time.Date(2026, 7, 15, 9, 0, 0, 0, ny),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NextFire(tc.expr, tc.from, tc.loc)
			if err != nil {
				t.Fatalf("NextFire(%q, %v, %v) unexpected error: %v", tc.expr, tc.from, tc.loc, err)
			}
			// The returned time must be in the schedule's location and equal want.
			if !got.Equal(tc.want) {
				t.Errorf("NextFire(%q, %v, %v) = %v (loc %v), want %v (loc %v)",
					tc.expr, tc.from, tc.loc, got, got.Location(), tc.want, tc.want.Location())
			}
			// Invariants on every success case.
			if got.Location() != tc.loc {
				t.Errorf("NextFire(%q) returned time in %v, want location %v", tc.expr, got.Location(), tc.loc)
			}
			if !got.After(tc.from) {
				t.Errorf("NextFire(%q) = %v is not strictly after from %v", tc.expr, got, tc.from)
			}
		})
	}
}

func TestNextFireNewYorkSummerIsEDT(t *testing.T) {
	// Pin the DST offset explicitly: a July 09:00 NY fire must be UTC-4 (EDT),
	// i.e. 13:00 UTC. This guards against a future tzdata shift silently
	// changing the offset the schedule fires in.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("failed to load America/New_York: %v", err)
	}
	from := time.Date(2026, 7, 15, 10, 30, 0, 0, time.UTC)
	got, err := NextFire("0 9 * * *", from, ny)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("NY summer 09:00 fire = %v (UTC %v), want %v", got, got.In(time.UTC), want)
	}
	_, offset := got.In(ny).Zone()
	if offset != -4*60*60 {
		t.Errorf("NY summer offset = %d, want -14400 (EDT, UTC-4)", offset)
	}
}

func TestNextFireInvalidExpression(t *testing.T) {
	utc := time.UTC
	from := time.Date(2026, 1, 15, 10, 30, 0, 0, utc)
	for _, expr := range []string{
		"not a cron",
		"99 99 99 99 99",
		"",
		"@bogus",
	} {
		t.Run(expr, func(t *testing.T) {
			got, err := NextFire(expr, from, utc)
			if err == nil {
				t.Errorf("NextFire(%q) want error, got %v", expr, got)
			}
			if !got.IsZero() {
				t.Errorf("NextFire(%q) error case must return zero time, got %v", expr, got)
			}
		})
	}
}

func TestNextFireNilLocationDefaultsUTC(t *testing.T) {
	// A nil location must not panic and must behave as UTC.
	from := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	got, err := NextFire("@daily", from, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("NextFire with nil loc = %v, want %v", got, want)
	}
	if got.Location() != time.UTC {
		t.Errorf("NextFire with nil loc returned %v, want UTC", got.Location())
	}
}

func TestNextFireFromLocationIsHonoured(t *testing.T) {
	// A `from` expressed in a different location must still resolve against the
	// schedule's location: 10:30 UTC is 06:30 NY (EDT), so the next 09:00 NY
	// fire is the same day at 13:00 UTC.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("failed to load America/New_York: %v", err)
	}
	from := time.Date(2026, 7, 15, 10, 30, 0, 0, time.UTC) // 06:30 EDT
	got, err := NextFire("0 9 * * *", from, ny)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC) // 09:00 EDT
	if !got.Equal(want) {
		t.Errorf("NextFire = %v (UTC %v), want %v", got, got.In(time.UTC), want)
	}
}

func TestNextFireErrorIsWrapped(t *testing.T) {
	// The error must be identifiable as a cron-parse error via errors.Is on the
	// inner robfig error type only loosely; here we assert the message carries
	// the expression and is non-empty, and that it's not a sentinel that masks
	// other failures.
	from := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	_, err := NextFire("99 99 99 99 99", from, time.UTC)
	if err == nil {
		t.Fatal("want error for invalid expression")
	}
	if !errors.Is(err, err) { // sanity: the error chain is self-consistent
		t.Errorf("errors.Is self-check failed: %v", err)
	}
	if !strings.Contains(err.Error(), "99 99 99 99 99") {
		t.Errorf("error message must mention the offending expression; got: %v", err)
	}
}

func TestNextFireImpossibleButParseable(t *testing.T) {
	// A cron expression that PARSES but can never fire — "0 0 30 2 *" (Feb 30
	// never exists). robfig's Schedule.Next caps its forward search and returns
	// the ZERO time with NO error. NextFire must NOT pass that through as
	// (zeroTime, nil): a zero next-fire is interpreted downstream as "no further
	// fire → disable the schedule", silently turning an impossible cron into a
	// completed one. It must fail-closed with an error so the create-seam rejects
	// it (review #189).
	from := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	got, err := NextFire("0 0 30 2 *", from, time.UTC)
	if err == nil {
		t.Fatalf("NextFire(impossible) = %v, want error (never fires)", got)
	}
	if !got.IsZero() {
		t.Errorf("NextFire(impossible) error case must return zero time, got %v", got)
	}
	if !strings.Contains(err.Error(), "0 0 30 2 *") {
		t.Errorf("error must mention the offending expression; got: %v", err)
	}
}
