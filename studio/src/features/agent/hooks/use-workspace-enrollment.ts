"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import {
  cancelWorkspaceEnrollment,
  connectWorkspaceServices,
  fetchSessionConnectors,
  isStaleEnrollment,
  retryWorkspaceEnrollment,
  type SessionConnectors,
  type WorkspaceEnrollment,
} from "@/lib/harness/enrollment";
import { refreshSlashCommands } from "../composer-capabilities";
import { useRuntimeStatus } from "../runtime-status";

/**
 * The per-chat workspace-services enrollment state — the TUI's
 * "workspace services not connected — /tools-connect to enable protected
 * tools" notice and its connect / retry / cancel actions.
 *
 * Gated on the daemon's `workspace_enrollment` capability and hidden for a
 * debug-target session (the TUI's `workspaceEnrollmentActive`). Where the
 * daemon also grants `mcp_connector_status`, the connector inventory is read
 * once per open so an already-connected chat shows no notice; otherwise the
 * notice shows until a connect succeeds (TUI parity).
 *
 * The consent URL the daemon hands back on connect/retry is EPHEMERAL: it
 * goes straight to the window opened for it and lives only in a ref until the
 * enrollment settles — never in React state, this hook's return value, the
 * DOM or a log line.
 */

export type WorkspaceEnrollmentPhase =
  /** Unsupported, no session, or the connector probe is still in flight. */
  | "unknown"
  /** The connector inventory says this deployment needs no enrollment step. */
  | "not_required"
  | "not_connected"
  | "pending"
  | "connected"
  | "failed";

/** How the consent window stands while an enrollment is pending. */
export type EnrollmentWindowState =
  /** Studio opened it and pointed it at the consent page. */
  | "open"
  /** The browser blocked the popup: the user must open it with a click. */
  | "blocked"
  /** The window was closed before the enrollment settled. */
  | "closed"
  /** The pending enrollment was started by another client (no URL here). */
  | "elsewhere";

export interface WorkspaceEnrollmentView {
  /** Capability present, a real (non-debug) session is selected. */
  supported: boolean;
  phase: WorkspaceEnrollmentPhase;
  /** The pending enrollment's id, "" when none is known. */
  enrollmentId: string;
  requiredServices: number;
  /** `failed` only: the terminal status word (denied / expired / failed /
   *  timed out), or "" when a request itself failed (see `error`). */
  outcome: string;
  error: string | null;
  /** A user action is in flight (polls never set this). */
  busy: boolean;
  /** The chat is idle: the daemon only accepts these controls then. */
  idle: boolean;
  /** The not-connected notice was dismissed for this session (this tab). */
  dismissed: boolean;
  window: EnrollmentWindowState;
  connect: () => void;
  retry: () => void;
  cancel: () => void;
  dismiss: () => void;
  /** Re-opens the consent window for the pending enrollment (a click). */
  reopenWindow: () => void;
}

interface EnrollmentState {
  phase: WorkspaceEnrollmentPhase;
  enrollmentId: string;
  requiredServices: number;
  outcome: string;
  error: string | null;
  busy: boolean;
  dismissed: boolean;
  window: EnrollmentWindowState;
}

const INITIAL: EnrollmentState = {
  phase: "unknown",
  enrollmentId: "",
  requiredServices: 0,
  outcome: "",
  error: null,
  busy: false,
  dismissed: false,
  window: "elsewhere",
};

/** The TUI's observe cadence (`workspaceEnrollmentPollInterval`). */
export const ENROLLMENT_POLL_INTERVAL_MS = 3_000;
/** How long Studio keeps observing a pending enrollment before giving up. */
export const ENROLLMENT_POLL_CAP_MS = 5 * 60_000;
export const ENROLLMENT_TIMED_OUT = "timed out";

const POPUP_NAME = "mecatl-workspace-enrollment";
const POPUP_FEATURES = "width=520,height=680";
const DISMISSED_KEY = "mecatl-studio.enrollment-dismissed:";
const CONNECTED_KEY = "mecatl-studio.enrollment-connected:";

function readMarker(prefix: string, sessionId: string): boolean {
  try {
    return window.sessionStorage.getItem(prefix + sessionId) === "1";
  } catch {
    return false;
  }
}

