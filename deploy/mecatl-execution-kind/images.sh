#!/bin/sh

# The alias name is not proof of its target: compare containerd's canonical
# descriptor before reusing it. Never overwrite a conflicting retained alias.
pin_loaded() {
  tagged=$1
  images=$("$runtime" exec "${cluster}-control-plane" ctr -n k8s.io images ls) || return 1
  digest=$(printf '%s\n' "$images" | awk -v ref="$tagged" '$1 == ref {print $3; exit}')
  printf '%s\n' "$digest" | grep -Eq '^sha256:[0-9a-f]{64}$' || { echo "loaded image digest unavailable or invalid" >&2; return 1; }
  pinned="${tagged%:*}@${digest}"
  existing=$(printf '%s\n' "$images" | awk -v ref="$pinned" '$1 == ref {print $3; exit}')
  if [ -n "$existing" ]; then
    [ "$existing" = "$digest" ] || { echo "loaded image alias conflicts with canonical digest" >&2; return 1; }
  else
    "$runtime" exec "${cluster}-control-plane" ctr -n k8s.io images tag "$tagged" "$pinned" >/dev/null || return 1
  fi
  printf '%s\n' "$pinned"
}
