package schedparse

import (
	"testing"
	"time"
)

// fixedNow is the deterministic reference instant for the one-shot tests:
// Wednesday 2026-07-13 09:00:00 UTC.
func fixedNow() time.Time {
	return time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC)
}

func TestCompileEveryNMinutes(t *testing.T) {
	r, ok := Compile("every 5 minutes", fixedNow())
	if !ok {
		t.Fatal("expected match")
	}
	if r.Cron != "*/5 * * * *" || !r.OneShot.IsZero() {
		t.Fatalf("got %+v, want cron */5 * * * *", r)
	}
	// singular form
	r, ok = Compile("every 1 minute", fixedNow())
	if !ok || r.Cron != "*/1 * * * *" {
		t.Fatalf("singular: got %+v ok=%v", r, ok)
	}
}

func TestCompileEveryNHours(t *testing.T) {
	r, ok := Compile("every 3 hours", fixedNow())
	if !ok {
		t.Fatal("expected match")
	}
	if r.Cron != "0 */3 * * *" || !r.OneShot.IsZero() {
		t.Fatalf("got %+v, want cron 0 */3 * * *", r)
	}
}

func TestCompileEveryHour(t *testing.T) {
	r, ok := Compile("every hour", fixedNow())
	if !ok || r.Cron != "0 * * * *" {
		t.Fatalf("got %+v ok=%v", r, ok)
	}
}

func TestCompileDailyAt(t *testing.T) {
	cases := map[string]string{
		"daily at 9am":     "0 9 * * *",
		"daily at 9":       "0 9 * * *",
		"daily at 9:30am":  "30 9 * * *",
		"daily at 9:30 pm": "30 21 * * *",
		"daily at 12am":    "0 0 * * *",  // midnight
		"daily at 12pm":    "0 12 * * *", // noon
		"daily at 11:45pm": "45 23 * * *",
	}
	for in, want := range cases {
		r, ok := Compile(in, fixedNow())
		if !ok {
			t.Errorf("%q: expected match", in)
			continue
		}
		if r.Cron != want {
			t.Errorf("%q: got cron %q, want %q", in, r.Cron, want)
		}
	}
}

func TestCompileEveryWeekdayAt(t *testing.T) {
	r, ok := Compile("every weekday at 9am", fixedNow())
	if !ok {
		t.Fatal("expected match")
	}
	if r.Cron != "0 9 * * 1-5" {
		t.Fatalf("got cron %q, want 0 9 * * 1-5", r.Cron)
	}
	// with minutes + pm
	r, ok = Compile("every weekday at 5:30pm", fixedNow())
	if !ok || r.Cron != "30 17 * * 1-5" {
		t.Fatalf("got %+v ok=%v", r, ok)
	}
}

func TestCompileEveryDowAt(t *testing.T) {
	cases := map[string]string{
		"every monday at 9am":     "0 9 * * 1",
		"every friday at 5pm":     "0 17 * * 5",
		"every sunday at 10:30am": "30 10 * * 7",
	}
	for in, want := range cases {
		r, ok := Compile(in, fixedNow())
		if !ok {
			t.Errorf("%q: expected match", in)
			continue
		}
		if r.Cron != want {
			t.Errorf("%q: got cron %q, want %q", in, r.Cron, want)
		}
	}
}

func TestCompileNextDowOneShot(t *testing.T) {
	now := fixedNow() // Mon 2026-07-13 09:00 UTC
	// next monday → today is monday, 9am == now → roll to next week (2026-07-20).
	r, ok := Compile("next monday 9am", now)
	if !ok {
		t.Fatal("expected match")
	}
	want := time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)
	if !r.OneShot.Equal(want) || r.Cron != "" {
		t.Fatalf("got %+v, want one-shot %v", r, want)
	}
	// next friday 5pm → 2026-07-17 17:00
	r, ok = Compile("next friday 5pm", now)
	if !ok {
		t.Fatal("expected match")
	}
	want = time.Date(2026, 7, 17, 17, 0, 0, 0, time.UTC)
	if !r.OneShot.Equal(want) {
		t.Fatalf("got %v, want %v", r.OneShot, want)
	}
	// next wednesday 8am — today is mon, so Wed is 2 days ahead (2026-07-15 08:00).
	r, ok = Compile("next wednesday 8am", now)
	if !ok {
		t.Fatal("expected match")
	}
	want = time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	if !r.OneShot.Equal(want) {
		t.Fatalf("got %v, want %v", r.OneShot, want)
	}
	// next monday 10am — today is monday, 10am is later today.
	r, ok = Compile("next monday 10am", now)
	if !ok {
		t.Fatal("expected match")
	}
	want = time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	if !r.OneShot.Equal(want) {
		t.Fatalf("got %v, want %v (today, later)", r.OneShot, want)
	}
}

