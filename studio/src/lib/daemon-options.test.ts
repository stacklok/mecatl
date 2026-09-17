import { describe, expect, it } from "vitest";
import {
  DEFAULT_COMMAND_DIRS,
  DEFAULT_DAEMON_OPTIONS,
  daemonOptionArgs,
  daemonOptionDirFields,
  effectiveDaemonDirs,
  normalizeDaemonOptions,
  normalizeOptionDir,
  optionDirScope,
  optionDirWithinRoots,
  resolveOptionDir,
} from "./daemon-options.mjs";

/**
 * The daemon-options document: the shape the controller keeps and the
 * spawn flags it renders. Pins (1) the default document renders the
 * pre-feature command line byte for byte; (2) every toggle's exact flag
 * — absence is meaningful for `--memory-dir` (off = omitted) and
 * `--skills-dir` (off = omitted, `--skills-conventional` never); (3) the
 * path grammar refuses traversal, control characters and the root; (4) the
 * vocabularies (review interval, ToolHive group) reject what mecated would
 * not parse; (5) directory resolution and the containment/scope helpers.
 */

const context = {
  workspace: "/repo",
  pinnedSkillsDir: "/repo/.mecatl/skills",
  defaultMemoryDir: "/repo/.scratch/studio-memory",
};

const options = (patch: Record<string, unknown> = {}) =>
  normalizeDaemonOptions({
    ...DEFAULT_DAEMON_OPTIONS,
    ...patch,
  });

describe("normalizeDaemonOptions", () => {
  it("defaults an empty body to the pre-feature document", () => {
    expect(normalizeDaemonOptions({})).toEqual(DEFAULT_DAEMON_OPTIONS);
    expect(normalizeDaemonOptions(undefined)).toEqual(DEFAULT_DAEMON_OPTIONS);
  });

  it("drops unknown keys and coerces a numeric-string interval", () => {
    const next = normalizeDaemonOptions({
      userModel: { enabled: false, reviewInterval: "4", extra: true },
      shell: false,
    });
    expect(next.userModel).toEqual({
      enabled: false,
      dir: "",
      reviewInterval: 4,
    });
    expect("shell" in next).toBe(false);
  });

  it("rejects a non-boolean flag and a non-object section", () => {
    expect(() =>
      normalizeDaemonOptions({ skills: { enabled: "yes" } }),
    ).toThrow(/skills\.enabled must be true or false/);
    expect(() => normalizeDaemonOptions({ mcp: [] })).toThrow(
      /mcp must be an object/,
    );
  });

  it("bounds the review interval to whole numbers from 1 to 1000", () => {
    for (const value of [0, -1, 1.5, 1001, "x", true]) {
      expect(
        () => normalizeDaemonOptions({ userModel: { reviewInterval: value } }),
        String(value),
      ).toThrow(/userModel\.reviewInterval/);
    }
    expect(
      normalizeDaemonOptions({ userModel: { reviewInterval: 1000 } }).userModel
        .reviewInterval,
    ).toBe(1000);
  });

  it("holds the ToolHive group to its grammar", () => {
    expect(
      normalizeDaemonOptions({ mcp: { toolhiveGroup: " team-a.v2 " } }).mcp
        .toolhiveGroup,
    ).toBe("team-a.v2");
    for (const group of ["two words", "a/b", "x".repeat(65), "$(rm)"]) {
      expect(
        () => normalizeDaemonOptions({ mcp: { toolhiveGroup: group } }),
        group,
      ).toThrow(/mcp\.toolhiveGroup/);
    }
    expect(() => normalizeDaemonOptions({ mcp: { toolhiveGroup: 3 } })).toThrow(
      /mcp\.toolhiveGroup must be a string/,
    );
  });
});

