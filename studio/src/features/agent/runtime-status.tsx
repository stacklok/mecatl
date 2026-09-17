"use client";

import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react";
import {
  fetchHarnessCompatibility,
  fetchHarnessControlStatus,
  type HarnessCompatibility,
  type HarnessControlStatus,
  probeHarness,
  setActiveHarnessProvider,
} from "@/lib/harness/client";
import { AuthRecoveryBanner } from "./auth-recovery-banner";
import { refreshComposerCapabilities } from "./composer-capabilities";
import { classifyOffline, type OfflineCause } from "./offline-cause";

const POLL_INTERVAL_MS = 5_000;

export type RuntimeConnectionState = "connecting" | "connected" | "offline";

export interface RuntimeStatus {
  state: RuntimeConnectionState;
  /** True exactly when state === "connected"; the common gate for loads. */
  connected: boolean;
  /** "external" when Studio proxies to MECATL_BASE_URL; "managed" otherwise. */
  mode: "managed" | "external";
  provider: string;
  /** True when `provider` is the offline mock — see MockProviderNotice. */
  isMock: boolean;
  /** Provider names configured in auth.yaml (never credentials). */
  configuredProviders: string[];
  /** Whether the ToolHive LLM gateway is reachable right now. */
  toolhiveAvailable: boolean;
  gateway: { name: string; url: string } | null;
  /** Why the daemon is unreachable, when it is. */
  detail: string;
  /** The NAMED cause behind an offline state — plain connectivity, or a
   *  refused credential (sign-in required / session expired / credential
   *  rejected / identity provider unreachable, `offline-cause.ts`); null
   *  while connecting or connected. */
  offlineCause: OfflineCause | null;
  /** The daemon's open feature registry (GET /v1/compatibility, ADR 0248).
   *  Empty against an older daemon — every feature-gated surface must treat
   *  absence as "not supported", never assume. */
  features: ReadonlySet<string>;
  /** The operator-enabled server capabilities off the same document. */
  serverCapabilities: Record<string, unknown>;
  /** Operator-set deployment label ("" when unset / older daemon). */
  deployment: string;
  /** The daemon-REPORTED effective operator posture (`capabilities.posture`,
   *  the SDK's `ServerPosture` ladder strict/trusted/auto/yolo); "" against
   *  an older daemon that omits it — the capability gate for every posture
   *  surface (the top-nav badge, the chat strip, Permissions, Diagnostics).
   *  Same value as `serverCapabilities.posture`, already narrowed. */
  posture: string;
  /** The SAVED permissions the managed daemon was spawned with (posture,
   *  project trust, shell-less mode); null in external mode or while the
   *  controller is unreachable. The EFFECTIVE posture is
   *  `serverCapabilities.posture` (absent on an older daemon). */
  permissions: HarnessControlStatus["permissions"];
  /** The controller's OWN project-trust decision for the current spawn
   *  (authority, decision, source, live anchor); null in external mode,
   *  against an older controller, or while it is unreachable. */
  trust: NonNullable<HarnessControlStatus["trust"]> | null;
  /** The controller's workspace label — display/keying only ("" when
   *  unknown). Never a placement input (Studio rule 2). */
  workspace: string;
  /** False only when the daemon reports an API major Studio does not speak. */
  apiCompatible: boolean;
  /** Forces an immediate re-probe (the offline screen's Retry). */
  refresh: () => Promise<void>;
  /**
   * Switches the daemon's active provider ("mock", "toolhive", or a name in
   * `configuredProviders`) and restarts it, then re-probes. Managed mode
   * only — external mode's controller has no daemon to restart.
   */
  switchProvider: (kind: string) => Promise<void>;
}

const RuntimeStatusContext = createContext<RuntimeStatus | null>(null);

/**
 * The single connection authority for every daemon-backed surface.
 *
 * Studio is daemon-only: there is no demo fallback, so an unreachable daemon
 * is a real state every surface must render. This provider polls the daemon
 * (via /api/mecatl) and the controller (via /api/mecatl-control) every five
 * seconds and exposes one shared answer, replacing the prototype's
 * per-hook one-shot probes — which could disagree with each other and never
 * noticed a daemon that died after mount.
 */
