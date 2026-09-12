//go:build !linux

package authfile

import (
	"context"
	"errors"
)

var updateTestHook func(string) error

func updateSupported() bool { return false }

func preflightAPIKeyUpdateTarget(string) error {
	return errors.New("auth mutation is unsupported on this platform")
}

func updateAPIKey(context.Context, string, APIKeyUpdate) (CommitState, error) {
	return CommitNotApplied, errors.New("auth mutation is unsupported on this platform")
}
