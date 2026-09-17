import { describe, expect, it } from "vitest";
import {
  builtinSlashCommands,
  CLOSED_BUILTIN_GATES,
  classifySlashLine,
  isBuiltinGatedOff,
  isStudioBuiltinCommand,
  STUDIO_BUILTIN_COMMANDS,
} from "./composer-builtins";

/**
 * Pins the client-owned slash layer: the TUI's fixed palette order (minus
 * `/quit`), the capability gate hiding `/compact`, and the classifier that
 * decides — on the FIRST line only — whether a submission is a bare built-in
 * (run locally), a held built-in (arguments/second line), a gated-off one,
 * or ordinary text for the daemon.
 */

const OPEN = { manualCompaction: true };

/** Every capability the gated set reads, all on. */
const ALL_CAPABILITIES: Record<string, unknown> = {
  mcp: true,
  mcp_connector_status: true,
  agents: true,
  skills: true,
  soul: true,
  user_model: true,
  learning_proposals: true,
  reflection: true,
  manual_dream: { project_memory: { generate: true } },
  model_selection: true,
  scheduling: true,
  workspace_enrollment: true,
  posture: "trusted",
};

/** The always-present rows: no capability document needed. */
const ALWAYS = [
  "clear",
  "help",
  "session",
  "retry",
  "diagnostics",
  "compact",
  "title",
  "learning",
];

describe("builtinSlashCommands", () => {
  it("lists the built-ins in the fixed order — the always-present six, the capability-gated set, then the operator rows", () => {
    expect(
      builtinSlashCommands({ ...OPEN, capabilities: ALL_CAPABILITIES }).map(
        (c) => c.name,
      ),
    ).toEqual([
      "clear",
      "help",
      "session",
      "retry",
      "diagnostics",
      "compact",
      "title",
      "mcp",
      "prompts",
      "resources",
      "agents",
      "skills",
      "soul",
      "usermodel",
      "reflections",
      "reflect",
      "dream",
      "models",
      "effort",
      "schedule",
      "tools-connect",
      "tools-cancel",
      "posture",
      "learning",
    ]);
  });

  it("hides /compact when the daemon lacks manual compaction", () => {
    const names = builtinSlashCommands(CLOSED_BUILTIN_GATES).map((c) => c.name);
    expect(names).not.toContain("compact");
    expect(names).toEqual([
      "clear",
      "help",
      "session",
      "retry",
      "diagnostics",
      "title",
      "learning",
    ]);
  });

  it("hides every capability-gated built-in without a capability document (fail-closed)", () => {
    expect(builtinSlashCommands(OPEN).map((c) => c.name)).toEqual(ALWAYS);
    // An EMPTY document hides every `=== true` row the same way; only
    // /models and /effort appear, because the picker they open is shown
    // unless the daemon says `model_selection: false`.
    expect(
      builtinSlashCommands({ ...OPEN, capabilities: {} }).map((c) => c.name),
    ).toEqual([
      "clear",
      "help",
      "session",
      "retry",
      "diagnostics",
      "compact",
      "title",
      "models",
      "effort",
      "learning",
    ]);
  });

  // Each capability flag adds exactly its own rows and nothing else.
  it.each<[string, Record<string, unknown>, string[]]>([
    ["mcp", { mcp: true }, ["mcp", "prompts", "resources"]],
    // The connector-status-only daemon gets the inventory panel but never a
    // prompt/resource RPC (the TUI invariant).
    ["mcp_connector_status", { mcp_connector_status: true }, ["mcp"]],
    ["agents", { agents: true }, ["agents"]],
    ["skills", { skills: true }, ["skills"]],
    ["soul", { soul: true }, ["soul"]],
    ["user_model", { user_model: true }, ["usermodel"]],
    ["learning_proposals", { learning_proposals: true }, ["reflections"]],
    ["reflection", { reflection: true }, ["reflect"]],
    ["manual_dream", { manual_dream: { user_model: {} } }, ["dream"]],
    ["scheduling", { scheduling: true }, ["schedule"]],
    [
      "workspace_enrollment",
      { workspace_enrollment: true },
      ["tools-connect", "tools-cancel"],
    ],
    ["posture", { posture: "yolo" }, ["posture"]],
  ])("%s adds exactly its rows", (_flag, capabilities, added) => {
    const names = builtinSlashCommands({ ...OPEN, capabilities }).map(
      (c) => c.name,
    );
    // Every capability-gated row present is one this flag owns; /models and
    // /effort ride the picker's own gate (shown unless model_selection is
    // false), so they are always there once a document exists.
    const gated = names.filter(
      (n) => !ALWAYS.includes(n) && n !== "models" && n !== "effort",
    );
    expect(gated).toEqual(added);
  });

  it("mirrors the composer's picker gate for /models and /effort: hidden only on model_selection false", () => {
    const with_ = (caps: Record<string, unknown>) =>
      builtinSlashCommands({ ...OPEN, capabilities: caps }).map((c) => c.name);
    expect(with_({ model_selection: false })).not.toContain("models");
    expect(with_({ model_selection: false })).not.toContain("effort");
    expect(with_({ model_selection: true })).toContain("models");
    // Absent reads as supported, like `effortSupported` in chat-workspace.
    expect(with_({ mcp: true })).toContain("effort");
  });

  it("treats a blank or non-string posture and an empty manual_dream as off", () => {
    const names = (caps: Record<string, unknown>) =>
      builtinSlashCommands({ ...OPEN, capabilities: caps }).map((c) => c.name);
    expect(names({ posture: "" })).not.toContain("posture");
    expect(names({ posture: 3 })).not.toContain("posture");
    expect(names({ manual_dream: {} })).not.toContain("dream");
    expect(names({ manual_dream: [] })).not.toContain("dream");
    // A non-boolean truthy flag is not `true`.
    expect(names({ agents: "yes" })).not.toContain("agents");
  });

  it("documents every built-in with the TUI's description and the builtin mark", () => {
    expect(STUDIO_BUILTIN_COMMANDS).toHaveLength(24);
    for (const command of STUDIO_BUILTIN_COMMANDS) {
      expect(command.builtin).toBe(true);
      expect(command.description.length).toBeGreaterThan(0);
    }
    expect(
      STUDIO_BUILTIN_COMMANDS.find((c) => c.name === "retry")?.description,
    ).toBe(
      "retry the last eligible failed model step without resending its prompt",
    );
    expect(isStudioBuiltinCommand("quit")).toBe(false);
    expect(isStudioBuiltinCommand("clear")).toBe(true);
  });
});

