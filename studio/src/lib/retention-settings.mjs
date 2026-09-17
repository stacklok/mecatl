/**
 * The controller's session-RETENTION settings — how long the managed mecated
 * keeps main chats, delegation children and scheduled fires before its GC
 * sweep deletes them, plus the sweep cadence — normalised and turned into
 * spawn flags. Shared by the controller (`scripts/local-controller.mjs`), the
 * Storage page's form and the vitest suite, the same dual-import pattern as
 * storage-settings.mjs, so the flag grammar and the acknowledgement gate
 * cannot drift between the process that spawns mecated, the form that
 * validates the draft, and the tests that pin both.
 *
 * Flags, not a settings.yaml `retention:` block: the controller passes ONE
 * --permission-config file today, and mecated lets an explicit CLI flag
 * out-rank the file per field (`internal/app/retention.go`, `RetentionCLISet`),
 * so a flag can never be silently overridden by an imported operator file.
 * A `null` field means "leave mecated's own default" and emits no flag.
 * Nothing here is a credential.
 */

/** The three session families mecated sweeps independently. */
export const RETENTION_FAMILIES = Object.freeze(["main", "child", "scheduled"]);

/** The mecated flag pair (age, count) each family's limits become. */
const FAMILY_FLAGS = Object.freeze({
  main: Object.freeze(["--main-retention", "--main-retention-max-total"]),
  child: Object.freeze([
    "--child-retention",
    "--child-retention-max-per-family",
  ]),
  scheduled: Object.freeze([
    "--schedule-fire-retention",
    "--schedule-fire-retention-max-total",
  ]),
});

/**
 * Go `time.ParseDuration` as the retention flags accept it, minus the sign
 * (mecated refuses a negative anyway): one or more `<number><unit>` groups
 * with a decimal fraction allowed, units ns/us/µs/μs/ms/s/m/h. NO `d`
 * suffix — Go has none, and a "7d" the daemon rejects would fail the
 * restart the save triggers. A lone "0" is the documented "off".
 */
const DURATION_GROUP = /(\d+(\.\d+)?|\.\d+)(ns|us|µs|μs|ms|s|m|h)/g;
const DURATION_RE = /^((\d+(\.\d+)?|\.\d+)(ns|us|µs|μs|ms|s|m|h))+$/;

const UNIT_SECONDS = Object.freeze({
  ns: 1e-9,
  us: 1e-6,
  µs: 1e-6,
  μs: 1e-6,
  ms: 1e-3,
  s: 1,
  m: 60,
  h: 3600,
});

/** The gate's exact wording, shared with the form so the two agree. */
export const ACKNOWLEDGE_MAIN_MESSAGE =
  "Acknowledge automatic deletion of main chats before enabling main retention";

const bad = (message) => Object.assign(new Error(message), { statusCode: 400 });

/**
 * True when `raw` is a duration mecated's retention flags accept ("0", or
 * Go-grammar groups such as "168h", "1.5h", "30m", "1h30m").
 * @param {unknown} raw
 * @returns {boolean}
 */
export function isRetentionDuration(raw) {
  if (typeof raw !== "string") return false;
  const trimmed = raw.trim();
  return trimmed === "0" || DURATION_RE.test(trimmed);
}

/**
 * The duration in seconds, or NaN when `raw` is not one. "0" → 0.
 * @param {unknown} raw
 * @returns {number}
 */
export function retentionDurationSeconds(raw) {
  if (!isRetentionDuration(raw)) return Number.NaN;
  const trimmed = /** @type {string} */ (raw).trim();
  if (trimmed === "0") return 0;
  let seconds = 0;
  for (const match of trimmed.matchAll(DURATION_GROUP)) {
    seconds += Number(match[1]) * UNIT_SECONDS[match[3]];
  }
  return seconds;
}

/**
 * One family's max age: null (absent/blank → mecated's default) or a valid
 * duration string, trimmed. Throws a 400-shaped error otherwise.
 * @param {unknown} value
 * @param {string} what
 * @returns {string|null}
 */
function normalizeMaxAge(value, what) {
  if (value === undefined || value === null) return null;
  if (typeof value !== "string") throw bad(`${what} must be a duration`);
  const trimmed = value.trim();
  if (!trimmed) return null;
  if (!isRetentionDuration(trimmed))
    throw bad(
      `${what} must be a Go duration such as 168h, 30m or 1h30m (0 disables); days are not a unit — use hours`,
    );
  return trimmed;
}

