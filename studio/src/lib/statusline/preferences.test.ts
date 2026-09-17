import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { memoryStorage } from "@/test/memory-storage";
import {
  DEFAULT_STATUS_LINE,
  isDefaultStatusLine,
  parseStatusLinePreferences,
  STATUS_LINE_KEY,
  serializeStatusLinePreferences,
  useStatusLinePreferences,
  validateStatusLinePreferences,
} from "./preferences";

/**
 * The status-line preference store: the defaults reproduce today's chat
 * (empty header, the shipped meter in the footer), reading is strict AND
 * fail-safe (unknown keys dropped, over-long templates fall back, the
 * interval clamps, malformed → defaults), the import validator REPORTS the
 * same problems, and the hook round-trips through localStorage with every
 * mounted instance in sync.
 */

const stored = () => {
  const raw = window.localStorage.getItem(STATUS_LINE_KEY);
  return raw ? JSON.parse(raw) : null;
};

describe("parseStatusLinePreferences", () => {
  it("defaults to an empty header and the shipped meter in the footer", () => {
    expect(parseStatusLinePreferences(null)).toEqual(DEFAULT_STATUS_LINE);
    expect(parseStatusLinePreferences("")).toEqual(DEFAULT_STATUS_LINE);
    expect(DEFAULT_STATUS_LINE.header).toEqual({
      full: "",
      compact: "",
      minimal: "",
    });
    expect(DEFAULT_STATUS_LINE.footer.full).toBe("{{context_meter}}");
    expect(DEFAULT_STATUS_LINE.footer.compact).toBe("{{context_meter}}");
    expect(DEFAULT_STATUS_LINE.footer.minimal).toBe("{{context_bar}}");
    expect(DEFAULT_STATUS_LINE.intervalSeconds).toBe(60);
  });

  it("drops unknown keys and keeps the rest", () => {
    const prefs = parseStatusLinePreferences(
      JSON.stringify({
        header: { full: "{{model}}", bogus: "x" },
        zzz: 1,
      }),
    );
    expect(prefs.header).toEqual({
      full: "{{model}}",
      compact: "",
      minimal: "",
    });
    expect(prefs.footer).toEqual(DEFAULT_STATUS_LINE.footer);
    expect(prefs.intervalSeconds).toBe(60);
    expect("bogus" in prefs.header).toBe(false);
  });

  it("falls back to the default for an over-long or non-string template", () => {
    const prefs = parseStatusLinePreferences(
      JSON.stringify({
        footer: { full: "x".repeat(501), compact: 7, minimal: "{{model}}" },
      }),
    );
    expect(prefs.footer.full).toBe("{{context_meter}}");
    expect(prefs.footer.compact).toBe("{{context_meter}}");
    expect(prefs.footer.minimal).toBe("{{model}}");
  });

  it("clamps the interval into 1..3600 whole seconds and defaults a non-number", () => {
    const at = (intervalSeconds: unknown) =>
      parseStatusLinePreferences(JSON.stringify({ intervalSeconds }))
        .intervalSeconds;
    expect(at(0)).toBe(1);
    expect(at(-5)).toBe(1);
    expect(at(99_999)).toBe(3600);
    expect(at(2.6)).toBe(3);
    expect(at("abc")).toBe(60);
    expect(at(120)).toBe(120);
  });

  it("yields the defaults for malformed JSON or a non-object root", () => {
    expect(parseStatusLinePreferences("{not json")).toEqual(
      DEFAULT_STATUS_LINE,
    );
    expect(parseStatusLinePreferences("[1,2]")).toEqual(DEFAULT_STATUS_LINE);
    expect(parseStatusLinePreferences('"str"')).toEqual(DEFAULT_STATUS_LINE);
    expect(
      parseStatusLinePreferences(JSON.stringify({ header: "nope" })),
    ).toEqual(DEFAULT_STATUS_LINE);
  });

  it("round-trips through serialize", () => {
    const prefs = {
      header: { full: "{{clock}}", compact: "", minimal: "" },
      footer: { full: "{{model}}", compact: "{{model}}", minimal: "" },
      intervalSeconds: 5,
    };
    expect(
      parseStatusLinePreferences(serializeStatusLinePreferences(prefs)),
    ).toEqual(prefs);
  });
});

