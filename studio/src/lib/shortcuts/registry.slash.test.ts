import { describe, expect, it } from "vitest";
import { SHORTCUTS } from "./registry";

/**
 * The `/` row of the Shortcuts page names BOTH palette layers — Studio's
 * built-ins (capability-gated) and the chat's workspace commands — so a
 * reader learns the built-ins exist without opening the composer.
 */
describe("composer.slash", () => {
  it("documents the built-in layer and the workspace layer", () => {
    const def = SHORTCUTS.find((s) => s.id === "composer.slash");
    expect(def).toBeDefined();
    expect(def?.combo).toBe("/");
    expect(def?.group).toBe("Composer");
    expect(def?.description).toMatch(/built-ins/i);
    expect(def?.description).toMatch(/workspace commands/);
    // The gated set is named as such so nobody reads a missing row as a bug.
    expect(def?.description).toMatch(/capability-gated/);
  });
});
