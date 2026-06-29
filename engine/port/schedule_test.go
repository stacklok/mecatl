package port

import (
	"errors"
	"testing"
	"time"
)

func TestTriggerSpec_Validate(t *testing.T) {
	t.Run("neither set is an error", func(t *testing.T) {
		s := TriggerSpec{}
		if err := s.Validate(); err == nil {
			t.Fatalf("Validate(both-zero) = nil, want error")
		}
	})
	t.Run("both set is an error", func(t *testing.T) {
		s := TriggerSpec{Cron: "@daily", OneShot: time.Now().Add(time.Hour)}
		if err := s.Validate(); err == nil {
			t.Fatalf("Validate(both-set) = nil, want error")
		}
	})
	t.Run("cron only is nil", func(t *testing.T) {
		s := TriggerSpec{Cron: "@daily"}
		if err := s.Validate(); err != nil {
			t.Fatalf("Validate(cron-only) = %v, want nil", err)
		}
	})
	t.Run("oneshot only is nil", func(t *testing.T) {
		s := TriggerSpec{OneShot: time.Now().Add(time.Hour)}
		if err := s.Validate(); err != nil {
			t.Fatalf("Validate(oneshot-only) = %v, want nil", err)
		}
	})
}

func TestTriggerSpec_Kind(t *testing.T) {
	t.Run("cron only -> TriggerCron", func(t *testing.T) {
		s := TriggerSpec{Cron: "0 9 * * *"}
		if got := s.Kind(); got != TriggerCron {
			t.Fatalf("Kind(cron-only) = %v, want TriggerCron", got)
		}
	})
	t.Run("oneshot only -> TriggerOneShot", func(t *testing.T) {
		s := TriggerSpec{OneShot: time.Now().Add(time.Hour)}
		if got := s.Kind(); got != TriggerOneShot {
			t.Fatalf("Kind(oneshot-only) = %v, want TriggerOneShot", got)
		}
	})
	t.Run("neither -> TriggerNone", func(t *testing.T) {
		s := TriggerSpec{}
		if got := s.Kind(); got != TriggerNone {
			t.Fatalf("Kind(neither) = %v, want TriggerNone", got)
		}
	})
	t.Run("both -> TriggerNone (does not lie about a single arm)", func(t *testing.T) {
		s := TriggerSpec{Cron: "@daily", OneShot: time.Now().Add(time.Hour)}
		if got := s.Kind(); got != TriggerNone {
			t.Fatalf("Kind(both) = %v, want TriggerNone (Validate is the gate)", got)
		}
	})
}

func TestMisfirePolicyZeroValueIsFireOnceNow(t *testing.T) {
	// The zero value is the default fire-once-now policy (fail-safe: a missed run
	// is not silently dropped).
	var m MisfirePolicy
	if m != MisfireFireOnceNow {
		t.Fatalf("zero MisfirePolicy = %v, want MisfireFireOnceNow", m)
	}
}

func TestScheduleSentinelsDistinct(t *testing.T) {
	// ErrScheduleNotFound and ErrScheduleUnsupported must be distinct sentinels so a
	// consumer can errors.Is them apart (not-found vs backend-unsupported are
	// different operational conditions).
	if errors.Is(ErrScheduleNotFound, ErrScheduleUnsupported) {
		t.Fatalf("ErrScheduleNotFound is ErrScheduleUnsupported — they must be distinct")
	}
	if errors.Is(ErrScheduleUnsupported, ErrScheduleNotFound) {
		t.Fatalf("ErrScheduleUnsupported is ErrScheduleNotFound — they must be distinct")
	}
	// And neither is ErrSessionNotFound (a sibling port's sentinel).
	if errors.Is(ErrScheduleNotFound, ErrSessionNotFound) {
		t.Fatalf("ErrScheduleNotFound is ErrSessionNotFound — they must be distinct")
	}
}

func TestSchedulerLeaderLeaseID(t *testing.T) {
	// The well-known leader-lease id is the documented sentinel string.
	const want = "__scheduler__"
	if got := string(SchedulerLeaderLeaseID); got != want {
		t.Fatalf("SchedulerLeaderLeaseID = %q, want %q", got, want)
	}
}
