#!/bin/sh
set -eu

[ "$#" -eq 1 ] || { echo "usage: $0 owned-state" >&2; exit 2; }
state=$1
# Portable stand-ins for GNU stat -c and sha256sum --check.
owned() { [ -n "$(find "$1" -prune -user "$(id -u)" ${2:+-perm "$2"})" ]; }
hash_ok() { [ "$(sha256sum <"$1" | cut -c1-64)" = "$hash" ]; }
[ -d "$state" ] && [ ! -L "$state" ] && owned "$state" 700 && [ -f "$state/ownership" ] && [ ! -L "$state/ownership" ] || { echo "fixture ownership unavailable" >&2; exit 1; }
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) asset=linux_amd64; hash=0af054c26a2b64ce286fa868d029164867695a93c3eb893662ce16f35e708db0 ;;
  Linux/aarch64|Linux/arm64) asset=linux_arm64; hash=0261c9dc17711659548a1cb9014326f47cf472a699b48150fe133ae17054cf8c ;;
  Darwin/x86_64) asset=darwin_amd64; hash=019e63e759ed6ee6a88a0f92073bbf2de3001da13ae76008694e34bb57a6fca5 ;;
  Darwin/arm64) asset=darwin_arm64; hash=158413ab2f8466c2680317a9618396ed206f4207451d6c24f6f14ccdc6788d4e ;;
  *) echo "unsupported cmctl fixture platform" >&2; exit 1 ;;
esac
umask 077
tools=$state/tools
if [ ! -e "$tools" ] && [ ! -L "$tools" ]; then mkdir -m 700 "$tools"; fi
[ -d "$tools" ] && [ ! -L "$tools" ] && owned "$tools" 700 || { echo "fixture tools directory is not private and owned" >&2; exit 1; }
if [ ! -e "$tools/cmctl" ] && [ ! -L "$tools/cmctl" ]; then
  download=$(mktemp "$tools/cmctl.download.XXXXXX")
  curl --fail --location --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 10 --max-time 120 \
    "https://github.com/cert-manager/cmctl/releases/download/v2.6.1/cmctl_$asset" --output "$download"
  hash_ok "$download" || { echo "cmctl checksum mismatch" >&2; exit 1; }
  # Create-only publication; a competing installation cannot be overwritten.
  ln "$download" "$tools/cmctl"
fi
[ -f "$tools/cmctl" ] && [ ! -L "$tools/cmctl" ] && owned "$tools/cmctl" || { echo "cmctl is not an owned regular file" >&2; exit 1; }
hash_ok "$tools/cmctl" || { echo "cmctl checksum mismatch" >&2; exit 1; }
chmod 700 "$tools/cmctl"
