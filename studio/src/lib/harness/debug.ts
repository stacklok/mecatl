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
import { wrapAsDebuggerRuntimeContext } from "./diagnostics-report";
import { getHarnessClient, harness } from "./sdk";

/**
 * The diagnostic objective mecatui submits the moment a debug session opens
 * (`cmd/mecatui/main.go` `defaultDebugPrompt`), byte for byte: the daemon's
 * create path starts no run, so the opening objective is CLIENT-side in both
 * clients, and a debug chat opened from Studio must read like one opened
 * from the TUI.
 */
export const DEFAULT_DEBUG_PROMPT =
  "Diagnose the bound target session and explain the most likely cause of its reported behavior.";

/**
 * The opening message Studio submits into a freshly created debug session
 * (the TUI's `initialPromptForConfig` for `debug TARGET`):
 *
 * - the default objective;
 * - when reporting servers were attached (`debug_mcp_servers`), the TUI's
 *   exact suffix naming them — and its reminder that their availability
 *   authorizes NO publication or sending (every debugger MCP call still asks
 *   for approval);
 * - when a Studio/daemon diagnostics report is supplied, that report fenced as
 *   `CURRENT_DEBUGGER_RUNTIME_CONTEXT` after a blank line, so the model reads
 *   it as context about THIS client, never as evidence about the target.
 *
 * Pure: the caller decides what to attach; this only spells the message.
 */
export function debugOpeningPrompt({
  servers = [],
  runtimeContext,
}: {
  /** The attached reporting-server names, in the order they were chosen. */
  servers?: readonly string[];
  /** A sanitized diagnostics report (`buildDiagnosticsReport`), or nothing. */
  runtimeContext?: string | null;
} = {}): string {
  let prompt = DEFAULT_DEBUG_PROMPT;
  if (servers.length > 0) {
    prompt +=
      ` Selected reporting servers are available: ${servers.join(", ")}.` +
      " Their availability does not authorize publication or sending.";
  }
  if (runtimeContext) {
    prompt += `\n\n${wrapAsDebuggerRuntimeContext(runtimeContext)}`;
  }
  return prompt;
}

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
