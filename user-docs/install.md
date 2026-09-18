---
sidebar_position: 3
title: Install Mecatl
description:
  Install the mecatui client and the mecated server with Homebrew, a signed
  release archive, or a source build.
---

# Install Mecatl

Install and verify Mecatl release artifacts from this page. For the fastest path
to a first local session, follow [Run your first local session](/mecatui/getting-started.md)
after installing `mecatui`.

## Homebrew

The `mecatl` formula in Stacklok's tap installs both executables. It supports
macOS on Apple silicon and Intel, and Linux on x86_64 and arm64.
[Windows users can install WSL](https://learn.microsoft.com/en-us/windows/wsl/install)
and run the Linux formula from the WSL shell.

```sh
brew install stacklok/tap/mecatl
mecatui --version
mecated --version
```

Both commands should print the installed release tag, such as `v0.1.0`.

Upgrade, pin, or uninstall the formula:

```sh
brew update && brew upgrade mecatl
brew pin mecatl
brew uninstall mecatl
```

Homebrew links the executables into `$(brew --prefix)/bin`. For an absolute
path, use `$(brew --prefix mecatl)/bin/mecatui` or
`$(brew --prefix mecatl)/bin/mecated`.

## Release archives

[GitHub releases](https://github.com/stacklok/mecatl/releases) provide `darwin`
and `linux` archives for `amd64` and `arm64`. Each release also includes
checksums, Cosign signature bundles, SBOMs, and build provenance.

```sh
tar -xzf mecatl_<VERSION>_darwin_arm64.tar.gz
./mecatui --version
```

Each archive contains both executables at its root, plus the license and README.

Verify the checksum, signature, and provenance:

```sh
shasum -a 256 -c checksums.txt --ignore-missing

cosign verify-blob \
  --bundle mecatl_<VERSION>_darwin_arm64.tar.gz.sigstore.json \
  --certificate-identity-regexp '^https://github.com/stacklok/mecatl/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  mecatl_<VERSION>_darwin_arm64.tar.gz

gh attestation verify mecatl_<VERSION>_darwin_arm64.tar.gz --repo stacklok/mecatl
```

The Cosign command verifies that a GitHub Actions workflow in the
`stacklok/mecatl` repository signed the archive.

## Deployment artifacts

Deployment-specific guides own the images, charts, and operational instructions:

- [Run mecated standalone](/operating/mecated.md) for the general
  server image and server operation.
- [Run the mecatui container image](/operating/mecatui.md) for the
  importable terminal-client image.
- [Deploy mecak8s](/operating/mecak8s.md) for the Kubernetes image
  and Helm chart.
- [Use mecatequi in CI](/operating/mecatequi.md) for the one-shot CI
  runtime.

## Build from source

A source build requires Go 1.27 or later and [Task](https://taskfile.dev/) v3.
Run the build from the repository root.

Build all executables into `bin/`:

```sh
task build
```

Run an executable from `bin/`. To install `mecatui` and `mecated` into `GOBIN`
or `GOPATH/bin`, run `task install`.

## Next steps

- [Get started with mecatui](/mecatui/getting-started.md) to launch a local
  session.
- [Run mecated standalone](/operating/mecated.md) to operate the
  server.
- [Choose how to run Mecatl](/operating/choose-deployment.md)
  to choose between `mecated`, `mecak8s`, `mecatequi`, and an engine embedding.
