"use client";

import type { ComposerModelOption } from "@/app/workspace/_components/chat-input";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import type { AgentSession } from "@/features/agent/types";
import { copyToClipboard } from "@/lib/clipboard";
import { permissionModeLabel } from "@/lib/permission-mode";
import { postureSummary, postureTone } from "@/lib/posture";
import type { SessionPermissionMode } from "@/lib/protocol";
import { shortSessionHandle } from "@/lib/protocol/session-handle";
import { cn } from "@/lib/utils";

/**
 * The persistent chat status strip — mecatui's header bar
 * (`docs/tui.md` "Header bar": `session <handle> · <model> · mode <mode> ·
 * <server>` with a right-aligned posture badge), rendered directly under the
 * chat's title row so a Studio user always sees WHICH daemon session they are
 * driving, on WHAT effective model, in WHICH permission mode, against WHICH
 * daemon, at WHAT operator posture.
 *
 * Segment rules:
 * - The handle is the documented 12-column grammar and is rendered BARE (no
 *   leading `#`, exactly as docs/tui.md states) so the literal on screen is
 *   the one positional `mecatui debug <handle>` accepts. It is display-only:
 *   the full id stays on the title and a click copies it.
 * - The model is the daemon-RESOLVED effective model (never the inventory
 *   row's configured id while a resolve is pending); "resolving model…"
 *   shows only while the detail read is genuinely in flight or the daemon
 *   connection is still connecting, and settles to the configured id or
 *   "model unavailable" when the read failed or an older daemon echoes
 *   nothing — never a forever-spinner. The downstream provider route rides
 *   as `/route` (ADR 0210) and the effective reasoning effort as a suffix.
 * - The mode is the server-confirmed permission mode; a switch deferred
 *   until the run ends reads "(pending)", the TUI's `mode <target> pending`.
 * - The server segment names managed vs external mode plus the operator's
 *   deployment label — NEVER a URL or host: Studio's same-origin proxy
 *   deliberately hides the daemon address from the browser (studio/CLAUDE.md
 *   rule 3), so there is no address to show.
 * - The posture badge is the daemon-reported `serverCapabilities.posture`;
 *   hidden entirely against an older daemon that reports none.
 *
 * NOT reproduced from the TUI, by decision: the "next: <model>" badge
 * (Studio has no apply-on-next-create model pick — a pick forks the chat
 * immediately) and the changed-files tail (the daemon exposes no
 * changed-files count to any client).
 *
 * On an AI-debug session (ADR 0254) the strip turns amber and prepends
 * `DEBUG target <handle>` (the TUI's `debugHeaderTarget`), and a second
 * persistent line carries the privacy disclosure the TUI keeps in its
 * header: the pre-creation consent dialog is gone once the chat opens, so
 * this is the durable user-visible disclosure.
 */

export type ModelResolution = "loading" | "ok" | "failed";

export interface ChatStatusStripProps {
  session: AgentSession;
  /** True while the daemon connection is up (the chat is live). */
  live: boolean;
  /** `resolved_model.model_id` off the session snapshot; null = none echoed. */
  resolvedModelId: string | null;
  /** `resolved_model.reasoning_effort`; ""/absent = none echoed. */
  reasoningEffort?: string;
  /** Whether the snapshot read behind `resolvedModelId` is pending/landed/failed. */
  modelResolution: ModelResolution;
  /** Live daemon models, for the model's display name. */
  models?: ComposerModelOption[];
  /** The downstream provider the current/last turn was routed to ("" = none). */
  providerRoute?: string;
  /** The server-confirmed permission mode; absent hides the segment. */
  mode?: SessionPermissionMode;
  /** A mode switch held until the run ends (rendered "(pending)"). */
  pendingMode?: SessionPermissionMode | null;
  /** Reporting MCP servers bound to a debug session (`--debug-mcp`). */
  debugMcpServers?: string[];
  /** The debugger MCP tools those servers mounted; each call asks anew. */
  debugMcpTools?: string[];
  /** Selects another chat (the DEBUG target handle is a link to it). */
  onOpenSession?: (sessionId: string) => void;
}

/** The TUI's durable privacy disclosure, word for word (`view.go`). */
export const DEBUG_PRIVACY_NOTICE =
  "PRIVACY: target evidence sent to the configured model may include prompts, assistant output, tool arguments/results, file paths, and secrets.";

export function debugServersNotice(servers: readonly string[]): string {
  return `Selected reporting servers available: ${servers.join(", ")}; availability does not authorize publication or sending.`;
}

/** The debugger MCP tools mounted from those servers — every call asks. */
export function debugToolsNotice(tools: readonly string[]): string {
  return `Debugger MCP tools (each call asks for approval; Always allow is not learned): ${tools.join(", ")}.`;
}

const SEGMENT_CLASS = "min-w-0 truncate";

function Separator() {
  return (
    <span aria-hidden="true" className="shrink-0 select-none opacity-60">
      ·
    </span>
  );
}