/**
 * One family's max count: null (absent/blank → mecated's default) or a
 * non-negative safe integer (a digit string is accepted so a form's number
 * field can post either shape). Throws a 400-shaped error otherwise.
 * @param {unknown} value
 * @param {string} what
 * @returns {number|null}
 */
function normalizeMaxCount(value, what) {
  if (value === undefined || value === null) return null;
  if (typeof value === "string") {
    const trimmed = value.trim();
    if (!trimmed) return null;
    if (!/^\d+$/.test(trimmed))
      throw bad(`${what} must be a whole number (0 disables)`);
    value = Number(trimmed);
  }
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0)
    throw bad(`${what} must be a whole number (0 disables)`);
  return value;
}

/**
 * Validates and fills a retention document (a saved state file or a
 * POST /retention body). Every limit is optional — null leaves mecated's
 * own default for that flag — and mecated's destructive-main gate is
 * mirrored here so a save can never spawn a daemon that refuses to start:
 * a non-zero main age or count REQUIRES `acknowledgeMainDeletion: true`.
 * Child and scheduled limits need no acknowledgement (they never touch a
 * user's own chats).
 *
 * @param {unknown} input
 * @returns {{
 *   main: {maxAge: string|null, maxCount: number|null},
 *   child: {maxAge: string|null, maxCount: number|null},
 *   scheduled: {maxAge: string|null, maxCount: number|null},
 *   sweepCadence: string|null,
 *   acknowledgeMainDeletion: boolean,
 * }}
 */
export function normalizeRetentionSettings(input) {
  const source =
    input && typeof input === "object" && !Array.isArray(input)
      ? /** @type {Record<string, unknown>} */ (input)
      : {};
  const families = {};
  for (const family of RETENTION_FAMILIES) {
    const raw = source[family];
    const block =
      raw && typeof raw === "object" && !Array.isArray(raw)
        ? /** @type {Record<string, unknown>} */ (raw)
        : {};
    const label = family[0].toUpperCase() + family.slice(1);
    families[family] = {
      maxAge: normalizeMaxAge(block.maxAge, `${label} max age`),
      maxCount: normalizeMaxCount(block.maxCount, `${label} max count`),
    };
  }
  const sweepCadence = normalizeMaxAge(source.sweepCadence, "Sweep cadence");
  const acknowledgeMainDeletion = source.acknowledgeMainDeletion === true;
  const settings = {
    main: families.main,
    child: families.child,
    scheduled: families.scheduled,
    sweepCadence,
    acknowledgeMainDeletion,
  };
  if (mainRetentionEnabled(settings) && !acknowledgeMainDeletion)
    throw bad(ACKNOWLEDGE_MAIN_MESSAGE);
  return settings;
}

/**
 * True when the document turns ON destructive main-chat cleanup: a main
 * max age that is a non-zero duration, or a main max count above zero.
 * @param {{main: {maxAge: string|null, maxCount: number|null}}} settings
 * @returns {boolean}
 */
export function mainRetentionEnabled(settings) {
  const { maxAge, maxCount } = settings.main;
  return (
    (maxAge !== null && retentionDurationSeconds(maxAge) > 0) ||
    (maxCount !== null && maxCount > 0)
  );
}

/**
 * The mecated flags for a retention document: one flag per SET field
 * (null emits nothing, so mecated's own default stands), the sweep cadence
 * as `--child-gc-interval` (its name predates main/scheduled retention; it
 * is the one periodic sweep), and `--acknowledge-main-retention` when the
 * user acknowledged destructive main cleanup.
 * @param {ReturnType<typeof normalizeRetentionSettings>} settings
 * @returns {string[]}
 */
export function retentionArgs(settings) {
  const args = [];
  for (const family of RETENTION_FAMILIES) {
    const [ageFlag, countFlag] = FAMILY_FLAGS[family];
    const { maxAge, maxCount } = settings[family];
    if (maxAge !== null) args.push(ageFlag, maxAge);
    if (maxCount !== null) args.push(countFlag, String(maxCount));
  }
  if (settings.sweepCadence !== null)
    args.push("--child-gc-interval", settings.sweepCadence);
  if (settings.acknowledgeMainDeletion)
    args.push("--acknowledge-main-retention");
  return args;
}
