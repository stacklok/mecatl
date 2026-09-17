/**
 * Scheduled-task delivery notes (issue #386): when a schedule fire finishes,
 * the daemon records its outcome into the ORIGIN chat as a user-role message
 * wrapped in the model's untrusted fence with a provenance header
 * (`internal/app/scheduler_delivery.go`, `renderFireStarted` /
 * `renderFireDelivery`):
 *
 *     <<<UNTRUSTED
 *     [scheduled task NAME started (fire ID)]
 *     <<<UNTRUSTED
 *
 *     <<<UNTRUSTED
 *     [scheduled task NAME (fire ID) completed with stop reason: STOP]
 *     ...the fire's final text, or "(no output)"...
 *     <<<UNTRUSTED
 *
 * The fence and header are machine markers for the model (the trust
 * boundary); the operator wants the attribution and the body. This module is
 * Studio's ONE detection point, sharing the TUI's contract
 * (`cmd/mecatui/client/msgs.go` `deliverNoteFrom`) and the relay's
 * (`internal/adapter/server/grpc.go` `isDeliveryNoteText`): the text must
 * start with the fence opener AND the very next line must start with the
 * header prefix. An un-fenced match is a user who typed the prefix, so it is
 * NOT a note. Anything unparseable returns null and renders as it came in —
 * content is never dropped.
 *
 * Imports nothing from `features/` (types.ts imports this module directly).
 */

type DeliveryNoteKind = "started" | "completed";

export interface DeliveryNoteInfo {
  /** The schedule's name — the route segment of `/workspace/schedules/<name>`. */
  scheduleName: string;
  /** The fire's id (its session id); the dedup key together with `kind`. */
  fireId: string;
  kind: DeliveryNoteKind;
  /** The fire's stop reason (completed notes only), e.g. `end_turn`, `error`. */
  stop?: string;
}

export interface ParsedDeliveryNote extends DeliveryNoteInfo {
  /** The note with the fence markers and the provenance header stripped. */
  body: string;
}

/** `governance.UntrustedFence` — the delimiter around untrusted blocks. */
const FENCE = "<<<UNTRUSTED";
const FENCE_OPENER = `${FENCE}\n`;
/** The literal both `renderFireStarted` and `renderFireDelivery` begin with. */
const HEADER_PREFIX = "[scheduled task ";
const FIRE_TAG = " (fire ";
const STARTED_SUFFIX = " started";
const COMPLETED_TAG = ") completed with stop reason: ";

/**
 * Parses one recorded user-role text as a delivery note. Returns null for
 * anything that is not provably a note (fail-soft: the caller renders the
 * text as an ordinary message).
 */
export function parseDeliveryNote(text: string): ParsedDeliveryNote | null {
  if (!text.startsWith(FENCE_OPENER)) return null;
  const inner = text.slice(FENCE_OPENER.length);
  if (!inner.startsWith(HEADER_PREFIX)) return null;

  const newline = inner.indexOf("\n");
  const header = newline === -1 ? inner : inner.slice(0, newline);
  const rest = newline === -1 ? "" : inner.slice(newline + 1);

  const afterPrefix = header.slice(HEADER_PREFIX.length);
  const fireIndex = afterPrefix.indexOf(FIRE_TAG);
  if (fireIndex < 0) return null;
  let scheduleName = afterPrefix.slice(0, fireIndex);
  const afterFire = afterPrefix.slice(fireIndex + FIRE_TAG.length);
  const close = afterFire.indexOf(")");
  if (close < 0) return null;
  const fireId = afterFire.slice(0, close);
  const tail = afterFire.slice(close);

  let kind: DeliveryNoteKind;
  let stop: string | undefined;
  if (tail.startsWith(COMPLETED_TAG) && tail.endsWith("]")) {
    kind = "completed";
    stop = tail.slice(COMPLETED_TAG.length, -1);
  } else if (tail === ")]" && scheduleName.endsWith(STARTED_SUFFIX)) {
    kind = "started";
    scheduleName = scheduleName.slice(0, -STARTED_SUFFIX.length);
  } else {
    return null;
  }
  if (!scheduleName || !fireId) return null;

  const info: DeliveryNoteInfo = { scheduleName, fireId, kind };
  if (kind === "completed") info.stop = stop;
  return { ...info, body: stripClosingFence(rest) };
}

/**
 * Drops the trailing fence closer (`FenceUntrusted` ends the block with
 * `"\n<<<UNTRUSTED\n"`) and any trailing whitespace. A body with no closer
 * (a truncated record) is returned trimmed rather than rejected.
 */
function stripClosingFence(rest: string): string {
  let body = rest.trimEnd();
  if (body === FENCE) return "";
  if (body.endsWith(`\n${FENCE}`)) {
    body = body.slice(0, -(FENCE.length + 1)).trimEnd();
  }
  return body;
}
