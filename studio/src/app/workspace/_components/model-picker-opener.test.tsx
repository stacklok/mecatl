import { render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  ModelPickerOpener,
  registerModelPickerOpener,
  requestOpenModelPicker,
  resetModelPickerOpeners,
} from "./model-picker-opener";

/**
 * The `/models` / `/effort` opener seam: the composer registers its desktop
 * dropdown and mobile sheet (both always mounted, one CSS-hidden) and a
 * request opens the one the viewport shows, falling back to whichever is
 * registered; false — so the caller can say so — when none is.
 */

function stubViewport(mobile: boolean) {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) => ({
      matches: query === "(max-width: 499px)" ? mobile : false,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    })),
  );
}

beforeEach(() => {
  resetModelPickerOpeners();
  stubViewport(false);
});

afterEach(() => {
  vi.unstubAllGlobals();
  resetModelPickerOpeners();
});

describe("requestOpenModelPicker", () => {
  it("is false with no composer registered", () => {
    expect(requestOpenModelPicker()).toBe(false);
  });

  it("opens the desktop picker on a wide viewport and the sheet on a phone", () => {
    const desktop = vi.fn();
    const mobile = vi.fn();
    registerModelPickerOpener("desktop", desktop);
    registerModelPickerOpener("mobile", mobile);
    expect(requestOpenModelPicker()).toBe(true);
    expect(desktop).toHaveBeenCalledTimes(1);
    expect(mobile).not.toHaveBeenCalled();

    stubViewport(true);
    expect(requestOpenModelPicker()).toBe(true);
    expect(mobile).toHaveBeenCalledTimes(1);
    expect(desktop).toHaveBeenCalledTimes(1);
  });

  it("falls back to whichever surface is registered", () => {
    const mobile = vi.fn();
    registerModelPickerOpener("mobile", mobile);
    expect(requestOpenModelPicker()).toBe(true);
    expect(mobile).toHaveBeenCalledTimes(1);
  });

  it("unregisters only the opener that was registered", () => {
    const first = vi.fn();
    const second = vi.fn();
    const unregisterFirst = registerModelPickerOpener("desktop", first);
    registerModelPickerOpener("desktop", second);
    // The stale unregister must not evict the newer registration.
    unregisterFirst();
    expect(requestOpenModelPicker()).toBe(true);
    expect(second).toHaveBeenCalledTimes(1);
    expect(first).not.toHaveBeenCalled();
  });
});

describe("ModelPickerOpener", () => {
  it("registers while mounted, reads the latest onOpen, and unregisters on unmount", () => {
    const first = vi.fn();
    const view = render(<ModelPickerOpener surface="desktop" onOpen={first} />);
    expect(requestOpenModelPicker()).toBe(true);
    expect(first).toHaveBeenCalledTimes(1);

    const second = vi.fn();
    view.rerender(<ModelPickerOpener surface="desktop" onOpen={second} />);
    expect(requestOpenModelPicker()).toBe(true);
    expect(second).toHaveBeenCalledTimes(1);
    expect(first).toHaveBeenCalledTimes(1);

    view.unmount();
    expect(requestOpenModelPicker()).toBe(false);
  });
});
