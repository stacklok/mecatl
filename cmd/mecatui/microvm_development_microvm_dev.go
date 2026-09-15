//go:build microvm_dev

package main

import (
	"errors"
	"flag"

	"github.com/stacklok/mecatl/internal/adapter/microvmmanager"
)

func init() {
	flagApplicabilityByFlag["microvm-dev-release"] = flagApplicability{group: groupSession, common: false, local: true, connect: false}
	flagApplicabilityByFlag["microvm-dev-acknowledge-untrusted-local-artifacts"] = flagApplicability{group: groupSession, common: false, local: true, connect: false}
}

func registerMicroVMDevelopmentFlags(fs *flag.FlagSet, descriptor *string, acknowledge *bool) {
	fs.StringVar(descriptor, "microvm-dev-release", "", "UNSUPPORTED DEVELOPMENT ONLY: absolute path to a local microVM development release descriptor")
	fs.BoolVar(acknowledge, "microvm-dev-acknowledge-untrusted-local-artifacts", false, "UNSUPPORTED DEVELOPMENT ONLY: acknowledge that local microVM artifacts are not a published release")
}

func microVMDevelopmentReadyRequest(descriptor string, acknowledge bool, sourceBuildIdentity string, releaseStamped bool, egress ...microvmmanager.GuestEgressSelection) (microvmmanager.ReadyRequest, bool, error) {
	if descriptor == "" && !acknowledge {
		return microvmmanager.ReadyRequest{}, false, nil
	}
	if descriptor == "" || !acknowledge {
		return microvmmanager.ReadyRequest{}, true, errors.New("--microvm-dev-release and --microvm-dev-acknowledge-untrusted-local-artifacts are required together")
	}
	if releaseStamped {
		return microvmmanager.ReadyRequest{}, true, errors.New("published release binaries cannot activate microVM development releases")
	}
	request, err := microvmmanager.ReadyRequestFromDevelopmentDescriptor(descriptor, sourceBuildIdentity, egress...)
	return request, true, err
}
