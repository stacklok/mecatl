"use client";

import { useCallback, useEffect, useState, useSyncExternalStore } from "react";

/**
 * Cosmetic, browser-local identity preferences: the agent's display name and
 * an optional profile picture. Neither has a daemon concept — there is no
 * server-side "agent name" or user-identity record — so these live in
 * localStorage only, same as the appearance/notification prefs on this page.
 */
const AGENT_NAME_KEY = "mecatl-studio.agent-name";
const AVATAR_KEY = "mecatl-studio.user-avatar";
const USER_NAME_KEY = "mecatl-studio.user-name";
const AGENT_AVATAR_KEY = "mecatl-studio.agent-avatar";
const SESSION_LIST_SIDE_KEY = "mecatl-studio.session-list-side";
const DEFAULT_AGENT_NAME = "Mecatl";

function readLocalStorage(key: string): string | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeLocalStorage(key: string, value: string | null) {
  if (typeof window === "undefined") return;
  try {
    if (value === null) window.localStorage.removeItem(key);
    else window.localStorage.setItem(key, value);
  } catch {
    // Storage disabled or full — the preference just doesn't persist.
  }
}

/** The agent's display name shown in chat, e.g. "Mecatl" or "Astra". */
export function useAgentDisplayName() {
  const [name, setNameState] = useState(DEFAULT_AGENT_NAME);
  useEffect(() => {
    const stored = readLocalStorage(AGENT_NAME_KEY);
    if (stored) setNameState(stored);
  }, []);

  const setName = useCallback((next: string) => {
    const trimmed = next.trim();
    const value = trimmed || DEFAULT_AGENT_NAME;
    setNameState(value);
    writeLocalStorage(AGENT_NAME_KEY, trimmed ? value : null);
  }, []);

  return { name, setName, defaultName: DEFAULT_AGENT_NAME };
}

function useStoredAvatar(key: string) {
  const [avatarUrl, setAvatarUrlState] = useState<string | null>(null);
  useEffect(() => {
    setAvatarUrlState(readLocalStorage(key));
  }, [key]);

  const setAvatarUrl = useCallback(
    (next: string | null) => {
      setAvatarUrlState(next);
      writeLocalStorage(key, next);
    },
    [key],
  );

  return { avatarUrl, setAvatarUrl };
}

/** The user's profile picture, stored as a data URL (no upload endpoint exists). */
export function useUserAvatar() {
  return useStoredAvatar(AVATAR_KEY);
}

/** The agent's picture, replacing the default bot mark in chat when set. */
export function useAgentAvatar() {
  return useStoredAvatar(AGENT_AVATAR_KEY);
}

export const UI_SCALE_MIN = 0.85;
export const UI_SCALE_MAX = 1.3;

const UI_SCALE_KEY = "mecatl-studio.ui-scale";

/** The root font-size is calc()'d against --ui-scale (see globals.css), so
 *  the multiplier composes with the per-viewport defaults instead of
 *  replacing them. */
function applyUiScale(scale: number) {
  if (typeof document === "undefined") return;
  if (scale === 1) {
    document.documentElement.style.removeProperty("--ui-scale");
  } else {
    document.documentElement.style.setProperty("--ui-scale", String(scale));
  }
}

function clampUiScale(value: number): number {
  const stepped = Math.round(value * 20) / 20;
  return Math.min(UI_SCALE_MAX, Math.max(UI_SCALE_MIN, stepped));
}

/**
 * Interface scale preference, browser-local: a multiplier over the UI's
 * default type scale (everything downstream is rem-based). Mount one
 * instance app-wide (see ClientProviders) so the stored scale applies on
 * load.
 */
export function useUiScale() {
  const [scale, setScaleState] = useState(1);
  useEffect(() => {
    const stored = Number.parseFloat(readLocalStorage(UI_SCALE_KEY) ?? "");
    if (Number.isFinite(stored)) {
      const value = clampUiScale(stored);
      setScaleState(value);
      applyUiScale(value);
    }
  }, []);

  const setScale = useCallback((next: number) => {
    const value = clampUiScale(next);
    setScaleState(value);
    writeLocalStorage(UI_SCALE_KEY, value === 1 ? null : String(value));
    applyUiScale(value);
  }, []);

  return { scale, setScale };
}

export type SessionListSide = "left" | "right";

/**
 * Which side of the chat the session list docks on. The thread and document
 * panels stay on the right regardless — only the list moves.
 */
