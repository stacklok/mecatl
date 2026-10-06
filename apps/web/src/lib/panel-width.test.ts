// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import {
  clampPanelWidth,
  defaultPanelWidth,
  maxPanelWidth,
  minPanelWidth,
  panelWidthStorageKeys,
  storedPanelWidth,
} from "./panel-width";

function memoryStorage(entries: Record<string, string>): Pick<Storage, "getItem"> {
  return { getItem: (key) => entries[key] ?? null };
}

describe("panel width", () => {
  it("clamps and rounds requested widths", () => {
    expect(clampPanelWidth(100)).toBe(minPanelWidth);
    expect(clampPanelWidth(900)).toBe(maxPanelWidth);
    expect(clampPanelWidth(301.6)).toBe(302);
  });

  it("stores the chat list and content preview widths under separate keys", () => {
    expect(panelWidthStorageKeys.chatList).toBe("studio.chat.panelWidth");
    expect(panelWidthStorageKeys.contentPreview).not.toBe(panelWidthStorageKeys.chatList);
    expect(panelWidthStorageKeys.contentPreview.startsWith("studio.")).toBe(true);

    const storage = memoryStorage({ "studio.chat.panelWidth": "300" });
    expect(storedPanelWidth(storage, "chatList")).toBe(300);
    expect(storedPanelWidth(storage, "contentPreview")).toBe(defaultPanelWidth);
  });
});
