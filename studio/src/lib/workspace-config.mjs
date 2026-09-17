import { createHash } from "node:crypto";
import { isAbsolute, resolve, sep } from "node:path";
import { resolveStoreDir } from "./storage-settings.mjs";

/**
 * The controller's WORKSPACE ROOT setting — the directory the managed
 * mecated is spawned against (`mecated serve --workspace <root>`, the TUI's
 * `--workspace` deployment choice) — plus every directory the controller
 * derives from it. Shared by the controller (`scripts/local-controller.mjs`)
 * and its vitest suite, the same dual-import pattern as
 * storage-settings.mjs, so the derivation cannot drift between the process
 * that spawns mecated and the tests that pin where its state lands.
 *
 * Three rules, each with a reason:
 *
 * 1. The DEFAULT root (the monorepo, where Studio lives) keeps every
 *    historical path byte-for-byte — `.scratch/studio-sessions`,
 *    `.scratch/studio-memory` — so an upgrade loses no chat and no memory.
 * 2. Another root gets its OWN session store and memory store, keyed by a
 *    stable hash of its resolved path, and both live under the DEFAULT
 *    root's `.scratch` — never inside the chosen directory. A session is
 *    bound to the placement it was created in (ADR 0291), so a store shared
 *    across roots would list chats that cannot run; and a foreign repo must
 *    not sprout untracked state directories because Studio was pointed at
 *    it. Switching back to a root brings its own chats back.
 * 3. Project skills DO follow the root (`<root>/.mecatl/skills`): a
 *    SKILL.md steers the model the way AGENTS.md does, so discovery is
 *    pinned to the project being edited, not to Studio. mecated refuses to
 *    start on a missing --skills-dir, so the controller creates that one
 *    directory inside the chosen root — the confirm dialog says so.
 *
 * Nothing here is a credential and nothing here writes settings.yaml.
 */

/** The state file's basename under the controller's `.scratch`. */
export const WORKSPACE_STATE_FILE = "studio-workspace.json";

/** Where the store lived before roots became a setting (storage-settings). */
export const DEFAULT_MEMORY_DIR = ".scratch/studio-memory";

/** Project-scoped skills, relative to the ROOT (rule 3). */
export const SKILLS_DIR_REL = ".mecatl/skills";

/** Longest root path the state file / a POST body may carry. */
const maxWorkspaceLength = 4096;

/** How many hex characters of the root hash key a per-root directory. */
const rootKeyLength = 12;

/** Whether a string is an absolute POSIX or Windows path. */
const looksAbsolute = (path) => isAbsolute(path);

/**
 * The shape half of the check: a string, trimmed, non-empty, without a NUL
 * byte, within the length bound, absolute. Returns the trimmed path or
 * throws a 400-shaped error naming the reason. No filesystem access.
 * @param {unknown} value
 * @returns {string}
 */
export function workspacePathShape(value) {
  const bad = (message) =>
    Object.assign(new Error(message), { statusCode: 400 });
  if (typeof value !== "string") throw bad("Workspace root must be a string");
  const trimmed = value.trim();
  if (!trimmed) throw bad("Workspace root must not be empty");
  if (trimmed.includes("\0"))
    throw bad("Workspace root must not contain a NUL byte");
  if (trimmed.length > maxWorkspaceLength)
    throw bad(`Workspace root is longer than ${maxWorkspaceLength} characters`);
  if (!looksAbsolute(trimmed))
    throw bad(`Workspace root must be an absolute path (got "${trimmed}")`);
  return trimmed;
}

/**
 * Normalises a saved state document (`{ workspace }`). Garbage — a
 * non-object, a missing/non-string/relative/empty path — falls back to the
 * default root rather than throwing: a corrupt state file must never keep
 * the daemon from starting. Whether the saved directory still EXISTS is the
 * controller's load-time check (validateWorkspacePath), not this one.
 * @param {unknown} input
 * @param {string} defaultWorkspace
 * @returns {{workspace: string}}
 */
export function normalizeWorkspaceConfig(input, defaultWorkspace) {
  const source =
    input && typeof input === "object" && !Array.isArray(input)
      ? /** @type {Record<string, unknown>} */ (input)
      : {};
  try {
    return { workspace: workspacePathShape(source.workspace) };
  } catch {
    return { workspace: defaultWorkspace };
  }
}

/** Whether `workspace` IS the default root (the controller canonicalises a
 *  chosen path that resolves to the default back to the default binding, so
 *  a plain string compare is the whole test). */
export function isDefaultWorkspace(workspace, defaultWorkspace) {
  return workspace === defaultWorkspace;
}

/**
 * The stable per-root key: the first 12 hex characters of SHA-256 over the
 * root's RESOLVED path. Long enough that two roots on one machine cannot
 * collide by accident, short enough to read in a directory listing.
 * @param {string} workspace
 */
