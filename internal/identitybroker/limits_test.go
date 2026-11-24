package identitybroker_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// validLimits are limits Validate accepts beside a hosted AS-1.
func validLimits() identitybroker.Limits {
	return identitybroker.Limits{
		IDJAGMaxLifetime: 12 * time.Hour, IDJAGLifetime: time.Minute, ClientAssertionLifetime: time.Minute,
		AS1RequestTimeout: 10 * time.Second, AS2RequestTimeout: 10 * time.Second,
	}
}

func TestLimitsValidateAcceptsTheBounds(t *testing.T) {
	t.Parallel()
	config := identitybroker.Config{ClockSkew: identitybroker.MaxClockSkew}
	for name, l := range map[string]identitybroker.Limits{
		"the defaults": validLimits(),
		"the maximums": {
			IDJAGMaxLifetime: identitybroker.MaxIDJAGMaxLifetime, IDJAGLifetime: identitybroker.MaxIDJAGLifetime,
			ClientAssertionLifetime: identitybroker.MaxClientAssertionLifetime,
			AS1RequestTimeout:       identitybroker.MaxAS1RequestTimeout, AS2RequestTimeout: identitybroker.MaxAS2RequestTimeout,
		},
		"the minimums with ten skews": {
			IDJAGMaxLifetime: 10 * identitybroker.MaxClockSkew, IDJAGLifetime: time.Second, ClientAssertionLifetime: time.Second,
			AS1RequestTimeout: time.Second, AS2RequestTimeout: time.Second,
		},
	} {
		if err := l.Validate(config, identitybroker.Host{AS1: true}); err != nil {
			t.Errorf("%s: Validate = %v, want nil", name, err)
		}
	}
	without := validLimits()
	without.IDJAGMaxLifetime, without.IDJAGLifetime = 0, 0
	if err := without.Validate(config, identitybroker.Host{}); err != nil {
		t.Errorf("without AS-1's values or a hosted AS-1: Validate = %v, want nil", err)
	}
}

func TestLimitsValidateRefuses(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		edit func(*identitybroker.Limits, *identitybroker.Config)
		as1  bool
		want string
	}{
		"a zero client assertion lifetime":      {edit: func(l *identitybroker.Limits, _ *identitybroker.Config) { l.ClientAssertionLifetime = 0 }, as1: true, want: "client assertion lifetime 0s must be between 1s and 1m0s"},
		"a client assertion lifetime above 60s": {edit: func(l *identitybroker.Limits, _ *identitybroker.Config) { l.ClientAssertionLifetime = 61 * time.Second }, as1: true, want: "client assertion lifetime"},
		"a fractional client assertion lifetime": {edit: func(l *identitybroker.Limits, _ *identitybroker.Config) {
			l.ClientAssertionLifetime = 1500 * time.Millisecond
		}, as1: true, want: "must be whole seconds"},
		"a sub-second AS-1 timeout": {edit: func(l *identitybroker.Limits, _ *identitybroker.Config) { l.AS1RequestTimeout = time.Second / 2 }, as1: true, want: "AS-1 request timeout"},
		"an AS-1 timeout above the maximum": {edit: func(l *identitybroker.Limits, _ *identitybroker.Config) {
			l.AS1RequestTimeout = identitybroker.MaxAS1RequestTimeout + time.Second
		}, as1: true, want: "AS-1 request timeout 31s must be between 1s and 30s"},
		"a zero AS-2 timeout": {edit: func(l *identitybroker.Limits, _ *identitybroker.Config) { l.AS2RequestTimeout = 0 }, as1: true, want: "AS-2 request timeout"},
		"an AS-2 timeout above the maximum": {edit: func(l *identitybroker.Limits, _ *identitybroker.Config) {
			l.AS2RequestTimeout = identitybroker.MaxAS2RequestTimeout + time.Second
		}, as1: true, want: "AS-2 request timeout"},
		"an ID-JAG grant maximum above 720h":         {edit: func(l *identitybroker.Limits, _ *identitybroker.Config) { l.IDJAGMaxLifetime = 721 * time.Hour }, as1: true, want: "ID-JAG grant maximum lifetime 721h0m0s must be between 1s and 720h0m0s"},
		"no ID-JAG grant maximum with a hosted AS-1": {edit: func(l *identitybroker.Limits, _ *identitybroker.Config) { l.IDJAGMaxLifetime = 0 }, as1: true, want: "ID-JAG grant maximum lifetime 0s"},
		"a fractional ID-JAG grant maximum": {edit: func(l *identitybroker.Limits, _ *identitybroker.Config) {
			l.IDJAGMaxLifetime = time.Hour + time.Millisecond
		}, as1: true, want: "must be whole seconds"},
		"an ID-JAG lifetime above 300s":                 {edit: func(l *identitybroker.Limits, _ *identitybroker.Config) { l.IDJAGLifetime = 301 * time.Second }, as1: true, want: "ID-JAG lifetime 5m1s must be between 1s and 5m0s"},
		"a fractional ID-JAG lifetime":                  {edit: func(l *identitybroker.Limits, _ *identitybroker.Config) { l.IDJAGLifetime = 1500 * time.Millisecond }, as1: true, want: "ID-JAG lifetime 1.5s must be whole seconds"},
		"an ID-JAG grant maximum without a hosted AS-1": {edit: func(l *identitybroker.Limits, _ *identitybroker.Config) { l.IDJAGLifetime = 0 }, want: "ID-JAG grant maximum lifetime requires a hosted AS-1"},
		"an ID-JAG lifetime without a hosted AS-1":      {edit: func(l *identitybroker.Limits, _ *identitybroker.Config) { l.IDJAGMaxLifetime = 0 }, want: "ID-JAG lifetime requires a hosted AS-1"},
		"a skew above a tenth of the ID-JAG grant maximum": {
			edit: func(l *identitybroker.Limits, c *identitybroker.Config) {
				l.IDJAGMaxLifetime, c.ClockSkew = 5*time.Minute, 31*time.Second
			}, as1: true, want: "clock skew 31s must be at most a tenth of the ID-JAG grant maximum lifetime 5m0s",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			l, config := validLimits(), identitybroker.Config{ClockSkew: identitybroker.ClockSkew}
			tc.edit(&l, &config)
			err := l.Validate(config, identitybroker.Host{AS1: tc.as1})
			if !errors.Is(err, identitybroker.ErrInvalidConfig) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate = %v, want ErrInvalidConfig containing %q", err, tc.want)
			}
		})
	}
}
