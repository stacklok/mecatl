/**
 * Whether mid-run steering is available from THIS daemon (C1.2), folded from
 * the two signals GET /v1/compatibility carries:
 *
 * - `capabilities.steer` — the operator's live decision (`!DisableSteer`:
 *   `mecated --no-steer` or an operator-tier `steer: false` turns it off).
 *   The daemon always emits it as a boolean, so when the key is present it
 *   DECIDES: an explicit false wins.
 * - the `http_steer` feature-registry row — a STATIC "the route exists" fact
 *   a rebuilt daemon lists unconditionally, even with steering disabled. It
 *   is only the fallback for an older daemon that has no `steer` capability
 *   key at all.
 *
 * Reading the row first would keep every steer affordance lit after the
 * operator turned steering off, and every mid-run send would round-trip to
 * a `too_late` refusal before queueing. Absent both signals, steering is not
 * supported and mid-run sends queue instead.
 */
export function resolveSteerSupported(
  serverCapabilities: Record<string, unknown>,
  features: ReadonlySet<string>,
): boolean {
  const capability = serverCapabilities.steer;
  if (typeof capability === "boolean") return capability;
  return features.has("http_steer");
}
