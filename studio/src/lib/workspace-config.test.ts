import { describe, expect, it } from "vitest";
import { BUILTIN_STORAGE_DEFAULTS } from "./storage-settings.mjs";
import {
  DEFAULT_MEMORY_DIR,
  isDefaultWorkspace,
  memoryDirFor,
  normalizeWorkspaceConfig,
  rootKey,
  SKILLS_DIR_REL,
  skillsDirFor,
  storageArgsFor,
  storeDirFor,
  validateWorkspacePath,
  workspacePathShape,
} from "./workspace-config.mjs";

/**
 * The controller's workspace-root grammar: what a saved document normalises
 * to, which directories a root derives (the half that decides whether an
 * upgrade keeps every chat, and whether a foreign repo sprouts state
 * directories), and what the POST /workspace validator refuses.
 */

const defaultWorkspace = "/srv/checkout/mecatl";
const other = "/home/dev/other-repo";

const thrown = async (
  fn: () => Promise<unknown>,
): Promise<{ message: string; statusCode?: number }> => {
  try {
    await fn();
  } catch (error) {
    return error as { message: string; statusCode?: number };
  }
  throw new Error("expected a throw");
};

describe("normalizeWorkspaceConfig", () => {
  it("keeps an absolute saved root", () => {
    expect(
      normalizeWorkspaceConfig({ workspace: `  ${other} ` }, defaultWorkspace),
    ).toEqual({ workspace: other });
  });

  it("falls back to the default on garbage", () => {
    for (const input of [
      null,
      undefined,
      "string",
      [],
      {},
      { workspace: "" },
      { workspace: "   " },
      { workspace: "relative/path" },
      { workspace: 42 },
      { workspace: "/has\0nul" },
      { workspace: `/${"x".repeat(5000)}` },
    ]) {
      expect(normalizeWorkspaceConfig(input, defaultWorkspace)).toEqual({
        workspace: defaultWorkspace,
      });
    }
  });
});

describe("workspacePathShape", () => {
  it("names the reason a path is refused", () => {
    expect(() => workspacePathShape(7)).toThrow(/must be a string/);
    expect(() => workspacePathShape("")).toThrow(/must not be empty/);
    expect(() => workspacePathShape("/a\0b")).toThrow(/NUL/);
    expect(() => workspacePathShape("repo")).toThrow(/absolute path/);
    expect(workspacePathShape(" /repo ")).toBe("/repo");
  });
});

describe("derived directories", () => {
  it("keeps the historical store and memory paths for the default root", () => {
    expect(isDefaultWorkspace(defaultWorkspace, defaultWorkspace)).toBe(true);
    expect(
      storeDirFor(BUILTIN_STORAGE_DEFAULTS, defaultWorkspace, defaultWorkspace),
    ).toBe(`${defaultWorkspace}/.scratch/studio-sessions`);
    expect(memoryDirFor(defaultWorkspace, defaultWorkspace)).toBe(
      `${defaultWorkspace}/${DEFAULT_MEMORY_DIR}`,
    );
    expect(skillsDirFor(defaultWorkspace)).toBe(
      `${defaultWorkspace}/${SKILLS_DIR_REL}`,
    );
  });

  it("keys another root's store and memory by a stable hash under the default root", () => {
    const key = rootKey(other);
    expect(key).toMatch(/^[0-9a-f]{12}$/);
    expect(rootKey(other)).toBe(key);
    expect(rootKey(`${other}2`)).not.toBe(key);

    const store = storeDirFor(
      BUILTIN_STORAGE_DEFAULTS,
      other,
      defaultWorkspace,
    );
    expect(store).toBe(`${defaultWorkspace}/.scratch/studio-sessions-${key}`);
    // Never inside the chosen directory — a foreign repo must not sprout
    // untracked state because Studio was pointed at it.
    expect(store.startsWith(other)).toBe(false);
    expect(memoryDirFor(other, defaultWorkspace)).toBe(
      `${defaultWorkspace}/${DEFAULT_MEMORY_DIR}-${key}`,
    );
    // Project skills are the ONE directory that follows the root.
    expect(skillsDirFor(other)).toBe(`${other}/${SKILLS_DIR_REL}`);
  });

  it("suffixes a custom store location too, absolute or relative", () => {
    const key = rootKey(other);
    expect(
      storeDirFor(
        { storeDir: "/var/lib/mecatl/sessions" },
        other,
        defaultWorkspace,
      ),
    ).toBe(`/var/lib/mecatl/sessions-${key}`);
    expect(
      storeDirFor({ storeDir: "state/sessions" }, other, defaultWorkspace),
    ).toBe(`${defaultWorkspace}/state/sessions-${key}`);
  });

  it("emits --store-dir only for a durable store", () => {
    expect(
      storageArgsFor(
        { persistence: "durable", storeDir: ".scratch/studio-sessions" },
        other,
        defaultWorkspace,
      ),
    ).toEqual([
      "--store-dir",
      `${defaultWorkspace}/.scratch/studio-sessions-${rootKey(other)}`,
    ]);
    expect(
      storageArgsFor(
        { persistence: "memory", storeDir: ".scratch/studio-sessions" },
        other,
        defaultWorkspace,
      ),
    ).toEqual([]);
  });
});

