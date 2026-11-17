#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
if [ "$#" -eq 0 ]; then
  temporary=$(mktemp -d)
  trap 'rm -rf "$temporary"' EXIT
  mkdir "$temporary/home" "$temporary/xdg" "$temporary/hooks" "$temporary/template"
  python_dir=$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.executable).resolve().parent)')
  env -i PATH="$python_dir:$PATH" HOME="$temporary/home" XDG_CONFIG_HOME="$temporary/xdg" TMPDIR="$temporary" \
    GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_COUNT=4 \
    GIT_CONFIG_KEY_0=core.hooksPath GIT_CONFIG_VALUE_0="$temporary/hooks" \
    GIT_CONFIG_KEY_1=init.templateDir GIT_CONFIG_VALUE_1="$temporary/template" \
    GIT_CONFIG_KEY_2=commit.gpgSign GIT_CONFIG_VALUE_2=false \
    GIT_CONFIG_KEY_3=tag.gpgSign GIT_CONFIG_VALUE_3=false \
    GOENV=off GOWORK=off GOTOOLCHAIN=local \
    sh "$root/.github/scripts/check-module-publication_test.sh" --isolated
  exit
fi
[ "$#" -eq 1 ] && [ "$1" = --isolated ] || exit 2

# The Python fixture supplies the complete production policy inventory, then
# invokes this shell entry point for each focused negative control.
python3 "$root/.github/scripts/module-release-ci_test.py" \
  ShellPublication.test_actual_shell_checker_rejects_off_main_and_wrong_namespace_before_download
