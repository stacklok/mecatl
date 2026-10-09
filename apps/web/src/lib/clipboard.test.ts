// SPDX-License-Identifier: Apache-2.0

import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { clipboardFailureMessage, copyToClipboard, writeClipboardText } from "./clipboard";

/**
 * Pins the one copy-with-feedback policy: a page with no Clipboard API says
 * so (and tells the user to copy by hand), a refused write is reported as
 * blocked, and a landed write confirms with the caller's label. The boolean
 * result lets a button flip to "Copied" only when the copy happened.
 */

// The no-isolate rules (src/test-setup.ts) forbid `vi.mock`, so the toasts
// are stubbed on the real module (`restoreMocks` undoes the spies; a stub
// keeps the shared sonner store empty for later files) and the
// Clipboard API is a stubbed `navigator` (`unstubGlobals` undoes it).
function installClipboard(value: unknown) {
  vi.stubGlobal("navigator", { clipboard: value });
}

beforeEach(() => {
  vi.spyOn(toast, "success").mockReturnValue(0);
  vi.spyOn(toast, "error").mockReturnValue(0);
});

describe("copyToClipboard", () => {
  it("reports an absent Clipboard API and asks for a manual copy", async () => {
    installClipboard(undefined);
    await expect(copyToClipboard("session-1", "Session ID")).resolves.toBe(false);
    expect(toast.error).toHaveBeenCalledWith(
      "Clipboard unavailable — select the text and copy it manually",
    );
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("reports a refused write as blocked", async () => {
    installClipboard({
      writeText: vi.fn(() => Promise.reject(new Error("denied"))),
    });
    await expect(copyToClipboard("session-1", "Session ID")).resolves.toBe(false);
    expect(toast.error).toHaveBeenCalledWith("Couldn't copy — clipboard blocked");
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("writes the exact text and confirms with the label", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    installClipboard({ writeText });
    await expect(copyToClipboard("session-1", "Session ID")).resolves.toBe(true);
    expect(writeText).toHaveBeenCalledWith("session-1");
    expect(toast.success).toHaveBeenCalledWith("Session ID copied");
    expect(toast.error).not.toHaveBeenCalled();
  });
});

describe("writeClipboardText", () => {
  it("names the failure without toasting", async () => {
    installClipboard(undefined);
    await expect(writeClipboardText("x")).resolves.toEqual({
      ok: false,
      reason: "unavailable",
    });
    installClipboard({
      writeText: vi.fn(() => Promise.reject(new Error("denied"))),
    });
    await expect(writeClipboardText("x")).resolves.toEqual({
      ok: false,
      reason: "blocked",
    });
    expect(toast.error).not.toHaveBeenCalled();
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("phrases each failure for the user", () => {
    expect(clipboardFailureMessage("unavailable")).toMatch(/copy it manually/);
    expect(clipboardFailureMessage("blocked")).toMatch(/clipboard blocked/);
  });
});
