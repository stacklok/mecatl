---
sidebar_position: 2
title: Install Mecatl
description: Install the mecatui client and the mecated server with Homebrew, a signed release archive, or a source build.
---

# Install Mecatl

Mecatl publishes two executables for workstations and hosts: `mecatui`, the
terminal client, and `mecated`, the server it can embed or connect to. This page
is the canonical source for getting them; each guide then covers what to do next.

## Homebrew

The `mecatl` formula in Stacklok's tap installs both executables. It supports
macOS on Apple silicon and Intel, and Linux on x86_64 and arm64.

```sh
brew install stacklok/tap/mecatl
mecatui --version
mecated --version
```

`--version` prints the build identity and exits without starting a server or
reading configuration, so it is a safe first check.

Upgrade, hold, and remove the formula with Homebrew's normal commands:

```sh
brew update && brew upgrade mecatl
brew pin mecatl
brew uninstall mecatl
```

Homebrew installs into `$(brew --prefix)/bin`, which is `/opt/homebrew/bin` on
Apple silicon, `/usr/local/bin` on Intel macOS, and
`/home/linuxbrew/.linuxbrew/bin` on Linux. Anything that needs an absolute path —
a systemd unit, a launchd plist, an editor's spawn configuration — must use the
prefix for the machine it runs on rather than assuming `/usr/local/bin`. Print the
exact location with `brew --prefix mecatl`.

## Release archives

Each `vX.Y.Z` tag attaches `darwin` and `linux` archives for `amd64` and `arm64`
to its [GitHub release](https://github.com/stacklok/mecatl/releases), alongside a
`checksums.txt`, a cosign signature bundle and an SBOM per archive, and build
provenance. Download an archive when you do not want a package manager, or when
you want to verify the artifact before it reaches a host.

```sh
tar -xzf mecatl_<version>_darwin_arm64.tar.gz
./mecatui --version
```

Each archive contains both executables at its root, plus the licence and README.

Verify a download before you trust it. Confirm the checksum, then the signature
and the provenance:

```sh
shasum -a 256 -c checksums.txt --ignore-missing

cosign verify-blob \
  --bundle mecatl_<version>_darwin_arm64.tar.gz.sigstore.json \
  --certificate-identity-regexp '^https://github.com/stacklok/mecatl/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  mecatl_<version>_darwin_arm64.tar.gz

gh attestation verify mecatl_<version>_darwin_arm64.tar.gz --repo stacklok/mecatl
```

The signatures are keyless: the certificate identity is the release workflow that
produced the archive, not a long-lived key, so verification asserts *which
workflow in which repository* built the file.

## Container images

The same release publishes signed container images to GHCR, which suit a service
deployment rather than a workstation. See
[the mecatui container image](/building/deployment/mecatui.md) for the
importable client image and
[Run mecated standalone](/building/deployment/mecated.md) for server operation.
[Cloud-native Kubernetes with mecak8s](/building/deployment/mecak8s.md) covers the
Kubernetes runtime, which is image-only and not part of the formula.

## Build from source

A checkout builds every supplied executable into `bin/`, including the ones the
formula does not ship — `mecademo` (the offline demo), `mecatequi` (the
single-shot CI runner), and `mecak8s`:

```sh
task build
```

Run a source build with its path prefix, `bin/mecatui`, or put `mecated` and
`mecatui` on your `PATH` with `task install`. The rest of this documentation
writes the plain command name. The
[prerequisites and build reference](https://github.com/stacklok/mecatl/blob/main/docs/usage/install.md)
lists the required toolchain versions and the other build targets.

## Next steps

- [Get started with mecatui](/mecatui/getting-started.md) — launch a local session.
- [Run mecated standalone](/building/deployment/mecated.md) — operate the server.
- [Pick your deployment shape](/building/getting-started/deployment-decision.md) — choose between `mecated`, `mecak8s`, `mecatequi`, and an engine embedding.
