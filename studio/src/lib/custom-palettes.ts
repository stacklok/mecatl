"use client";

import { useCallback, useEffect, useSyncExternalStore } from "react";
import {
  type CustomPalette,
  customPaletteId,
  parsePaletteDocument,
  toPaletteDocument,
} from "@/lib/palette-schema";
import { BUILT_IN_PALETTES, type PaletteDef } from "@/lib/palettes";

/**
 * The custom-palette catalogue: the user's own palettes (this browser's
 * localStorage — the `~/.config/mecatui/themes/` analogue) plus the
 * operator's (`STUDIO_PALETTE_DIR`, read once per page from `/api/palettes`
 * — the `--theme-dir` analogue), merged over the built-ins. Later source
 * wins by id, mecatui's precedence: user > operator > built-in.
 *
 * One module-level store, the `usePalette` idiom: the picker, the
 * Personalize section and the <style> injector all mount at once and must
 * agree, so they share a snapshot and re-render together on every change.
 */

export const CUSTOM_PALETTES_KEY = "mecatl-studio.custom-palettes";
/** How many palettes one browser may keep. */
export const USER_PALETTE_LIMIT = 8;
export const OPERATOR_PALETTES_URL = "/api/palettes";

type OperatorPaletteState = "idle" | "loading" | "ready" | "error";

export interface CustomPaletteSnapshot {
  user: readonly CustomPalette[];
  operator: readonly CustomPalette[];
  operatorState: OperatorPaletteState;
  /** Plain words for the section when `/api/palettes` could not be read. */
  operatorError: string | null;
  /** Built-in + operator + user, the list every picker renders. */
  catalogue: readonly PaletteDef[];
}

export type AddPaletteResult =
  | { ok: true; palette: CustomPalette }
  | { ok: false; error: string };

// ── storage ─────────────────────────────────────────────────────────────────

/**
 * Later entries win by name and count as the newest (delete-then-set moves a
 * repeated name to the end, so the cap below drops the OLDEST entries, never
 * the one just written); the result is capped at USER_PALETTE_LIMIT.
 */
function dedupeByName(palettes: readonly CustomPalette[]): CustomPalette[] {
  const byName = new Map<string, CustomPalette>();
  for (const palette of palettes) {
    byName.delete(palette.name);
    byName.set(palette.name, palette);
  }
  return [...byName.values()].slice(-USER_PALETTE_LIMIT);
}

/**
 * The user's stored palettes: a JSON array of documents, every entry
 * re-validated on read (a hand-edited or stale entry is dropped, never
 * rendered), later duplicates winning.
 */
export function readUserPalettes(): CustomPalette[] {
  if (typeof window === "undefined") return [];
  let raw: string | null = null;
  try {
    raw = window.localStorage.getItem(CUSTOM_PALETTES_KEY);
  } catch {
    return [];
  }
  if (!raw) return [];
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return [];
  }
  if (!Array.isArray(parsed)) return [];
  const palettes: CustomPalette[] = [];
  for (const entry of parsed) {
    const result = parsePaletteDocument(entry, "user");
    if (result.ok) palettes.push(result.palette);
  }
  return dedupeByName(palettes);
}

/** Writes the user's palettes as documents; an empty list removes the key. */
export function writeUserPalettes(palettes: readonly CustomPalette[]) {
  if (typeof window === "undefined") return;
  try {
    if (palettes.length === 0) {
      window.localStorage.removeItem(CUSTOM_PALETTES_KEY);
    } else {
      window.localStorage.setItem(
        CUSTOM_PALETTES_KEY,
        JSON.stringify(palettes.map(toPaletteDocument)),
      );
    }
  } catch {
    // Storage disabled or full — the palette just doesn't persist.
  }
}

// ── catalogue ───────────────────────────────────────────────────────────────

/** The picker's swatch: the palette's accent, or the first surface it sets. */
export function paletteSwatchColor(palette: CustomPalette): string {
  const light = palette.light;
  return (
    light.brand ??
    light["btn-primary"] ??
    light["nav-background"] ??
    light["shell-gradient-start"] ??
    light.background ??
    Object.values(light)[0] ??
    "transparent"
  );
}

export function customPaletteDef(palette: CustomPalette): PaletteDef {
  return {
    id: customPaletteId(palette.name),
    label: palette.label,
    description:
      palette.source === "user"
        ? "Custom palette added in this browser."
        : "Custom palette supplied by the operator.",
    swatch: paletteSwatchColor(palette),
    source: palette.source,
  };
}

/**
 * Built-ins first, then operator, then user palettes; a later entry with the
 * same id replaces the earlier one IN PLACE, so a user palette named like an
 * operator one shadows it (mecatui's later-source-wins) without reordering.
 */
