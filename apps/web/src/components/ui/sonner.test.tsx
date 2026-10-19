// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, cleanup, render, screen } from "@testing-library/react";
import { toast } from "sonner";
import { afterEach, describe, expect, it } from "vitest";
import { appearanceStore } from "../../lib/theme";
import { Toaster } from "./sonner";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(() => {
  act(() => toast.dismiss());
  cleanup();
  appearanceStore.setTheme("system");
});

describe("Toaster", () => {
  it("shows a toast when toast() is called", async () => {
    render(<Toaster />);
    act(() => {
      toast("Saved schedule");
    });
    expect(await screen.findByText("Saved schedule")).toBeTruthy();
  });

  it("follows Studio's effective theme", async () => {
    appearanceStore.setTheme("dark");
    render(<Toaster />);
    act(() => {
      toast("Dark toast");
    });
    await screen.findByText("Dark toast");
    const toaster = document.querySelector("[data-sonner-toaster]");
    expect(toaster?.getAttribute("data-sonner-theme")).toBe("dark");
    act(() => appearanceStore.setTheme("light"));
    expect(toaster?.getAttribute("data-sonner-theme")).toBe("light");
  });
});
