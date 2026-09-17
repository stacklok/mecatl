/**
 * The reasoning-effort vocabulary Studio shares with the daemon.
 *
 * The daemon accepts a neutral tier on `CreateSessionRequest.reasoning_effort`
 * and `ForkSessionRequest.reasoning_effort` and echoes the EFFECTIVE tier on
 * `resolved_model.reasoning_effort` (mirroring cmd/mecatui/ui/effort.go). On
 * the wire the tiers are `low | medium | high | xhigh | max`, and the empty
 * string means "unset": the operator's `--reasoning-effort` default applies
 * (or the provider's own default when there is none). Studio shows that
 * sentinel as "Auto" and never sends it — a create/fork body simply omits the
 * field, so the daemon's default wins.
 *
 * Unknown values are never dropped: a newer daemon's tier labels itself.
 */

/** The picker's rows, in rank order; `auto` is the unset sentinel. */
export const EFFORT_TIERS = [
  "auto",
  "low",
  "medium",
  "high",
  "xhigh",
  "max",
] as const;

export type EffortTier = (typeof EFFORT_TIERS)[number];

/** The picker value for "let the daemon decide" (wire value ""). */
export const AUTO_EFFORT: EffortTier = "auto";

const LABELS: Record<string, string> = {
  "": "Auto",
  auto: "Auto",
  low: "Low",
  medium: "Medium",
  high: "High",
  xhigh: "Extra high",
  max: "Max",
};

/** The wire value for a tier: "" for `auto` (the field is omitted), else the tier. */
export function effortWire(tier: string): string {
  const normalised = tier.trim().toLowerCase();
  return normalised === AUTO_EFFORT ? "" : normalised;
}

/** The picker tier for a wire value: "" (or an explicit "auto") is `auto`. */
export function effortTier(wire: string): string {
  const normalised = wire.trim().toLowerCase();
  return normalised === "" ? AUTO_EFFORT : normalised;
}

/**
 * The human label for a wire value or tier: "Auto" for the unset sentinel,
 * the tier's label otherwise. A value Studio does not know (a newer daemon's
 * tier) is shown verbatim rather than hidden.
 */
export function effortLabel(value: string): string {
  const normalised = value.trim().toLowerCase();
  return LABELS[normalised] ?? normalised;
}

/** True for the unset sentinel ("" or "auto") and every known tier. */
export function isKnownEffort(value: string): boolean {
  return (EFFORT_TIERS as readonly string[]).includes(effortTier(value));
}
