import { describe, expect, it } from "vitest";
import {
  builtinSlashCommands,
  classifySlashLine,
  gatedReason,
  isBuiltinGatedOff,
  STUDIO_BUILTIN_COMMANDS,
} from "./composer-builtins";

/**
 * The capability-gated half of the palette (the TUI's builtins.go gated
 * set): each daemon capability adds exactly its rows, an absent document
 * hides every gated row, every check reads `=== true` on the SNAKE_CASE
 * wire key (a non-boolean is off) — except `/models` and `/effort`, which
 * ride the composer picker's own gate (shown unless `model_selection:
 * false`) so the palette and the toolbar agree — `/dream` needs a
 * non-empty per-target object and `/posture` a non-empty tier word. The
 * palette, the send-path classifier and the dispatcher read the ONE gate
 * (`isBuiltinGatedOff`), so a shown row is never refused as unavailable.
 */

const BASE = { manualCompaction: true };

/** Offered with an EMPTY capability document, in palette order. */
const EMPTY_DOCUMENT = [
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
];

/** Offered with NO document at all: the always-set alone. */
const NO_DOCUMENT = [
  "clear",
  "help",
  "session",
  "retry",
  "diagnostics",
  "compact",
  "title",
  "learning",
];

function names(capabilities: Record<string, unknown>): string[] {
  return builtinSlashCommands({ ...BASE, capabilities }).map((c) => c.name);
}

/** The rows a document adds beyond the empty document's, in palette order. */
function added(capabilities: Record<string, unknown>): string[] {
  return names(capabilities).filter((name) => !EMPTY_DOCUMENT.includes(name));
}

describe("capability-gated built-ins", () => {
  it("offers the always-set plus the picker rows with an empty document", () => {
    expect(names({})).toEqual(EMPTY_DOCUMENT);
  });

  it("hides every gated row when no document is supplied at all", () => {
    expect(builtinSlashCommands(BASE).map((c) => c.name)).toEqual(NO_DOCUMENT);
  });

  it("hides /models and /effort only when the daemon says model selection is off", () => {
    const off = names({ model_selection: false });
    expect(off).not.toContain("models");
    expect(off).not.toContain("effort");
    expect(names({ model_selection: true })).toEqual(EMPTY_DOCUMENT);
  });

  it.each<[string, Record<string, unknown>, string[]]>([
    ["mcp", { mcp: true }, ["mcp", "prompts", "resources"]],
    // Connector status alone opens the panel, never the prompt/resource RPCs.
    ["mcp_connector_status", { mcp_connector_status: true }, ["mcp"]],
    ["agents", { agents: true }, ["agents"]],
    ["skills", { skills: true }, ["skills"]],
    ["soul", { soul: true }, ["soul"]],
    ["user_model", { user_model: true }, ["usermodel"]],
    ["learning_proposals", { learning_proposals: true }, ["reflections"]],
    ["reflection", { reflection: true }, ["reflect"]],
    [
      "manual_dream (a target)",
      { manual_dream: { project_memory: { generate: true } } },
      ["dream"],
    ],
    ["manual_dream (no target)", { manual_dream: {} }, []],
    ["scheduling", { scheduling: true }, ["schedule"]],
    [
      "workspace_enrollment",
      { workspace_enrollment: true },
      ["tools-connect", "tools-cancel"],
    ],
    ["posture", { posture: "auto" }, ["posture"]],
    ["posture (empty)", { posture: "" }, []],
    // Non-boolean truthy values read as off — the daemon-only rule.
    ["non-boolean flags", { mcp: "yes", agents: 1, skills: {} }, []],
  ])("%s adds exactly its rows", (_label, capabilities, expected) => {
    expect(added(capabilities)).toEqual(expected);
  });

  it("offers the whole documented set, in order, with everything on", () => {
    const all = names({
      mcp: true,
      agents: true,
      skills: true,
      soul: true,
      user_model: true,
      learning_proposals: true,
      reflection: true,
      manual_dream: { user_model: { generate: true } },
      model_selection: true,
      scheduling: true,
      workspace_enrollment: true,
      posture: "strict",
    });
    expect(all).toEqual(STUDIO_BUILTIN_COMMANDS.map((c) => c.name));
    expect(all.slice(0, 6)).toEqual(NO_DOCUMENT.slice(0, 6));
    expect(all.at(-1)).toBe("learning");
  });

  it("the send path refuses a hidden row with the daemon warning and runs a shown one", () => {
    expect(classifySlashLine("/mcp", BASE)).toEqual({
      kind: "gated",
      name: "mcp",
      reason: gatedReason("mcp"),
    });
    expect(gatedReason("mcp")).toBe("/mcp is not available on this daemon");
    expect(
      classifySlashLine("/mcp", { ...BASE, capabilities: { mcp: true } }),
    ).toEqual({ kind: "builtin", name: "mcp" });
    // Hyphenated names classify like the rest.
    expect(
      classifySlashLine("/tools-connect", {
        ...BASE,
        capabilities: { workspace_enrollment: true },
      }),
    ).toEqual({ kind: "builtin", name: "tools-connect" });
  });

  it("exposes the one gate the dispatcher shares with the palette", () => {
    expect(isBuiltinGatedOff("title", BASE)).toBe(false);
    expect(isBuiltinGatedOff("learning", BASE)).toBe(false);
    expect(isBuiltinGatedOff("skills", BASE)).toBe(true);
    expect(
      isBuiltinGatedOff("skills", { ...BASE, capabilities: { skills: true } }),
    ).toBe(false);
    expect(isBuiltinGatedOff("compact", { manualCompaction: false })).toBe(
      true,
    );
  });
});
