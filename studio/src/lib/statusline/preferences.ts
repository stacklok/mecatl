"use client";

/**
 * The status-line preference — mecatui's client-owned
 * `status_customization.templates` + `interval`, for the browser. Header and
 * footer each carry three width variants (full / compact / minimal, picked by
 * container queries, like the TUI's richest-that-fits selection) plus one
 * refresh interval that only matters to `{{clock}}`/`{{date}}`.
 *
 * Stored in this browser's localStorage as strict JSON (a per-person UI
 * preference, the same tier as the keymap — not an operator `settings.yaml`
 * knob). Reading is fail-safe: unknown keys are dropped, an over-long
 * template falls back to its default, the interval clamps, and anything
 * structurally wrong yields the defaults, so a corrupt value can never break
 * the chat. `validateStatusLinePreferences` is the strict twin the Settings
 * page's JSON import uses to REPORT those problems instead of hiding them —
 * the export/import pair is how the preference travels between browsers.
 */

import { useCallback, useSyncExternalStore } from "react";

export const STATUS_LINE_KEY = "mecatl-studio.status-line";

export type StatusSurface = "header" | "footer";
export type StatusVariant = "full" | "compact" | "minimal";

const STATUS_SURFACES: readonly StatusSurface[] = ["header", "footer"];
export const STATUS_VARIANTS: readonly StatusVariant[] = [
  "full",
  "compact",
  "minimal",
];

export interface SurfaceTemplates {
  full: string;
  compact: string;
  minimal: string;
}

export interface StatusLinePreferences {
  header: SurfaceTemplates;
  footer: SurfaceTemplates;
  /** How often `{{clock}}`/`{{date}}` re-render, in seconds (1..3600). */
  intervalSeconds: number;
}

const STATUS_TEMPLATE_MAX_CHARS = 500;
const STATUS_INTERVAL_MIN_SECONDS = 1;
const STATUS_INTERVAL_MAX_SECONDS = 3600;
const STATUS_INTERVAL_DEFAULT_SECONDS = 60;

/**
 * The defaults reproduce today's chat exactly: an empty header lane, and a
 * footer that IS the shipped context meter at every width (`{{context_meter}}`
 * renders the same component the footer always showed), so nothing changes
 * until someone customises it. The minimal variant keeps just the bar.
 */
export const DEFAULT_STATUS_LINE: StatusLinePreferences = {
  header: { full: "", compact: "", minimal: "" },
  footer: {
    full: "{{context_meter}}",
    compact: "{{context_meter}}",
    minimal: "{{context_bar}}",
  },
  intervalSeconds: STATUS_INTERVAL_DEFAULT_SECONDS,
};

function clampStatusInterval(seconds: number): number {
  if (!Number.isFinite(seconds)) return STATUS_INTERVAL_DEFAULT_SECONDS;
  return Math.min(
    STATUS_INTERVAL_MAX_SECONDS,
    Math.max(STATUS_INTERVAL_MIN_SECONDS, Math.round(seconds)),
  );
}

type Problem = string;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Fold one surface's stored value onto its defaults, collecting problems:
 * unknown variant keys are dropped, a non-string or over-long template keeps
 * the default, a non-object surface is ignored wholesale.
 */
function readSurface(
  name: StatusSurface,
  value: unknown,
  problems: Problem[],
): SurfaceTemplates {
  const out: SurfaceTemplates = { ...DEFAULT_STATUS_LINE[name] };
  if (value === undefined) return out;
  if (!isRecord(value)) {
    problems.push(`"${name}" must be an object with full/compact/minimal`);
    return out;
  }
  for (const [key, template] of Object.entries(value)) {
    if (!STATUS_VARIANTS.includes(key as StatusVariant)) {
      problems.push(`"${name}.${key}" is not a known variant`);
      continue;
    }
    if (typeof template !== "string") {
      problems.push(`"${name}.${key}" must be a string`);
      continue;
    }
    if (template.length > STATUS_TEMPLATE_MAX_CHARS) {
      problems.push(
        `"${name}.${key}" is longer than ${STATUS_TEMPLATE_MAX_CHARS} characters`,
      );
      continue;
    }
    out[key as StatusVariant] = template;
  }
  return out;
}

