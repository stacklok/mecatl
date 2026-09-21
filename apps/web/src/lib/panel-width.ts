// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useState } from "react";

/**
 * The resizable panels, each with its own persisted width. The chat list
 * keeps the original key so a width saved before the panels were separated
 * still applies to it. The content preview key also covers a side thread,
 * which opens in the same slot.
 */
export const panelWidthStorageKeys = {
  chatList: "studio.chat.panelWidth",
  contentPreview: "studio.chat.contentPanelWidth",
} as const;

export type ResizablePanel = keyof typeof panelWidthStorageKeys;

const changedEvent = "studio:panel-width-changed";
export const defaultPanelWidth = 256;
export const minPanelWidth = 220;
export const maxPanelWidth = 420;

export function clampPanelWidth(value: number) {
  return Math.round(Math.min(maxPanelWidth, Math.max(minPanelWidth, value)));
}

/** The width stored for `panel`, or the default when nothing usable is stored. */
export function storedPanelWidth(storage: Pick<Storage, "getItem">, panel: ResizablePanel) {
  const stored = Number.parseFloat(storage.getItem(panelWidthStorageKeys[panel]) ?? "");
  return Number.isFinite(stored) ? clampPanelWidth(stored) : defaultPanelWidth;
}

export function usePanelWidth(panel: ResizablePanel) {
  const [value, setValueState] = useState(() => readPanelWidth(panel));
  useEffect(() => {
    const synchronize = () => setValueState(readPanelWidth(panel));
    window.addEventListener(changedEvent, synchronize);
    window.addEventListener("storage", synchronize);
    return () => {
      window.removeEventListener(changedEvent, synchronize);
      window.removeEventListener("storage", synchronize);
    };
  }, [panel]);

  const setValue = useCallback(
    (next: number) => {
      const value = clampPanelWidth(next);
      const storageKey = panelWidthStorageKeys[panel];
      setValueState(value);
      try {
        if (value === defaultPanelWidth) window.localStorage.removeItem(storageKey);
        else window.localStorage.setItem(storageKey, String(value));
      } catch {
        // Resizing still works for this page if browser storage is unavailable.
      }
      window.dispatchEvent(new Event(changedEvent));
    },
    [panel],
  );
  return { setValue, value };
}

function readPanelWidth(panel: ResizablePanel) {
  try {
    return storedPanelWidth(window.localStorage, panel);
  } catch {
    return defaultPanelWidth;
  }
}
