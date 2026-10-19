// SPDX-License-Identifier: Apache-2.0

import type { RuntimeResponse, SessionSummaryResponse } from "@mecatl-studio/contracts";

/** The daemon's reason when it gives one, else Studio's fallback. */
export function unavailable(reason: string, fallback: string): string {
  return reason || fallback;
}

/**
 * Whether the caller may read a session's saved transcript, and the reason to
 * show when not. The capability rides the session inventory row; the session
 * detail does not carry it. The chat inspection dialog and the schedule run
 * log's transcript dialog share this one gate.
 */
export function transcriptAccess(
  session: SessionSummaryResponse,
  runtime: RuntimeResponse | undefined,
): { readable: boolean; reason: string } {
  const online = runtime?.connection === "online";
  return {
    readable: online && session.capabilities.inspect && session.capabilities.viewTranscript,
    reason: !online
      ? "Transcript unavailable while offline."
      : unavailable(session.capabilities.viewTranscriptReason, "Transcript unavailable."),
  };
}
