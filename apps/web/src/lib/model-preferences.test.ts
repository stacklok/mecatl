// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import {
  modelPreferenceId,
  parseDisabledModels,
  serializeDisabledModels,
} from "./model-preferences";

describe("model preferences", () => {
  it("round trips a sorted model set", () => {
    const stored = serializeDisabledModels(new Set(["z", "a"]));
    expect(stored).toBe('["a","z"]');
    expect([...parseDisabledModels(stored)]).toEqual(["a", "z"]);
  });

  it("ignores malformed values", () => {
    expect(parseDisabledModels("not json").size).toBe(0);
    expect(parseDisabledModels('{"model":true}').size).toBe(0);
    expect([...parseDisabledModels('["ok",2,"",null]')]).toEqual(["ok"]);
  });

  it("distinguishes the same model name across providers", () => {
    expect(modelPreferenceId({ id: "flash", providerId: "a" })).not.toBe(
      modelPreferenceId({ id: "flash", providerId: "b" }),
    );
  });
});
