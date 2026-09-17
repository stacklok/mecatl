import { describe, expect, it } from "vitest";
import type { SessionPermissionMode } from "@/lib/protocol";
import { SHORTCUTS } from "@/lib/shortcuts/registry";
import {
  MODE_CYCLE_COMBO,
  MODE_CYCLE_SHORTCUT_ID,
  modeAccentClass,
  modeBadgeVariant,
  modeComposerRingClass,
  modeDotClass,
  nextPermissionMode,
  PERMISSION_MODE_CYCLE,
  PERMISSION_MODE_OPTIONS,
  permissionModeLabel,
  resolveModeCycleKey,
} from "./permission-mode";

/**
 * The one permission-mode vocabulary: labels for the pill/badge/strip, the
 * Shift+Tab cycle order (the TUI's default → plan → accept-edits), the colour
 * cue (plan = info, accept-edits = success, default = none) and the pure
 * keydown decision the composer's editor listener calls.
 */

const ALL: readonly SessionPermissionMode[] = [
  "default",
  "plan",
  "acceptEdits",
];

describe("permission mode vocabulary", () => {
  it("labels the three daemon modes, Manual for the default", () => {
    expect(permissionModeLabel("default")).toBe("Manual");
    expect(permissionModeLabel("plan")).toBe("Plan");
    expect(permissionModeLabel("acceptEdits")).toBe("Accept edits");
    // An unknown value (a newer daemon) falls back to the everyday posture.
    expect(permissionModeLabel("bypass" as SessionPermissionMode)).toBe(
      "Manual",
    );
  });

  it("lists every mode exactly once in the menu options", () => {
    const ids = PERMISSION_MODE_OPTIONS.map((o) => o.id);
    expect([...ids].sort()).toEqual([...ALL].sort());
    expect(new Set(ids).size).toBe(ids.length);
    for (const option of PERMISSION_MODE_OPTIONS) {
      expect(option.label).toBe(permissionModeLabel(option.id));
      expect(option.description.length).toBeGreaterThan(0);
    }
  });
});

describe("nextPermissionMode", () => {
  it("cycles default → plan → acceptEdits → default, the TUI's ModeSwitch order", () => {
    expect(PERMISSION_MODE_CYCLE).toEqual(["default", "plan", "acceptEdits"]);
    expect(nextPermissionMode("default")).toBe("plan");
    expect(nextPermissionMode("plan")).toBe("acceptEdits");
    expect(nextPermissionMode("acceptEdits")).toBe("default");
  });

  it("visits every mode before returning to the start", () => {
    const seen = new Set<SessionPermissionMode>();
    let mode: SessionPermissionMode = "default";
    for (let i = 0; i < ALL.length; i++) {
      seen.add(mode);
      mode = nextPermissionMode(mode);
    }
    expect(mode).toBe("default");
    expect(seen.size).toBe(ALL.length);
  });

  it("restarts at default for an unknown mode", () => {
    expect(nextPermissionMode("bypass" as SessionPermissionMode)).toBe(
      "default",
    );
  });
});

describe("mode colour cue", () => {
  it("tints only the non-default modes (a colour always means not-Manual)", () => {
    expect(modeAccentClass("default")).toBe("");
    expect(modeBadgeVariant("default")).toBeNull();
    expect(modeComposerRingClass("default")).toBe("");
    expect(modeDotClass("default")).toBe("");
    for (const mode of ["plan", "acceptEdits"] as const) {
      expect(modeAccentClass(mode)).not.toBe("");
      expect(modeBadgeVariant(mode)).not.toBeNull();
      expect(modeComposerRingClass(mode)).toContain("ring-1");
      expect(modeDotClass(mode)).toMatch(/^bg-/);
    }
  });

  it("uses the TUI's mapping: plan → info, accept-edits → success", () => {
    expect(modeAccentClass("plan")).toBe("text-info");
    expect(modeBadgeVariant("plan")).toBe("info");
    expect(modeComposerRingClass("plan")).toContain("border-info");
    expect(modeAccentClass("acceptEdits")).toBe("text-success");
    expect(modeBadgeVariant("acceptEdits")).toBe("success");
    expect(modeComposerRingClass("acceptEdits")).toContain("border-success");
    // The pill's dot and the box tint share one hue per mode, so the pill
    // and the rail can never disagree.
    expect(modeDotClass("plan")).toBe("bg-info");
    expect(modeDotClass("acceptEdits")).toBe("bg-success");
    // Distinct cues: a glance tells plan from accept-edits.
    expect(modeAccentClass("plan")).not.toBe(modeAccentClass("acceptEdits"));
    expect(modeDotClass("plan")).not.toBe(modeDotClass("acceptEdits"));
  });
});

describe("resolveModeCycleKey", () => {
  const press = (
    key: string,
    mods: Partial<{
      shift: boolean;
      meta: boolean;
      ctrl: boolean;
      alt: boolean;
    }> = {},
    gates: Partial<{ menuOpen: boolean; canChangeMode: boolean }> = {},
  ) =>
    resolveModeCycleKey({
      key,
      shiftKey: mods.shift ?? false,
      metaKey: mods.meta ?? false,
      ctrlKey: mods.ctrl ?? false,
      altKey: mods.alt ?? false,
      menuOpen: gates.menuOpen ?? false,
      canChangeMode: gates.canChangeMode ?? true,
    });

  it("reads its chord from the registry row, so the help page cannot drift", () => {
    const row = SHORTCUTS.find((s) => s.id === MODE_CYCLE_SHORTCUT_ID);
    expect(row).toBeDefined();
    expect(MODE_CYCLE_COMBO).toBe(row?.combo);
    expect(MODE_CYCLE_COMBO).toBe("shift+tab");
    // Component-owned: the global dispatcher never claims the chord.
    expect(row?.fixed).toBe(true);
  });

  it("fires on ⇧Tab with the caret in an enabled composer", () => {
    expect(press("Tab", { shift: true })).toBe(true);
  });

  it("leaves a bare Tab to the browser (forward focus)", () => {
    expect(press("Tab")).toBe(false);
  });

  it("never claims ⌘/Ctrl+⇧Tab (the browser's tab switch) or an Alt chord", () => {
    expect(press("Tab", { shift: true, meta: true })).toBe(false);
    expect(press("Tab", { shift: true, ctrl: true })).toBe(false);
    expect(press("Tab", { shift: true, alt: true })).toBe(false);
  });

  it("yields to an open autocomplete menu, where ⇧Tab picks the highlighted row", () => {
    expect(press("Tab", { shift: true }, { menuOpen: true })).toBe(false);
  });

  it("stays out of the way when the mode cannot be changed (no selector, disabled, or streaming without deferral)", () => {
    expect(press("Tab", { shift: true }, { canChangeMode: false })).toBe(false);
  });

  it("ignores every other key", () => {
    for (const key of ["Enter", "Escape", "ArrowUp", "a", " ", "PageUp"]) {
      expect(press(key, { shift: true })).toBe(false);
      expect(press(key)).toBe(false);
    }
  });
});
