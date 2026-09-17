import { describe, expect, it } from "vitest";
import { SAMPLE_STATUS_FACTS, type StatusFacts } from "./facts";
import {
  formatClock,
  parseStatusTemplate,
  renderedIsBlank,
  renderStatusSegments,
  renderStatusText,
  templateUsesClock,
} from "./template";

/**
 * The status-line template grammar: `{{fact}}` substitution with human
 * formatting, `|raw` for exact values, a formatted clock/date from an
 * injected instant, the two shipped components as their own segments, a
 * visible literal for an unknown key — and NO markup path, so a template
 * (or a daemon value) can never create an element.
 */

const NOW = new Date(2026, 0, 5, 9, 41, 7);

describe("parseStatusTemplate", () => {
  it("splits literal text from placeholders and keeps modifiers", () => {
    expect(
      parseStatusTemplate(
        "model {{model}} · {{input_tokens|raw}} {{ clock | HH:mm }}",
      ),
    ).toEqual([
      { kind: "text", text: "model " },
      { kind: "fact", key: "model", raw: false },
      { kind: "text", text: " · " },
      { kind: "fact", key: "input_tokens", raw: true },
      { kind: "text", text: " " },
      { kind: "fact", key: "clock", raw: false, format: "HH:mm" },
    ]);
  });

  it("yields the two shipped components as their own segments", () => {
    expect(parseStatusTemplate("{{context_meter}}")).toEqual([
      { kind: "meter" },
    ]);
    expect(parseStatusTemplate("{{context_bar}}")).toEqual([{ kind: "bar" }]);
  });

  it("treats anything that is not a placeholder as text, braces included", () => {
    expect(parseStatusTemplate("{ not } {{ }} {{9x}} plain")).toEqual([
      { kind: "text", text: "{ not } {{ }} {{9x}} plain" },
    ]);
    expect(parseStatusTemplate("")).toEqual([]);
  });
});

describe("renderStatusText", () => {
  it("substitutes a fact", () => {
    expect(renderStatusText("model {{model}}", SAMPLE_STATUS_FACTS)).toBe(
      "model gpt-5.1",
    );
  });

  it("formats tokens for people by default and exactly with |raw", () => {
    expect(
      renderStatusText(
        "{{input_tokens}} {{input_tokens|raw}} {{total_tokens}} {{context_window}} {{context_window|raw}}",
        SAMPLE_STATUS_FACTS,
      ),
    ).toBe("312.5k 312500 330.7k 200.0k 200000");
  });

  it("renders the context percentage as ~N% and empty when it cannot be honest", () => {
    expect(
      renderStatusText(
        "{{context_percent}}|{{context_percent|raw}}",
        SAMPLE_STATUS_FACTS,
      ),
    ).toBe("~42%|42");
    const noWindow: StatusFacts = { ...SAMPLE_STATUS_FACTS, contextWindow: 0 };
    expect(renderStatusText("{{context_percent}}", noWindow)).toBe("");
    const nothingCounted: StatusFacts = {
      ...SAMPLE_STATUS_FACTS,
      contextUsed: 0,
      contextOccupancy: 0,
    };
    expect(renderStatusText("{{context_percent}}", nothingCounted)).toBe("");
    expect(renderStatusText("{{context_used}}", nothingCounted)).toBe("");
  });

  it("labels mode, agent state, effort and server for people; ids with |raw", () => {
    expect(
      renderStatusText(
        "{{mode}}/{{mode|raw}} {{agent_state}}/{{agent_state|raw}} {{effort}}/{{effort|raw}} {{server}}/{{server|raw}}",
        SAMPLE_STATUS_FACTS,
      ),
    ).toBe("Plan/plan working/streaming High/high managed daemon/managed");
    expect(
      renderStatusText("{{agent_state}}", {
        ...SAMPLE_STATUS_FACTS,
        agentState: "awaiting",
      }),
    ).toBe("awaiting approval");
  });

  it("renders the cache hit rate off input tokens and empty with none", () => {
    expect(renderStatusText("{{cached_percent}}", SAMPLE_STATUS_FACTS)).toBe(
      "48%",
    );
    expect(
      renderStatusText("{{cached_percent}}", {
        ...SAMPLE_STATUS_FACTS,
        inputTokens: 0,
      }),
    ).toBe("");
  });

  it("shows an unknown key as a visible literal instead of dropping it", () => {
    expect(renderStatusText("a {{nope}} b", SAMPLE_STATUS_FACTS)).toBe(
      "a ⟨nope?⟩ b",
    );
  });

  it("renders the clock and date deterministically from the injected instant", () => {
    expect(
      renderStatusText(
        "{{clock|HH:mm:ss}} {{clock}} {{date}} {{date|ddd MMM DD YYYY}} {{clock|h:mm a}}",
        SAMPLE_STATUS_FACTS,
        { now: NOW },
      ),
    ).toBe("09:41:07 09:41 2026-01-05 Mon Jan 05 2026 9:41 AM");
    expect(renderStatusText("{{clock}}", SAMPLE_STATUS_FACTS)).toBe("");
  });

  it("stands the meter in as its text label", () => {
    expect(renderStatusText("{{context_meter}}", SAMPLE_STATUS_FACTS)).toBe(
      "84.0k / 200.0k · 42%",
    );
    expect(
      renderStatusText("{{context_bar}}", {
        ...SAMPLE_STATUS_FACTS,
        contextWindow: 0,
      }),
    ).toBe("ctx 84.0k");
  });

  it("keeps markup literal — there is no HTML path", () => {
    expect(renderStatusText("<b>{{model}}</b>", SAMPLE_STATUS_FACTS)).toBe(
      "<b>gpt-5.1</b>",
    );
    expect(
      renderStatusText("{{session_title}}", {
        ...SAMPLE_STATUS_FACTS,
        sessionTitle: "<img src=x onerror=alert(1)>",
      }),
    ).toBe("<img src=x onerror=alert(1)>");
  });
});