/** The facts the strip and the Session details submenu both render. */
function useChatStatusFacts({
  session,
  live,
  resolvedModelId,
  reasoningEffort = "",
  modelResolution,
  models,
  providerRoute = "",
  mode,
  pendingMode = null,
}: ChatStatusStripProps) {
  const runtime = useRuntimeStatus();
  const connecting = runtime.state === "connecting";
  const debugTarget = session.debugTargetSessionId ?? "";
  const isDebug = debugTarget !== "";

  // The TUI's "absent while connecting, then the resolved model": a stale
  // configured id is never shown as if it were the effective one while a
  // resolve is still possible — but a settled miss falls back honestly.
  const resolving =
    connecting || (live && !resolvedModelId && modelResolution === "loading");
  let modelText: string;
  if (resolving) {
    modelText = "resolving model…";
  } else if (resolvedModelId) {
    const displayName =
      models?.find((option) => option.id === resolvedModelId)?.label ??
      resolvedModelId;
    modelText = providerRoute ? `${displayName}/${providerRoute}` : displayName;
  } else {
    modelText = session.model || "model unavailable";
  }
  const effortText = !resolving && resolvedModelId ? reasoningEffort : "";

  const modeText = mode
    ? pendingMode && pendingMode !== mode
      ? `${permissionModeLabel(pendingMode)} (pending)`
      : permissionModeLabel(mode)
    : "";

  // Managed vs external plus the operator's deployment label — never a
  // URL/host (the same-origin proxy hides the daemon address by design).
  let serverText: string;
  if (runtime.state === "connecting") serverText = "connecting…";
  else if (runtime.state === "offline") serverText = "daemon offline";
  else {
    serverText =
      runtime.mode === "managed" ? "managed daemon" : "external daemon";
    if (runtime.deployment) serverText += ` · ${runtime.deployment}`;
  }

  const posture =
    typeof runtime.serverCapabilities.posture === "string"
      ? runtime.serverCapabilities.posture
      : "";
  const tone = postureTone(posture);

  const handle = shortSessionHandle(session.id);
  return {
    connecting,
    debugTarget,
    isDebug,
    resolving,
    modelText,
    effortText,
    modeText,
    serverText,
    posture,
    tone,
    handle,
  };
}

export function ChatStatusStrip(props: ChatStatusStripProps) {
  const {
    session,
    debugMcpServers = [],
    debugMcpTools = [],
    onOpenSession,
  } = props;
  const {
    debugTarget,
    isDebug,
    resolving,
    modelText,
    effortText,
    modeText,
    serverText,
    posture,
    tone,
    handle,
  } = useChatStatusFacts(props);

  return (
    <div
      data-testid="chat-status-strip"
      data-debug={isDebug ? "true" : undefined}
      className={cn(
        "shrink-0 border-b text-[11px] leading-none",
        isDebug
          ? "border-amber-500/40 bg-amber-500/10 text-amber-700 dark:text-amber-400"
          : "border-border/60 text-muted-foreground",
      )}
    >
      <div className="flex h-7 items-center gap-1.5 px-3 lg:px-6">
        {isDebug && (
          <>
            <span className="shrink-0 font-semibold">DEBUG</span>
            <span className="shrink-0">target</span>
            <button
              type="button"
              className="min-w-0 truncate rounded font-mono underline-offset-2 hover:underline focus-visible:outline-2 focus-visible:outline-ring"
              title={debugTarget}
              aria-label={`Open debug target session ${debugTarget}`}
              onClick={() => onOpenSession?.(debugTarget)}
            >
              {shortSessionHandle(debugTarget)}
            </button>
            <Separator />
          </>
        )}
        <button
          type="button"
          className="min-w-0 shrink-0 truncate rounded font-mono hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring"
          title={`Session ${session.id} — click to copy the full id`}
          aria-label={`Session ${handle}: copy the full session id`}
          onClick={() => void copyToClipboard(session.id, "Session id")}
        >
          {handle}
        </button>
        <Separator />
        <span
          className={SEGMENT_CLASS}
          data-testid="chat-status-model"
          aria-busy={resolving || undefined}
        >
          {modelText}
        </span>
        {effortText && (
          <span className="hidden min-w-0 items-center gap-1.5 sm:inline-flex">
            <Separator />
            <span className={SEGMENT_CLASS}>{effortText}</span>
          </span>
        )}
        {modeText && (
          <>
            <Separator />
            <span className={SEGMENT_CLASS} data-testid="chat-status-mode">
              Mode: {modeText}
            </span>
          </>
        )}
        <span className="hidden min-w-0 items-center gap-1.5 md:inline-flex">
          <Separator />
          <span className={SEGMENT_CLASS} data-testid="chat-status-server">
            {serverText}
          </span>
        </span>
        <span className="flex-1" />
        {posture && (
          <span
            data-testid="chat-posture-badge"
            data-tone={tone}
            title={postureSummary(posture)}
            className={cn(
              "shrink-0 rounded px-1.5 py-0.5 font-medium",
              tone === "muted" && "border border-border/70",
              tone === "warning" && "bg-warning/15 text-warning",
              tone === "danger" && "bg-destructive/15 text-destructive",
            )}
          >
            {tone === "muted" ? `posture ${posture}` : `⚠ ${posture}`}
          </span>
        )}
      </div>
      {isDebug && (
        <div
          role="note"
          data-testid="chat-debug-privacy"
          className="border-t border-amber-500/30 px-3 py-1 leading-snug lg:px-6"
        >
          {DEBUG_PRIVACY_NOTICE}
          {debugMcpServers.length > 0 && (
            <> {debugServersNotice(debugMcpServers)}</>
          )}
          {debugMcpTools.length > 0 && <> {debugToolsNotice(debugMcpTools)}</>}
        </div>
      )}
    </div>
  );
}