export function mergePaletteCatalogue(
  builtIn: readonly PaletteDef[],
  operator: readonly CustomPalette[],
  user: readonly CustomPalette[],
): PaletteDef[] {
  const merged = new Map<string, PaletteDef>();
  for (const palette of builtIn) merged.set(palette.id, palette);
  for (const palette of [...operator, ...user]) {
    const def = customPaletteDef(palette);
    merged.set(def.id, def);
  }
  return [...merged.values()];
}

// ── the store ───────────────────────────────────────────────────────────────

const SERVER_SNAPSHOT: CustomPaletteSnapshot = {
  user: [],
  operator: [],
  operatorState: "idle",
  operatorError: null,
  catalogue: BUILT_IN_PALETTES,
};

let operator: readonly CustomPalette[] = [];
let operatorState: OperatorPaletteState = "idle";
let operatorError: string | null = null;
let operatorLoad: Promise<void> | null = null;
let snapshot: CustomPaletteSnapshot | null = null;
const listeners = new Set<() => void>();

function compute(): CustomPaletteSnapshot {
  const user = readUserPalettes();
  return {
    user,
    operator,
    operatorState,
    operatorError,
    catalogue: mergePaletteCatalogue(BUILT_IN_PALETTES, operator, user),
  };
}

function getSnapshot(): CustomPaletteSnapshot {
  if (snapshot === null) snapshot = compute();
  return snapshot;
}

function emit() {
  snapshot = compute();
  for (const listener of listeners) listener();
}

function subscribe(callback: () => void): () => void {
  listeners.add(callback);
  // Another tab adding or removing a palette: follow it (null = a clear).
  const onStorage = (event: StorageEvent) => {
    if (event.key === null || event.key === CUSTOM_PALETTES_KEY) emit();
  };
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(callback);
    window.removeEventListener("storage", onStorage);
  };
}

function describeFetchFailure(error: unknown): string {
  if (error instanceof Error && error.message) return error.message;
  return "the request failed";
}

/**
 * Reads the operator's palettes once per page. The route already validated
 * and warned about broken files; the browser still re-validates every entry
 * (same-origin, but one validator on both sides is the whole design).
 * Failure is a plain sentence for the section, never a thrown error.
 */
export function loadOperatorPalettes(): Promise<void> {
  if (operatorLoad) return operatorLoad;
  operatorState = "loading";
  operatorError = null;
  emit();
  operatorLoad = (async () => {
    try {
      const response = await fetch(OPERATOR_PALETTES_URL, {
        cache: "no-store",
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const body = (await response.json()) as { palettes?: unknown };
      const entries = Array.isArray(body?.palettes) ? body.palettes : [];
      const palettes: CustomPalette[] = [];
      for (const entry of entries) {
        const result = parsePaletteDocument(entry, "operator");
        if (result.ok) palettes.push(result.palette);
      }
      operator = palettes;
      operatorState = "ready";
    } catch (error) {
      operator = [];
      operatorState = "error";
      operatorError = `Operator palettes could not be loaded (${describeFetchFailure(error)}).`;
    }
    emit();
  })();
  return operatorLoad;
}

/** Test seam: forgets the operator fetch and the cached snapshot. */
export function resetCustomPalettesForTests() {
  operator = [];
  operatorState = "idle";
  operatorError = null;
  operatorLoad = null;
  snapshot = null;
}

/**
 * Subscribe-only view of the catalogue for consumers that RESOLVE against it
 * (the palette provider) but must not start the operator fetch themselves.
 */
export function useCustomPaletteCatalogue(): CustomPaletteSnapshot {
  return useSyncExternalStore(subscribe, getSnapshot, () => SERVER_SNAPSHOT);
}

/**
 * The custom-palette catalogue plus the user's add/remove. Mounting it
 * starts the one-per-page operator fetch (the <style> injector in
 * ClientProviders is the app-wide instance; the Personalize section is
 * another).
 */
export function useCustomPalettes() {
  const state = useCustomPaletteCatalogue();

  useEffect(() => {
    void loadOperatorPalettes();
  }, []);

  const addUserPalette = useCallback((text: string): AddPaletteResult => {
    const result = parsePaletteDocument(text, "user");
    if (!result.ok) return result;
    const current = readUserPalettes();
    const replacing = current.some((p) => p.name === result.palette.name);
    if (!replacing && current.length >= USER_PALETTE_LIMIT) {
      return {
        ok: false,
        error: `This browser keeps up to ${USER_PALETTE_LIMIT} custom palettes — remove one first.`,
      };
    }
    const next = replacing
      ? current.map((p) =>
          p.name === result.palette.name ? result.palette : p,
        )
      : [...current, result.palette];
    writeUserPalettes(next);
    emit();
    return result;
  }, []);

  const removeUserPalette = useCallback((name: string) => {
    writeUserPalettes(readUserPalettes().filter((p) => p.name !== name));
    emit();
  }, []);

  return { ...state, addUserPalette, removeUserPalette };
}
