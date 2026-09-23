// SPDX-License-Identifier: Apache-2.0

import { useCallback, useEffect, useState } from "react";
import { readUserScopedItem, writeUserScopedItem } from "../../lib/account-storage";

const canvasKeyPrefix = "studio.chat.canvas.";

export function useLocalCanvas(sessionId: string) {
  const [value, setValueState] = useState(() => readCanvas(sessionId));
  useEffect(() => setValueState(readCanvas(sessionId)), [sessionId]);

  const setValue = useCallback(
    (next: string) => {
      setValueState(next);
      writeUserScopedItem(storageKey(sessionId), next || null);
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
  return readUserScopedItem(storageKey(sessionId)) ?? "";
}

function storageKey(sessionId: string) {
  return `${canvasKeyPrefix}${encodeURIComponent(sessionId || "draft")}`;
}
