import { describe, expect, it } from "vitest";
import { effectiveBindings } from "@/lib/shortcuts/keymap";
import {
  APPROVAL_VERDICT_SHORTCUTS,
  ariaKeyShortcuts,
  offersAlwaysAllow,
  stepVerdictIndex,
  verdictCombos,
  verdictForKeyEvent,
  visibleApprovalVerdicts,
} from "./approval-verdicts";
import type { ApprovalRequest } from "./types";

/**
 * The pure keyboard half of the verdict: the TUI's a/y → allow once, w →
 * always allow, d/n → deny, resolved against the registry's EFFECTIVE
 * bindings so a remapped key is honoured everywhere the helpers are read.
 */

function ev(
  key: string,
  mods: Partial<{
    meta: boolean;
    ctrl: boolean;
    shift: boolean;
    alt: boolean;
  }> = {},
) {
  return {
    key,
    metaKey: mods.meta ?? false,
    ctrlKey: mods.ctrl ?? false,
    shiftKey: mods.shift ?? false,
    altKey: mods.alt ?? false,
  };
}

const mainAsk: ApprovalRequest = {
  approvalId: "s1:1:c1:r1",
  sessionId: "s1",
  toolName: "Bash",
  description: "Bash needs your approval.",
  details: "ls",
};

const defaults = effectiveBindings({});

describe("offersAlwaysAllow / visibleApprovalVerdicts", () => {
  it("offers all three verdicts for a main-agent ask", () => {
    expect(offersAlwaysAllow(mainAsk)).toBe(true);
    expect(visibleApprovalVerdicts(true)).toEqual(["once", "always", "deny"]);
  });

  it("withholds Always allow for a child ask", () => {
    expect(offersAlwaysAllow({ ...mainAsk, child: true })).toBe(false);
    expect(visibleApprovalVerdicts(false)).toEqual(["once", "deny"]);
  });

  it("withholds Always allow for a debugger MCP ask in a debug session only", () => {
    const debugAsk: ApprovalRequest = {
      ...mainAsk,
      toolName: "mcp__github__create_issue",
      reason: "debug MCP call requires fresh current operator approval",
    };
    expect(offersAlwaysAllow(debugAsk, true)).toBe(false);
    expect(offersAlwaysAllow(debugAsk, false)).toBe(true);
  });
});

describe("stepVerdictIndex", () => {
  it("steps right and left with wrap over the visible buttons", () => {
    expect(stepVerdictIndex(0, "ArrowRight", 3)).toBe(1);
    expect(stepVerdictIndex(2, "ArrowRight", 3)).toBe(0);
    expect(stepVerdictIndex(0, "ArrowLeft", 3)).toBe(2);
    expect(stepVerdictIndex(1, "ArrowLeft", 2)).toBe(0);
  });

  it("jumps to the ends on Home / End", () => {
    expect(stepVerdictIndex(1, "Home", 3)).toBe(0);
    expect(stepVerdictIndex(1, "End", 3)).toBe(2);
  });

  it("lands on the first (→, Home) or last (←, End) button when none holds focus", () => {
    expect(stepVerdictIndex(-1, "ArrowRight", 3)).toBe(0);
    expect(stepVerdictIndex(-1, "ArrowLeft", 3)).toBe(2);
    expect(stepVerdictIndex(-1, "Home", 2)).toBe(0);
    expect(stepVerdictIndex(-1, "End", 2)).toBe(1);
  });

  it("is null for any other key, and for an empty bar", () => {
    expect(stepVerdictIndex(0, "ArrowDown", 3)).toBeNull();
    expect(stepVerdictIndex(0, "y", 3)).toBeNull();
    expect(stepVerdictIndex(0, "Tab", 3)).toBeNull();
    expect(stepVerdictIndex(0, "ArrowRight", 0)).toBeNull();
  });
});

describe("verdictForKeyEvent", () => {
  it("maps the TUI's keys: y/a allow once, w always, n/d deny", () => {
    expect(verdictForKeyEvent(defaults, ev("y"))).toBe("once");
    expect(verdictForKeyEvent(defaults, ev("a"))).toBe("once");
    expect(verdictForKeyEvent(defaults, ev("w"))).toBe("always");
    expect(verdictForKeyEvent(defaults, ev("n"))).toBe("deny");
    expect(verdictForKeyEvent(defaults, ev("d"))).toBe("deny");
  });

  it("matches a Caps Lock capital but not a modifier chord", () => {
    expect(verdictForKeyEvent(defaults, ev("Y"))).toBe("once");
    expect(verdictForKeyEvent(defaults, ev("y", { meta: true }))).toBeNull();
    expect(verdictForKeyEvent(defaults, ev("y", { ctrl: true }))).toBeNull();
    expect(verdictForKeyEvent(defaults, ev("N", { shift: true }))).toBeNull();
  });

  it("is null for every other key", () => {
    expect(verdictForKeyEvent(defaults, ev("j"))).toBeNull();
    expect(verdictForKeyEvent(defaults, ev("Escape"))).toBeNull();
    expect(verdictForKeyEvent(defaults, ev("Enter"))).toBeNull();
  });

  it("honours a remapped key and drops the default it replaced", () => {
    const remapped = effectiveBindings({ "approval.deny": "x" });
    expect(verdictForKeyEvent(remapped, ev("x"))).toBe("deny");
    expect(verdictForKeyEvent(remapped, ev("n"))).toBeNull();
    // The alternate stays.
    expect(verdictForKeyEvent(remapped, ev("d"))).toBe("deny");
  });

  it("covers every registry approval id", () => {
    const ids = new Set(APPROVAL_VERDICT_SHORTCUTS.map((s) => s.id));
    const registryIds = defaults
      .filter((b) => b.id.startsWith("approval.") && !b.fixed)
      .map((b) => b.id);
    expect(registryIds.length).toBeGreaterThan(0);
    for (const id of registryIds) expect(ids.has(id)).toBe(true);
  });
});

describe("verdictCombos / ariaKeyShortcuts", () => {
  it("lists the combos for a verdict, primary first", () => {
    expect(verdictCombos(defaults, "once")).toEqual(["y", "a"]);
    expect(verdictCombos(defaults, "always")).toEqual(["w"]);
    expect(verdictCombos(defaults, "deny")).toEqual(["n", "d"]);
    expect(verdictCombos(defaults, "session")).toEqual([]);
  });

  it("reflects a remap in the hint order", () => {
    const remapped = effectiveBindings({ "approval.allow": "shift+y" });
    expect(verdictCombos(remapped, "once")).toEqual(["shift+y", "a"]);
  });

  it("spells combos as UI Events key names for aria-keyshortcuts", () => {
    expect(ariaKeyShortcuts(["y", "a"])).toBe("y a");
    expect(ariaKeyShortcuts(["mod+k"])).toBe("Control+k");
    expect(ariaKeyShortcuts(["shift+esc", "right"])).toBe(
      "Shift+Escape ArrowRight",
    );
    expect(ariaKeyShortcuts([])).toBe("");
  });
});
