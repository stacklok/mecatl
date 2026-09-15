"use client";

import { useCallback, useEffect, useState } from "react";
import {
  connectHarnessGateway,
  fetchHarnessControlStatus,
  fetchHarnessRouter,
  type HarnessControlStatus,
  type HarnessRouterCategory,
  type HarnessRouterConfig,
  listHarnessModels,
  saveHarnessRouter,
  startHarnessGatewayOAuth,
  waitForHarnessGateway,
} from "@/lib/harness/client";
import { useRuntimeStatus } from "../runtime-status";

export interface HarnessModel {
  id: string;
  providerId: string;
  displayName: string;
  /** Total context window in tokens (0 = unknown). */
  contextLimit: number;
  /** Accepts image prompt input. */
  image: boolean;
  /** Emits reasoning/thinking. */
  reasoning: boolean;
}

/**
 * The runtime configuration behind the agent: which provider serves it, how
 * prompts are routed across models, and which MCP gateway its tools come from.
 *
 * Two backends, deliberately kept distinct because they fail differently: the
 * DAEMON (read-only model inventory) and the CONTROLLER (config writes, each
 * of which restarts the daemon and drops in-flight runs). In external mode
 * every controller write answers 409 — the deployment owns its configuration —
 * which callers surface as-is.
 *
 * There is deliberately NO provider-credential entry here: credentials never
 * cross the browser/controller boundary. mecated reads them from its auth
 * file, and the provider card only reports status.
 */
export function useHarnessRuntime() {
  const { connected, mode } = useRuntimeStatus();
  const [status, setStatus] = useState<HarnessControlStatus | null>(null);
  const [router, setRouter] = useState<HarnessRouterConfig | null>(null);
  const [models, setModels] = useState<HarnessModel[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    setIsLoading(true);
    try {
      // Independent reads, so they go out together rather than in a waterfall.
      const [nextStatus, nextRouter, nextModels] = await Promise.all([
        fetchHarnessControlStatus(signal),
        fetchHarnessRouter(signal).catch(() => null),
        listHarnessModels(signal).catch(() => []),
      ]);
      if (signal?.aborted) return;
      setStatus(nextStatus);
      setRouter(nextRouter);
      setModels(nextModels);
    } finally {
      if (!signal?.aborted) setIsLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!connected) return;
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [connected, load]);

  const refresh = useCallback(async () => {
    await load();
  }, [load]);

  /** Wraps a config write: one at a time, always followed by a re-read. */
  const runWrite = useCallback(
    async (label: string, work: () => Promise<void>, done: string) => {
      setBusy(label);
      setError(null);
      setNotice(null);
      try {
        await work();
        await load();
        setNotice(done);
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : String(caught));
      } finally {
        setBusy("");
      }
    },
    [load],
  );

  /**
   * Connects an MCP gateway by name and URL. The URL is user-entered here and
   * validated by the controller (HTTPS only, no credentials in the URL,
   * loopback HTTP only behind an operator opt-in).
   */
  const connectGateway = useCallback(
    async (name: string, url: string, token?: string) =>
      runWrite(
        "gateway",
        () => connectHarnessGateway(name, url, token),
        "MCP gateway connected. Its tools are now in the agent's catalog.",
      ),
    [runWrite],
  );

  /**
   * Runs the gateway's OAuth flow. `popup` is opened synchronously by the
   * caller before any await, because a popup opened after an await is blocked.
   * Completion is observed by polling controller status — robust regardless of
   * which origin the callback page's postMessage targets.
   */
  const connectGatewayOAuth = useCallback(
    async (
      name: string,
      url: string,
      popup: {
        setUrl: (target: string) => void;
        isClosed: () => boolean;
      },
    ) => {
      setBusy("gateway");
      setError(null);
      setNotice(null);
      try {
        popup.setUrl(await startHarnessGatewayOAuth(name, url));
        const gatewayConnected = await waitForHarnessGateway(popup.isClosed);
        await load();
        setNotice(
          gatewayConnected
            ? "Gateway connected. Its tools are now in the agent's catalog."
            : "Sign-in did not complete — no gateway was connected.",
        );
      } catch (caught) {
        setError(caught instanceof Error ? caught.message : String(caught));
      } finally {
        setBusy("");
      }
    },
    [load],
  );

  const saveRouter = useCallback(
    async (config: {
      enabled: boolean;
      classifierModel: string;
      defaultCategory: string;
      categories: HarnessRouterCategory[];
    }) =>
      runWrite(
        "router",
        () => saveHarnessRouter(config),
        "Routing saved. The daemon restarted with the new tiers.",
      ),
    [runWrite],
  );

  return {
    live: connected,
    /** "external": config is owned by the deployment; writes answer 409. */
    mode,
    status,
    router,
    models,
    isLoading,
    busy,
    error,
    notice,
    refresh,
    connectGateway,
    connectGatewayOAuth,
    saveRouter,
  };
}
