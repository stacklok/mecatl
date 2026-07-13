// Package schedparse is a small client-side natural-language → trigger
// compiler for the mecatui /schedule overlay's Create form.
//
// It is a small pattern table for common scheduling phrases, NOT a full NLP
// engine: a handful of ordered regexes, first-match-wins. A phrase that does
// not match any pattern returns ok=false, and the caller falls back to
// treating the input verbatim as a raw cron expression.
//
// The package is stdlib-only (regexp + time) and depends on nothing in
// engine/ or internal/ — it is a pure leaf that runs client-side.
package schedparse

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Result is the outcome of Compile: exactly one of Cron or OneShot is set. The
// caller decides which kind of trigger to build (a cron schedule vs a one-shot
// schedule) — Compile only produces the value.
type Result struct {
	Cron    string    // a 5-field cron expression, set when the phrase is recurring
	OneShot time.Time // a wall-clock instant, set when the phrase is a one-shot (zero otherwise)
}

// weekday map: mon=1 … sun=7 (cron's DOW convention, where 0 and 7 are both
// Sunday; we use 1-7 with mon=1 to match the pattern table's promise).
var weekdayNum = map[string]int{
	"mon": 1, "tue": 2, "wed": 3, "thu": 4,
	"fri": 5, "sat": 6, "sun": 7,
}

// parseClock normalizes an hour[:minute] + am/pm into 24h minute/hour. hour is
// 1-12 (or 0-23 when no am/pm is given). Returns ok=false on an out-of-range
// value so the caller can treat the phrase as unmatched (fall back to raw cron).
func parseClock(hourStr, minStr, meridian string) (hour, minute int, ok bool) {
	h, err := strconv.Atoi(hourStr)
	if err != nil {
		return 0, 0, false
	}
	mm := 0
	if minStr != "" {
		mm, err = strconv.Atoi(minStr)
		if err != nil {
			return 0, 0, false
		}
	}
	if meridian != "" {
		// 12-hour clock: normalize 12 → 0 then add 12 for pm.
		switch strings.ToLower(meridian) {
		case "am":
			if h == 12 {
				h = 0
			}
		case "pm":
			if h != 12 {
				h += 12
			}
		default:
			return 0, 0, false
		}
	}
	if h < 0 || h > 23 || mm < 0 || mm > 59 {
		return 0, 0, false
	}
	return h, mm, true
}

// nextWeekdayTime computes the next instant (>= now) that lands on weekday w
// (1=mon … 7=sun) at the given hour:minute in the same location as now. If the
// time today has not passed, it is today; otherwise next week.
func nextWeekdayTime(now time.Time, w, hour, minute int) time.Time {
	loc := now.Location()
	// Target on today's date.
	y, m, d := now.Date()
	target := time.Date(y, m, d, hour, minute, 0, 0, loc)
	// cron weekday: 1=mon..7=sun. Go: Sunday=0..Saturday=6. Map.
	goWd := int(target.Weekday())
	if goWd == 0 {
		goWd = 7 // Sunday
	}
	// Days to advance to reach w (may be 0 if today is the weekday).
	days := w - goWd
	if days < 0 {
		days += 7
	}
	target = target.AddDate(0, 0, days)
	// If today is the weekday but the time has passed (or is exactly now),
	// roll forward a week — "next monday 9am" said at mon 09:00 means next week.
	if days == 0 && !target.After(now) {
		target = target.AddDate(0, 0, 7)
	}
	return target
}

// pattern is one row of the ordered table.
type pattern struct {
	re  *regexp.Regexp
	cls func(now time.Time, m []string) (Result, bool)
}

