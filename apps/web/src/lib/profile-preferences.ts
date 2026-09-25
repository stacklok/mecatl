// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useState } from "react";
import { readUserScopedItem, writeUserScopedItem } from "./account-storage";

const agentNameKey = "studio.profile.agent-name";
const agentAvatarKey = "studio.profile.agent-avatar";
const userNameKey = "studio.profile.user-name";
const userAvatarKey = "studio.profile.user-avatar";
const uiScaleKey = "studio.profile.ui-scale";
const sessionListSideKey = "studio.profile.session-list-side";
const showToolCallsKey = "studio.profile.show-tool-calls";
const expandDetailsKey = "studio.profile.expand-details";
const enterSendBehaviorKey = "studio.chat.composer.enterToSend";
const startOnKey = "studio.profile.start-on";

export const uiScaleMin = 0.85;
export const uiScaleMax = 1.3;
export type SessionListSide = "left" | "right";
export type EnterSendBehavior = "queue" | "steer";
export type StartOn = "draft" | "latest";

export const defaultAgentName = "Mecatl";

export function useAgentDisplayName() {
  return useStoredText(agentNameKey, defaultAgentName);
}

export function useUserDisplayName() {
  return useStoredText(userNameKey, "");
}

export function useAgentAvatar() {
  return useStoredText(agentAvatarKey, "");
}

export function useUserAvatar() {
  return useStoredText(userAvatarKey, "");
}

export function initializeProfilePreferences() {
  applyUiScale(readUiScale());
}

export function useUiScale() {
  const [value, setValueState] = useState(readUiScale);
  const setValue = useCallback((next: number) => {
    const value = clampUiScale(next);
    setValueState(value);
    writePreference(uiScaleKey, value === 1 ? undefined : String(value));
    applyUiScale(value);
  }, []);
  return { setValue, value };
}

export function useSessionListSide() {
  return useStoredChoice<SessionListSide>(sessionListSideKey, "right", ["left", "right"]);
}

export function useShowToolCalls() {
  const preference = useStoredChoice(showToolCallsKey, "hidden", ["hidden", "visible"]);
  return {
    setValue: (next: boolean) => preference.setValue(next ? "visible" : "hidden"),
    value: preference.value === "visible",
  };
}

/**
 * Whether tool rows, reasoning summaries, and failed-turn raw payloads start
 * expanded. Read once by each disclosure as its initial state — flipping the
 * preference does not retroactively expand a disclosure already on screen.
 */
export function useExpandDetails() {
  const preference = useStoredChoice(expandDetailsKey, "collapsed", ["collapsed", "expanded"]);
  return {
    setValue: (next: boolean) => preference.setValue(next ? "expanded" : "collapsed"),
    value: preference.value === "expanded",
  };
}

export function useStartOn() {
  return useStoredChoice<StartOn>(startOnKey, "draft", ["draft", "latest"]);
}

export function useEnterSendBehavior() {
  return useStoredChoice<EnterSendBehavior>(enterSendBehaviorKey, "queue", ["queue", "steer"]);
}

function useStoredText(key: string, fallback: string) {
  const [value, setValueState] = useState(() => readPreference(key) ?? fallback);

  useEffect(() => {
    function synchronize(event: StorageEvent) {
      if (event.key === key || event.key === null) setValueState(readPreference(key) ?? fallback);
    }
    window.addEventListener("storage", synchronize);
    return () => window.removeEventListener("storage", synchronize);
  }, [fallback, key]);

  const setValue = useCallback(
    (next: string) => {
      setValueState(next);
      writePreference(key, next || undefined);
    },
    [key],
  );

  return { setValue, value };
}

function useStoredChoice<Value extends string>(key: string, fallback: Value, choices: Value[]) {
  const [value, setValueState] = useState(() => {
    const stored = readPreference(key);
    return choices.includes(stored as Value) ? (stored as Value) : fallback;
  });

  const setValue = useCallback(
    (next: Value) => {
      setValueState(next);
      writePreference(key, next === fallback ? undefined : next);
    },
    [fallback, key],
  );

  return { setValue, value };
}

function readUiScale() {
  const stored = Number.parseFloat(readPreference(uiScaleKey) ?? "");
  return Number.isFinite(stored) ? clampUiScale(stored) : 1;
}

function clampUiScale(value: number) {
  const stepped = Math.round(value * 20) / 20;
  return Math.min(uiScaleMax, Math.max(uiScaleMin, stepped));
}

function applyUiScale(value: number) {
  if (value === 1) document.documentElement.style.removeProperty("--ui-scale");
  else document.documentElement.style.setProperty("--ui-scale", String(value));
}

function readPreference(key: string): string | undefined {
  return readUserScopedItem(key) ?? undefined;
}

function writePreference(key: string, value?: string) {
  writeUserScopedItem(key, value ?? null);
}
