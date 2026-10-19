// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { useIsTruncated } from "./use-is-truncated";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

/** A detached-from-layout element whose box sizes the test controls. */
function sizedElement(size: { client: number; scroll: number }) {
  const element = document.createElement("span");
  Object.defineProperty(element, "clientWidth", { get: () => size.client });
  Object.defineProperty(element, "scrollWidth", { get: () => size.scroll });
  Object.defineProperty(element, "clientHeight", { get: () => 20 });
  Object.defineProperty(element, "scrollHeight", { get: () => 20 });
  document.body.append(element);
  return element;
}

describe("useIsTruncated", () => {
  it("is false without an element", () => {
    const { result } = renderHook(() => useIsTruncated(null));
    expect(result.current).toBe(false);
  });

  it("reports overflowing text and re-measures when the content changes", async () => {
    const size = { client: 100, scroll: 100 };
    const element = sizedElement(size);
    const { result } = renderHook(() => useIsTruncated(element));
    await waitFor(() => expect(result.current).toBe(false));

    size.scroll = 240;
    act(() => {
      element.textContent = "A label far too long for its column";
    });
    await waitFor(() => expect(result.current).toBe(true));

    size.scroll = 80;
    act(() => {
      element.textContent = "Short";
    });
    await waitFor(() => expect(result.current).toBe(false));
    element.remove();
  });

  it("re-measures on window resize", async () => {
    const size = { client: 100, scroll: 100 };
    const element = sizedElement(size);
    const { result } = renderHook(() => useIsTruncated(element));
    await waitFor(() => expect(result.current).toBe(false));

    size.client = 50;
    act(() => {
      window.dispatchEvent(new Event("resize"));
    });
    await waitFor(() => expect(result.current).toBe(true));
    element.remove();
  });
});