export function rootKey(workspace) {
  return createHash("sha256")
    .update(workspace, "utf8")
    .digest("hex")
    .slice(0, rootKeyLength);
}

/**
 * The session store for a root (rule 2): the storage document resolves
 * against the DEFAULT root exactly as before — a relative location under
 * `<defaultWorkspace>/`, an absolute one as given — and a non-default root
 * suffixes `-<rootKey>` so its chats are stored apart. Byte-identical to
 * `resolveStoreDir(storage, workspace)` for the default root.
 * @param {{storeDir: string}} storage
 * @param {string} workspace
 * @param {string} defaultWorkspace
 */
export function storeDirFor(storage, workspace, defaultWorkspace) {
  const base = resolveStoreDir(storage, defaultWorkspace);
  return isDefaultWorkspace(workspace, defaultWorkspace)
    ? base
    : `${base}-${rootKey(workspace)}`;
}

/**
 * The mecated flags for a storage document against a root: `--store-dir
 * <absolute>` for a durable store, NOTHING for in-memory (mecated has no
 * --no-store; omitting the flag is the in-memory store).
 * @param {{persistence: string, storeDir: string}} storage
 * @param {string} workspace
 * @param {string} defaultWorkspace
 * @returns {string[]}
 */
export function storageArgsFor(storage, workspace, defaultWorkspace) {
  if (storage.persistence !== "durable") return [];
  return ["--store-dir", storeDirFor(storage, workspace, defaultWorkspace)];
}

/**
 * The per-project memory store for a root (rule 2): the historical
 * `<defaultWorkspace>/.scratch/studio-memory` for the default root, the
 * same directory suffixed `-<rootKey>` otherwise — under the DEFAULT root,
 * never inside the chosen one.
 * @param {string} workspace
 * @param {string} defaultWorkspace
 */
export function memoryDirFor(workspace, defaultWorkspace) {
  const base = resolve(defaultWorkspace, DEFAULT_MEMORY_DIR);
  return isDefaultWorkspace(workspace, defaultWorkspace)
    ? base
    : `${base}-${rootKey(workspace)}`;
}

/**
 * Project skills for a root (rule 3): `<root>/.mecatl/skills`. Follows the
 * root by design — the one directory the controller creates inside it.
 * @param {string} workspace
 */
export function skillsDirFor(workspace) {
  return resolve(workspace, SKILLS_DIR_REL);
}

/** Whether `path` equals `root` or sits under it (lexical, separator-aware). */
const withinRoot = (path, root) =>
  path === root || path.startsWith(root.endsWith(sep) ? root : root + sep);

/**
 * The filesystem half of the check, with the I/O injected so the vitest can
 * run it against a fake tree: the path must pass workspacePathShape,
 * `realpath` must resolve it (a missing directory is named as such), and it
 * must be a directory. It must not be — or sit under — any `forbidden`
 * root (the controller's own state directory: pointing mecated at the
 * place that holds its state files would let the model edit them). A path
 * that resolves to the default root's realpath is CANONICALISED to the
 * `defaultWorkspace` binding, so `isDefaultWorkspace` stays a plain compare
 * even when the default is reached through a symlink. Returns the resolved
 * root or throws a 400-shaped error.
 *
 * @param {unknown} value
 * @param {{
 *   realpath: (path: string) => Promise<string>,
 *   stat: (path: string) => Promise<{isDirectory(): boolean}>,
 *   defaultWorkspace?: string,
 *   forbidden?: string[],
 * }} io
 * @returns {Promise<string>}
 */
export async function validateWorkspacePath(value, io) {
  const bad = (message) =>
    Object.assign(new Error(message), { statusCode: 400 });
  const path = workspacePathShape(value);
  let real;
  try {
    real = await io.realpath(path);
  } catch (error) {
    if (error?.code === "ENOENT" || error?.code === "ENOTDIR")
      throw bad(`Workspace root does not exist: ${path}`);
    throw bad(
      `Workspace root cannot be resolved: ${path} (${error?.message || error})`,
    );
  }
  let info;
  try {
    info = await io.stat(real);
  } catch (error) {
    throw bad(
      `Workspace root cannot be read: ${path} (${error?.message || error})`,
    );
  }
  if (!info.isDirectory())
    throw bad(`Workspace root must be a directory: ${path}`);
  for (const root of io.forbidden ?? []) {
    const realRoot = await io.realpath(root).catch(() => root);
    if (withinRoot(real, realRoot) || withinRoot(path, root))
      throw bad(
        `Workspace root must not be Studio's own state directory (${root}) or sit inside it`,
      );
  }
  if (io.defaultWorkspace) {
    const realDefault = await io
      .realpath(io.defaultWorkspace)
      .catch(() => io.defaultWorkspace);
    if (real === realDefault || path === io.defaultWorkspace)
      return io.defaultWorkspace;
  }
  return real;
}
