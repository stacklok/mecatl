/**
 * The fixed 12-column session handle mecatui shows for every ordinary session
 * (`cmd/mecatui/client/client.go` `SessionHandle`, docs/tui.md "Header
 * bar"): a Studio user and a mecatui user looking at the same chat read the
 * SAME short literal, so a handle pasted from one client resolves in the
 * other (`mecatui debug <handle>` accepts exactly this grammar).
 *
 * The grammar, byte for byte with the Go encoder:
 * - safe bytes `[A-Za-z0-9._]` are literal, and so is `-` EXCEPT as the
 *   leading byte, where it becomes `%2D` (a bare leading hyphen would read as
 *   a flag on a command line);
 * - every other UTF-8 byte is one uppercase `%HH` atom;
 * - only COMPLETE atoms that fit in 12 columns are kept — a `%HH` atom that
 *   would straddle the boundary is dropped, never split.
 *
 * Display only: Studio selects sessions by click and sends the full opaque
 * id back to the daemon; the handle never crosses the wire.
 */

/** The fixed maximum column width of a handle (`client.SessionHandleWidth`). */
export const SESSION_HANDLE_WIDTH = 12;

const HEX = "0123456789ABCDEF";

const encoder = new TextEncoder();

function isSafeByte(byte: number, index: number): boolean {
  return (
    (byte >= 0x41 && byte <= 0x5a) || // A-Z
    (byte >= 0x61 && byte <= 0x7a) || // a-z
    (byte >= 0x30 && byte <= 0x39) || // 0-9
    byte === 0x2e || // .
    byte === 0x5f || // _
    (byte === 0x2d && index > 0) // - (never leading)
  );
}

/**
 * The 12-column handle for `id`, or "" for an empty id. Mirrors the Go
 * encoder: literal safe bytes, `%HH` for everything else (a lone surrogate
 * encodes as U+FFFD's bytes, the closest a JS string gets to invalid UTF-8),
 * cut on an atom boundary at the column bound.
 */
export function shortSessionHandle(id: string): string {
  if (!id) return "";
  const bytes = encoder.encode(id);
  let out = "";
  for (let index = 0; index < bytes.length; index += 1) {
    const byte = bytes[index] as number;
    const safe = isSafeByte(byte, index);
    const atomLength = safe ? 1 : 3;
    if (out.length + atomLength > SESSION_HANDLE_WIDTH) break;
    out += safe
      ? String.fromCharCode(byte)
      : `%${HEX[byte >> 4]}${HEX[byte & 0x0f]}`;
  }
  return out;
}