describe("normalizeOptionDir", () => {
  it("accepts absolute and workspace-relative paths, trimming a trailing slash", () => {
    expect(normalizeOptionDir("/srv/skills/", "skills.dir")).toBe(
      "/srv/skills",
    );
    expect(normalizeOptionDir(" .mecatl/commands ", "commands.dir")).toBe(
      ".mecatl/commands",
    );
    expect(normalizeOptionDir("", "skills.dir")).toBe("");
    expect(normalizeOptionDir(undefined, "skills.dir")).toBe("");
  });

  it("refuses traversal, empty segments, the root and control characters", () => {
    for (const dir of [
      "../outside",
      "a/../b",
      "/srv/./skills",
      "/srv//skills",
      "/",
      "/tmp/x\n",
      "skills\u0000",
    ]) {
      expect(() => normalizeOptionDir(dir, "skills.dir"), dir).toThrow(
        /skills\.dir/,
      );
    }
    expect(() => normalizeOptionDir("x".repeat(513), "skills.dir")).toThrow(
      /at most 512/,
    );
    expect(() => normalizeOptionDir(7, "skills.dir")).toThrow(
      /must be a string/,
    );
  });
});

describe("daemonOptionArgs", () => {
  it("renders the default document as the pre-feature command line", () => {
    expect(daemonOptionArgs(DEFAULT_DAEMON_OPTIONS, context)).toEqual([
      "--skills-dir",
      "/repo/.mecatl/skills",
      "--memory-dir",
      "/repo/.scratch/studio-memory",
    ]);
  });

  it("turns project memory off by OMITTING --memory-dir", () => {
    const args = daemonOptionArgs(
      options({ projectMemory: { enabled: false } }),
      context,
    );
    expect(args).not.toContain("--memory-dir");
    expect(args).toEqual(["--skills-dir", "/repo/.mecatl/skills"]);
  });

  it("relocates project memory, resolving a relative dir against the workspace", () => {
    expect(
      daemonOptionArgs(
        options({ projectMemory: { enabled: true, dir: ".memory" } }),
        context,
      ),
    ).toEqual([
      "--skills-dir",
      "/repo/.mecatl/skills",
      "--memory-dir",
      "/repo/.memory",
    ]);
  });

  it("turns the Skill tool off by OMITTING --skills-dir and never passes --skills-conventional", () => {
    const args = daemonOptionArgs(
      options({ skills: { enabled: false, dir: "/srv/skills" } }),
      context,
    );
    expect(args).toEqual(["--memory-dir", "/repo/.scratch/studio-memory"]);
    expect(args.join(" ")).not.toMatch(/skills/);
  });

  it("points --skills-dir at the override when set", () => {
    expect(
      daemonOptionArgs(
        options({ skills: { enabled: true, dir: "/srv/skills" } }),
        context,
      ).slice(0, 2),
    ).toEqual(["--skills-dir", "/srv/skills"]);
  });

  it("disables the user model with --no-user-model and drops its dir/interval", () => {
    const args = daemonOptionArgs(
      options({
        userModel: { enabled: false, dir: "/home/me/um", reviewInterval: 5 },
      }),
      context,
    );
    expect(args).toContain("--no-user-model");
    expect(args).not.toContain("--user-model-dir");
    expect(args).not.toContain("--user-model-review-interval");
  });

  it("relocates the user model and emits the interval only when it differs from 1", () => {
    expect(
      daemonOptionArgs(
        options({
          userModel: { enabled: true, dir: "/home/me/um", reviewInterval: 1 },
        }),
        context,
      ),
    ).toEqual([
      "--skills-dir",
      "/repo/.mecatl/skills",
      "--memory-dir",
      "/repo/.scratch/studio-memory",
      "--user-model-dir",
      "/home/me/um",
    ]);
    expect(
      daemonOptionArgs(
        options({ userModel: { enabled: true, dir: "", reviewInterval: 3 } }),
        context,
      ).slice(-2),
    ).toEqual(["--user-model-review-interval", "3"]);
  });

  it("enables slash commands with --enable-commands, or --commands-dir when a dir is set", () => {
    expect(
      daemonOptionArgs(options({ commands: { enabled: true } }), context).slice(
        -1,
      ),
    ).toEqual(["--enable-commands"]);
    expect(
      daemonOptionArgs(
        options({ commands: { enabled: true, dir: "prompts" } }),
        context,
      ).slice(-2),
    ).toEqual(["--commands-dir", "/repo/prompts"]);
    // The switch is the one control: a saved dir with commands off emits nothing.
    expect(
      daemonOptionArgs(
        options({ commands: { enabled: false, dir: "prompts" } }),
        context,
      ).join(" "),
    ).not.toMatch(/commands/);
  });

  it("renders the four MCP discovery flags, the group only while discovery is on", () => {
    expect(
      daemonOptionArgs(
        options({
          mcp: {
            toolhive: false,
            toolhiveGroup: "team",
            resourceTools: false,
            prompts: false,
          },
        }),
        context,
      ).slice(4),
    ).toEqual([
      "--toolhive=false",
      "--mcp-resource-tools=false",
      "--mcp-prompts=false",
    ]);
    expect(
      daemonOptionArgs(
        options({ mcp: { toolhive: true, toolhiveGroup: "team" } }),
        context,
      ).slice(4),
    ).toEqual(["--toolhive-group", "team"]);
  });
});

