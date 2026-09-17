import { describe, expect, it } from "vitest";
import {
  DEFAULT_RUNTIME_SETTINGS,
  effectiveRuntimeSettings,
  LEARNING_MODES,
  LEARNING_SENSITIVITIES,
  NO_INHERITED_SETTINGS,
  normalizeRuntimeSettings,
  normalizeSoulFile,
  readLearningBlock,
  readSteerScalar,
  renderRuntimeSettingsYAML,
  runtimeSettingsArgs,
  runtimeSettingsHasYAML,
  SOUL_MAX_BYTES,
  soulFileWithinRoots,
} from "./runtime-settings.mjs";

/**
 * The controller's runtime-settings grammar: what a saved document or PUT
 * /runtime-settings body normalises to (mecated's closed learning
 * vocabularies, boolean flags, a bounded soul path), exactly which spawn
 * flags a document becomes, the CLI-tier `learning:` file it renders — the
 * FULL merged block, so mecated's whole-block capture never drops the
 * operator's skills/automatic settings — and the effective-value fold the
 * read half reports.
 */

const thrown = (
  fn: () => unknown,
): { message: string; statusCode?: number } => {
  try {
    fn();
  } catch (error) {
    return error as { message: string; statusCode?: number };
  }
  throw new Error("expected a throw");
};

describe("normalizeRuntimeSettings", () => {
  it("fills an empty body with the daemon-default document", () => {
    expect(normalizeRuntimeSettings({})).toEqual(DEFAULT_RUNTIME_SETTINGS);
    expect(normalizeRuntimeSettings(undefined)).toEqual(
      DEFAULT_RUNTIME_SETTINGS,
    );
    expect(runtimeSettingsHasYAML(DEFAULT_RUNTIME_SETTINGS)).toBe(false);
  });

  it("accepts every vocabulary value, case-folded and trimmed, and drops unknown keys", () => {
    for (const mode of LEARNING_MODES) {
      expect(
        normalizeRuntimeSettings({ learning: { mode } }).learning.mode,
      ).toBe(mode);
    }
    for (const sensitivity of LEARNING_SENSITIVITIES) {
      expect(
        normalizeRuntimeSettings({ learning: { sensitivity } }).learning
          .sensitivity,
      ).toBe(sensitivity);
    }
    expect(
      normalizeRuntimeSettings({
        learning: { mode: " Review ", extra: 1 },
        steer: { enabled: false },
        soul: {
          enabled: false,
          strict: true,
          file: "/home/me/.config/mecatl/soul.md",
        },
        bogus: true,
      }),
    ).toEqual({
      learning: { mode: "review", sensitivity: "" },
      steer: { enabled: false },
      soul: {
        enabled: false,
        strict: true,
        file: "/home/me/.config/mecatl/soul.md",
      },
    });
  });

  it("refuses a value outside mecated's closed vocabularies with a 400", () => {
    const mode = thrown(() =>
      normalizeRuntimeSettings({ learning: { mode: "sometimes" } }),
    );
    expect(mode.statusCode).toBe(400);
    expect(mode.message).toMatch(
      /learning\.mode must be one of off, review, auto/,
    );
    const sensitivity = thrown(() =>
      normalizeRuntimeSettings({ learning: { sensitivity: "high" } }),
    );
    expect(sensitivity.statusCode).toBe(400);
    expect(sensitivity.message).toMatch(/conservative, balanced, eager/);
    expect(
      thrown(() => normalizeRuntimeSettings({ learning: { mode: 1 } })).message,
    ).toMatch(/learning\.mode must be a string/);
  });

  it("refuses a non-boolean flag and a non-object section", () => {
    expect(
      thrown(() => normalizeRuntimeSettings({ steer: { enabled: "yes" } })),
    ).toMatchObject({
      statusCode: 400,
      message: "steer.enabled must be true or false",
    });
    expect(
      thrown(() => normalizeRuntimeSettings({ soul: { strict: 1 } })).message,
    ).toBe("soul.strict must be true or false");
    expect(thrown(() => normalizeRuntimeSettings({ soul: [] })).message).toBe(
      "soul must be an object",
    );
  });
});

