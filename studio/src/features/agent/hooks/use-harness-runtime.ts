"use client";

import { useCallback, useEffect, useState } from "react";
import {
  connectHarnessGateway,
  fetchHarnessControlStatus,
  fetchHarnessPermissions,
  type HarnessControlStatus,
  type HarnessPermissionsConfig,
  type HarnessPermissionsState,
  type HarnessRetentionSettings,
  type HarnessStorageSettings,
  listHarnessModels,
  saveHarnessPermissions,
  saveHarnessRetention,
  saveHarnessStorageSettings,
  startHarnessGatewayOAuth,
  trustWorkspace,
  trustWorkspaceOnce,
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
  const { connected, mode, provider } = useRuntimeStatus();
  const [status, setStatus] = useState<HarnessControlStatus | null>(null);
  const [permissions, setPermissions] = useState<{
    config: HarnessPermissionsState;
    operatorSettings: boolean;
  } | null>(null);
  const [models, setModels] = useState<HarnessModel[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    setIsLoading(true);
    try {
      // Independent reads, so they go out together rather than in a waterfall.
      const [nextStatus, nextPermissions, nextModels] = await Promise.all([
        fetchHarnessControlStatus(signal),
        fetchHarnessPermissions(signal).catch(() => null),
        listHarnessModels(signal).catch(() => []),
      ]);
      if (signal?.aborted) return;
      setStatus(nextStatus);
      setPermissions(nextPermissions);
      setModels(nextModels);
    } finally {
      if (!signal?.aborted) setIsLoading(false);
    }
  }, []);

  // Re-read when the connection flips AND when the active provider changes:
  // a provider switch restarts the daemon with a different model catalogue,
  // and a composer mounted before the switch must not keep the old list.
  // biome-ignore lint/correctness/useExhaustiveDependencies: `provider` is the re-read trigger, not a value the effect reads
  useEffect(() => {
    if (!connected) return;
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [connected, provider, load]);

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
        "Gateway connected. The agent can now use its tools.",
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
            ? "Gateway connected. The agent can now use its tools."
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

  /**
   * Saves the operator posture / project trust / shell-less document. The
   * controller restarts the daemon on the new flags; a refused start (auto or
   * yolo as root outside a sandbox) is rolled back there and lands in `error`.
   */
  const savePermissions = useCallback(
    async (config: HarnessPermissionsConfig) =>
      runWrite(
        "permissions",
        () => saveHarnessPermissions(config),
        "Saved. The agent restarted.",
      ),
    [runWrite],
  );

  /**
   * The REMEMBERED project-trust grant — mecatui's "trust" answer. The
   * controller persists `trustProject: true` stamped with the workspace's
   * live identity anchor (also how a DRIFTED grant is re-accepted after the
   * changed instructions were reviewed) and restarts the daemon with
   * `--trust-project`. Bodyless: the anchor is the controller's, never the
   * browser's. `/status.trust` is re-read afterwards so the page shows the
   * decision the new spawn actually got.
   */
  const trustProject = useCallback(
    async () =>
      runWrite(
        "trust",
        () => trustWorkspace(),
        "This project is now trusted. The agent restarted.",
      ),
    [runWrite],
  );

  /**
   * The "trust once" answer: `--trust-project` for THIS controller process
   * only. Nothing is persisted — the grant rides every daemon restart in
   * between and dies when Studio's controller exits.
   */
  const trustProjectOnce = useCallback(
    async () =>
      runWrite(
        "trust",
        () => trustWorkspaceOnce(),
        "Trusted until Studio restarts. Nothing is saved. The agent restarted.",
      ),
    [runWrite],
  );

  /**
   * Saves the session-store document (durable directory or in-memory). The
   * controller restarts the daemon on the new flag; a store it cannot use is
   * rolled back there and lands in `error`.
   */
  const saveStorage = useCallback(
    async (settings: HarnessStorageSettings) =>
      runWrite(
        "storage",
        async () => {
          await saveHarnessStorageSettings(settings);
        },
        "Saved. The agent restarted.",
      ),
    [runWrite],
  );

  /**
   * Saves the retention document (family age/count limits, sweep cadence,
   * main-deletion acknowledgement). The controller restarts the daemon on
   * the new flags; flags mecated refuses are rolled back there and land in
   * `error`.
   */
  const saveRetention = useCallback(
    async (settings: HarnessRetentionSettings) =>
      runWrite(
        "retention",
        async () => {
          await saveHarnessRetention(settings);
        },
        "Saved. The agent restarted.",
      ),
    [runWrite],
  );

  return {
    live: connected,
    /** "external": config is owned by the deployment; writes answer 409. */
    mode,
    status,
    /** The saved permissions document + whether an imported operator
     *  settings file is active; null in external mode / before the load. */
    permissions,
    models,
    isLoading,
    busy,
    error,
    notice,
    refresh,
    connectGateway,
    connectGatewayOAuth,
    savePermissions,
    /** The remembered project-trust grant (restarts the daemon); `busy`
     *  reads "trust" while either grant runs. */
    trustProject,
    /** The this-controller-process-only grant (restarts the daemon). */
    trustProjectOnce,
    saveStorage,
    saveRetention,
  };
}
