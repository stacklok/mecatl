//go:build !linux

package main

import "errors"

// errRequiresLinux is a variable, not an inline errors.New, so staticcheck
// cannot prove the stubs' error is always non-nil and flag the shared callers'
// err != nil checks (SA4023) on non-Linux builds.
var errRequiresLinux = errors.New("microVM guest agent requires Linux")

func lockWorkloadPrivileges() error {
	return errRequiresLinux
}

func prepareGuestNetwork() error {
	return errRequiresLinux
}

func prepareRepositoryGuestMount() error {
	return errRequiresLinux
}

func prepareGuestMounts() error {
	return errRequiresLinux
}