func TestCompileInDuration(t *testing.T) {
	now := fixedNow()
	cases := []struct {
		in   string
		want time.Time
	}{
		{"in 30 minutes", now.Add(30 * time.Minute)},
		{"in 2 hours", now.Add(2 * time.Hour)},
		{"in 1 day", now.Add(24 * time.Hour)},
		{"in 1 week", now.Add(7 * 24 * time.Hour)},
		{"in 1 minute", now.Add(1 * time.Minute)},
	}
	for _, c := range cases {
		r, ok := Compile(c.in, now)
		if !ok {
			t.Errorf("%q: expected match", c.in)
			continue
		}
		if !r.OneShot.Equal(c.want) || r.Cron != "" {
			t.Errorf("%q: got %+v, want one-shot %v", c.in, r, c.want)
		}
	}
}

func TestCompileTomorrowAt(t *testing.T) {
	now := fixedNow() // Wed 2026-07-13 09:00 UTC
	r, ok := Compile("tomorrow at 9am", now)
	if !ok {
		t.Fatal("expected match")
	}
	want := time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	if !r.OneShot.Equal(want) || r.Cron != "" {
		t.Fatalf("got %+v, want one-shot %v", r, want)
	}
	r, ok = Compile("tomorrow at 5:30pm", now)
	if !ok {
		t.Fatal("expected match")
	}
	want = time.Date(2026, 7, 14, 17, 30, 0, 0, time.UTC)
	if !r.OneShot.Equal(want) {
		t.Fatalf("got %v, want %v", r.OneShot, want)
	}
}

func TestCompileCaseInsensitive(t *testing.T) {
	r, ok := Compile("EVERY 30 MINUTES", fixedNow())
	if !ok || r.Cron != "*/30 * * * *" {
		t.Fatalf("uppercase: got %+v ok=%v", r, ok)
	}
}

func TestCompileWhitespaceTolerant(t *testing.T) {
	r, ok := Compile("  every 30 minutes  ", fixedNow())
	if !ok || r.Cron != "*/30 * * * *" {
		t.Fatalf("padded: got %+v ok=%v", r, ok)
	}
}

func TestCompileEmpty(t *testing.T) {
	if _, ok := Compile("", fixedNow()); ok {
		t.Error("empty string should not match")
	}
	if _, ok := Compile("   ", fixedNow()); ok {
		t.Error("whitespace-only should not match")
	}
}

func TestCompileNoMatchGibberish(t *testing.T) {
	for _, in := range []string{
		"hello world",
		"asdf",
		"run the tests",
		"every day at noon", // not a supported phrase
		"each monday",       // not "every"
	} {
		if _, ok := Compile(in, fixedNow()); ok {
			t.Errorf("gibberish %q should not match", in)
		}
	}
}

func TestCompilePartialMatchDoesNotMatch(t *testing.T) {
	// "every 30 minutes and then run tests" must NOT match — the patterns are
	// anchored, so a trailing-suffix phrase falls through to no-match (raw cron).
	if _, ok := Compile("every 30 minutes and then run tests", fixedNow()); ok {
		t.Error("trailing-suffix phrase should not match (anchored patterns)")
	}
	// "daily at 25pm" — out-of-range hour, so parseClock fails → no match.
	if _, ok := Compile("daily at 25pm", fixedNow()); ok {
		t.Error("out-of-range hour should not match")
	}
	// "every 0 minutes" — zero interval is invalid.
	if _, ok := Compile("every 0 minutes", fixedNow()); ok {
		t.Error("zero interval should not match")
	}
}

func TestCompileRawCronFallsThrough(t *testing.T) {
	// A raw cron expression like "*/5 * * * *" is NOT one of the NL phrases,
	// so Compile returns ok=false — the caller treats it as raw cron.
	if _, ok := Compile("*/5 * * * *", fixedNow()); ok {
		t.Error("a raw cron expression should not match an NL pattern")
	}
	if _, ok := Compile("0 9 * * 1-5", fixedNow()); ok {
		t.Error("a raw cron expression should not match an NL pattern")
	}
}
