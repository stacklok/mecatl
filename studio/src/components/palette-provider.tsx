"use client";

import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useLayoutEffect,
  useSyncExternalStore,
} from "react";
import { useCustomPaletteCatalogue } from "@/lib/custom-palettes";
import {
  applyPaletteAttribute,
  DEFAULT_PALETTE_ID,
  isCustomPaletteId,
  isKnownPalette,
  PALETTE_STORAGE_KEY,
  type PaletteDef,
  readStoredPalette,
  resolvePaletteId,
  writeStoredPalette,
} from "@/lib/palettes";

/** The deployment default (`BRAND_PALETTE`), threaded from the root layout. */
const PaletteDefaultContext = createContext(DEFAULT_PALETTE_ID);

const listeners = new Set<() => void>();

function subscribePalette(callback: () => void): () => void {
  listeners.add(callback);
  // Another tab changing the preference: the `storage` event names the key
  // (null = a wholesale clear), so the attribute follows across tabs too.
  const onStorage = (event: StorageEvent) => {
    if (event.key === null || event.key === PALETTE_STORAGE_KEY) callback();
  };
  window.addEventListener("storage", onStorage);
  return () => {
    listeners.delete(callback);
    window.removeEventListener("storage", onStorage);
  };
}

/**
 * Resolves the stored id against the MERGED catalogue (built-in + operator +
 * user palettes). While the operator list is still loading, a well-formed
 * custom id is kept rather than resolved to the fallback: the boot script
 * already put it on <html>, and flipping to the deployment default for the
 * few milliseconds of the fetch would flash a palette the user did not pick.
 * Once the fetch settled, a custom id no catalogue entry backs (a removed
 * palette) resolves to the default exactly like an unknown built-in id.
 */
function resolveAgainstCatalogue(
  stored: string | null,
  fallback: string,
  catalogue: readonly PaletteDef[],
  operatorPending: boolean,
): string {
  if (
    operatorPending &&
    isCustomPaletteId(stored) &&
    !isKnownPalette(stored, catalogue)
  ) {
    return stored;
  }
  return resolvePaletteId(stored, fallback, catalogue);
}

/**
 * The palette preference: a GLOBAL browser-local choice (the picker in
 * Personalize and the provider that applies it both mount at once, so
 * instances sync through a shared store — the `useShowToolCalls` idiom).
 * An unknown or removed stored id resolves to the deployment default; the
 * SSR snapshot is that default, which is exactly what the boot script paints,
 * so hydration never disagrees with the DOM. `catalogue` is the merged list
 * (built-in + custom) every picker should render.
 */
export function usePalette() {
  const defaultPalette = useContext(PaletteDefaultContext);
  const { catalogue, operatorState } = useCustomPaletteCatalogue();
  const operatorPending =
    operatorState === "idle" || operatorState === "loading";
  const palette = useSyncExternalStore(
    subscribePalette,
    () =>
      resolveAgainstCatalogue(
        readStoredPalette(),
        defaultPalette,
        catalogue,
        operatorPending,
      ),
    () => defaultPalette,
  );

  const setPalette = useCallback(
    (next: string) => {
      const id = resolvePaletteId(next, defaultPalette, catalogue);
      // Store nothing while the choice equals the deployment default, so a
      // later change of `BRAND_PALETTE` reaches browsers that never chose;
      // an explicit different choice (the shipped default included) sticks.
      writeStoredPalette(id === defaultPalette ? null : id);
      applyPaletteAttribute(id);
      for (const fn of listeners) fn();
    },
    [defaultPalette, catalogue],
  );

  return { palette, setPalette, defaultPalette, catalogue };
}

/**
 * Keeps `data-palette` on `<html>` in step with the resolved preference. The
 * boot script already set it before first paint; this re-applies after React
 * clears root attributes on the dev Strict Mode remount, and follows a change
 * made in another tab. Layout-timed so it lands before paint.
 */
function PaletteInit() {
  const { palette } = usePalette();
  useLayoutEffect(() => {
    applyPaletteAttribute(palette);
  }, [palette]);
  return null;
}

export function PaletteProvider({
  defaultPalette = DEFAULT_PALETTE_ID,
  children,
}: {
  defaultPalette?: string;
  children: ReactNode;
}) {
  return (
    <PaletteDefaultContext.Provider value={defaultPalette}>
      <PaletteInit />
      {children}
    </PaletteDefaultContext.Provider>
  );
}