describe("validateStatusLinePreferences", () => {
  it("reports every problem the fail-safe reader would have repaired", () => {
    const result = validateStatusLinePreferences(
      JSON.stringify({
        zzz: 1,
        header: { full: "x".repeat(501), nope: "" },
        intervalSeconds: 0,
      }),
    );
    expect(result.ok).toBe(false);
    if (result.ok) throw new Error("expected problems");
    expect(result.error).toContain('"zzz" is not a known setting');
    expect(result.error).toContain('"header.full" is longer than 500');
    expect(result.error).toContain('"header.nope" is not a known variant');
    expect(result.error).toContain('"intervalSeconds" must be 1–3600');
  });

  it("refuses invalid JSON and accepts a clean document", () => {
    expect(validateStatusLinePreferences("{oops")).toEqual({
      ok: false,
      error: "not valid JSON",
    });
    const result = validateStatusLinePreferences(
      JSON.stringify({ header: { full: "{{model}}" } }),
    );
    expect(result.ok).toBe(true);
    if (!result.ok) throw new Error("expected ok");
    expect(result.prefs.header.full).toBe("{{model}}");
    expect(result.prefs.footer).toEqual(DEFAULT_STATUS_LINE.footer);
  });
});

describe("useStatusLinePreferences", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("reads the defaults and stores nothing until customised", () => {
    const { result } = renderHook(() => useStatusLinePreferences());
    expect(result.current.prefs).toEqual(DEFAULT_STATUS_LINE);
    expect(result.current.isDefault).toBe(true);
    expect(stored()).toBeNull();
  });

  it("persists a template edit, survives a remount and syncs mounted instances", () => {
    const first = renderHook(() => useStatusLinePreferences());
    const second = renderHook(() => useStatusLinePreferences());
    act(() => first.result.current.setTemplate("header", "full", "{{model}}"));
    expect(stored()).toEqual({
      header: { full: "{{model}}", compact: "", minimal: "" },
      footer: DEFAULT_STATUS_LINE.footer,
      intervalSeconds: 60,
    });
    expect(second.result.current.prefs.header.full).toBe("{{model}}");
    expect(second.result.current.isDefault).toBe(false);

    const third = renderHook(() => useStatusLinePreferences());
    expect(third.result.current.prefs.header.full).toBe("{{model}}");
  });

  it("cuts an over-long template to the limit and clamps the interval", () => {
    const { result } = renderHook(() => useStatusLinePreferences());
    act(() => result.current.setTemplate("footer", "full", "y".repeat(600)));
    expect(result.current.prefs.footer.full).toHaveLength(500);
    act(() => result.current.setIntervalSeconds(0));
    expect(result.current.prefs.intervalSeconds).toBe(1);
    act(() => result.current.setIntervalSeconds(1e9));
    expect(result.current.prefs.intervalSeconds).toBe(3600);
  });

  it("replaces a whole surface and adopts an imported document", () => {
    const { result } = renderHook(() => useStatusLinePreferences());
    act(() =>
      result.current.setSurface("footer", {
        full: "a",
        compact: "b",
        minimal: "c",
      }),
    );
    expect(result.current.prefs.footer).toEqual({
      full: "a",
      compact: "b",
      minimal: "c",
    });
    act(() =>
      result.current.importPreferences({
        ...DEFAULT_STATUS_LINE,
        intervalSeconds: 5,
      }),
    );
    expect(result.current.prefs.intervalSeconds).toBe(5);
    expect(result.current.prefs.footer).toEqual(DEFAULT_STATUS_LINE.footer);
  });

  it("reset removes the key, and so does editing back to the defaults", () => {
    const { result } = renderHook(() => useStatusLinePreferences());
    act(() => result.current.setTemplate("header", "full", "{{model}}"));
    expect(stored()).not.toBeNull();
    act(() => result.current.reset());
    expect(stored()).toBeNull();
    expect(result.current.prefs).toEqual(DEFAULT_STATUS_LINE);

    act(() => result.current.setIntervalSeconds(30));
    expect(stored()).not.toBeNull();
    act(() => result.current.setIntervalSeconds(60));
    expect(stored()).toBeNull();
    expect(isDefaultStatusLine(result.current.prefs)).toBe(true);
  });
});
