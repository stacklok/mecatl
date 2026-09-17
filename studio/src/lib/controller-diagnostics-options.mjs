/**
 * The controller's DIAGNOSTICS OPTIONS document — the observability spawn
 * flags of the managed mecated (log level, the loopback admin/metrics
 * listener with its perf MCP mount and goroutine alarm, product-metrics
 * opt-out) plus one controller-side knob (`quiet`: whether the controller
 * mirrors mecated's stderr onto its own). Normalised, merged, and turned
 * into mecated flags here; shared by the controller
 * (`scripts/local-controller.mjs`) and its vitest suite, the same
 * dual-import pattern as controller-permissions.mjs, so the flag grammar
 * cannot drift between the process that spawns mecated and the tests that
 * pin what it passes.
 *
 * This document deliberately does NOT carry the operator posture: that is
 * the permissions document (`controller-permissions.mjs`, `GET|POST
 * /permissions`), and one flag with two writers would be a bug. Nothing
 * here is a credential and nothing here writes settings.yaml.
 */

/** mecated's `--log-level` vocabulary (internal/cliconfig/logging.go). */
export const LOG_LEVELS = Object.freeze(["debug", "info", "warn", "error"]);

/** The highest `--goroutine-warn-threshold` the controller accepts; a
 *  healthy mecated holds a low-hundreds count, so anything above this is a
 *  typo, not a ceiling. */
export const MAX_GOROUTINE_WARN_THRESHOLD = 1_000_000;

/** mecated's own defaults, spelled out: info logging, NO admin listener
 *  (see `diagnosticsOptionArgs` for why the absence is explicit), product
 *  metrics on, controller stderr mirroring on. */
export const DEFAULT_DIAGNOSTICS_OPTIONS = Object.freeze({
  logLevel: "info",
  quiet: false,
  admin: Object.freeze({
    enabled: false,
    perfMcp: false,
    goroutineWarnThreshold: 0,
  }),
  productMetrics: Object.freeze({ enabled: true, dryRun: false }),
});

const TOP_LEVEL_KEYS = Object.freeze([
  "logLevel",
  "quiet",
  "admin",
  "productMetrics",
]);
const ADMIN_KEYS = Object.freeze([
  "enabled",
  "perfMcp",
  "goroutineWarnThreshold",
]);
const PRODUCT_METRICS_KEYS = Object.freeze(["enabled", "dryRun"]);

/** A 400 the controller's route handler passes straight to the client. */
function invalid(message) {
  return Object.assign(new Error(message), { statusCode: 400 });
}

