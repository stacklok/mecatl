//go:build !linux && !darwin

package privatefile

import (
	"context"
	"errors"
)

func supported() bool { return false }

func preflight(string, string, int64, func([]byte) error) error {
	return errors.New("private file mutation is unsupported on this platform")
}

func update(context.Context, string, string, int64, Mutate) (CommitState, error) {
	return CommitNotApplied, errors.New("private file mutation is unsupported on this platform")
}
