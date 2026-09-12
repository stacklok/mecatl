//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package permconfig

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/internal/adapter/authfile"
)

var defaultUpdateTestHook func(string) error

func UpdateDefaults(context.Context, string, DefaultUpdate) (authfile.CommitState, error) {
	return authfile.CommitNotApplied, errors.New("settings default mutation is unsupported on this platform")
}
