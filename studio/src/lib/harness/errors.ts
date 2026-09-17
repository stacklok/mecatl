/**
 * Studio's typed daemon error. Every harness module throws this so UI flow
 * control can branch on the stable machine `code` from the daemon's RFC 9457
 * problem body (ADR 0248), never on message prose.
 */

/**
 * A daemon HTTP error, typed on the stable machine `code` from the RFC 9457
 * problem-details body (ADR 0248). The legacy top-level `error` key is still
 * sent by every daemon, so `message` always carries the server's own words;
 * `code` is "" against a pre-problem-details daemon. Flow control should
 * branch on `code`, never on message prose.
 */
export class HarnessApiError extends Error {
  readonly status: number;
  readonly code: string;
  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "HarnessApiError";
    this.status = status;
    this.code = code;
  }
}

/** Codes whose raw detail deserves plainer user-facing framing. */
const codeFraming: Record<string, string> = {
  draining: "The daemon is restarting — try again in a moment.",
  session_leased_elsewhere:
    "Another client is driving this chat right now — try again when its run finishes.",
};

/** Decodes a non-OK CONTROLLER response into a HarnessApiError (the daemon
 *  side is translated from SDK errors in ./sdk.ts). */
export async function apiError(response: Response): Promise<HarnessApiError> {
  const fallback = `${response.status} ${response.statusText}`;
  try {
    const body = (await response.json()) as {
      error?: string;
      code?: string;
      detail?: string;
      title?: string;
    };
    const code = typeof body.code === "string" ? body.code : "";
    const message =
      codeFraming[code] ?? body.error ?? body.detail ?? body.title ?? fallback;
    return new HarnessApiError(response.status, code, message);
  } catch {
    return new HarnessApiError(response.status, "", fallback);
  }
}