export function useSessionListSide() {
  const [side, setSideState] = useState<SessionListSide>("right");
  useEffect(() => {
    if (readLocalStorage(SESSION_LIST_SIDE_KEY) === "left") {
      setSideState("left");
    }
  }, []);

  const setSide = useCallback((next: SessionListSide) => {
    setSideState(next);
    writeLocalStorage(SESSION_LIST_SIDE_KEY, next === "left" ? "left" : null);
  }, []);

  return { side, setSide };
}

const MOCK_FEATURES_KEY = "mecatl-studio.mock-features";

/**
 * Labs preference: show the clearly-labeled mock feature-tour content (a
 * synthetic chat demonstrating file cards, previews, and threads). Browser-
 * local demo content only — nothing mock ever reaches the daemon. Default
 * OFF; the key stores "1" only while enabled.
 */
export function useMockFeatures() {
  const [enabled, setEnabledState] = useState(false);
  useEffect(() => {
    if (readLocalStorage(MOCK_FEATURES_KEY) === "1") {
      setEnabledState(true);
    }
  }, []);

  const setEnabled = useCallback((next: boolean) => {
    setEnabledState(next);
    writeLocalStorage(MOCK_FEATURES_KEY, next ? "1" : null);
  }, []);

  return { enabled, setEnabled };
}

const DEVELOPER_TOOLS_KEY = "mecatl-studio.developer-tools";

/**
 * Labs preference: Studio's developer tools — the web analogue of mecatui's
 * client debug mode. On, the composer offers `/debug-ask` (a FAKE permission
 * ask that exercises the approval panel and is never sent to the daemon;
 * `features/agent/debug-ask.ts`), the chat menu gets "Inject fake approval",
 * and the queue strip shows the steer correlation trace (steer ids, drain
 * watermarks, decisions; `features/agent/steer-trace.ts`). Browser-local,
 * default OFF; the key stores "1" only while enabled.
 */
export function useDeveloperTools() {
  const [enabled, setEnabledState] = useState(false);
  useEffect(() => {
    if (readLocalStorage(DEVELOPER_TOOLS_KEY) === "1") {
      setEnabledState(true);
    }
  }, []);

  const setEnabled = useCallback((next: boolean) => {
    setEnabledState(next);
    writeLocalStorage(DEVELOPER_TOOLS_KEY, next ? "1" : null);
  }, []);

  return { enabled, setEnabled };
}

const HIDE_STARTER_PROMPTS_KEY = "mecatl-studio.hide-starter-prompts";

/**
 * Whether a new chat offers the suggested starter prompts — the web analogue
 * of the TUI's `--no-banner`. Default SHOWN; the key stores "1" only while
 * hidden, so turning the prompts back on removes it. Browser-local like the
 * other Personalize preferences.
 */
export function useShowStarterPrompts() {
  const [show, setShowState] = useState(true);
  useEffect(() => {
    if (readLocalStorage(HIDE_STARTER_PROMPTS_KEY) === "1") {
      setShowState(false);
    }
  }, []);

  const setShow = useCallback((next: boolean) => {
    setShowState(next);
    writeLocalStorage(HIDE_STARTER_PROMPTS_KEY, next ? null : "1");
  }, []);

  return { show, setShow };
}

const SHOW_TOOL_CALLS_KEY = "mecatl-studio.show-tool-calls";

const showToolCallsListeners = new Set<() => void>();

function subscribeShowToolCalls(callback: () => void): () => void {
  showToolCallsListeners.add(callback);
  return () => showToolCallsListeners.delete(callback);
}

function readShowToolCalls(): boolean {
  return readLocalStorage(SHOW_TOOL_CALLS_KEY) === "1";
}

/**
 * Whether chat transcripts render each turn's tool activity ("Show Tools").
 * A GLOBAL browser-local preference, not per session: the chat menu and the
 * thread panel's menu read and write the same stored value, and both mount
 * at once, so instances sync through a shared store (the use-panel-width
 * pattern) instead of hydrating independently. Default OFF; the key stores
 * "1" only while enabled. SSR renders "off" and patches up after hydration.
 */
export function useShowToolCalls() {
  const showToolCalls = useSyncExternalStore(
    subscribeShowToolCalls,
    readShowToolCalls,
    () => false,
  );

  const setShowToolCalls = useCallback((next: boolean) => {
    writeLocalStorage(SHOW_TOOL_CALLS_KEY, next ? "1" : null);
    for (const fn of showToolCallsListeners) fn();
  }, []);

  return { showToolCalls, setShowToolCalls };
}

const EXPAND_DETAILS_KEY = "mecatl-studio.expand-details";

const expandDetailsListeners = new Set<() => void>();

