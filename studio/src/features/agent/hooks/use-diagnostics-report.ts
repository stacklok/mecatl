"use client";

import { useCallback, useRef } from "react";
import { useRuntimeStatus } from "@/features/agent/runtime-status";
import { buildDiagnosticsReport } from "@/lib/harness/diagnostics-report";
import {
  type HarnessServerInfoProbe,
  probeHarnessServerInfo,
} from "@/lib/harness/server-info";
import { studioBuild, studioPlatform } from "@/lib/studio-build";

/**
 * The session half of the `/diagnostics` report — what only the chat
 * workspace knows. Every field is optional: the About card composes with no
 * session at all (its lines read "unavailable").
 */
export interface DiagnosticsSessionContext {
  /** The session's resolved provider/model (GET-session echo); the
   *  provider id also selects the endpoint the identity probe projects. */
  resolvedModel?: {
    providerId: string;
    modelId: string;
    reasoningEffort?: string;
  } | null;
  /** The composer's permission mode (Studio vocabulary). */
  permissionMode?: string;
  /** A probe result already in hand (the About card's) — skips the call. */
  server?: HarnessServerInfoProbe;
}

/**
 * The endpoint the browser reaches the daemon through: Studio's own
 * same-origin proxy. The daemon's real address is server-owned (ADR 0344)
 * and never enters the browser, so this — not a remote URL — is the honest
 * `server endpoint:` line. "" during SSR, which the report reads as
 * "unavailable".
 */
export function studioServerEndpoint(): string {
  if (typeof window === "undefined") return "";
  try {
    return new URL("/api/mecatl", window.location.origin).toString();
  } catch {
    return "";
  }
}

/**
 * Composes the sanitized client/server diagnostics report (the TUI's
 * `/diagnostics`, cmd/mecatui/ui/diagnostics.go) from Studio's runtime
 * status, the identity probe (GET /v1/info, ADR 0245) and whatever session
 * context the caller has. The ONE composition point: the composer's
 * `/diagnostics` built-in and the About card's "Send to a new chat" both
 * call it, so the two never drift.
 *
 * Runtime status is read through a ref so `compose` stays referentially
 * stable across the provider's five-second polls.
 */
export function useDiagnosticsReport(): {
  compose: (context?: DiagnosticsSessionContext) => Promise<string>;
} {
  const { mode, deployment, serverCapabilities } = useRuntimeStatus();
  const posture =
    typeof serverCapabilities.posture === "string"
      ? serverCapabilities.posture
      : "";
  const runtimeRef = useRef({ mode, deployment, posture });
  runtimeRef.current = { mode, deployment, posture };

  const compose = useCallback(
    async (context: DiagnosticsSessionContext = {}): Promise<string> => {
      const runtime = runtimeRef.current;
      const resolvedModel = context.resolvedModel ?? null;
      // An older daemon or a transient fault reads as a lookup class,
      // never blocks the report.
      const server =
        context.server ??
        (await probeHarnessServerInfo(resolvedModel?.providerId || undefined));
      return buildDiagnosticsReport({
        platform: studioPlatform(),
        clientBuild: studioBuild(),
        mode: runtime.mode,
        serverInfo: server.info,
        serverEndpoint: studioServerEndpoint(),
        lookup: server.lookup,
        deployment: runtime.deployment,
        posture: runtime.posture,
        resolvedModel,
        reasoningEffort: resolvedModel?.reasoningEffort ?? "",
        permissionMode: context.permissionMode ?? "",
      });
    },
    [],
  );

  return { compose };
}