function writeMarker(prefix: string, sessionId: string): void {
  try {
    window.sessionStorage.setItem(prefix + sessionId, "1");
  } catch {
    // storage unavailable: the notice simply shows again next open
  }
}

/** Maps the connector inventory's enrollment vocabulary onto a phase. */
export function phaseFromConnectors(
  inventory: SessionConnectors | null,
): WorkspaceEnrollmentPhase {
  switch (inventory?.enrollmentState) {
    case "completed":
      return "connected";
    case "not_required":
      return "not_required";
    case "pending":
      return "pending";
    default:
      // not_started, unknown, an unknown word, or inspection unavailable:
      // the TUI's unconditional notice.
      return "not_connected";
  }
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function closeWindow(popup: Window | null | undefined): void {
  try {
    popup?.close();
  } catch {
    // a cross-origin or already-closed window: nothing to do
  }
}

export function useWorkspaceEnrollment(
  sessionId: string | null,
  options: { idle: boolean; debugSession: boolean },
): WorkspaceEnrollmentView {
  const { serverCapabilities } = useRuntimeStatus();
  const capability = serverCapabilities.workspace_enrollment === true;
  const connectorStatus = serverCapabilities.mcp_connector_status === true;
  const supported = capability && Boolean(sessionId) && !options.debugSession;

  const [state, setState] = useState<EnrollmentState>(INITIAL);
  const stateRef = useRef(state);
  stateRef.current = state;
  const idleRef = useRef(options.idle);
  idleRef.current = options.idle;

  // Every async result is checked against the generation it started under:
  // a session change, unmount, or a newer user action retires older ones.
  const generation = useRef(0);
  const abortRef = useRef<AbortController | null>(null);
  const pollTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pollStarted = useRef(0);
  // The consent window and its URL: refs only (see the module comment).
  const popupRef = useRef<Window | null>(null);
  const urlRef = useRef("");

  const clearPoll = useCallback(() => {
    if (pollTimer.current) clearTimeout(pollTimer.current);
    pollTimer.current = null;
  }, []);

  const forgetWindow = useCallback(() => {
    popupRef.current = null;
    urlRef.current = "";
  }, []);

  // Reset and probe on every session change.
  useEffect(() => {
    generation.current += 1;
    const gen = generation.current;
    abortRef.current?.abort();
    const abort = new AbortController();
    abortRef.current = abort;
    clearPoll();
    forgetWindow();

    if (!supported || !sessionId) {
      setState(INITIAL);
      return () => {
        abort.abort();
        clearPoll();
      };
    }

    const dismissed = readMarker(DISMISSED_KEY, sessionId);
    const remembered = readMarker(CONNECTED_KEY, sessionId);
    setState({
      ...INITIAL,
      dismissed,
      phase: connectorStatus
        ? "unknown"
        : remembered
          ? "connected"
          : "not_connected",
    });
    if (connectorStatus) {
      fetchSessionConnectors(sessionId, abort.signal)
        .then((inventory) => {
          if (generation.current !== gen) return;
          const probed = phaseFromConnectors(inventory);
          // Inspection unavailable (null) defers to what this tab already saw.
          const phase = inventory === null && remembered ? "connected" : probed;
          setState((s) => ({ ...s, phase }));
        })
        .catch(() => {
          if (generation.current !== gen) return;
          setState((s) => ({
            ...s,
            phase: remembered ? "connected" : "not_connected",
          }));
        });
    }
    return () => {
      abort.abort();
      clearPoll();
    };
  }, [sessionId, supported, connectorStatus, clearPoll, forgetWindow]);

  const settleConnected = useCallback(
    (id: string) => {
      clearPoll();
      closeWindow(popupRef.current);
      forgetWindow();
      writeMarker(CONNECTED_KEY, id);
      setState((s) => ({
        ...s,
        phase: "connected",
        enrollmentId: "",
        outcome: "",
        error: null,
        busy: false,
      }));
      toast.success("Workspace services connected");
      // The session's tool catalogue just changed.
      void refreshSlashCommands(id);
    },
    [clearPoll, forgetWindow],
  );

  const settleNotConnected = useCallback(() => {
    clearPoll();
    closeWindow(popupRef.current);
    forgetWindow();
    setState((s) => ({
      ...s,
      phase: "not_connected",
      enrollmentId: "",
      requiredServices: 0,
      outcome: "",
      error: null,
      busy: false,
      window: "elsewhere",
    }));
  }, [clearPoll, forgetWindow]);

  const settleFailed = useCallback(
    (outcome: string, error: string | null, keepId: boolean) => {
      clearPoll();
      closeWindow(popupRef.current);
      forgetWindow();
      setState((s) => ({
        ...s,
        phase: "failed",
        enrollmentId: keepId ? s.enrollmentId : "",
        outcome,
        error,
        busy: false,
        window: "elsewhere",
      }));
    },
    [clearPoll, forgetWindow],
  );

  // Declared as a ref so `applyResult` and `poll` can refer to each other.
  const armPollRef = useRef<() => void>(() => {});

  /**
   * Folds one enrollment projection into state. `popup` is the window the
   * triggering click opened (null for a poll): it receives the consent URL
   * on a fresh begin and is closed on every other outcome.
   */
  const applyResult = useCallback(
    (id: string, result: WorkspaceEnrollment, popup: Window | null) => {
      switch (result.status) {
        case "connected":
          closeWindow(popup);
          settleConnected(id);
          return;
        case "pending": {
          let windowState: EnrollmentWindowState;
          if (result.presentationUrl) {
            // A fresh begin: hand the URL to the click's window and keep it
            // only for a reopen; it never reaches state.
            urlRef.current = result.presentationUrl;
            if (popup) {
              popup.location.href = result.presentationUrl;
              popupRef.current = popup;
              windowState = "open";
            } else {
              popupRef.current = null;
              windowState = "blocked";
            }
          } else {
            // The observe form carries no URL: the enrollment was begun by
            // an earlier call (ours, or another client's).
            closeWindow(popup);
            if (popupRef.current) {
              windowState = popupRef.current.closed ? "closed" : "open";
            } else if (urlRef.current) {
              windowState = stateRef.current.window;
            } else {
              windowState = "elsewhere";
            }
          }
          setState((s) => ({
            ...s,
            phase: "pending",
            enrollmentId: result.enrollmentId,
            requiredServices: result.requiredServices,
            outcome: "",
            error: null,
            busy: false,
            window: windowState,
          }));
          armPollRef.current();
          return;
        }
        case "cancelled":
          closeWindow(popup);
          settleNotConnected();
          return;
        default:
          // denied / expired / failed, or a word this build does not know:
          // the daemon has cleared its pending record either way.
          closeWindow(popup);
          settleFailed(result.status, null, false);
      }
    },
    [settleConnected, settleNotConnected, settleFailed],
  );

  const poll = useCallback(() => {
    pollTimer.current = null;
    const id = sessionId;
    const gen = generation.current;
    if (!id || stateRef.current.phase !== "pending") return;
    if (Date.now() - pollStarted.current > ENROLLMENT_POLL_CAP_MS) {
      settleFailed(ENROLLMENT_TIMED_OUT, null, true);
      return;
    }
    // The daemon accepts the observe only on an idle session, and a user
    // action owns the wire while it is in flight: skip this tick.
    if (!idleRef.current || stateRef.current.busy) {
      armPollRef.current();
      return;
    }
    if (
      popupRef.current?.closed &&
      stateRef.current.window === "open" &&
      generation.current === gen
    ) {
      setState((s) => ({ ...s, window: "closed" }));
    }
    connectWorkspaceServices(id, abortRef.current?.signal)
      .then((result) => {
        if (generation.current !== gen) return;
        applyResult(id, result, null);
      })
      .catch((error: unknown) => {
        if (generation.current !== gen || abortRef.current?.signal.aborted) {
          return;
        }
        // A failed observe is not a failed enrollment: keep waiting, say why.
        setState((s) => ({ ...s, error: errorText(error) }));
        armPollRef.current();
      });
  }, [sessionId, applyResult, settleFailed]);

  armPollRef.current = () => {
    clearPoll();
    pollTimer.current = setTimeout(poll, ENROLLMENT_POLL_INTERVAL_MS);
  };

  /** Runs one user-triggered control; `popup` is the click's window. */
  const runControl = useCallback(
    (
      id: string,
      popup: Window | null,
      call: (signal: AbortSignal | undefined) => Promise<WorkspaceEnrollment>,
    ) => {
      clearPoll();
      generation.current += 1;
      const gen = generation.current;
      pollStarted.current = Date.now();
      setState((s) => ({ ...s, busy: true, error: null }));
      call(abortRef.current?.signal)
        .then((result) => {
          if (generation.current !== gen) {
            closeWindow(popup);
            return;
          }
          applyResult(id, result, popup);
        })
        .catch((error: unknown) => {
          closeWindow(popup);
          if (generation.current !== gen || abortRef.current?.signal.aborted) {
            return;
          }
          settleFailed("", errorText(error), true);
        });
    },
    [applyResult, clearPoll, settleFailed],
  );

  const connect = useCallback(() => {
    const id = sessionId;
    const current = stateRef.current;
    if (!id || !supported || !idleRef.current || current.busy) return;
    // Opened synchronously, before any await: a popup created after one is
    // blocked. The observe form (a pending enrollment already exists) hands
    // back no URL and the blank window is closed again.
    const popup = window.open("about:blank", POPUP_NAME, POPUP_FEATURES);
    runControl(id, popup, (signal) => connectWorkspaceServices(id, signal));
  }, [sessionId, supported, runControl]);

  const retry = useCallback(() => {
    const id = sessionId;
    const current = stateRef.current;
    if (!id || !supported || !idleRef.current || current.busy) return;
    const enrollmentId = current.enrollmentId;
    const popup = window.open("about:blank", POPUP_NAME, POPUP_FEATURES);
    runControl(id, popup, async (signal) => {
      if (!enrollmentId) return connectWorkspaceServices(id, signal);
      try {
        return await retryWorkspaceEnrollment(id, enrollmentId, signal);
      } catch (error) {
        // The daemon already settled that enrollment (a terminal status it
        // observed cleared the record): begin a fresh one instead.
        if (!isStaleEnrollment(error)) throw error;
        return connectWorkspaceServices(id, signal);
      }
    });
  }, [sessionId, supported, runControl]);

  const cancel = useCallback(() => {
    const id = sessionId;
    const current = stateRef.current;
    if (!id || !supported || !idleRef.current || current.busy) return;
    clearPoll();
    closeWindow(popupRef.current);
    forgetWindow();
    const enrollmentId = current.enrollmentId;
    if (!enrollmentId) {
      settleNotConnected();
      return;
    }
    generation.current += 1;
    const gen = generation.current;
    setState((s) => ({ ...s, busy: true, error: null }));
    cancelWorkspaceEnrollment(id, enrollmentId, abortRef.current?.signal)
      .then(() => {
        if (generation.current !== gen) return;
        settleNotConnected();
      })
      .catch((error: unknown) => {
        if (generation.current !== gen || abortRef.current?.signal.aborted) {
          return;
        }
        // Stale id: the daemon has nothing pending any more — same outcome.
        if (isStaleEnrollment(error)) {
          settleNotConnected();
          return;
        }
        setState((s) => ({
          ...s,
          busy: false,
          error: `Could not cancel: ${errorText(error)}`,
        }));
      });
  }, [sessionId, supported, clearPoll, forgetWindow, settleNotConnected]);

  const reopenWindow = useCallback(() => {
    if (stateRef.current.phase !== "pending" || !urlRef.current) return;
    // A click handler: the browser allows this open. The URL comes from the
    // ref and goes nowhere else.
    const popup = window.open(urlRef.current, POPUP_NAME, POPUP_FEATURES);
    popupRef.current = popup;
    setState((s) => ({ ...s, window: popup ? "open" : "blocked" }));
  }, []);

  const dismiss = useCallback(() => {
    if (sessionId) writeMarker(DISMISSED_KEY, sessionId);
    setState((s) => ({ ...s, dismissed: true }));
  }, [sessionId]);

  return useMemo<WorkspaceEnrollmentView>(
    () => ({
      supported,
      phase: state.phase,
      enrollmentId: state.enrollmentId,
      requiredServices: state.requiredServices,
      outcome: state.outcome,
      error: state.error,
      busy: state.busy,
      idle: options.idle,
      dismissed: state.dismissed,
      window: state.window,
      connect,
      retry,
      cancel,
      dismiss,
      reopenWindow,
    }),
    [
      supported,
      state,
      options.idle,
      connect,
      retry,
      cancel,
      dismiss,
      reopenWindow,
    ],
  );
}
