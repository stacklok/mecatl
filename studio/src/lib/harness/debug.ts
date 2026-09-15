/**
 * AI session debugger — ADR 0254.
 *
 * `sessions.create` with `debugTargetSessionId` creates a SEPARATE durable
 * diagnostic session bound to one stored target. The daemon requires the
 * `no-fs` profile (the debug engine has exactly one read-only tool,
 * InspectSession, and never touches a filesystem), authorizes the target
 * server-side before creating anything (an unowned or unknown target answers
 * 404 — ownership failures are concealed as not-found), and never copies
 * target conversation state. The new session then opens and runs like an
 * ordinary chat; its inventory row carries
 * `relationship.debug_target_session_id` plus capabilities that deny
 * rename/delete. Gated by `capabilities.session_debug` (and the optional
 * server list by `capabilities.debug_mcp`).
 *
 * CONSENT: invoking the debugger sends the target's stored transcript and
 * event evidence — everything in it, secrets included — to the selected model.
 * Callers must put the ADR-0254 disclosure in front of the user BEFORE calling
 * this; this module only speaks to the SDK.
 */

import { SessionMode } from "@stacklok-oss/mecatl-sdk";
import { getHarnessClient, harness } from "./sdk";

/** Creates a debug session bound to `targetSessionId`; returns the new id. */
export async function createHarnessDebugSession(
  targetSessionId: string,
  options?: {
    /** Optional debug MCP server names (gated by `capabilities.debug_mcp`). */
    mcpServers?: string[];
    signal?: AbortSignal;
  },
): Promise<string> {
  const session = await harness(() =>
    getHarnessClient().sessions.create(
      {
        mode: SessionMode.Default,
        profile: "no-fs",
        debugTargetSessionId: targetSessionId,
        ...(options?.mcpServers?.length
          ? { debugMcpServers: options.mcpServers }
          : {}),
      },
      { signal: options?.signal },
    ),
  );
  if (!session.id) throw new Error("harness returned no session id");
  return session.id;
}