function subscribeExpandDetails(callback: () => void): () => void {
  expandDetailsListeners.add(callback);
  return () => expandDetailsListeners.delete(callback);
}

function readExpandDetails(): boolean {
  return readLocalStorage(EXPAND_DETAILS_KEY) === "1";
}

/**
 * Whether transcript details start EXPANDED (the TUI's ctrl+t "expand /
 * collapse details"): each turn's tool-call rows with their inline
 * summaries and Edit/Write diffs, the reasoning summary, and a failed
 * turn's raw payload. A GLOBAL browser-local preference flipped from the
 * chat menu and the `chat.expandDetails` shortcut; every open disclosure
 * follows a flip (the shared-store pattern of `useShowToolCalls`). Default
 * OFF (collapsed summaries); the key stores "1" only while on.
 */
export function useExpandDetails() {
  const expandDetails = useSyncExternalStore(
    subscribeExpandDetails,
    readExpandDetails,
    () => false,
  );

  const setExpandDetails = useCallback((next: boolean) => {
    writeLocalStorage(EXPAND_DETAILS_KEY, next ? "1" : null);
    for (const fn of expandDetailsListeners) fn();
  }, []);

  return { expandDetails, setExpandDetails };
}

/**
 * What Enter does while the agent is replying. `queue` and `steer` are the
 * two-way preference (Shift+Enter does the opposite); `queue-only` is the
 * client-level "never steer" switch — the web analogue of `mecatui
 * --no-steer`: Enter AND Shift+Enter queue, and every other steer affordance
 * (the queued row's Steer action) is withdrawn, whatever the daemon
 * supports.
 */
export type EnterSendBehavior = "queue" | "steer" | "queue-only";

const ENTER_SEND_BEHAVIOR_KEY = "mecatl-studio.enter-send-behavior";

/**
 * What Enter does while the agent is replying: queue the message for the next
 * run (the factory default), steer it into the in-flight run at the next
 * step, or `queue-only` — never steer, even with Shift+Enter. Hydrates on
 * mount, so the first frame always reads "queue" — the composer tolerates
 * that. Only the non-default values are stored; an unknown stored value
 * reads as "queue".
 */
export function useEnterSendBehavior() {
  const [behavior, setBehaviorState] = useState<EnterSendBehavior>("queue");
  useEffect(() => {
    const stored = readLocalStorage(ENTER_SEND_BEHAVIOR_KEY);
    if (stored === "steer" || stored === "queue-only") {
      setBehaviorState(stored);
    }
  }, []);

  const setBehavior = useCallback((next: EnterSendBehavior) => {
    setBehaviorState(next);
    writeLocalStorage(ENTER_SEND_BEHAVIOR_KEY, next === "queue" ? null : next);
  }, []);

  return { behavior, setBehavior };
}

/**
 * The user's display name — browser-local, cosmetic. It labels your chat
 * messages in Studio; the AGENT learns your name in conversation (its memory
 * stores user/identity/name itself — Studio has no write path into the
 * daemon's user model by design).
 */
export function useUserDisplayName() {
  const [name, setNameState] = useState("");
  useEffect(() => {
    const stored = readLocalStorage(USER_NAME_KEY);
    if (stored) setNameState(stored);
  }, []);
  const setName = useCallback((value: string) => {
    setNameState(value);
    const trimmed = value.trim();
    writeLocalStorage(USER_NAME_KEY, trimmed ? value : null);
  }, []);
  return { name, setName };
}

export type LaunchTarget = "draft" | "latest";

const LAUNCH_TARGET_KEY = "mecatl-studio.launch-target";

/**
 * What the bare chat route (`/workspace/chat`, no chat in the URL) opens —
 * the web analogue of mecatui's `--resume-latest`: a new draft (the factory
 * default) or the most recent eligible chat (`features/agent/latest-chat`).
 * Browser-local like the other Personalize preferences; the key stores
 * "latest" only while chosen. Hydrates on mount, so the first frame reads
 * "draft" — the launch effect tolerates that (it decides once the daemon
 * is connected and the inventory has loaded, well after hydration).
 */
export function useLaunchTarget() {
  const [target, setTargetState] = useState<LaunchTarget>("draft");
  useEffect(() => {
    if (readLocalStorage(LAUNCH_TARGET_KEY) === "latest") {
      setTargetState("latest");
    }
  }, []);

  const setTarget = useCallback((next: LaunchTarget) => {
    setTargetState(next);
    writeLocalStorage(LAUNCH_TARGET_KEY, next === "latest" ? "latest" : null);
  }, []);

  return { target, setTarget };
}
