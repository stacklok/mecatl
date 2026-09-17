import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SAMPLE_STATUS_FACTS, type StatusFacts } from "@/lib/statusline/facts";
import {
  DEFAULT_STATUS_LINE,
  STATUS_LINE_KEY,
  type StatusLinePreferences,
} from "@/lib/statusline/preferences";
import { memoryStorage } from "@/test/memory-storage";
import {
  STATUS_VARIANT_CLASS,
  StatusTemplateText,
  TemplatedStatusLine,
} from "./templated-status-line";

/**
 * The templated status lanes: the footer's default IS the shipped meter
 * (nothing changes until customised), an all-empty surface renders nothing,
 * a meter with nothing counted collapses the lane, the three variants carry
 * the container-query classes that pick the richest that fits, a clock ticks
 * on the interval only when a template uses it, and every substituted value
 * is text — no markup path.
 */

function storePrefs(prefs: StatusLinePreferences) {
  window.localStorage.setItem(STATUS_LINE_KEY, JSON.stringify(prefs));
}

const NOTHING_COUNTED: StatusFacts = {
  ...SAMPLE_STATUS_FACTS,
  contextOccupancy: 0,
  contextUsed: 0,
  inputTokens: 0,
  outputTokens: 0,
  cacheReadTokens: 0,
  cacheWriteTokens: 0,
};

describe("TemplatedStatusLine", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders the footer default as the shipped meter over the facts", () => {
    render(
      <TemplatedStatusLine surface="footer" facts={SAMPLE_STATUS_FACTS} />,
    );
    const lane = screen.getByTestId("status-line-footer");
    // Full + compact are `{{context_meter}}` (model · effort, bar, figures,
    // facets); minimal is `{{context_bar}}` (bar + figures only).
    expect(screen.getAllByText("gpt-5.1 · High")).toHaveLength(2);
    expect(screen.getAllByText("84.0k / 200.0k · 42%")).toHaveLength(3);
    expect(
      screen.getAllByText("↑312.5k ↓18.2k · ⊕4.0k · 48% cached"),
    ).toHaveLength(2);
    for (const variant of ["full", "compact", "minimal"] as const) {
      const span = lane.querySelector(`[data-variant="${variant}"]`);
      expect(span).not.toBeNull();
      for (const cls of STATUS_VARIANT_CLASS[variant].split(" ")) {
        expect(span).toHaveClass(cls);
      }
    }
  });

  it("renders nothing for a surface whose templates are all empty (the header default)", () => {
    const { container } = render(
      <TemplatedStatusLine surface="header" facts={SAMPLE_STATUS_FACTS} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("collapses the footer when the meter has nothing to show yet", () => {
    const { container } = render(
      <TemplatedStatusLine surface="footer" facts={NOTHING_COUNTED} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("renders a custom header template as text, unknown keys as literals, markup literally", () => {
    storePrefs({
      ...DEFAULT_STATUS_LINE,
      header: {
        full: "<b>{{session_title}}</b> · {{mode}} · {{nope}}",
        compact: "{{context_percent}}",
        minimal: "",
      },
    });
    const { container } = render(
      <TemplatedStatusLine
        surface="header"
        facts={{ ...SAMPLE_STATUS_FACTS, sessionTitle: "<i>Fix</i> it" }}
        className="lane"
      />,
    );
    const lane = screen.getByTestId("status-line-header");
    expect(lane).toHaveClass("@container", "lane");
    expect(lane.querySelector('[data-variant="full"]')).toHaveTextContent(
      "<b><i>Fix</i> it</b> · Plan · ⟨nope?⟩",
    );
    expect(lane.querySelector('[data-variant="compact"]')).toHaveTextContent(
      "~42%",
    );
    expect(
      lane.querySelector('[data-variant="minimal"]'),
    ).toBeEmptyDOMElement();
    expect(container.querySelector("b")).toBeNull();
    expect(container.querySelector("i")).toBeNull();
  });

  it("ticks the clock on the preference's interval only when a template uses it", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 0, 5, 9, 41, 7));
    storePrefs({
      ...DEFAULT_STATUS_LINE,
      header: { full: "{{clock|HH:mm:ss}}", compact: "", minimal: "" },
      intervalSeconds: 2,
    });
    render(
      <TemplatedStatusLine surface="header" facts={SAMPLE_STATUS_FACTS} />,
    );
    const full = () =>
      screen
        .getByTestId("status-line-header")
        .querySelector('[data-variant="full"]');
    expect(full()).toHaveTextContent("09:41:07");
    act(() => {
      vi.advanceTimersByTime(1_000);
    });
    expect(full()).toHaveTextContent("09:41:07");
    act(() => {
      vi.advanceTimersByTime(1_000);
    });
    expect(full()).toHaveTextContent("09:41:09");
  });

  it("sets no timer for a surface without a clock", () => {
    vi.useFakeTimers();
    const spy = vi.spyOn(globalThis, "setInterval");
    render(
      <TemplatedStatusLine surface="footer" facts={SAMPLE_STATUS_FACTS} />,
    );
    expect(spy).not.toHaveBeenCalled();
  });
});

describe("StatusTemplateText", () => {
  it("renders one template over facts with the bar as a component", () => {
    const { container } = render(
      <StatusTemplateText
        template="{{model}} {{context_bar}} {{clock}}"
        facts={SAMPLE_STATUS_FACTS}
        now={new Date(2026, 0, 5, 9, 41, 7)}
      />,
    );
    expect(container).toHaveTextContent("gpt-5.1 84.0k / 200.0k · 42% 09:41");
    // The bar itself, not just its figures.
    expect(container.querySelector(".rounded-full.bg-border")).not.toBeNull();
  });
});
