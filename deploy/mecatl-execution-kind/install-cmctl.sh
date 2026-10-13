#!/bin/sh
set -eu

[ "$#" -eq 1 ] || { echo "usage: $0 owned-state" >&2; exit 2; }
state=$1
[ -d "$state" ] && [ ! -L "$state" ] && [ "$(stat -c '%u:%a' "$state")" = "$(id -u):700" ] && [ -f "$state/ownership" ] && [ ! -L "$state/ownership" ] || { echo "fixture ownership unavailable" >&2; exit 1; }
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) asset=linux_amd64; hash=0af054c26a2b64ce286fa868d029164867695a93c3eb893662ce16f35e708db0 ;;
  Linux/aarch64|Linux/arm64) asset=linux_arm64; hash=0261c9dc17711659548a1cb9014326f47cf472a699b48150fe133ae17054cf8c ;;
  *) echo "unsupported cmctl fixture platform" >&2; exit 1 ;;
esac
umask 077
tools=$state/tools
if [ ! -e "$tools" ] && [ ! -L "$tools" ]; then mkdir -m 700 "$tools"; fi
[ -d "$tools" ] && [ ! -L "$tools" ] && [ "$(stat -c '%u:%a' "$tools")" = "$(id -u):700" ] || { echo "fixture tools directory is not private and owned" >&2; exit 1; }
if [ ! -e "$tools/cmctl" ] && [ ! -L "$tools/cmctl" ]; then
  download=$(mktemp "$tools/cmctl.download.XXXXXX")
  curl --fail --location --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 10 --max-time 120 \
    "https://github.com/cert-manager/cmctl/releases/download/v2.6.1/cmctl_$asset" --output "$download"
  printf '%s  %s\n' "$hash" "$download" | sha256sum --check --status || { echo "cmctl checksum mismatch" >&2; exit 1; }
  # Create-only publication; a competing installation cannot be overwritten.
  ln "$download" "$tools/cmctl"
fi
[ -f "$tools/cmctl" ] && [ ! -L "$tools/cmctl" ] && [ "$(stat -c '%u' "$tools/cmctl")" = "$(id -u)" ] || { echo "cmctl is not an owned regular file" >&2; exit 1; }
printf '%s  %s\n' "$hash" "$tools/cmctl" | sha256sum --check --status || { echo "cmctl checksum mismatch" >&2; exit 1; }
chmod 700 "$tools/cmctl"
