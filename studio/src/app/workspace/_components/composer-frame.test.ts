import { describe, expect, it } from "vitest";
import {
  COMPOSER_DEFAULT_MIN_ROWS,
  COMPOSER_MAX_ROWS,
  COMPOSER_MIN_ROWS_VAR,
  composerEditorStyle,
  composerFrameClass,
  composerMinRows,
} from "./composer-frame";

/**
 * The composer box's pure layout decisions: which border it wears (one state
 * wins at a time — drag-over > window drag > streaming with a draft > mode
 * tint > plain) and how many rows it rests at (`compact` → 1, an explicit
 * `rows` clamped to the scroll cap, else the TUI's three).
 */

const idle = {
  isDragOver: false,
  isWindowDrag: false,
  isStreaming: false,
  hasText: false,
};

describe("composerFrameClass", () => {
  it("wears the plain border for Manual mode and for a surface with no Mode selector", () => {
    expect(composerFrameClass({ ...idle, mode: "default" })).toBe(
      "border-zinc-300 dark:border-zinc-700",
    );
    expect(composerFrameClass(idle)).toBe(
      "border-zinc-300 dark:border-zinc-700",
    );
  });

  it("tints the box by mode on the TUI's mapping: Plan → info, Accept edits → success", () => {
    const plan = composerFrameClass({ ...idle, mode: "plan" });
    expect(plan).toContain("border-info");
    expect(plan).not.toContain("border-zinc");
    const edits = composerFrameClass({ ...idle, mode: "acceptEdits" });
    expect(edits).toContain("border-success");
    expect(edits).not.toContain("border-zinc");
  });

  it("a draft held while a run streams beats the mode tint (warning)", () => {
    const cls = composerFrameClass({
      ...idle,
      mode: "plan",
      isStreaming: true,
      hasText: true,
    });
    expect(cls).toContain("border-warning");
    expect(cls).not.toContain("border-info");
  });

  it("an empty composer during a stream keeps the mode tint (nothing to steer or queue)", () => {
    expect(
      composerFrameClass({ ...idle, mode: "plan", isStreaming: true }),
    ).toContain("border-info");
  });

  it("a drag anywhere in the window beats streaming", () => {
    const cls = composerFrameClass({
      ...idle,
      mode: "acceptEdits",
      isStreaming: true,
      hasText: true,
      isWindowDrag: true,
    });
    expect(cls).toContain("border-brand/50");
    expect(cls).not.toContain("border-warning");
    expect(cls).not.toContain("border-success");
  });

  it("a file dragged over the box beats everything (the strongest brand frame)", () => {
    const cls = composerFrameClass({
      mode: "acceptEdits",
      isDragOver: true,
      isWindowDrag: true,
      isStreaming: true,
      hasText: true,
    });
    expect(cls).toContain("border-brand ");
    expect(cls).toContain("ring-2");
    expect(cls).not.toContain("border-brand/50");
    expect(cls).not.toContain("border-warning");
    expect(cls).not.toContain("border-success");
  });

  it("yields exactly one border class, so states never blend", () => {
    const states = [
      { ...idle, mode: "plan" as const },
      { ...idle, mode: "acceptEdits" as const },
      { ...idle, mode: "plan" as const, isStreaming: true, hasText: true },
      { ...idle, isWindowDrag: true },
      { ...idle, isDragOver: true },
      idle,
    ];
    for (const state of states) {
      const borders = composerFrameClass(state)
        .split(/\s+/)
        .filter((c) => /^border-(?!zinc)/.test(c) || c === "border-zinc-300");
      expect(borders, JSON.stringify(state)).toHaveLength(1);
    }
  });
});

describe("composerMinRows", () => {
  it("rests at the TUI's three rows when the caller asks for nothing", () => {
    expect(COMPOSER_DEFAULT_MIN_ROWS).toBe(3);
    expect(composerMinRows({})).toBe(3);
    expect(composerMinRows({ compact: false })).toBe(3);
  });

  it("a compact (side-panel) composer is one row, whatever rows says", () => {
    expect(composerMinRows({ compact: true })).toBe(1);
    expect(composerMinRows({ compact: true, rows: 6 })).toBe(1);
  });

  it("honours an explicit rows, clamped to [1, the eight-row scroll cap]", () => {
    expect(COMPOSER_MAX_ROWS).toBe(8);
    expect(composerMinRows({ rows: 1 })).toBe(1);
    expect(composerMinRows({ rows: 5 })).toBe(5);
    expect(composerMinRows({ rows: 5.7 })).toBe(5);
    expect(composerMinRows({ rows: 0 })).toBe(1);
    expect(composerMinRows({ rows: -2 })).toBe(1);
    expect(composerMinRows({ rows: 20 })).toBe(COMPOSER_MAX_ROWS);
    expect(composerMinRows({ rows: Number.NaN })).toBe(3);
  });
});

describe("composerEditorStyle", () => {
  it("sets no inline style when the stylesheet default should stand alone", () => {
    expect(composerEditorStyle({})).toBeUndefined();
    expect(composerEditorStyle({ compact: false })).toBeUndefined();
  });

  it("carries the row count as the custom property the stylesheet reads", () => {
    expect(COMPOSER_MIN_ROWS_VAR).toBe("--composer-min-rows");
    expect(composerEditorStyle({ rows: 5 })).toEqual({
      "--composer-min-rows": 5,
    });
    expect(composerEditorStyle({ compact: true })).toEqual({
      "--composer-min-rows": 1,
    });
    expect(composerEditorStyle({ rows: 40 })).toEqual({
      "--composer-min-rows": COMPOSER_MAX_ROWS,
    });
  });
});
