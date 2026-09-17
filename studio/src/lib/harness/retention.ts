import { apiError } from "./errors";

/**
 * The managed daemon's RETENTION settings — how long main chats,
 * delegation children and scheduled fires are kept before the GC sweep
 * deletes them, and how often it sweeps. Controller-owned spawn flags
 * (`--main-retention`, `--child-retention`, `--schedule-fire-retention`,
 * their count caps, `--child-gc-interval`, `--acknowledge-main-retention`),
 * never a settings.yaml key and never a daemon route. Read off
 * `/status.retention`; written through `POST /retention`, which restarts
 * the daemon (in-flight runs end) and rolls back server-side when the new
 * flags make mecated refuse to start.
 *
 * The EFFECTIVE policy — what the daemon actually applies — is a different
 * read: `fetchStorageHealth().policy` (see ./storage.ts).
 */

const CONTROL_API = "/api/mecatl-control";

/** One family's limits. `null` leaves mecated's own default for that flag;
 *  a duration is Go grammar ("168h", "30m", "0" = off), a count a whole
 *  number (0 = off). */
interface HarnessRetentionFamily {
  maxAge: string | null;
  maxCount: number | null;
}

/** The controller's saved retention document (POST /retention body). */
export interface HarnessRetentionSettings {
  main: HarnessRetentionFamily;
  child: HarnessRetentionFamily;
  scheduled: HarnessRetentionFamily;
  sweepCadence: string | null;
  /** `--acknowledge-main-retention`: required by mecated whenever a main
   *  age or count limit is on — it deletes the user's own chats. */
  acknowledgeMainDeletion: boolean;
}

/** The controller's `/status.retention` mirror. */
export interface HarnessRetentionState {
  settings: HarnessRetentionSettings;
  /** "operator-settings": an imported operator settings file is active and
   *  the controller does not pass retention flags alongside it. */
  managedBy: "studio" | "operator-settings";
}

export const EMPTY_RETENTION_SETTINGS: HarnessRetentionSettings = {
  main: { maxAge: null, maxCount: null },
  child: { maxAge: null, maxCount: null },
  scheduled: { maxAge: null, maxCount: null },
  sweepCadence: null,
  acknowledgeMainDeletion: false,
};

const readFamily = (raw: unknown): HarnessRetentionFamily => {
  const body = (raw && typeof raw === "object" ? raw : {}) as {
    maxAge?: unknown;
    maxCount?: unknown;
  };
  return {
    maxAge: typeof body.maxAge === "string" ? body.maxAge : null,
    maxCount:
      typeof body.maxCount === "number" && Number.isFinite(body.maxCount)
        ? body.maxCount
        : null,
  };
};

/**
 * Decodes `/status.retention`. Null for an older controller that does not
 * report it, for external mode (`retention: null`), or for a malformed
 * document — the Retention card then says so rather than inventing limits.
 */
export function readRetentionState(raw: unknown): HarnessRetentionState | null {
  if (!raw || typeof raw !== "object") return null;
  const body = raw as { settings?: unknown; managedBy?: unknown };
  if (!body.settings || typeof body.settings !== "object") return null;
  const settings = body.settings as {
    main?: unknown;
    child?: unknown;
    scheduled?: unknown;
    sweepCadence?: unknown;
    acknowledgeMainDeletion?: unknown;
  };
  return {
    settings: {
      main: readFamily(settings.main),
      child: readFamily(settings.child),
      scheduled: readFamily(settings.scheduled),
      sweepCadence:
        typeof settings.sweepCadence === "string"
          ? settings.sweepCadence
          : null,
      acknowledgeMainDeletion: settings.acknowledgeMainDeletion === true,
    },
    managedBy:
      body.managedBy === "operator-settings" ? "operator-settings" : "studio",
  };
}

/** Saves the retention document. RESTARTS the daemon; flags mecated refuses
 *  (it re-checks the main-deletion acknowledgement itself) are rolled back
 *  by the controller and surface here as the thrown error. */
export async function saveHarnessRetention(
  settings: HarnessRetentionSettings,
): Promise<HarnessRetentionState | null> {
  const response = await fetch(`${CONTROL_API}/retention`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(settings),
  });
  if (!response.ok) throw await apiError(response);
  const body = (await response.json().catch(() => null)) as {
    retention?: unknown;
  } | null;
  return readRetentionState(body?.retention);
}