/**
 * The shared strict reader: the parsed preferences plus every problem found.
 * Invalid JSON or a non-object root is one problem and yields the defaults.
 */
function readPreferences(json: string | null): {
  prefs: StatusLinePreferences;
  problems: Problem[];
} {
  const problems: Problem[] = [];
  if (json === null || json === "") {
    return { prefs: DEFAULT_STATUS_LINE, problems };
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(json);
  } catch {
    return { prefs: DEFAULT_STATUS_LINE, problems: ["not valid JSON"] };
  }
  if (!isRecord(parsed)) {
    return {
      prefs: DEFAULT_STATUS_LINE,
      problems: ["the top level must be an object"],
    };
  }
  for (const key of Object.keys(parsed)) {
    if (key !== "header" && key !== "footer" && key !== "intervalSeconds") {
      problems.push(`"${key}" is not a known setting`);
    }
  }
  let intervalSeconds = STATUS_INTERVAL_DEFAULT_SECONDS;
  if (parsed.intervalSeconds !== undefined) {
    if (typeof parsed.intervalSeconds !== "number") {
      problems.push('"intervalSeconds" must be a number');
    } else {
      const clamped = clampStatusInterval(parsed.intervalSeconds);
      if (clamped !== parsed.intervalSeconds) {
        problems.push(
          `"intervalSeconds" must be ${STATUS_INTERVAL_MIN_SECONDS}–${STATUS_INTERVAL_MAX_SECONDS} whole seconds (clamped to ${clamped})`,
        );
      }
      intervalSeconds = clamped;
    }
  }
  return {
    prefs: {
      header: readSurface("header", parsed.header, problems),
      footer: readSurface("footer", parsed.footer, problems),
      intervalSeconds,
    },
    problems,
  };
}

/** Fail-safe read: drops what it cannot use, never throws, never leaks a
 *  malformed template into the chat. */
export function parseStatusLinePreferences(
  json: string | null,
): StatusLinePreferences {
  return readPreferences(json).prefs;
}

/** Strict read for the Settings import: the same rules, but every problem is
 *  reported instead of silently repaired. */
export function validateStatusLinePreferences(
  json: string,
): { ok: true; prefs: StatusLinePreferences } | { ok: false; error: string } {
  const { prefs, problems } = readPreferences(json);
  if (problems.length > 0) return { ok: false, error: problems.join("; ") };
  return { ok: true, prefs };
}

export function serializeStatusLinePreferences(
  prefs: StatusLinePreferences,
): string {
  return JSON.stringify(
    {
      header: { ...prefs.header },
      footer: { ...prefs.footer },
      intervalSeconds: prefs.intervalSeconds,
    },
    null,
    2,
  );
}

