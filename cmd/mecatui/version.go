package main

import "github.com/stacklok/mecatl/internal/buildinfo"

// version is replaceable in tests and directly linker-stampable for release
// builds. Ordinary builds inherit the canonical build ID at package init.
var version = "dev"

func init() {
	if version == "dev" {
		version = buildinfo.BuildID
	}
}

// microVMReleaseStampRequired is set on published host binaries so a missing or
// misnamed defaults/version linker assignment fails before any lifecycle command.
var microVMReleaseStampRequired string

// microVMReleaseDefaultsB64 is release-generated, base64 JSON stamped into the
// published mecatui binary. Ordinary source builds intentionally leave it empty;
// microvm-local is supported by the signed Linux-amd64 release binary.
var microVMReleaseDefaultsB64 string
