#!/bin/sh
set -eu

# Explicit subcommands share the Python policy/proof implementation. With no
# arguments retain the public-only adapters/root/integration publication check.
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ "$#" -gt 0 ]; then
  exec python3 "$script_dir/module-release.py" "$@"
fi

# Only the trusted CI caller installs this directory after authenticated PR/R
# classification. Ordinary development never starts the staged resolver.
if [ -n "${MODULE_RELEASE_CI_DIRECTORY:-}" ]; then
  exec python3 "$MODULE_RELEASE_CI_DIRECTORY/trusted/.github/scripts/module-release-ci.py" publication
fi
exec python3 "$script_dir/module-release-ci.py" ordinary-publication --root "$(git rev-parse --show-toplevel)"