/** True when anything differs from the shipped defaults. */
export function isDefaultStatusLine(prefs: StatusLinePreferences): boolean {
  return (
    prefs.intervalSeconds === DEFAULT_STATUS_LINE.intervalSeconds &&
    STATUS_SURFACES.every((surface) =>
      STATUS_VARIANTS.every(
        (variant) =>
          prefs[surface][variant] === DEFAULT_STATUS_LINE[surface][variant],
      ),
    )
  );
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

function storage(): Storage | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

function readRaw(): string | null {
  try {
    return storage()?.getItem(STATUS_LINE_KEY) ?? null;
  } catch {
    return null;
  }
}

const listeners = new Set<() => void>();

function notify() {
  for (const fn of listeners) fn();
}

/** Non-hook read (tests, one-shot callers). */
function readStatusLinePreferences(): StatusLinePreferences {
  return parseStatusLinePreferences(readRaw());
}

/** Persist (the defaults remove the key) and notify every mounted instance. */
function writeStatusLinePreferences(prefs: StatusLinePreferences): void {
  try {
    const s = storage();
    if (!s) return;
    if (isDefaultStatusLine(prefs)) s.removeItem(STATUS_LINE_KEY);
    else s.setItem(STATUS_LINE_KEY, serializeStatusLinePreferences(prefs));
  } catch {
    // Storage disabled or full — the preference just doesn't persist.
  } finally {
    notify();
  }
}

// `useSyncExternalStore` needs a referentially stable snapshot while the
// store is unchanged, so the parsed value is cached against the raw string.
let cachedRaw: string | null = null;
let cachedSnapshot: StatusLinePreferences = DEFAULT_STATUS_LINE;

function getSnapshot(): StatusLinePreferences {
  const raw = readRaw();
  if (raw === cachedRaw) return cachedSnapshot;
  cachedRaw = raw;
  cachedSnapshot = parseStatusLinePreferences(raw);
  return cachedSnapshot;
}

function getServerSnapshot(): StatusLinePreferences {
  return DEFAULT_STATUS_LINE;
}

function subscribe(callback: () => void): () => void {
  listeners.add(callback);
  // Another Studio tab editing the status line should reach this one too.
  const onStorage = (e: StorageEvent) => {
    if (e.key === null || e.key === STATUS_LINE_KEY) callback();
  };
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(callback);
    window.removeEventListener("storage", onStorage);
  };
}

/**
 * The status-line preference plus its mutators. Every mounted instance (both
 * chat lanes, the Settings page) shares one store, so an edit lands in the
 * chat as it is typed. SSR renders the defaults and patches up after
 * hydration.
 */
export function useStatusLinePreferences(): {
  prefs: StatusLinePreferences;
  /** Replace one surface's three templates. */
  setSurface: (surface: StatusSurface, templates: SurfaceTemplates) => void;
  /** Replace one template (over-long text is cut to the limit). */
  setTemplate: (
    surface: StatusSurface,
    variant: StatusVariant,
    text: string,
  ) => void;
  setIntervalSeconds: (seconds: number) => void;
  /** Adopt a whole (already validated) preference — the JSON import. */
  importPreferences: (prefs: StatusLinePreferences) => void;
  reset: () => void;
  isDefault: boolean;
} {
  const prefs = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);

  const setSurface = useCallback(
    (surface: StatusSurface, templates: SurfaceTemplates) => {
      const current = readStatusLinePreferences();
      writeStatusLinePreferences({
        ...current,
        [surface]: {
          full: templates.full.slice(0, STATUS_TEMPLATE_MAX_CHARS),
          compact: templates.compact.slice(0, STATUS_TEMPLATE_MAX_CHARS),
          minimal: templates.minimal.slice(0, STATUS_TEMPLATE_MAX_CHARS),
        },
      });
    },
    [],
  );

  const setTemplate = useCallback(
    (surface: StatusSurface, variant: StatusVariant, text: string) => {
      const current = readStatusLinePreferences();
      writeStatusLinePreferences({
        ...current,
        [surface]: {
          ...current[surface],
          [variant]: text.slice(0, STATUS_TEMPLATE_MAX_CHARS),
        },
      });
    },
    [],
  );

  const setIntervalSeconds = useCallback((seconds: number) => {
    const current = readStatusLinePreferences();
    writeStatusLinePreferences({
      ...current,
      intervalSeconds: clampStatusInterval(seconds),
    });
  }, []);

  const importPreferences = useCallback((next: StatusLinePreferences) => {
    writeStatusLinePreferences(next);
  }, []);

  const reset = useCallback(() => {
    writeStatusLinePreferences(DEFAULT_STATUS_LINE);
  }, []);

  return {
    prefs,
    setSurface,
    setTemplate,
    setIntervalSeconds,
    importPreferences,
    reset,
    isDefault: isDefaultStatusLine(prefs),
  };
}
