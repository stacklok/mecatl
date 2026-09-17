/**
 * The controller's DAEMON LOG helpers — the pure half of Studio's analogue
 * of mecatui's embedded-server diagnostics file (`cmd/mecatui/diaglog.go`:
 * `$XDG_STATE_HOME/mecatl/mecatui.log`, 10 MiB retention, `--quiet`).
 * mecated itself has no log-file flag — it writes diagnostics to stderr —
 * so in managed mode the process holding that stream, the local controller
 * (`scripts/local-controller.mjs`), owns the file, the bounded in-memory
 * tail the Diagnostics page reads, and the retention. Shared with the
 * vitest suite through the same dual-import pattern as
 * `controller-diagnostics-options.mjs`, so what the tests pin is what the
 * controller runs. Nothing here touches the filesystem.
 */

/** The log's file name inside `studio/.scratch` (the controller's 0700
 *  state directory) and its ONE rotated generation. */
export const LOG_FILE_NAME = "mecated.log";
export const ROTATED_LOG_FILE_NAME = "mecated.log.1";

/** mecatui's fixed retention (`maxDiagLogBytes = 10 << 20`), mirrored:
 *  local runtime state, deliberately not another operator setting. */
export const MAX_LOG_FILE_BYTES = 10 * 1024 * 1024;

/** How many tail lines `GET /logs` serves by default, and the most it will. */
export const DEFAULT_TAIL_LINES = 200;
export const MAX_TAIL_LINES = 2000;

/** The in-memory tail's byte budget: enough for a startup transcript and a
 *  few hundred lines of turn diagnostics, small enough to hold forever. */
export const DEFAULT_RING_BYTES = 262_144;

/**
 * Whether the current generation must rotate BEFORE another `incoming`
 * bytes are appended: the file never exceeds `maxBytes`, and an empty file
 * never rotates (a single oversized chunk lands in a fresh generation).
 *
 * @param {number} sizeBytes the current generation's size
 * @param {number} incoming the bytes about to be appended
 * @param {number} [maxBytes]
 */
export function shouldRotate(
  sizeBytes,
  incoming,
  maxBytes = MAX_LOG_FILE_BYTES,
) {
  if (!Number.isFinite(sizeBytes) || sizeBytes <= 0) return false;
  return sizeBytes + Math.max(0, incoming) > maxBytes;
}

/**
 * Parses the `?lines=` query of `GET /logs`: absent = the default, otherwise
 * a whole number clamped to [1, max]. A value that is not a whole number is
 * a 400 rather than a silent default — the UI only ever sends integers, so
 * anything else is a bug worth surfacing.
 *
 * @param {string | null | undefined} raw
 * @param {{defaultLines?: number, max?: number}} [bounds]
 */
export function parseTailLines(
  raw,
  { defaultLines = DEFAULT_TAIL_LINES, max = MAX_TAIL_LINES } = {},
) {
  if (raw === undefined || raw === null || raw === "") return defaultLines;
  if (!/^\d+$/.test(String(raw).trim()))
    throw Object.assign(new Error("lines must be a whole number"), {
      statusCode: 400,
    });
  const n = Number.parseInt(String(raw).trim(), 10);
  return Math.min(max, Math.max(1, n));
}

/**
 * A bounded ring of COMPLETE lines. Text arrives in arbitrary chunks (a
 * pipe splits wherever it likes), so a trailing partial line is held back
 * until its newline arrives; the oldest complete lines are dropped once the
 * ring's byte budget is exceeded. `lines(n)` returns the most recent `n`
 * complete lines, oldest first.
 *
 * @param {number} [maxBytes]
 */
export function createLogRing(maxBytes = DEFAULT_RING_BYTES) {
  /** @type {string[]} */
  const complete = [];
  let held = ""; // the unterminated tail of the last chunk
  let bytes = 0; // UTF-16 code units of `complete`, one per line for "\n"
  let dropped = 0;

  /** @param {string} text */
  function append(text) {
    if (typeof text !== "string" || text === "") return;
    const combined = held + text;
    const parts = combined.split("\n");
    held = parts.pop() ?? "";
    for (const line of parts) {
      // A line longer than the whole budget is kept whole (truncating a
      // provider error body in the middle would hide the useful half) and
      // simply evicts everything before it.
      complete.push(line);
      bytes += line.length + 1;
    }
    while (complete.length > 1 && bytes > maxBytes) {
      const evicted = complete.shift();
      bytes -= (evicted?.length ?? 0) + 1;
      dropped += 1;
    }
  }

  /** @param {number} n */
  function lines(n) {
    const count = Math.max(0, Math.floor(n));
    if (count === 0) return []; // slice(-0) is slice(0): everything
    return count >= complete.length ? complete.slice() : complete.slice(-count);
  }

  return {
    append,
    lines,
    /** How many complete lines the ring holds right now. */
    get length() {
      return complete.length;
    },
    /** How many complete lines were evicted since the ring was created —
     *  non-zero means the file holds more than the ring can show. */
    get dropped() {
      return dropped;
    },
    /** The current byte estimate of the held lines. */
    get bytes() {
      return bytes;
    },
  };
}

/**
 * The `GET /logs` payload's `truncated` flag: the response does not show
 * everything the log file holds — either the ring evicted older lines or
 * the caller asked for fewer than the ring has.
 *
 * @param {{length: number, dropped: number}} ring
 * @param {number} requested
 */
export function tailIsTruncated(ring, requested) {
  return ring.dropped > 0 || ring.length > requested;
}
