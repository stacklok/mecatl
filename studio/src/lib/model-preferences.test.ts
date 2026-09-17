import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { memoryStorage } from "@/test/memory-storage";
import {
  parseDefaultModel,
  parseDisabledModels,
  serializeDefaultModel,
  serializeDisabledModels,
  useDefaultModel,
} from "./model-preferences";

/**
 * The default-model preference (the ctrl+g / models.yaml analogue) is
 * browser-local storage too: the codec degrades garbage to "no default",
 * never a half-record, and the hook's instances share one store so the
 * picker, the workspace and the provider page can never disagree.
 */
describe("default-model codec", () => {
  it("round-trips a {modelId, providerId} pair", () => {
    const stored = serializeDefaultModel({
      modelId: "gpt-5",
      providerId: "openai",
    });
    expect(stored).toBe('{"modelId":"gpt-5","providerId":"openai"}');
    expect(parseDefaultModel(stored)).toEqual({
      modelId: "gpt-5",
      providerId: "openai",
    });
  });

  it("serializes null or an incomplete pair as null (remove the key)", () => {
    expect(serializeDefaultModel(null)).toBeNull();
    expect(serializeDefaultModel({ modelId: "", providerId: "p" })).toBeNull();
    expect(serializeDefaultModel({ modelId: "m", providerId: "" })).toBeNull();
  });

  it("degrades malformed JSON and a missing provider to null", () => {
    for (const raw of [
      null,
      "",
      "not json",
      "[]",
      '"gpt-5"',
      '{"modelId":"gpt-5"}',
      '{"modelId":"gpt-5","providerId":""}',
      '{"modelId":3,"providerId":"openai"}',
      '{"providerId":"openai"}',
    ]) {
      expect(parseDefaultModel(raw), String(raw)).toBeNull();
    }
  });

  it("drops extra fields on parse", () => {
    expect(
      parseDefaultModel('{"modelId":"m","providerId":"p","label":"ignored"}'),
    ).toEqual({ modelId: "m", providerId: "p" });
  });
});

describe("useDefaultModel", () => {
  // jsdom exposes no localStorage here; the shared in-memory stub stands in
  // (the global afterEach unstubs it, so every test starts empty).
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("starts empty, persists a set, and shares the value across instances", () => {
    const a = renderHook(() => useDefaultModel());
    const b = renderHook(() => useDefaultModel());
    expect(a.result.current.defaultModel).toBeNull();

    act(() => {
      a.result.current.setDefaultModel({ modelId: "m1", providerId: "p" });
    });
    expect(a.result.current.defaultModel).toEqual({
      modelId: "m1",
      providerId: "p",
    });
    // The second instance follows without remounting (shared store).
    expect(b.result.current.defaultModel).toEqual({
      modelId: "m1",
      providerId: "p",
    });
    expect(window.localStorage.getItem("mecatl-studio.default-model")).toBe(
      '{"modelId":"m1","providerId":"p"}',
    );

    act(() => {
      b.result.current.clearDefaultModel();
    });
    expect(a.result.current.defaultModel).toBeNull();
    expect(
      window.localStorage.getItem("mecatl-studio.default-model"),
    ).toBeNull();
  });

  it("reads a stored default on mount and keeps its identity across renders", () => {
    window.localStorage.setItem(
      "mecatl-studio.default-model",
      '{"modelId":"m2","providerId":"p"}',
    );
    const { result, rerender } = renderHook(() => useDefaultModel());
    const first = result.current.defaultModel;
    expect(first).toEqual({ modelId: "m2", providerId: "p" });
    rerender();
    expect(result.current.defaultModel).toBe(first);
  });
});

/**
 * The disabled-models preference is browser-local storage, so the pure
 * codec is what carries the correctness: garbage in storage degrades to
 * "nothing disabled" (never a crash, never a partial set of non-strings),
 * and an empty set clears the key instead of storing [] forever.
 */
describe("disabled-models codec", () => {
  it("round-trips a set, sorted for stable storage", () => {
    const stored = serializeDisabledModels(new Set(["z/model", "a/model"]));
    expect(stored).toBe('["a/model","z/model"]');
    expect(parseDisabledModels(stored)).toEqual(
      new Set(["a/model", "z/model"]),
    );
  });

  it("serializes an empty set as null (remove the key)", () => {
    expect(serializeDisabledModels(new Set())).toBeNull();
  });

  it("degrades garbage to an empty set", () => {
    for (const raw of [null, "", "not json", "{}", '"a"', "[1,2]", "[null]"]) {
      expect(parseDisabledModels(raw), String(raw)).toEqual(new Set());
    }
  });

  it("keeps only non-empty string ids from mixed input", () => {
    expect(parseDisabledModels('["ok","",3,null,"also-ok"]')).toEqual(
      new Set(["ok", "also-ok"]),
    );
  });
});