describe("renderStatusSegments", () => {
  it("merges adjacent text runs and keeps components separate", () => {
    expect(
      renderStatusSegments(
        parseStatusTemplate("a {{model}} b {{context_meter}} {{queued}}"),
        SAMPLE_STATUS_FACTS,
      ),
    ).toEqual([
      { kind: "text", text: "a gpt-5.1 b " },
      { kind: "meter" },
      { kind: "text", text: " 2" },
    ]);
  });
});

describe("renderedIsBlank", () => {
  it("is blank for whitespace text and for a meter with nothing counted", () => {
    const empty: StatusFacts = {
      ...SAMPLE_STATUS_FACTS,
      contextUsed: 0,
      contextOccupancy: 0,
    };
    expect(
      renderedIsBlank([{ kind: "text", text: "  " }], SAMPLE_STATUS_FACTS),
    ).toBe(true);
    expect(renderedIsBlank([{ kind: "meter" }], empty)).toBe(true);
    expect(renderedIsBlank([{ kind: "meter" }], SAMPLE_STATUS_FACTS)).toBe(
      false,
    );
    expect(renderedIsBlank([{ kind: "text", text: "x" }], empty)).toBe(false);
    expect(renderedIsBlank([], SAMPLE_STATUS_FACTS)).toBe(true);
  });
});

describe("templateUsesClock", () => {
  it("is true only when the clock or date is rendered", () => {
    expect(templateUsesClock("{{clock}}")).toBe(true);
    expect(templateUsesClock("{{date|YYYY}}")).toBe(true);
    expect(templateUsesClock("{{model}} clock")).toBe(false);
    expect(templateUsesClock("")).toBe(false);
  });
});

describe("formatClock", () => {
  it("replaces tokens longest-first and leaves other text alone", () => {
    expect(formatClock(NOW, "HH:mm:ss")).toBe("09:41:07");
    expect(formatClock(new Date(2026, 0, 5, 0, 5), "h:mm a")).toBe("12:05 AM");
    expect(formatClock(new Date(2026, 11, 25, 13, 0), "H hh a")).toBe(
      "13 01 PM",
    );
    expect(formatClock(NOW, "YYYY/MM/DD -")).toBe("2026/01/05 -");
  });
});
