"use client";

import { useCallback, useEffect, useState, useSyncExternalStore } from "react";

/**
 * Browser-local set of model ids the user has switched off on the provider
 * detail page. HONEST SCOPE: the daemon has no per-model disable knob — the
 * operator `models.allowlist` in settings.yaml caps project-tier model
 * BINDINGS (aliases/slots/default), it does not filter `GET /v1/models` or
 * refuse a session selector — so this is a Studio-side preference that hides
 * models from Studio's own pickers. The daemon can still be asked for them
 * by other clients, and the UI says so wherever the switch appears.
 */
const DISABLED_MODELS_KEY = "mecatl-studio.disabled-models";

/** Parses the stored JSON into a set of non-empty model-id strings. */
export function parseDisabledModels(raw: string | null): Set<string> {
  if (!raw) return new Set();
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!Array.isArray(parsed)) return new Set();
    return new Set(
      parsed.filter((id): id is string => typeof id === "string" && id !== ""),
    );
  } catch {
    return new Set();
  }
}

/** Serializes the set for storage; null when nothing is disabled (so the
 *  key disappears rather than storing an empty list forever). */
export function serializeDisabledModels(ids: Set<string>): string | null {
  if (ids.size === 0) return null;
  return JSON.stringify([...ids].sort());
}

function readStored(): Set<string> {
  if (typeof window === "undefined") return new Set();
  try {
    return parseDisabledModels(
      window.localStorage.getItem(DISABLED_MODELS_KEY),
    );
  } catch {
    return new Set();
  }
}

function writeStored(ids: Set<string>) {
  if (typeof window === "undefined") return;
  try {
    const value = serializeDisabledModels(ids);
    if (value === null) window.localStorage.removeItem(DISABLED_MODELS_KEY);
    else window.localStorage.setItem(DISABLED_MODELS_KEY, value);
  } catch {
    // Storage disabled or full — the preference just doesn't persist.
  }
}

/**
 * The disabled-model preference: `disabled` hydrates on mount (the first
 * frame reads "nothing disabled" — consumers tolerate that, same as every
 * other localStorage preference here), `setModelEnabled` flips one id.
 */
export function useDisabledModels() {
  const [disabled, setDisabled] = useState<Set<string>>(new Set());
  useEffect(() => {
    setDisabled(readStored());
  }, []);

  const setModelEnabled = useCallback((id: string, enabled: boolean) => {
    setDisabled((previous) => {
      const next = new Set(previous);
      if (enabled) next.delete(id);
      else next.add(id);
      writeStored(next);
      return next;
    });
  }, []);

  return { disabled, setModelEnabled };
}

/**
 * The browser-local default model for NEW chats — the web analogue of the
 * TUI's ctrl+g / models.yaml global default. HONEST SCOPE: it is a Studio
 * preference in this browser only; the daemon's own default (`--default-
 * model`, the model router) is untouched, and other clients never see it. A
 * draft whose picker was left untouched resolves to this model when the
 * daemon's inventory lists it; when it does not (the provider went away, the
 * model was renamed), the chat falls back to the daemon default with a
 * warning and the preference is left as-is (the TUI leaves models.yaml
 * intact too) so it applies again once the model is back.
 */
const DEFAULT_MODEL_KEY = "mecatl-studio.default-model";

export interface DefaultModel {
  modelId: string;
  /** The daemon requires provider_id whenever model_id rides a create. */
  providerId: string;
}

/** Parses the stored JSON; anything but `{modelId, providerId}` of non-empty
 *  strings degrades to null (no default). */
export function parseDefaultModel(raw: string | null): DefaultModel | null {
  if (!raw) return null;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null) return null;
    const { modelId, providerId } = parsed as Record<string, unknown>;
    if (typeof modelId !== "string" || modelId === "") return null;
    if (typeof providerId !== "string" || providerId === "") return null;
    return { modelId, providerId };
  } catch {
    return null;
  }
}

/** Serializes the default for storage; null clears the key. */
export function serializeDefaultModel(
  value: DefaultModel | null,
): string | null {
  if (!value?.modelId || !value.providerId) return null;
  return JSON.stringify({
    modelId: value.modelId,
    providerId: value.providerId,
  });
}

// The composer picker, the chat workspace (which applies the default on the
// first send) and the provider page all mount at once and must agree, so the
// instances share one store (the useShowToolCalls pattern) rather than each
// hydrating its own copy. The snapshot is cached by raw string so an
// unchanged value keeps its identity (useSyncExternalStore requires that).
const defaultModelListeners = new Set<() => void>();
let cachedDefaultRaw: string | null | undefined;
let cachedDefault: DefaultModel | null = null;

function subscribeDefaultModel(callback: () => void): () => void {
  defaultModelListeners.add(callback);
  return () => defaultModelListeners.delete(callback);
}

function readDefaultModel(): DefaultModel | null {
  let raw: string | null = null;
  try {
    raw = window.localStorage.getItem(DEFAULT_MODEL_KEY);
  } catch {
    raw = null;
  }
  if (raw !== cachedDefaultRaw) {
    cachedDefaultRaw = raw;
    cachedDefault = parseDefaultModel(raw);
  }
  return cachedDefault;
}

function writeDefaultModel(value: DefaultModel | null) {
  try {
    const raw = serializeDefaultModel(value);
    if (raw === null) window.localStorage.removeItem(DEFAULT_MODEL_KEY);
    else window.localStorage.setItem(DEFAULT_MODEL_KEY, raw);
  } catch {
    // Storage disabled or full — the preference just doesn't persist.
  }
  for (const fn of defaultModelListeners) fn();
}

/**
 * The default-model preference. `defaultModel` is null when none is set (and
 * during SSR — the client re-reads after hydration); `setDefaultModel` and
 * `clearDefaultModel` write through to every mounted instance.
 */
export function useDefaultModel() {
  const defaultModel = useSyncExternalStore(
    subscribeDefaultModel,
    readDefaultModel,
    () => null,
  );
  const setDefaultModel = useCallback((value: DefaultModel) => {
    writeDefaultModel(value);
  }, []);
  const clearDefaultModel = useCallback(() => {
    writeDefaultModel(null);
  }, []);
  return { defaultModel, setDefaultModel, clearDefaultModel };
}
