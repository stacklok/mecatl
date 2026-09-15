import { describe, expect, it } from "vitest";
import {
  parseDisabledModels,
  serializeDisabledModels,
} from "./model-preferences";

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
