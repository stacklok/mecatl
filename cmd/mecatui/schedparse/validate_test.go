package schedparse_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/cmd/mecatui/internal/timezones"
	"github.com/stacklok/mecatl/cmd/mecatui/schedparse"
	"github.com/stacklok/mecatl/engine/adapter/cronparse"
)

func TestValidatedPhrases(t *testing.T) {
	now := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ input, cron string }{
		{"run every 30 minutes", "*/30 * * * *"},
		{" Run everyday at 9am ", "0 9 * * *"},
		{"every day at 09:30", "30 9 * * *"},
		{"RUN DAILY AT 12AM", "0 0 * * *"},
		{"daily at 12pm", "0 12 * * *"},
		{"daily at 0:59", "59 0 * * *"},
		{"daily at 23:59", "59 23 * * *"},
		{"every weekday at 5pm", "0 17 * * 1-5"},
		{"run every sunday at 9am", "0 9 * * 0"},
		{"every 59 minutes", "*/59 * * * *"},
		{"every 23 hours", "0 */23 * * *"},
		{"every 1 hour", "0 */1 * * *"},
		{"every hour", "0 * * * *"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			preview, err := schedparse.Validate(tc.input, "UTC", now)
			if err != nil || preview.Cron != tc.cron || !preview.OneShot.IsZero() || preview.Timezone != "UTC" || !preview.Next.After(now) {
				t.Fatalf("preview=%+v err=%v", preview, err)
			}
		})
	}
}

func TestInvalidTimingAndZone(t *testing.T) {
	now := time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)
	for _, input := range []string{
		"", "everyday at noon", "every 30 minutes and run tests", "run run every hour",
		"run */5 * * * *", "run @daily", "daily at 0am", "daily at 0pm", "daily at 13am",
		"daily at 13pm", "daily at 24", "daily at 9:60", "daily at -1", "next sunday 0am",
		"tomorrow at 13pm", "every weekday at 24:00", "every 60 minutes", "every 24 hours",
		"every 0 hours", "in 0 minutes", "in -1 day", "in 999999999999999999999 hours",
		"in 153722868 minutes", "in 307445735 minutes", "in 2562048 hours", "in 106752 days", "in 15251 weeks",
		"0 0 30 2 *", "0 9 * * 7", "0 0 0 * *", "0 0 1 13 *", "* * * * * *", "@nonsense",
	} {
		t.Run(input, func(t *testing.T) {
			preview, err := schedparse.Validate(input, "UTC", now)
			var fieldErr *schedparse.ValidationError
			if !errors.As(err, &fieldErr) || fieldErr.Field != "timing" || preview != (schedparse.Preview{}) {
				t.Fatalf("preview=%+v err=%v", preview, err)
			}
		})
	}
	for _, zone := range []string{"", "Local", "PST", "America/Typo", "utc"} {
		preview, err := schedparse.Validate("every hour", zone, now)
		var fieldErr *schedparse.ValidationError
		if !errors.As(err, &fieldErr) || fieldErr.Field != "timezone" || preview != (schedparse.Preview{}) {
			t.Fatalf("zone=%q preview=%+v err=%v", zone, preview, err)
		}
	}
	for _, prefix := range []string{"TZ=", "tz=", "Tz=", "CRON_TZ=", "cron_tz=", "Cron_Tz="} {
		for _, suffix := range []string{"America/New_York 0 9 * * *", "UTC @daily"} {
			input := " \t" + prefix + suffix
			preview, err := schedparse.Validate(input, "UTC", now)
			if err == nil || !strings.Contains(err.Error(), "timezone control") || preview != (schedparse.Preview{}) {
				t.Fatalf("embedded override %q: preview=%+v err=%v", input, preview, err)
			}
		}
	}
}

func TestPreviewBackendParity(t *testing.T) {
	for _, zone := range []string{"UTC", "America/New_York", "Asia/Kolkata"} {
		loc, err := timezones.Location(zone)
		if err != nil {
			t.Fatal(err)
		}
		for _, now := range []time.Time{
			time.Date(2026, 7, 13, 9, 0, 0, 0, loc), // exactly on a daily slot
			time.Date(2026, 3, 8, 0, 0, 0, 0, loc),  // spring gap
			time.Date(2026, 11, 1, 0, 0, 0, 0, loc), // autumn fold
		} {
			for _, expr := range []string{
				"0 9 * * *", "30 2 * * *", "30 1 * * *", "*/30 * * * *", "0 9 * * 0",
				"0 9 * * MON-FRI", "0 0 29 2 *", "0 0 30 2 *", "bad cron", "0 9 * * 7",
				"@hourly", "@daily", "@midnight", "@weekly", "@monthly", "@yearly", "@annually", "@every 90m",
			} {
				preview, gotErr := schedparse.Validate(expr, zone, now)
				want, wantErr := cronparse.NextFire(expr, now, loc)
				if (gotErr != nil) != (wantErr != nil) {
					t.Fatalf("%s %s %v: client=%v backend=%v", zone, expr, now, gotErr, wantErr)
				}
				if gotErr == nil && (!preview.Next.Equal(want) || !preview.Next.After(now) || preview.Cron != expr || preview.Timezone != zone) {
					t.Fatalf("%s %s: got=%+v want=%v", zone, expr, preview, want)
				}
			}
		}
	}
}

