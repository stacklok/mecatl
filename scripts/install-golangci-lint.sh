#!/bin/sh
set -eu

version=v2.13.1
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
	linux/amd64) checksum=b17bfbc9d4aaa48be7f4f1ce3240bc3d8200c870c072bacf15c26219e2cfb9cc ;;
	linux/arm64) checksum=908317c23db18448f924e853b3d8a659fd919614cd438f224810a4053daa2607 ;;
	darwin/amd64) checksum=2c373363953e4e0bee2a03b7fe864a5eb6a3822927cb077d9ca33f2ae3cb2da2 ;;
	darwin/arm64) checksum=0c9818baf6fb8ad26c6d2ef51b68d5a1e260ef07727036b1431647cc44637c7c ;;
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
