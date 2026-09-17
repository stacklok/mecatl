import { describe, expect, it } from "vitest";
import {
  builtinSlashCommands,
  CLOSED_BUILTIN_GATES,
  classifySlashLine,
  DEVELOPER_BUILTIN_COMMANDS,
  gatedReason,
  isStudioBuiltinCommand,
  STUDIO_BUILTIN_COMMANDS,
} from "./composer-builtins";

/**
 * `/debug-ask` is a developer-tools built-in: offered in the palette only
 * while Settings → Labs "Developer tools" is on, intercepted on send either
 * way (never sent to the model), refused with a Labs pointer when off, and
 * kept OUT of the documented reference list the help page renders.
 */
describe("composer built-ins — /debug-ask (developer tools)", () => {
  it("is hidden from the palette by default and offered last when developer tools are on", () => {
    const off = builtinSlashCommands({ manualCompaction: true }).map(
      (c) => c.name,
    );
    expect(off).not.toContain("debug-ask");
    expect(
      builtinSlashCommands(CLOSED_BUILTIN_GATES).map((c) => c.name),
    ).not.toContain("debug-ask");

    const on = builtinSlashCommands({
      manualCompaction: true,
      developerTools: true,
    }).map((c) => c.name);
    expect(on.at(-1)).toBe("debug-ask");
    expect(on.slice(0, -1)).toEqual(off);
  });

  it("stays out of the documented reference list", () => {
    expect(STUDIO_BUILTIN_COMMANDS.map((c) => c.name)).not.toContain(
      "debug-ask",
    );
    expect(DEVELOPER_BUILTIN_COMMANDS.map((c) => c.name)).toEqual([
      "debug-ask",
    ]);
    expect(DEVELOPER_BUILTIN_COMMANDS[0]?.description).toMatch(
      /never sent to the daemon/,
    );
  });

  it("is intercepted on send: gated off it refuses with the Labs pointer, on it runs locally", () => {
    expect(isStudioBuiltinCommand("debug-ask")).toBe(true);
    expect(classifySlashLine("/debug-ask", CLOSED_BUILTIN_GATES)).toEqual({
      kind: "gated",
      name: "debug-ask",
      reason: gatedReason("debug-ask"),
    });
    expect(gatedReason("debug-ask")).toBe(
      "/debug-ask needs Developer tools — turn it on in Settings → Labs",
    );
    expect(
      classifySlashLine("/debug-ask", {
        manualCompaction: false,
        developerTools: true,
      }),
    ).toEqual({ kind: "builtin", name: "debug-ask" });
    expect(
      classifySlashLine("/debug-ask now", {
        manualCompaction: false,
        developerTools: true,
      }).kind,
    ).toBe("held");
  });
});
