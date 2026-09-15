"use client";

import { useCallback, useEffect, useState } from "react";

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
