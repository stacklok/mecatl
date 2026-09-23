// SPDX-License-Identifier: Apache-2.0

import { useCallback, useState } from "react";

const disabledModelsKey = "studio.chat.models.disabled";

export function modelPreferenceId(model: { id: string; providerId: string }) {
  return JSON.stringify([model.providerId, model.id]);
}

export function parseDisabledModels(raw: string | null | undefined): Set<string> {
  if (!raw) return new Set();
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return new Set();
    return new Set(parsed.filter((id): id is string => typeof id === "string" && id.length > 0));
  } catch {
    return new Set();
  }
}

export function serializeDisabledModels(ids: Set<string>): string | undefined {
  return ids.size > 0 ? JSON.stringify([...ids].sort()) : undefined;
}

export function useDisabledModels() {
  const [disabled, setDisabled] = useState(readDisabledModels);
  const setModelEnabled = useCallback((id: string, enabled: boolean) => {
    setDisabled((current) => {
      const next = new Set(current);
      if (enabled) next.delete(id);
      else next.add(id);
      writeDisabledModels(next);
      return next;
    });
  }, []);
  return { disabled, setModelEnabled };
}

function readDisabledModels() {
  try {
    return parseDisabledModels(window.localStorage.getItem(disabledModelsKey));
  } catch {
    return new Set<string>();
  }
}

function writeDisabledModels(ids: Set<string>) {
  try {
    const value = serializeDisabledModels(ids);
    if (value) window.localStorage.setItem(disabledModelsKey, value);
    else window.localStorage.removeItem(disabledModelsKey);
  } catch {
    // Browser storage is an enhancement; keep the in-memory value for this page.
  }
}
