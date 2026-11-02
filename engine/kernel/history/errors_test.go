package history_test

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
)

func TestPageTooSmallError(t *testing.T) {
	err := history.PageTooSmallError{RequiredBytes: 7}
	if !errors.Is(err, history.ErrPageTooSmall) {
		t.Fatal("PageTooSmallError must unwrap ErrPageTooSmall")
	}
	if got, want := err.Error(), "history page too small"; got != want {
		t.Fatalf("Error() = %q; want %q", got, want)
	}
}
