// SPDX-License-Identifier: Apache-2.0

import { useSyncExternalStore } from "react";
import { appearanceStore } from "./theme";

export type Palette = "default" | "aztec" | "mono" | "solar";

export type PaletteDef = {
  id: Palette;
  label: string;
  description: string;
  swatch: string;
};

export const BUILT_IN_PALETTES: readonly PaletteDef[] = Object.freeze([
  { id: "default", label: "Default", description: "Stacklok green.", swatch: "hsl(161 94% 21%)" },
  {
    id: "aztec",
    label: "Aztec",
    description: "Jade, turquoise and gold on obsidian.",
    swatch: "#0f7f6c",
  },
  {
    id: "mono",
    label: "Mono",
    description: "Neutral greys with a blue accent.",
    swatch: "#2563eb",
  },
  { id: "solar", label: "Solar", description: "Warm and light-leaning.", swatch: "#9a7300" },
]);

export function usePalette(): { palette: Palette; setPalette(next: Palette): void } {
  const snapshot = useSyncExternalStore(
    appearanceStore.subscribe,
    appearanceStore.getSnapshot,
    appearanceStore.getServerSnapshot,
  );
  return { palette: snapshot.palette, setPalette: appearanceStore.setPalette };
}