describe("normalizeSoulFile", () => {
  it("accepts the empty (conventional) value and an absolute .md path", () => {
    expect(normalizeSoulFile("")).toBe("");
    expect(normalizeSoulFile(undefined)).toBe("");
    expect(normalizeSoulFile("  /Users/me/.config/mecatl/soul.md ")).toBe(
      "/Users/me/.config/mecatl/soul.md",
    );
  });

  it("refuses relative paths, traversal, control characters, dotfiles and non-Markdown", () => {
    const cases: [string, RegExp][] = [
      ["soul.md", /absolute/],
      ["/tmp/../etc/soul.md", /\.\. path segments/],
      ["/tmp//soul.md", /empty/],
      ["/tmp/soul.md\n", /control characters/],
      ["/tmp/soul\t.md", /control characters/],
      [`/tmp/so${String.fromCharCode(0)}ul.md`, /control characters/],
      ["/home/me/.config/mecatl/.soul.md", /dotfile/],
      ["/home/me/.config/mecatl/auth.yaml", /Markdown/],
      ["/home/me/.config/mecatl/.md", /dotfile/],
      [`/${"a".repeat(1030)}.md`, /at most 1024/],
    ];
    for (const [input, pattern] of cases) {
      const error = thrown(() => normalizeSoulFile(input));
      expect(error.statusCode, input).toBe(400);
      expect(error.message, input).toMatch(pattern);
    }
    expect(thrown(() => normalizeSoulFile(5)).message).toBe(
      "soul.file must be a string",
    );
  });

  it("checks lexical containment under a root, never the root itself or a sibling prefix", () => {
    const roots = ["/home/me/.config/mecatl", "/work/repo/"];
    expect(soulFileWithinRoots("/home/me/.config/mecatl/soul.md", roots)).toBe(
      true,
    );
    expect(soulFileWithinRoots("/work/repo/.mecatl/soul.md", roots)).toBe(true);
    expect(soulFileWithinRoots("/home/me/.config/mecatl", roots)).toBe(false);
    expect(
      soulFileWithinRoots("/home/me/.config/mecatl-evil/soul.md", roots),
    ).toBe(false);
    expect(soulFileWithinRoots("/etc/soul.md", roots)).toBe(false);
    expect(soulFileWithinRoots("/etc/soul.md", ["/"])).toBe(false);
  });

  it("pins the daemon's 20 KiB soul ceiling", () => {
    expect(SOUL_MAX_BYTES).toBe(20480);
  });
});

describe("runtimeSettingsArgs", () => {
  it("emits nothing for the default document", () => {
    expect(runtimeSettingsArgs(DEFAULT_RUNTIME_SETTINGS)).toEqual([]);
  });

  it("turns each knob into its mecated flag", () => {
    expect(
      runtimeSettingsArgs({
        steer: { enabled: false },
        soul: { enabled: true, strict: true, file: "/x/soul.md" },
      }),
    ).toEqual(["--no-steer", "--soul-strict", "--soul-file", "/x/soul.md"]);
  });

  it("emits --approve-soul only for the spawn that asks for it", () => {
    const config = normalizeRuntimeSettings({});
    expect(runtimeSettingsArgs(config, { approveSoul: true })).toEqual([
      "--approve-soul",
    ]);
    expect(runtimeSettingsArgs(config)).toEqual([]);
  });

  it("lets --no-soul stand alone: strict, file and approve are meaningless without a soul", () => {
    expect(
      runtimeSettingsArgs(
        {
          steer: { enabled: true },
          soul: { enabled: false, strict: true, file: "/x/soul.md" },
        },
        { approveSoul: true },
      ),
    ).toEqual(["--no-soul"]);
  });
});

describe("readLearningBlock", () => {
  const userSettings = [
    "posture: trusted",
    "learning:",
    "  mode: review",
    "  sensitivity: 'eager'   # tuned",
    "  skills:",
    "    activation: validated",
    "  # budgets",
    "  automatic:",
    "    cooldown: 10m",
    "    max_reflections: 4",
    "",
    "steer: false",
    "retention:",
    "  main: 168h",
    "",
  ].join("\n");

  it("returns null when the text has no top-level learning: key", () => {
    expect(readLearningBlock("posture: trusted\n")).toBeNull();
    expect(readLearningBlock("  learning:\n    mode: auto\n")).toBeNull();
    expect(readLearningBlock("")).toBeNull();
  });

  it("reads the two scalars and carries every other line of the block verbatim", () => {
    expect(readLearningBlock(userSettings)).toEqual({
      mode: "review",
      sensitivity: "eager",
      extraLines: [
        "  skills:",
        "    activation: validated",
        "  # budgets",
        "  automatic:",
        "    cooldown: 10m",
        "    max_reflections: 4",
      ],
    });
  });

  it("reads an empty block as present-but-blank and ignores a nested mode:", () => {
    expect(readLearningBlock("learning: {}\n")).toEqual({
      mode: "",
      sensitivity: "",
      extraLines: [],
    });
    expect(
      readLearningBlock("learning:\n  skills:\n    mode: nested\n"),
    ).toEqual({
      mode: "",
      sensitivity: "",
      extraLines: ["  skills:", "    mode: nested"],
    });
  });

  it("reads the top-level steer: scalar as a boolean, null otherwise", () => {
    expect(readSteerScalar(userSettings)).toBe(false);
    expect(readSteerScalar("steer: true\n")).toBe(true);
    expect(readSteerScalar('steer: "false" # off\n')).toBe(false);
    expect(readSteerScalar("steer: maybe\n")).toBeNull();
    expect(readSteerScalar("posture: auto\n")).toBeNull();
    expect(readSteerScalar("  steer: false\n")).toBeNull();
  });
});

