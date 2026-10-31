package schedparse

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/stacklok/mecatl/cmd/mecatui/internal/timezones"
)

// Preview is a validated trigger and local estimate, not a server acceptance promise.
// Re-run Validate at Create with a fresh now, especially for relative phrases.
type Preview struct {
	Result
	Timezone string
	Next     time.Time // strictly after now, in the chosen timezone
}

// ValidationError identifies the form field that needs correction.
type ValidationError struct {
	Field   string // "timing" or "timezone"
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// Validate accepts finite phrases or backend-standard five-field cron/@ macros.
// The explicit timezone is authoritative. now is supplied by the caller, never read here.
// On error no usable trigger is returned.
func Validate(input, timezone string, now time.Time) (Preview, error) {
	loc, err := timezones.Location(timezone)
	if err != nil {
		return Preview{}, &ValidationError{Field: "timezone", Message: err.Error()}
	}
	input = strings.TrimSpace(input)
	upper := strings.ToUpper(input)
	if strings.HasPrefix(upper, "TZ=") || strings.HasPrefix(upper, "CRON_TZ=") {
		return Preview{}, &ValidationError{Field: "timing", Message: "use the timezone control instead of TZ= or CRON_TZ="}
	}
	result, ok := Compile(input, now.In(loc))
	if !ok {
		result.Cron = input
	}
	preview := Preview{Result: result, Timezone: timezone}
	if !result.OneShot.IsZero() {
		if !result.OneShot.After(now) {
			return Preview{}, &ValidationError{Field: "timing", Message: "choose a one-shot time in the future"}
		}
		preview.Next = result.OneShot
		return preview, nil
	}
	schedule, err := cron.ParseStandard(result.Cron)
	if err != nil {
		return Preview{}, &ValidationError{Field: "timing", Message: "enter a supported phrase (for example every day at 9am), five-field cron, or @ macro; check clock and interval bounds"}
	}
	preview.Next = schedule.Next(now.In(loc))
	if preview.Next.IsZero() || !preview.Next.After(now) {
		return Preview{}, &ValidationError{Field: "timing", Message: fmt.Sprintf("timing %q has no future occurrence; check the calendar date", input)}
	}
	return preview, nil
}