describe("classifySlashLine", () => {
  it("runs a bare built-in", () => {
    expect(classifySlashLine("/clear", OPEN)).toEqual({
      kind: "builtin",
      name: "clear",
    });
  });

  it("is case-insensitive and tolerates surrounding whitespace", () => {
    expect(classifySlashLine("/CLEAR ", OPEN)).toEqual({
      kind: "builtin",
      name: "clear",
    });
    expect(classifySlashLine("  /Help\n", OPEN)).toEqual({
      kind: "builtin",
      name: "help",
    });
  });

  it("holds a built-in typed with arguments", () => {
    expect(classifySlashLine("/clear now", OPEN)).toEqual({
      kind: "held",
      name: "clear",
      reason: "/clear takes no arguments — remove the text to run it",
    });
  });

  it("holds a built-in followed by a second line", () => {
    expect(classifySlashLine("/diagnostics\nmore", OPEN)).toMatchObject({
      kind: "held",
      name: "diagnostics",
    });
  });

  it("refuses a gated-off built-in with the daemon warning", () => {
    expect(classifySlashLine("/compact", CLOSED_BUILTIN_GATES)).toEqual({
      kind: "gated",
      name: "compact",
      reason: "/compact is not available on this daemon",
    });
    expect(classifySlashLine("/compact", OPEN)).toEqual({
      kind: "builtin",
      name: "compact",
    });
  });

  it("passes daemon workspace commands and ordinary text through", () => {
    expect(classifySlashLine("/deploy prod", OPEN)).toEqual({ kind: "pass" });
    expect(classifySlashLine("/review", OPEN)).toEqual({ kind: "pass" });
    expect(classifySlashLine("hello /clear", OPEN)).toEqual({ kind: "pass" });
    expect(classifySlashLine("/clearance", OPEN)).toEqual({ kind: "pass" });
    expect(classifySlashLine("", OPEN)).toEqual({ kind: "pass" });
  });

  it("refuses a capability-hidden built-in typed anyway, and runs it once the daemon enables it", () => {
    // Typed on a daemon without MCP: intercepted (never sent) and refused
    // with the daemon warning — the same gate the palette applied.
    expect(classifySlashLine("/mcp", OPEN)).toEqual({
      kind: "gated",
      name: "mcp",
      reason: "/mcp is not available on this daemon",
    });
    expect(
      classifySlashLine("/mcp", { ...OPEN, capabilities: { mcp: true } }),
    ).toEqual({ kind: "builtin", name: "mcp" });
    // The always-present rows need no document.
    expect(classifySlashLine("/title", OPEN)).toEqual({
      kind: "builtin",
      name: "title",
    });
    expect(classifySlashLine("/tools-connect now", OPEN)).toMatchObject({
      kind: "gated",
      name: "tools-connect",
    });
  });
});

describe("isBuiltinGatedOff", () => {
  it("is the one gate the palette, the classifier and the dispatcher share", () => {
    expect(isBuiltinGatedOff("skills", OPEN)).toBe(true);
    expect(
      isBuiltinGatedOff("skills", { ...OPEN, capabilities: { skills: true } }),
    ).toBe(false);
    expect(isBuiltinGatedOff("clear", CLOSED_BUILTIN_GATES)).toBe(false);
    expect(isBuiltinGatedOff("compact", CLOSED_BUILTIN_GATES)).toBe(true);
  });
});