func TestCalendarAndElapsedOneShots(t *testing.T) {
	loc, err := timezones.Location("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 7, 17, 0, 0, 0, time.UTC) // local noon, before spring DST
	for _, tc := range []struct {
		input string
		want  time.Time
	}{
		{"tomorrow at 12pm", time.Date(2026, 3, 8, 12, 0, 0, 0, loc)},
		{"run next sunday 9am", time.Date(2026, 3, 8, 9, 0, 0, 0, loc)},
		{"in 1 day", now.Add(24 * time.Hour)},
		{"RUN IN 2 HOURS", now.Add(2 * time.Hour)},
		{"in 1 week", now.Add(7 * 24 * time.Hour)},
		{"in 153722867 minutes", now.Add(153722867 * time.Minute)},
	} {
		preview, err := schedparse.Validate(tc.input, "America/New_York", now)
		if err != nil || !preview.OneShot.Equal(tc.want) || !preview.Next.Equal(tc.want) || preview.Cron != "" || preview.Next.Location().String() != loc.String() {
			t.Fatalf("%s: preview=%+v err=%v want=%v", tc.input, preview, err, tc.want)
		}
	}
	// Sunday before, exactly on, and after its slot keeps next-Sunday semantics.
	for _, hour := range []int{8, 9, 10} {
		at := time.Date(2026, 7, 19, hour, 0, 0, 0, loc)
		preview, err := schedparse.Validate("next sunday 9am", "America/New_York", at.UTC())
		day := 19
		if hour >= 9 {
			day = 26
		}
		want := time.Date(2026, 7, day, 9, 0, 0, 0, loc)
		if err != nil || !preview.OneShot.Equal(want) {
			t.Fatalf("Sunday hour %d: %+v %v want %v", hour, preview, err, want)
		}
	}
	// UTC is already Tuesday, but the chosen zone is still Monday.
	boundary := time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC)
	calendar, err := schedparse.Validate("tomorrow at 9am", "America/New_York", boundary)
	if err != nil || !calendar.OneShot.Equal(time.Date(2026, 7, 14, 9, 0, 0, 0, loc)) {
		t.Fatalf("zone calendar boundary: %+v %v", calendar, err)
	}
	// Calendar phrases retain Go's own gap/fold normalization, not a new DST policy.
	for _, date := range []struct {
		month     time.Month
		day, hour int
	}{
		{time.March, 8, 2}, {time.November, 1, 1},
	} {
		before := time.Date(2026, date.month, date.day-1, 12, 0, 0, 0, loc)
		input := "tomorrow at 2:30"
		if date.hour == 1 {
			input = "tomorrow at 1:30"
		}
		preview, err := schedparse.Validate(input, "America/New_York", before.UTC())
		want := time.Date(2026, date.month, date.day, date.hour, 30, 0, 0, loc)
		if err != nil || !preview.OneShot.Equal(want) {
			t.Fatalf("DST calendar: %+v %v want %v", preview, err, want)
		}
	}
	// A fresh Create validation recomputes relative estimates instead of submitting an expired preview.
	first, err := schedparse.Validate("in 1 minute", "America/New_York", now)
	if err != nil {
		t.Fatal(err)
	}
	createNow := now.Add(2 * time.Minute)
	second, err := schedparse.Validate("in 1 minute", "America/New_York", createNow)
	if err != nil || !second.OneShot.After(createNow) || !first.OneShot.Before(createNow) {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
}

func TestRecurringLocalNineAcrossDST(t *testing.T) {
	for _, tc := range []struct {
		month   time.Month
		utcHour int
	}{{time.January, 14}, {time.July, 13}} {
		now := time.Date(2026, tc.month, 15, 0, 0, 0, 0, time.UTC)
		preview, err := schedparse.Validate("daily at 9am", "America/New_York", now)
		if err != nil || preview.Cron != "0 9 * * *" || preview.Next.Hour() != 9 || preview.Next.UTC().Hour() != tc.utcHour {
			t.Fatalf("%v: %+v %v", tc.month, preview, err)
		}
	}
}