describe("renderRuntimeSettingsYAML", () => {
  it("renders the header only for a document that overrides nothing", () => {
    const text = renderRuntimeSettingsYAML(DEFAULT_RUNTIME_SETTINGS);
    expect(text.startsWith("# Managed by Mecatl Studio")).toBe(true);
    expect(text).not.toMatch(/^learning:/m);
    expect(text).not.toMatch(/steer/);
  });

  it("emits only the overridden keys when nothing is inherited", () => {
    const text = renderRuntimeSettingsYAML(
      normalizeRuntimeSettings({ learning: { mode: "review" } }),
    );
    expect(text).toMatch(/^learning:\n {2}mode: "review"\n$/m);
    expect(text).not.toMatch(/sensitivity/);
    expect(text).not.toMatch(/steer/);
  });

  it("never renders a steer: key — the opt-out is the --no-steer flag", () => {
    const text = renderRuntimeSettingsYAML(
      normalizeRuntimeSettings({
        learning: { sensitivity: "eager" },
        steer: { enabled: false },
      }),
    );
    expect(text).toMatch(/^learning:\n {2}sensitivity: "eager"\n$/m);
    expect(text).not.toMatch(/steer/);
  });

  it("merges over the inherited block so the operator's skills/automatic settings survive", () => {
    const inherited = readLearningBlock(
      [
        "learning:",
        "    mode: off",
        "    sensitivity: conservative",
        "    skills:",
        "      activation: validated",
        "    automatic:",
        "      cooldown: 10m",
        "",
      ].join("\n"),
    );
    if (!inherited) throw new Error("expected a block");
    const text = renderRuntimeSettingsYAML(
      normalizeRuntimeSettings({ learning: { mode: "auto" } }),
      { learning: inherited },
    );
    const body = text.slice(text.indexOf("\nlearning:") + 1);
    expect(body).toBe(
      [
        "learning:",
        '  mode: "auto"',
        '  sensitivity: "conservative"',
        "  skills:",
        "    activation: validated",
        "  automatic:",
        "    cooldown: 10m",
        "",
      ].join("\n"),
    );
    // Every rendered line is either a comment, the header, or indented under it.
    for (const line of body.split("\n")) {
      if (!line) continue;
      expect(line === "learning:" || line.startsWith("  ")).toBe(true);
    }
  });
});

describe("effectiveRuntimeSettings", () => {
  const config = normalizeRuntimeSettings({
    learning: { mode: "review" },
    steer: { enabled: true },
  });
  const inherited = {
    learning: { mode: "auto", sensitivity: "eager", extraLines: [] },
    steer: false,
  };

  it("prefers Studio's override, then the inherited value, then the daemon default", () => {
    expect(effectiveRuntimeSettings(config, inherited)).toEqual({
      learning: { mode: "review", sensitivity: "eager" },
      steer: false,
    });
    expect(effectiveRuntimeSettings(config, NO_INHERITED_SETTINGS)).toEqual({
      learning: { mode: "review", sensitivity: "balanced" },
      steer: true,
    });
    expect(effectiveRuntimeSettings(DEFAULT_RUNTIME_SETTINGS)).toEqual({
      learning: { mode: "off", sensitivity: "balanced" },
      steer: true,
    });
  });

  it("drops Studio's learning override while an imported operator settings file is active", () => {
    expect(
      effectiveRuntimeSettings(config, inherited, {
        operatorSettingsActive: true,
      }).learning,
    ).toEqual({ mode: "auto", sensitivity: "eager" });
  });

  it("lets --no-steer tighten but never re-enable an operator steer: false", () => {
    const off = normalizeRuntimeSettings({ steer: { enabled: false } });
    expect(effectiveRuntimeSettings(off, NO_INHERITED_SETTINGS).steer).toBe(
      false,
    );
    expect(
      effectiveRuntimeSettings(DEFAULT_RUNTIME_SETTINGS, inherited).steer,
    ).toBe(false);
  });
});
