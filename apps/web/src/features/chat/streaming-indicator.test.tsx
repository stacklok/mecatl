// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { StreamingIndicator } from "./streaming-indicator";

afterEach(cleanup);

// happy-dom replaces the global URL, so resolve the stylesheet by path.
const css = readFileSync(join(import.meta.dirname, "../../styles.css"), "utf8");

describe("StreamingIndicator", () => {
  it("bounces three staggered dots on the shared thinking-bounce keyframe", () => {
    const { container } = render(<StreamingIndicator />);
    const dots = [...container.querySelectorAll('[aria-hidden="true"] > span')] as HTMLElement[];
    expect(dots).toHaveLength(3);
    for (const dot of dots) {
      expect(dot.className).toContain("animate-[thinking-bounce_0.9s_infinite]");
    }
    expect(dots.map((dot) => dot.style.animationDelay)).toEqual(["0ms", "160ms", "320ms"]);
    expect(container.querySelector("style")).toBeNull();
    // The 5px hop the dots used to declare inline now lives in styles.css.
    expect(css).toMatch(
      /@keyframes thinking-bounce \{\s*0%,\s*100% \{\s*transform: translateY\(0\);[\s\S]*?50% \{\s*transform: translateY\(-5px\);/u,
    );
  });

  it("names the phase and counts the elapsed time", () => {
    vi.useFakeTimers();
    render(<StreamingIndicator phaseLabel="Running Read" />);
    expect(screen.getByText("Running Read")).toBeTruthy();
    expect(screen.getByText("0s")).toBeTruthy();
    act(() => vi.advanceTimersByTime(61_000));
    expect(screen.getByText("1m 1s")).toBeTruthy();
  });

  it("falls back to Thinking without a phase", () => {
    render(<StreamingIndicator />);
    expect(screen.getByText("Thinking")).toBeTruthy();
  });
});
