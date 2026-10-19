#!/bin/sh
set -eu

version=v2.14.0
case "${1:-}" in
	"") ;;
	--version)
		printf '%s\n' "$version"
		exit 0
		;;
	*)
		echo "usage: $0 [--version]" >&2
		exit 2
		;;
esac

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
bare_version=${version#v}
destination="$root/.tools/bin/golangci-lint"
if [ -x "$destination" ]; then
	case "$("$destination" --version 2>/dev/null || true)" in
		*"version ${bare_version} "*) exit 0 ;;
	esac
fi

case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*)
		echo "unsupported operating system: $(uname -s)" >&2
		exit 1
		;;
esac
case "$(uname -m)" in
	x86_64|amd64) arch=amd64 ;;
	aarch64|arm64) arch=arm64 ;;
	*)
		echo "unsupported architecture: $(uname -m)" >&2
		exit 1
		;;
esac

case "$os/$arch" in
	linux/amd64) checksum=ab90aeb7b066f92a33415b638a50fe5344bbb75a0d32ad30cc248d88f81032ab ;;
	linux/arm64) checksum=ee7ec5f3453d15ddf106fae5a4d6c71737712348a979d1fe9cd52ec7ea299bae ;;
	darwin/amd64) checksum=a5667c1c3536be1740133213e1e822bfb8f0d98ea12903174d6d5f635e4ed68d ;;
	darwin/arm64) checksum=5ef5f36a7147e91dc58ef9ef4d11bb7bad5ead0c76eb6c01327a73c641d1dcc3 ;;
esac

archive="golangci-lint-${bare_version}-${os}-${arch}.tar.gz"
url="https://github.com/golangci/golangci-lint/releases/download/${version}/${archive}"
work="$root/.scratch/golangci-lint-${bare_version}-${os}-${arch}"

rm -rf "$work"
mkdir -p "$work" "$(dirname "$destination")"
trap 'rm -rf "$work"' EXIT

curl --fail --location --silent --show-error --output "$work/$archive" "$url"
if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$work/$archive" | awk '{print $1}')
else
	actual=$(shasum -a 256 "$work/$archive" | awk '{print $1}')
fi
if [ "$actual" != "$checksum" ]; then
	echo "checksum mismatch for $archive" >&2
	exit 1
fi

tar -xzf "$work/$archive" -C "$work"
install -m 0755 "$work/golangci-lint-${bare_version}-${os}-${arch}/golangci-lint" "$destination"
"$destination" --version