function isPlainObject(value) {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

/**
 * Validates and fills a diagnostics document (a saved state file or a
 * merged POST body). Unknown keys are IGNORED here — a file written by a
 * newer controller must still load — but every known key is validated
 * strictly and a bad value THROWS (statusCode 400) rather than falling
 * back: the UI only ever offers the known vocabularies, so a fifth log
 * level is a bug worth surfacing, not a preference to quietly downgrade.
 *
 * @param {unknown} input
 * @returns {{logLevel: string, quiet: boolean, admin: {enabled: boolean, perfMcp: boolean, goroutineWarnThreshold: number}, productMetrics: {enabled: boolean, dryRun: boolean}}}
 */
export function normalizeDiagnosticsOptions(input) {
  const source = isPlainObject(input) ? input : {};
  const defaults = DEFAULT_DIAGNOSTICS_OPTIONS;

  const rawLevel =
    source.logLevel === undefined
      ? defaults.logLevel
      : typeof source.logLevel === "string"
        ? source.logLevel.trim().toLowerCase()
        : source.logLevel;
  if (!LOG_LEVELS.includes(rawLevel))
    throw invalid(
      `Unknown log level ${JSON.stringify(rawLevel)} — expected one of ${LOG_LEVELS.join(", ")}`,
    );

  const quiet = readBoolean(source.quiet, defaults.quiet, "quiet");

  const admin = isPlainObject(source.admin) ? source.admin : {};
  const adminEnabled = readBoolean(
    admin.enabled,
    defaults.admin.enabled,
    "admin.enabled",
  );
  const perfMcp = readBoolean(
    admin.perfMcp,
    defaults.admin.perfMcp,
    "admin.perfMcp",
  );
  if (perfMcp && !adminEnabled)
    throw invalid(
      "The perf MCP server mounts on the admin listener — enable the admin listener first",
    );
  const rawThreshold =
    admin.goroutineWarnThreshold === undefined
      ? defaults.admin.goroutineWarnThreshold
      : admin.goroutineWarnThreshold;
  if (
    typeof rawThreshold !== "number" ||
    !Number.isInteger(rawThreshold) ||
    rawThreshold < 0 ||
    rawThreshold > MAX_GOROUTINE_WARN_THRESHOLD
  )
    throw invalid(
      `The goroutine warning threshold must be a whole number from 0 (off) to ${MAX_GOROUTINE_WARN_THRESHOLD}`,
    );

  const metrics = isPlainObject(source.productMetrics)
    ? source.productMetrics
    : {};
  const productMetrics = {
    enabled: readBoolean(
      metrics.enabled,
      defaults.productMetrics.enabled,
      "productMetrics.enabled",
    ),
    dryRun: readBoolean(
      metrics.dryRun,
      defaults.productMetrics.dryRun,
      "productMetrics.dryRun",
    ),
  };

  return {
    logLevel: rawLevel,
    quiet,
    admin: {
      enabled: adminEnabled,
      perfMcp,
      goroutineWarnThreshold: rawThreshold,
    },
    productMetrics,
  };
}

function readBoolean(value, fallback, name) {
  if (value === undefined) return fallback;
  if (typeof value !== "boolean")
    throw invalid(`${name} must be true or false`);
  return value;
}

/**
 * Applies a PARTIAL document (a POST body may name only the field it
 * changes) over the current one, nested sections merged per key. Unlike
 * normalisation, an unknown key here is REJECTED: the body came from a
 * client naming a field it wants changed, so a misspelling must not
 * silently no-op. The result still goes through
 * `normalizeDiagnosticsOptions`.
 *
 * @param {ReturnType<typeof normalizeDiagnosticsOptions>} current
 * @param {unknown} patch
 */
export function mergeDiagnosticsOptions(current, patch) {
  if (!isPlainObject(patch))
    throw invalid("The diagnostics options body must be a JSON object");
  rejectUnknownKeys(patch, TOP_LEVEL_KEYS, "");
  if (patch.admin !== undefined) {
    if (!isPlainObject(patch.admin))
      throw invalid("admin must be a JSON object");
    rejectUnknownKeys(patch.admin, ADMIN_KEYS, "admin.");
  }
  if (patch.productMetrics !== undefined) {
    if (!isPlainObject(patch.productMetrics))
      throw invalid("productMetrics must be a JSON object");
    rejectUnknownKeys(
      patch.productMetrics,
      PRODUCT_METRICS_KEYS,
      "productMetrics.",
    );
  }
  return {
    ...current,
    ...patch,
    admin: { ...current.admin, ...(patch.admin ?? {}) },
    productMetrics: {
      ...current.productMetrics,
      ...(patch.productMetrics ?? {}),
    },
  };
}

function rejectUnknownKeys(object, allowed, prefix) {
  for (const key of Object.keys(object)) {
    if (!allowed.includes(key))
      throw invalid(`Unknown diagnostics option "${prefix}${key}"`);
  }
}

/**
 * The mecated flags for a diagnostics document.
 *
 * - `--log-level <level>` only when it differs from mecated's own `info`.
 * - The admin listener is EXPLICIT in both directions: `--metrics-addr
 *   <adminAddr>` when enabled and the controller chose an address, else
 *   `--metrics-addr=` (an empty value disables the endpoint). mecated's
 *   built-in default is a FIXED loopback port (`127.0.0.1:9090`), which two
 *   managed daemons on one machine would fight over and which an "admin
 *   off" toggle would otherwise silently leave open — so the controller
 *   always says what it means.
 * - `--perf-mcp` rides ONLY with a real admin address: mecated refuses the
 *   flag without a metrics listener, and a refused start helps nobody.
 * - `--goroutine-warn-threshold <n>` when > 0 (a slog alarm, independent
 *   of the listener).
 * - `--product-metrics=false` when disabled; `--product-metrics-dry-run`
 *   when the dry run is on. Never `--product-metrics=true`: an
 *   environment opt-out (DO_NOT_TRACK / MECATL_PRODUCT_METRICS=false) is
 *   the operator's, and the explicit flag would out-rank it.
 *
 * `quiet` is controller-side only (stderr mirroring) and yields no flag.
 *
 * @param {ReturnType<typeof normalizeDiagnosticsOptions>} options
 * @param {{adminAddr?: string}} [context]
 * @returns {string[]}
 */
export function diagnosticsOptionArgs(options, { adminAddr = "" } = {}) {
  const args = [];
  if (options.logLevel !== DEFAULT_DIAGNOSTICS_OPTIONS.logLevel)
    args.push("--log-level", options.logLevel);
  const listening = options.admin.enabled && adminAddr !== "";
  if (listening) args.push("--metrics-addr", adminAddr);
  else args.push("--metrics-addr=");
  if (listening && options.admin.perfMcp) args.push("--perf-mcp");
  if (options.admin.goroutineWarnThreshold > 0)
    args.push(
      "--goroutine-warn-threshold",
      String(options.admin.goroutineWarnThreshold),
    );
  if (!options.productMetrics.enabled) args.push("--product-metrics=false");
  if (options.productMetrics.dryRun) args.push("--product-metrics-dry-run");
  return args;
}

/**
 * Whether moving from one document to another changes the mecated command
 * line — everything except `quiet`, which the controller applies live.
 *
 * @param {ReturnType<typeof normalizeDiagnosticsOptions>} previous
 * @param {ReturnType<typeof normalizeDiagnosticsOptions>} next
 */
export function diagnosticsRestartRequired(previous, next) {
  return (
    previous.logLevel !== next.logLevel ||
    previous.admin.enabled !== next.admin.enabled ||
    previous.admin.perfMcp !== next.admin.perfMcp ||
    previous.admin.goroutineWarnThreshold !==
      next.admin.goroutineWarnThreshold ||
    previous.productMetrics.enabled !== next.productMetrics.enabled ||
    previous.productMetrics.dryRun !== next.productMetrics.dryRun
  );
}

/**
 * Whether the controller's environment opts mecated out of product metrics
 * regardless of the saved document (mecated reads the inherited env:
 * `MECATL_PRODUCT_METRICS=false` or a set `DO_NOT_TRACK` that is not one
 * of the "off" spellings "0"/"false" — internal/cliconfig
 * productmetrics_config.go).
 *
 * @param {Record<string, string | undefined>} env
 */
export function productMetricsEnvOptOut(env) {
  const override = String(env.MECATL_PRODUCT_METRICS ?? "").trim();
  if (override !== "") {
    if (/^(0|f|false)$/i.test(override)) return true;
    if (/^(1|t|true)$/i.test(override)) return false;
  }
  const doNotTrack = String(env.DO_NOT_TRACK ?? "")
    .trim()
    .toLowerCase();
  return doNotTrack !== "" && doNotTrack !== "0" && doNotTrack !== "false";
}
