import { apiError } from "./errors";

/**
 * The managed daemon's SESSION STORE location and persistence mode — a
 * controller-owned spawn flag (`--store-dir <dir>`, or no flag at all for
 * the in-memory store), never a settings.yaml key and never a daemon
 * route. Read off `/status.storage`; written through `POST /storage`, which
 * restarts the daemon (in-flight runs end) and rolls back server-side when
 * the new store cannot be used.
 *
 * Paths are DISPLAY + EDIT metadata for the controller's own machine; the
 * store's contents never cross this boundary (rule 2 of studio/CLAUDE.md).
 */

const CONTROL_API = "/api/mecatl-control";

/** "durable": a JSONL store on disk. "memory": nothing is written — chats
 *  vanish when the daemon stops, scheduled tasks never fire, and storage
 *  health/retention/maintenance and resume-after-restart are unavailable. */
export type HarnessStoragePersistence = "durable" | "memory";

/** The controller's `/status.storage` mirror. */
export interface HarnessStorageState {
  persistence: HarnessStoragePersistence;
  /** The RESOLVED absolute directory the daemon was spawned with; "" when
   *  in-memory. */
  dir: string;
  /** The configured location as saved (absolute, or relative to the
   *  controller's workspace) — what the form edits. */
  storeDir: string;
  /** The resolved default (env or built-in) the form falls back to when
   *  the location is left empty. */
  defaultDir: string;
  /** The persistence the controller started from (env or built-in). */
  defaultPersistence: HarnessStoragePersistence;
}

/** The `POST /storage` body. An omitted `storeDir` keeps the controller's
 *  default location; an empty string is refused there (an empty
 *  `--store-dir` IS the in-memory store, so it must never be sent by
 *  accident). */
export interface HarnessStorageSettings {
  persistence: HarnessStoragePersistence;
  storeDir?: string;
}

const readPersistence = (raw: unknown): HarnessStoragePersistence | null =>
  raw === "durable" || raw === "memory" ? raw : null;

/**
 * Decodes `/status.storage`. Null for an older controller that does not
 * report it, for external mode (`storage: null`), or for a malformed
 * document — the Storage card then says so rather than inventing a path.
 */
export function readStorageState(raw: unknown): HarnessStorageState | null {
  if (!raw || typeof raw !== "object") return null;
  const body = raw as {
    persistence?: unknown;
    dir?: unknown;
    storeDir?: unknown;
    defaultDir?: unknown;
    defaultPersistence?: unknown;
  };
  const persistence = readPersistence(body.persistence);
  if (!persistence) return null;
  return {
    persistence,
    dir: typeof body.dir === "string" ? body.dir : "",
    storeDir: typeof body.storeDir === "string" ? body.storeDir : "",
    defaultDir: typeof body.defaultDir === "string" ? body.defaultDir : "",
    defaultPersistence: readPersistence(body.defaultPersistence) ?? "durable",
  };
}

/**
 * The exact body the controller receives: the persistence mode, plus the
 * location only when one was given (trimmed; blank means "use the
 * default", which is expressed by OMITTING the key, never by sending "").
 * Exported so the form test can pin what Save sends.
 */
export function storageSettingsBody(settings: HarnessStorageSettings): {
  persistence: HarnessStoragePersistence;
  storeDir?: string;
} {
  const storeDir = settings.storeDir?.trim() ?? "";
  return storeDir
    ? { persistence: settings.persistence, storeDir }
    : { persistence: settings.persistence };
}

/** Saves the session-store document. RESTARTS the daemon; a store the new
 *  document cannot use (an uncreatable directory, a refused start) is rolled
 *  back by the controller and surfaces here as the thrown error. */
export async function saveHarnessStorageSettings(
  settings: HarnessStorageSettings,
): Promise<HarnessStorageState | null> {
  const response = await fetch(`${CONTROL_API}/storage`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(storageSettingsBody(settings)),
  });
  if (!response.ok) throw await apiError(response);
  const body = (await response.json().catch(() => null)) as {
    storage?: unknown;
  } | null;
  return readStorageState(body?.storage);
}
