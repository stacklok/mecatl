//go:build !linux

package main

import "errors"

func lockWorkloadPrivileges() error {
	return errors.New("microVM guest agent requires Linux")
}

func prepareGuestNetwork() error {
	return errors.New("microVM guest agent requires Linux")
}

func prepareRepositoryGuestMount() error {
	return errors.New("microVM guest agent requires Linux")
}

func prepareGuestMounts() error {
	return errors.New("microVM guest agent requires Linux")
}
