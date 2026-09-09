//go:build !microvm_dev

package main

import (
	"flag"

	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
)

func registerMicroVMDevelopmentFlags(*flag.FlagSet, *string, *bool) {}

func microVMDevelopmentReadyRequest(string, bool, string, bool, ...microvmmanager.GuestEgressSelection) (microvmmanager.ReadyRequest, bool, error) {
	return microvmmanager.ReadyRequest{}, false, nil
}
