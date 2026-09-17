import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  getAllSlashCommands,
  getSlashCommands,
  refreshSlashCommands,
} from "./composer-capabilities";

/**
 * The composer's `/` palette is two layers: Studio's client-owned built-ins
 * (composer-builtins.ts — `/diagnostics` among them) ahead of the daemon's
 * per-session commands. Pins that a daemon refresh feeds the daemon layer
 * only, that the merged list keeps the built-ins first, and that a daemon
 * command sharing a built-in's name is dropped from the menu (the built-in
 * is what fires on send, so the menu must not promise otherwise).
 */

const client = vi.hoisted(() => ({
  listHarnessCommands: vi.fn(),
  listHarnessAgents: vi.fn(),
}));
vi.mock("@/lib/harness/client", () => client);

beforeEach(() => {
  client.listHarnessCommands.mockReset();
  client.listHarnessAgents.mockReset();
});

describe("slash command layers", () => {
  it("offers the built-ins before the daemon's commands after a refresh", async () => {
    client.listHarnessCommands.mockResolvedValue([
      { name: "review", description: "Review the diff" },
      { name: "", description: "nameless rows are dropped" },
    ]);
    await refreshSlashCommands("s1");
    expect(client.listHarnessCommands).toHaveBeenCalledWith("s1");
    expect(getSlashCommands()).toEqual([
      { name: "review", description: "Review the diff" },
    ]);

    const merged = getAllSlashCommands({ manualCompaction: true });
    const names = merged.map((command) => command.name);
    // No capability document: the always-present built-ins only (the
    // capability-gated set is hidden fail-closed), then the daemon's list.
    expect(names).toEqual([
      "clear",
      "help",
      "session",
      "retry",
      "diagnostics",
      "compact",
      "title",
      "learning",
      "review",
    ]);
    expect(merged.find((c) => c.name === "diagnostics")?.builtin).toBe(true);
    expect(merged.find((c) => c.name === "review")?.builtin).toBeUndefined();
  });

  it("hides a gated built-in and drops a daemon command a built-in shadows", async () => {
    client.listHarnessCommands.mockResolvedValue([
      { name: "diagnostics", description: "a workspace command of that name" },
      { name: "deploy", description: "Deploy" },
    ]);
    await refreshSlashCommands("s1");
    const merged = getAllSlashCommands({ manualCompaction: false });
    const names = merged.map((command) => command.name);
    expect(names).not.toContain("compact");
    expect(names.filter((name) => name === "diagnostics")).toHaveLength(1);
    // The surviving row is Studio's: it is the one that fires on send.
    expect(merged.find((c) => c.name === "diagnostics")?.builtin).toBe(true);
    expect(names.at(-1)).toBe("deploy");
  });

  it("keeps the previous daemon list when the refresh fails", async () => {
    client.listHarnessCommands.mockResolvedValue([
      { name: "review", description: "Review the diff" },
    ]);
    await refreshSlashCommands("s1");
    client.listHarnessCommands.mockRejectedValue(new Error("offline"));
    await refreshSlashCommands("s1");
    expect(getSlashCommands().map((c) => c.name)).toEqual(["review"]);
  });
});