export function RuntimeStatusProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<RuntimeConnectionState>("connecting");
  const [detail, setDetail] = useState("");
  /** The failed probe's HTTP status + machine code (`HarnessStatus`). */
  const [failure, setFailure] = useState({ status: 0, code: "" });
  const [control, setControl] = useState<HarnessControlStatus | null>(null);
  const [mode, setMode] = useState<"managed" | "external">("managed");
  const [compat, setCompat] = useState<HarnessCompatibility | null>(null);
  const capabilitiesLoaded = useRef(false);

  const probe = useCallback(async (signal?: AbortSignal) => {
    const [daemon, controlStatus] = await Promise.all([
      probeHarness(signal),
      fetchHarnessControlStatus(signal),
    ]);
    if (signal?.aborted) return;
    setControl(controlStatus);
    if (controlStatus?.mode) setMode(controlStatus.mode);
    if (daemon.live) {
      setState("connected");
      setDetail("");
      if (!capabilitiesLoaded.current) {
        capabilitiesLoaded.current = true;
        void refreshComposerCapabilities();
        // Feature detection rides each (re)connect: a restart may be a
        // different daemon version. Best-effort — an older daemon without
        // the endpoint reports null and every gate reads "unsupported".
        fetchHarnessCompatibility(signal)
          .then((doc) => {
            if (!signal?.aborted) setCompat(doc);
          })
          .catch(() => {
            if (!signal?.aborted) setCompat(null);
          });
      }
    } else {
      setState("offline");
      setDetail(daemon.detail);
      setFailure({ status: daemon.status, code: daemon.code });
      // The next reconnect re-reads mentions and commands: a restart may have
      // changed the resolved roster.
      capabilitiesLoaded.current = false;
    }
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void probe(controller.signal);
    const timer = setInterval(() => {
      void probe(controller.signal);
    }, POLL_INTERVAL_MS);
    return () => {
      controller.abort();
      clearInterval(timer);
    };
  }, [probe]);

  const refresh = useCallback(async () => {
    // A forced re-probe re-reads the capabilities too: a settings save has
    // just restarted the daemon (learning.mode flips what it advertises),
    // and the 5 s poll may never have seen it down.
    capabilitiesLoaded.current = false;
    await probe();
  }, [probe]);

  // Memo-free derivations: both are cheap and re-render-safe.
  const featureSet = new Set(compat?.features ?? []);
  // 0/absent = older daemon (compatible by definition of the additive era);
  // a REPORTED major other than 1 is a real skew Studio must not hide.
  const apiCompatible = compat === null || compat.apiMajor <= 1;
  // The named cause behind an offline state decides WHICH banner renders: a
  // refused credential gets the auth-recovery banner, not "unreachable".
  const offlineCause: OfflineCause | null =
    state === "offline" ? classifyOffline({ ...failure, detail }) : null;

  const switchProvider = useCallback(
    async (kind: string) => {
      await setActiveHarnessProvider(kind);
      await probe();
    },
    [probe],
  );

  return (
    <RuntimeStatusContext.Provider
      value={{
        state,
        connected: state === "connected",
        mode,
        provider: control?.provider ?? "",
        isMock: control?.isMock ?? false,
        configuredProviders: control?.configuredProviders ?? [],
        toolhiveAvailable: control?.toolhiveGateway?.available ?? false,
        gateway: control?.gateway ?? null,
        detail,
        offlineCause,
        features: featureSet,
        serverCapabilities: compat?.capabilities ?? {},
        deployment: compat?.deployment ?? "",
        posture:
          typeof compat?.capabilities.posture === "string"
            ? compat.capabilities.posture
            : "",
        permissions: control?.permissions ?? null,
        trust: control?.trust ?? null,
        workspace: control?.workspace ?? "",
        apiCompatible,
        refresh,
        switchProvider,
      }}
    >
      {state === "offline" &&
        (offlineCause && offlineCause.kind !== "connectivity" ? (
          <AuthRecoveryBanner
            cause={offlineCause}
            onRetry={refresh}
            mode={mode}
          />
        ) : (
          <OfflineBanner detail={detail} onRetry={refresh} />
        ))}
      {state === "connected" && !apiCompatible && (
        <div
          role="alert"
          className="flex items-center justify-center gap-3 border-b border-amber-500/40 bg-amber-500/10 px-4 py-1.5 text-xs text-amber-700 dark:text-amber-400"
        >
          <span className="font-medium">
            This daemon speaks API v{compat?.apiMajor} — Studio supports v1.
          </span>
          <span className="hidden sm:inline">
            Some features may not work; update Studio or the daemon.
          </span>
        </div>
      )}
      {children}
    </RuntimeStatusContext.Provider>
  );
}

function OfflineBanner({
  detail,
  onRetry,
}: {
  detail: string;
  onRetry: () => void;
}) {
  return (
    <div
      role="alert"
      className="flex items-center justify-center gap-3 border-b border-destructive/30 bg-destructive/10 px-4 py-1.5 text-xs text-destructive"
    >
      <span className="font-medium">Mecatl is unreachable.</span>
      <span className="hidden truncate sm:inline">
        {detail || "Run `task build`, then `task studio:dev` to start it."}
      </span>
      <button
        type="button"
        onClick={onRetry}
        className="rounded border border-destructive/40 px-2 py-0.5 font-medium hover:bg-destructive/20"
      >
        Retry
      </button>
    </div>
  );
}

export function useRuntimeStatus(): RuntimeStatus {
  const context = useContext(RuntimeStatusContext);
  if (!context) {
    throw new Error(
      "useRuntimeStatus must be used inside RuntimeStatusProvider",
    );
  }
  return context;
}

/**
 * `useRuntimeStatus` for a component that may also render OUTSIDE the
 * provider (a display-only composer, a unit test): null there, so a
 * capability-gated affordance simply stays hidden instead of throwing.
 */
export function useOptionalRuntimeStatus(): RuntimeStatus | null {
  return useContext(RuntimeStatusContext);
}
