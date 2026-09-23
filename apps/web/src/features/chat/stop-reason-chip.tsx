// SPDX-License-Identifier: Apache-2.0

import { AlertTriangle } from "lucide-react";
import { Badge } from "../../components/ui/badge";

/** Character cap for an unrecognised stop token rendered raw into the chip. */
const STOP_TOKEN_MAX_CHARS = 48;

/** Every control character folded to a space before an unknown token is shown. */
// biome-ignore lint/suspicious/noControlCharactersInRegex: intentionally stripping control characters from a daemon-influenced token
const CONTROL_CHARS = /[\x00-\x1f\x7f-\x9f]+/g;

interface StopReasonLabel {
  text: string;
  warn: boolean;
}

/**
 * Known daemon stop reasons, mirroring the SDK's vocabulary: a limit or
 * budget stop is a warning (something cut the turn short), a cancellation
 * or plan cue is a muted status note.
 */
const STOP_LABELS: Record<string, StopReasonLabel> = {
  budget: { text: "Stopped: token budget", warn: true },
  cancelled: { text: "Cancelled", warn: false },
  max_consecutive_failures: { text: "Stopped: repeated failures", warn: true },
  max_tool_calls: { text: "Stopped: tool-call limit", warn: true },
  max_turns: { text: "Stopped: turn limit", warn: true },
  no_progress: { text: "Stopped: no progress", warn: true },
  plan_approved: { text: "Plan approved · executing", warn: false },
  plan_iterate: { text: "Plan iterate · awaiting feedback", warn: false },
  structured_output: { text: "Stopped: schema unmet", warn: true },
};

function sanitizeToken(value: string): string {
  const cleaned = value.replace(CONTROL_CHARS, " ").replace(/\s+/g, " ").trim();
  return cleaned.length > STOP_TOKEN_MAX_CHARS
    ? `${cleaned.slice(0, STOP_TOKEN_MAX_CHARS - 1)}…`
    : cleaned;
}

/**
 * Resolves the same "worth telling the user about" verdict `StopReasonChip`
 * renders, without the JSX — so a caller deciding whether an otherwise-empty
 * turn is worth rendering at all can ask the identical question instead of
 * re-deriving it.
 */
function resolveStopReasonLabel(stopReason: string): StopReasonLabel | undefined {
  const normalized = stopReason.trim().toLowerCase();
  if (!normalized || normalized === "end_turn" || normalized === "error") return undefined;

  const label = STOP_LABELS[normalized];
  if (label) return label;
  const token = sanitizeToken(stopReason);
  return token ? { text: `Stopped: ${token}`, warn: false } : undefined;
}

/** Whether `StopReasonChip` would render a badge for this stop reason. */
export function hasVisibleStopReason(stopReason: string): boolean {
  return resolveStopReasonLabel(stopReason) !== undefined;
}

/**
 * The durable chip marking a run that ended on something other than a clean
 * finish: a limit or budget stop (warning tint), or a muted cue such as a
 * cancellation or plan-mode note. Renders nothing for a clean end
 * (`end_turn`, or no stop reason at all) or `error`, which the failed-turn
 * card already covers. An unrecognised, non-empty stop token still renders
 * — sanitised and clamped — so a newer daemon's vocabulary never vanishes.
 */
export function StopReasonChip({ stopReason }: { stopReason: string }) {
  const label = resolveStopReasonLabel(stopReason);
  if (!label) return null;

  return (
    <Badge
      className="mt-1.5"
      title="How this turn ended"
      variant={label.warn ? "warning" : "muted"}
    >
      {label.warn && <AlertTriangle aria-hidden="true" />}
      {label.text}
    </Badge>
  );
}