describe("validateWorkspacePath", () => {
  /** A fake tree: real paths map to their resolved form; `kind` says what
   *  stat sees. A symlink `/link` resolves to the default root. */
  const tree: Record<string, { real: string; kind: "dir" | "file" }> = {
    [defaultWorkspace]: { real: defaultWorkspace, kind: "dir" },
    "/link": { real: defaultWorkspace, kind: "dir" },
    [other]: { real: other, kind: "dir" },
    "/home/dev/notes.txt": { real: "/home/dev/notes.txt", kind: "file" },
    [`${defaultWorkspace}/studio/.scratch`]: {
      real: `${defaultWorkspace}/studio/.scratch`,
      kind: "dir",
    },
    [`${defaultWorkspace}/studio/.scratch/nested`]: {
      real: `${defaultWorkspace}/studio/.scratch/nested`,
      kind: "dir",
    },
  };
  const io = {
    realpath: async (path: string) => {
      const entry = tree[path];
      if (!entry)
        throw Object.assign(new Error(`ENOENT: ${path}`), { code: "ENOENT" });
      return entry.real;
    },
    stat: async (path: string) => {
      const entry = tree[path];
      if (!entry)
        throw Object.assign(new Error(`ENOENT: ${path}`), { code: "ENOENT" });
      return { isDirectory: () => entry.kind === "dir" };
    },
    defaultWorkspace,
    forbidden: [`${defaultWorkspace}/studio/.scratch`],
  };

  it("returns the resolved directory", async () => {
    await expect(validateWorkspacePath(` ${other} `, io)).resolves.toBe(other);
  });

  it("canonicalises the default root, symlinked or not", async () => {
    await expect(validateWorkspacePath(defaultWorkspace, io)).resolves.toBe(
      defaultWorkspace,
    );
    await expect(validateWorkspacePath("/link", io)).resolves.toBe(
      defaultWorkspace,
    );
  });

  it("rejects relative paths, missing directories and files", async () => {
    expect(
      (await thrown(() => validateWorkspacePath("repo", io))).message,
    ).toMatch(/absolute path/);
    const missing = await thrown(() => validateWorkspacePath("/nope", io));
    expect(missing.message).toBe("Workspace root does not exist: /nope");
    expect(missing.statusCode).toBe(400);
    expect(
      (await thrown(() => validateWorkspacePath("/home/dev/notes.txt", io)))
        .message,
    ).toMatch(/must be a directory/);
  });

  it("refuses the controller's own state directory and anything under it", async () => {
    for (const path of [
      `${defaultWorkspace}/studio/.scratch`,
      `${defaultWorkspace}/studio/.scratch/nested`,
    ]) {
      const error = await thrown(() => validateWorkspacePath(path, io));
      expect(error.message).toMatch(/own state directory/);
      expect(error.statusCode).toBe(400);
    }
  });
});