// compilePattern builds a case-insensitive, anchored regexp from src.
func compilePattern(src string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)^\s*` + src + `\s*$`)
}

// table is the ordered pattern table (first-match-wins).
var table = []pattern{
	// every N minutes → */N * * * *
	{
		compilePattern(`every (\d+) minutes?`),
		func(_ time.Time, m []string) (Result, bool) {
			n, err := strconv.Atoi(m[1])
			if err != nil || n <= 0 || n > 59 {
				return Result{}, false
			}
			return Result{Cron: fmt.Sprintf("*/%d * * * *", n)}, true
		},
	},
	// every N hours → 0 */N * * *
	{
		compilePattern(`every (\d+) hours?`),
		func(_ time.Time, m []string) (Result, bool) {
			n, err := strconv.Atoi(m[1])
			if err != nil || n <= 0 || n > 23 {
				return Result{}, false
			}
			return Result{Cron: fmt.Sprintf("0 */%d * * *", n)}, true
		},
	},
	// every hour → 0 * * * *
	{
		compilePattern(`every hour`),
		func(_ time.Time, _ []string) (Result, bool) {
			return Result{Cron: "0 * * * *"}, true
		},
	},
	// daily at H[:MM] [am|pm] → M H * * *
	{
		compilePattern(`daily at (\d{1,2})(?::(\d{2}))?\s*(am|pm)?`),
		func(_ time.Time, m []string) (Result, bool) {
			h, mm, ok := parseClock(m[1], m[2], m[3])
			if !ok {
				return Result{}, false
			}
			return Result{Cron: fmt.Sprintf("%d %d * * *", mm, h)}, true
		},
	},
	// every weekday at H[:MM] [am|pm] → M H * * 1-5
	{
		compilePattern(`every weekday at (\d{1,2})(?::(\d{2}))?\s*(am|pm)?`),
		func(_ time.Time, m []string) (Result, bool) {
			h, mm, ok := parseClock(m[1], m[2], m[3])
			if !ok {
				return Result{}, false
			}
			return Result{Cron: fmt.Sprintf("%d %d * * 1-5", mm, h)}, true
		},
	},
	// every <dow> at H[:MM] [am|pm] → M H * * D
	{
		compilePattern(`every (monday|tuesday|wednesday|thursday|friday|saturday|sunday) at (\d{1,2})(?::(\d{2}))?\s*(am|pm)?`),
		func(_ time.Time, m []string) (Result, bool) {
			d := weekdayNum[strings.ToLower(m[1][:3])]
			if d == 0 {
				return Result{}, false
			}
			h, mm, ok := parseClock(m[2], m[3], m[4])
			if !ok {
				return Result{}, false
			}
			return Result{Cron: fmt.Sprintf("%d %d * * %d", mm, h, d)}, true
		},
	},
	// next <dow> H[:MM] [am|pm] → one-shot
	{
		compilePattern(`next (monday|tuesday|wednesday|thursday|friday|saturday|sunday) (\d{1,2})(?::(\d{2}))?\s*(am|pm)?`),
		func(now time.Time, m []string) (Result, bool) {
			d := weekdayNum[strings.ToLower(m[1][:3])]
			if d == 0 {
				return Result{}, false
			}
			h, mm, ok := parseClock(m[2], m[3], m[4])
			if !ok {
				return Result{}, false
			}
			return Result{OneShot: nextWeekdayTime(now, d, h, mm)}, true
		},
	},
	// in N (minute|hour|day|week)s → one-shot (now + duration)
	{
		compilePattern(`in (\d+) (minute|hour|day|week)s?`),
		func(now time.Time, m []string) (Result, bool) {
			n, err := strconv.Atoi(m[1])
			if err != nil || n <= 0 {
				return Result{}, false
			}
			var dur time.Duration
			switch m[2] {
			case "minute", "minutes":
				dur = time.Duration(n) * time.Minute
			case "hour", "hours":
				dur = time.Duration(n) * time.Hour
			case "day", "days":
				dur = time.Duration(n) * 24 * time.Hour
			case "week", "weeks":
				dur = time.Duration(n) * 7 * 24 * time.Hour
			default:
				return Result{}, false
			}
			return Result{OneShot: now.Add(dur)}, true
		},
	},
	// tomorrow at H[:MM] [am|pm] → one-shot (tomorrow at the given time)
	{
		compilePattern(`tomorrow at (\d{1,2})(?::(\d{2}))?\s*(am|pm)?`),
		func(now time.Time, m []string) (Result, bool) {
			h, mm, ok := parseClock(m[1], m[2], m[3])
			if !ok {
				return Result{}, false
			}
			loc := now.Location()
			y, mo, d := now.Date()
			return Result{OneShot: time.Date(y, mo, d+1, h, mm, 0, 0, loc)}, true
		},
	},
}

// Compile parses a natural-language scheduling phrase into a trigger result.
// If the phrase matches a known pattern, exactly one of Cron or OneShot is set
// and ok is true. If no pattern matches, ok is false and the caller should
// treat the input verbatim as a raw cron expression.
//
// now is the reference instant for the one-shot patterns ("next monday 9am",
// "in 30 minutes", "tomorrow at noon"); it is ignored by the recurring (cron)
// patterns.
func Compile(nl string, now time.Time) (Result, bool) {
	nl = strings.TrimSpace(nl)
	if nl == "" {
		return Result{}, false
	}
	for _, p := range table {
		if mm := p.re.FindStringSubmatch(nl); mm != nil {
			return p.cls(now, mm)
		}
	}
	return Result{}, false
}
