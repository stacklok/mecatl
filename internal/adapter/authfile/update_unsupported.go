//go:build !linux

package authfile

import (
	"context"
	"errors"
)

var updateTestHook func(string) error

func updateAPIKey(context.Context, string, APIKeyUpdate) (CommitState, error) {
	return CommitNotApplied, errors.New("auth mutation is unsupported on this platform")
}
