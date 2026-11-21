package lease_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/lease"
)

func TestAcquireError(t *testing.T) {
	private := errors.New("private adapter payload")
	for _, tt := range []struct {
		failure lease.AcquireFailure
		text    string
	}{
		{lease.AcquireInvalid, "lease acquisition invalid"},
		{lease.AcquireHeld, "lease acquisition held elsewhere"},
		{lease.AcquireRetryable, "lease acquisition retryable"},
		{lease.AcquireUncertain, "lease acquisition uncertain"},
	} {
		t.Run(tt.text, func(t *testing.T) {
			cause := errors.Join(private, context.DeadlineExceeded)
			err := lease.NewAcquireError(tt.failure, cause)
			if err.Failure() != tt.failure || err.Error() != tt.text || err.Unwrap() != cause {
				t.Fatalf("wrong error contract: %v", err)
			}
			if !errors.Is(err, private) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("lost programmatic causes")
			}
			wrapped := fmt.Errorf("opening group: %w", err)
			var acquired *lease.AcquireError
			if !errors.As(wrapped, &acquired) || acquired.Failure() != tt.failure {
				t.Fatal("lost wrapped classification")
			}
			for _, text := range []string{err.Error(), wrapped.Error(), fmt.Sprintf("%+v", err)} {
				if strings.Contains(text, private.Error()) || strings.Contains(text, context.DeadlineExceeded.Error()) {
					t.Fatal("private cause text escaped")
				}
			}
		})
	}
}

func TestAcquireErrorFailClosed(t *testing.T) {
	var zero lease.AcquireError
	if zero.Failure() != lease.AcquireInvalid || zero.Unwrap() != nil || zero.Error() != "lease acquisition invalid" {
		t.Fatal("zero must stop acquisition")
	}
	for n := 0; n <= 255; n++ {
		failure := lease.AcquireFailure(n)
		want := failure
		if failure > lease.AcquireUncertain {
			want = lease.AcquireInvalid
		}
		err := lease.NewAcquireError(failure, nil)
		if err.Failure() != want || err.Unwrap() != nil {
			t.Fatalf("classification %d did not fail closed", n)
		}
	}
}
