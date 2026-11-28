package groupstorage_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/groupstorage"
)

func TestAcquireError(t *testing.T) {
	private := errors.New("private adapter payload")
	for _, tt := range []struct {
		failure groupstorage.AcquireFailure
		text    string
	}{
		{groupstorage.AcquireInvalid, "group claim invalid"},
		{groupstorage.AcquireHeld, "group claim held elsewhere"},
		{groupstorage.AcquireRetryable, "group claim retryable"},
		{groupstorage.AcquireUncertain, "group claim uncertain"},
	} {
		t.Run(tt.text, func(t *testing.T) {
			cause := errors.Join(private, context.DeadlineExceeded)
			err := groupstorage.NewAcquireError(tt.failure, cause)
			if err.Failure() != tt.failure || err.Error() != tt.text || err.Unwrap() != cause {
				t.Fatalf("wrong error contract: %v", err)
			}
			if !errors.Is(err, private) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("lost programmatic causes")
			}
			wrapped := fmt.Errorf("claiming group: %w", err)
			var acquired *groupstorage.AcquireError
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
	var zero groupstorage.AcquireError
	if zero.Failure() != groupstorage.AcquireInvalid || zero.Unwrap() != nil || zero.Error() != "group claim invalid" {
		t.Fatal("zero must stop claiming")
	}
	for n := 0; n <= 255; n++ {
		failure := groupstorage.AcquireFailure(n)
		want := failure
		if failure > groupstorage.AcquireUncertain {
			want = groupstorage.AcquireInvalid
		}
		err := groupstorage.NewAcquireError(failure, nil)
		if err.Failure() != want || err.Unwrap() != nil {
			t.Fatalf("classification %d did not fail closed", n)
		}
	}
}
