/**
 * Terminal stop labels and the transient status channel.
 *
 * `stopReasonLabel` mirrors the TUI footer's table
 * (cmd/mecatui/ui/footer.go `stopReasonLabel`) so both clients name a limit
 * stop the same way: a run that ended on a turn limit, a token budget, or no
 * progress must never look like a quiet success. `end_turn` says nothing
 * (the answer speaks for itself) and `error` is owned by the failed-turn card,
 * so both map to null here.
 *
 * The daemon's `stop` is a string passthrough — a new token from a newer
 * daemon must not vanish, so an unrecognised non-empty stop falls back to
 * `stopped · <stop>` after the same single-line/control-character/length clamp
 * the TUI applies (`sanitizeTerminal`) before rendering a daemon-influenced
 * token into the transcript.
 */

export type StopReasonTone = "muted" | "warn";

export interface StopReasonLabel {
  text: string;
  tone: StopReasonTone;
}

/**
 * The transient status line under the transcript: a no-progress nudge or the
 * pre-flight recover notice while a run is live, or how the last run stopped
 * once it ends. Cleared by the next run; never appended to a message's
 * notices (a replay must not resurrect it).
 */
export interface StatusMessage {
  text: string;
  tone: StopReasonTone;
  kind: "no_progress" | "recover_notice" | "stop";
}

/** Rune cap for an unrecognised stop token rendered raw into a chip. */
export const MAX_STOP_TOKEN_RUNES = 48;

/** Rune cap for a daemon advisory rendered on the status line. */
export const MAX_STATUS_TEXT_RUNES = 240;

/**
 * Every control (Cc) and invisible-format (Cf) character plus the two
 * Unicode line/paragraph separators — everything that could hide a forged
 * line or a terminal escape inside a daemon-influenced token.
 */
const CONTROL_OR_FORMAT = /[\p{Cc}\p{Cf}\u2028\u2029]+/gu;

/**
 * Collapses daemon-influenced text to ONE bounded line: every line
 * terminator (incl. CR, NEL, VT, FF, U+2028/U+2029) and every other control or
 * invisible-format character is folded to a space, runs of whitespace are
 * collapsed, and the result is clamped to `maxRunes` code points with an
 * ellipsis. The TUI's `sanitizeTerminal` analogue for a web transcript.
 */
export function sanitizeLine(text: string, maxRunes: number): string {
  const cleaned = text
    .replace(CONTROL_OR_FORMAT, " ")
    .replace(/\s+/g, " ")
    .trim();
  const runes = Array.from(cleaned);
  if (runes.length <= maxRunes) return cleaned;
  return `${runes.slice(0, Math.max(0, maxRunes - 1)).join("")}…`;
}

const STOP_LABELS: Record<string, StopReasonLabel> = {
  max_turns: { text: "stopped · turn limit", tone: "warn" },
  max_tool_calls: { text: "stopped · tool-call limit", tone: "warn" },
  max_consecutive_failures: {
    text: "stopped · repeated failures",
    tone: "warn",
  },
  budget: { text: "stopped · token budget", tone: "warn" },
  // The model went silent (no tool call, no text) across the nudge budget:
  // not a failure, but worth noticing — styled like the limit stops.
  no_progress: { text: "stopped · no progress", tone: "warn" },
  // A subagent could not satisfy the requested output schema within the
  // retry budget. Subagent-only today, mapped so the raw token never leaks.
  structured_output: { text: "stopped · schema unmet", tone: "warn" },
  cancelled: { text: "cancelled", tone: "muted" },
  plan_approved: { text: "plan approved · executing", tone: "muted" },
  // The operator chose to iterate on the plan: the run ends cleanly so the
  // next typed prompt drives the revision — a cue, not a warning.
  plan_iterate: {
    text: "plan iterate · awaiting your feedback",
    tone: "muted",
  },
};

/**
 * The label for a run's terminal `stop`, or null when there is nothing to
 * say: a clean `end_turn` (or an absent stop), and `error`, which the
 * failed-turn card already renders.
 */
export function stopReasonLabel(
  stop: string | undefined | null,
): StopReasonLabel | null {
  if (!stop || stop === "end_turn" || stop === "error") return null;
  const known = STOP_LABELS[stop];
  if (known) return known;
  const token = sanitizeLine(stop, MAX_STOP_TOKEN_RUNES);
  if (!token) return null;
  return { text: `stopped · ${token}`, tone: "muted" };
}

/** The status-line message for a run's terminal `stop` (null = clear it). */
export function statusFromStopReason(
  stop: string | undefined | null,
): StatusMessage | null {
  const label = stopReasonLabel(stop);
  return label ? { ...label, kind: "stop" } : null;
}