describe("directory helpers", () => {
  it("resolves relative dirs against the workspace and keeps absolute ones", () => {
    expect(resolveOptionDir("", "/repo")).toBe("");
    expect(resolveOptionDir("skills", "/repo")).toBe("/repo/skills");
    expect(resolveOptionDir("/srv/skills/", "/repo")).toBe("/srv/skills");
  });

  it("reports the effective directories, the overrides winning", () => {
    expect(effectiveDaemonDirs(DEFAULT_DAEMON_OPTIONS, context)).toEqual({
      skillsDir: "/repo/.mecatl/skills",
      memoryDir: "/repo/.scratch/studio-memory",
      userModelDir: "",
      commandsDir: "",
    });
    expect(
      effectiveDaemonDirs(
        options({
          skills: { enabled: false, dir: "s" },
          commands: { enabled: true, dir: "c" },
        }),
        context,
      ),
    ).toMatchObject({ skillsDir: "/repo/s", commandsDir: "/repo/c" });
  });

  it("checks containment separator-aware and never admits the filesystem root", () => {
    expect(optionDirWithinRoots("/repo/x", ["/repo"])).toBe(true);
    expect(optionDirWithinRoots("/repo", ["/repo"])).toBe(true);
    expect(optionDirWithinRoots("/repository/x", ["/repo"])).toBe(false);
    expect(optionDirWithinRoots("/etc", ["/"])).toBe(false);
  });

  it("scopes a directory by which root holds it", () => {
    const roots = {
      workspace: "/live",
      defaultWorkspace: "/repo",
      configDir: "/home/me/.config/mecatl",
    };
    expect(optionDirScope("/live/.mecatl/skills", roots)).toBe("project");
    expect(optionDirScope("/home/me/.config/mecatl/usermodel", roots)).toBe(
      "user",
    );
    expect(optionDirScope("/repo/.scratch/studio-memory-abc", roots)).toBe(
      "studio",
    );
    expect(optionDirScope("/srv/elsewhere", roots)).toBe("other");
  });

  it("lists only the set directory fields", () => {
    expect(daemonOptionDirFields(DEFAULT_DAEMON_OPTIONS)).toEqual([]);
    expect(
      daemonOptionDirFields(options({ commands: { dir: "prompts" } })),
    ).toEqual([["commands.dir", "prompts"]]);
  });

  it("exports mecated's default command directories", () => {
    expect(DEFAULT_COMMAND_DIRS).toEqual([
      ".mecatl/commands",
      ".claude/commands",
    ]);
  });
});
