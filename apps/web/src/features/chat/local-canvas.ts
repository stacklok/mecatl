// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useState } from "react";

const canvasKeyPrefix = "studio.chat.canvas.";

export function useLocalCanvas(sessionId: string) {
  const [value, setValueState] = useState(() => readCanvas(sessionId));
  useEffect(() => setValueState(readCanvas(sessionId)), [sessionId]);

  const setValue = useCallback(
    (next: string) => {
      setValueState(next);
      try {
        if (next) window.localStorage.setItem(storageKey(sessionId), next);
        else window.localStorage.removeItem(storageKey(sessionId));
      } catch {
        // The editor remains useful in memory when browser storage is unavailable.
      }
    },
    [sessionId],
  );

  return { setValue, value };
}

export function appendCanvasQuote(canvas: string, selection: string) {
  const quote = selection
    .trim()
    .split("\n")
    .map((line) => `> ${line}`)
    .join("\n");
  return canvas.trimEnd() ? `${canvas.trimEnd()}\n\n${quote}\n` : `${quote}\n`;
}

function readCanvas(sessionId: string) {
  try {
    return window.localStorage.getItem(storageKey(sessionId)) ?? "";
  } catch {
    return "";
  }
}

function storageKey(sessionId: string) {
  return `${canvasKeyPrefix}${encodeURIComponent(sessionId || "draft")}`;
}
