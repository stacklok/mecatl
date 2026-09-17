import { resolve } from "node:path";

/**
 * The controller's session-store settings — where the managed mecated keeps
 * its JSONL session store, or whether it runs without one — normalised and
 * turned into the spawn flag. Shared by the controller
 * (`scripts/local-controller.mjs`) and its vitest suite, the same
 * dual-import pattern as controller-permissions.mjs, so the flag grammar
 * cannot drift between the process that spawns mecated and the tests that
 * pin what it passes.
 *
 * mecated has NO `--no-store` flag: an EMPTY `--store-dir` IS the in-memory
 * store (cmd/mecated/main.go: "empty -> in-memory store"), so "no
 * persistence" here means the flag is simply not passed. Nothing here is a
 * credential and nothing here writes settings.yaml.
 */

/** The two persistence modes: a durable JSONL store on disk, or none. */
export const PERSISTENCE_MODES = Object.freeze(["durable", "memory"]);

/**
 * Where the store lived before it became a setting: RELATIVE TO THE
 * CONTROLLER'S WORKSPACE (the repo root, also the spawn cwd) — NOT to
 * studio/. Keeping that base is what keeps every existing chat in the
 * sidebar after the upgrade.
 */
export const DEFAULT_STORE_DIR = ".scratch/studio-sessions";

/** Longest store path the state file / a POST body may carry. */
const maxStoreDirLength = 4096;

/** The built-in defaults, before any environment variable is read. */
export const BUILTIN_STORAGE_DEFAULTS = Object.freeze({
  persistence: "durable",
  storeDir: DEFAULT_STORE_DIR,
});

/**
 * Validates one store-directory value: a non-empty string after trimming,
 * without a NUL byte (a path with one is a smuggling attempt, not a typo),
 * within the length bound. Throws a 400-shaped error.
 * @param {unknown} value
 * @param {string} what — names the source in the error ("Store location",
 *   "MECATL_STUDIO_STORE_DIR")
 * @returns {string}
 */
function validStoreDir(value, what) {
  if (typeof value !== "string") {
    throw Object.assign(new Error(`${what} must be a string`), {
      statusCode: 400,
    });
  }
  const trimmed = value.trim();
  if (!trimmed) {
    throw Object.assign(new Error(`${what} must not be empty`), {
      statusCode: 400,
    });
  }
  if (trimmed.includes("\0")) {
    throw Object.assign(new Error(`${what} must not contain a NUL byte`), {
      statusCode: 400,
    });
  }
  if (trimmed.length > maxStoreDirLength) {
    throw Object.assign(
      new Error(`${what} is longer than ${maxStoreDirLength} characters`),
      { statusCode: 400 },
    );
  }
  return trimmed;
}

/**
 * Validates and fills a storage document (a saved state file or a
 * POST /storage body) against the given defaults. `persistence` must be one
 * of PERSISTENCE_MODES when present (an unknown token THROWS rather than
 * falling back — the UI only ever offers the two, so a third is a bug worth
 * surfacing). `storeDir` falls back to the default only when ABSENT
 * (undefined/null): an explicit empty or whitespace-only string is rejected,
 * because "an empty --store-dir" is exactly the in-memory store and must
 * never be produced by accident.
 *
 * @param {unknown} input
 * @param {{persistence: string, storeDir: string}} [defaults]
 * @returns {{persistence: "durable"|"memory", storeDir: string}}
 */
export function normalizeStorageSettings(
  input,
  defaults = BUILTIN_STORAGE_DEFAULTS,
) {
  const source =
    input && typeof input === "object" && !Array.isArray(input)
      ? /** @type {Record<string, unknown>} */ (input)
      : {};
  let persistence = defaults.persistence;
  if (source.persistence !== undefined && source.persistence !== null) {
    const raw =
      typeof source.persistence === "string"
        ? source.persistence.trim().toLowerCase()
        : "";
    if (!PERSISTENCE_MODES.includes(raw)) {
      throw Object.assign(
        new Error(
          `Unknown persistence "${raw}" — expected one of ${PERSISTENCE_MODES.join(", ")}`,
        ),
        { statusCode: 400 },
      );
    }
    persistence = raw;
  }
  const storeDir =
    source.storeDir === undefined || source.storeDir === null
      ? defaults.storeDir
      : validStoreDir(source.storeDir, "Store location");
  return { persistence, storeDir };
}

/**
 * The defaults the controller starts from: MECATL_STUDIO_STORE_DIR (absolute
 * or workspace-relative; unset/blank keeps DEFAULT_STORE_DIR) and
 * MECATL_STUDIO_NO_STORE ("1" = in-memory). Validated the same way as the
 * other MECATL_STUDIO_* knobs — a bad value throws at startup rather than
 * spawning a daemon on a path nobody meant.
 *
 * @param {Record<string, string|undefined>} env
 * @returns {{persistence: "durable"|"memory", storeDir: string}}
 */
export function storageDefaultsFromEnv(env) {
  const noStore = (env.MECATL_STUDIO_NO_STORE ?? "").trim();
  if (noStore && noStore !== "0" && noStore !== "1") {
    throw Object.assign(
      new Error(
        `MECATL_STUDIO_NO_STORE must be "1" or unset (got "${noStore}")`,
      ),
      { statusCode: 400 },
    );
  }
  const rawDir = env.MECATL_STUDIO_STORE_DIR ?? "";
  return {
    persistence: noStore === "1" ? "memory" : "durable",
    storeDir: rawDir.trim()
      ? validStoreDir(rawDir, "MECATL_STUDIO_STORE_DIR")
      : DEFAULT_STORE_DIR,
  };
}

/**
 * The absolute store directory: an absolute `storeDir` stands as given, a
 * relative one resolves UNDER THE WORKSPACE (the spawn cwd), never under
 * studio/ or the controller's own directory.
 * @param {{storeDir: string}} settings
 * @param {string} workspace
 */
export function resolveStoreDir(settings, workspace) {
  return resolve(workspace, settings.storeDir);
}

/**
 * The mecated flags for a storage document: `--store-dir <absolute>` for a
 * durable store, NOTHING for in-memory (mecated has no --no-store; omitting
 * the flag is the in-memory store). The path is passed resolved so the
 * daemon's own cwd handling can never re-root it.
 * @param {{persistence: string, storeDir: string}} settings
 * @param {string} workspace
 * @returns {string[]}
 */
export function storageArgs(settings, workspace) {
  if (settings.persistence !== "durable") return [];
  return ["--store-dir", resolveStoreDir(settings, workspace)];
}

/**
 * The `/status.storage` projection: what the UI renders and edits. `dir` is
 * the RESOLVED absolute path the daemon was spawned with ("" when in-memory),
 * `storeDir` the configured value as saved (so the form can show a relative
 * path as the user typed it), `defaultDir` the resolved default the form
 * uses as its placeholder.
 * @param {{persistence: string, storeDir: string}} settings
 * @param {string} workspace
 * @param {{persistence: string, storeDir: string}} defaults
 */
export function storageStatus(settings, workspace, defaults) {
  return {
    persistence: settings.persistence,
    dir:
      settings.persistence === "durable"
        ? resolveStoreDir(settings, workspace)
        : "",
    storeDir: settings.storeDir,
    defaultDir: resolveStoreDir(defaults, workspace),
    defaultPersistence: defaults.persistence,
    managedBy: "studio",
  };
}
