/**
 * The controller's DIAGNOSTICS OPTIONS as the browser sees them through
 * /api/mecatl-control: the observability spawn flags of the MANAGED mecated
 * (log level, the loopback admin/metrics listener with its perf MCP mount
 * and goroutine alarm, product-metrics opt-out) plus the controller-side
 * `quiet` switch. The flag grammar itself lives in
 * `src/lib/controller-diagnostics-options.mjs`; this module only moves the
 * document. Nothing here is a credential.
 *
 * The operator POSTURE is deliberately NOT part of this document — it is
 * the permissions document (`fetchHarnessPermissions` /
 * `saveHarnessPermissions`), and one spawn flag with two writers would be
 * a bug. The Diagnostics page's posture card READS the daemon's effective
 * tier off `serverCapabilities.posture` and links to Permissions to change
 * it.
 */

import { apiError } from "./errors";

const CONTROL_API = "/api/mecatl-control";

export interface HarnessDiagnosticsOptions {
  /** mecated's `--log-level`: debug | info | warn | error. */
  logLevel: string;
  /** Controller-side only: stop mirroring mecated's stderr onto the
   *  controller's (no daemon flag, no restart). */
  quiet: boolean;
  admin: {
    /** Open the loopback admin listener (`--metrics-addr`, the controller
     *  picks the port); off = the endpoint is explicitly disabled. */
    enabled: boolean;
    /** Mount the read-only perf MCP server on it (`--perf-mcp`). */
    perfMcp: boolean;
    /** `--goroutine-warn-threshold`; 0 = the alarm is off. */
    goroutineWarnThreshold: number;
  };
  productMetrics: {
    /** The SAVED switch (`--product-metrics=false` when off). */
    enabled: boolean;
    /** `--product-metrics-dry-run`. */
    dryRun: boolean;
  };
}

/** A partial document: name only the fields to change. */
export interface HarnessDiagnosticsPatch {
  logLevel?: string;
  quiet?: boolean;
  admin?: Partial<HarnessDiagnosticsOptions["admin"]>;
  productMetrics?: Partial<HarnessDiagnosticsOptions["productMetrics"]>;
}

export interface HarnessDiagnosticsState {
  /** The saved document the managed daemon was spawned with. */
  options: HarnessDiagnosticsOptions;
  productMetrics: {
    /** Whether product metrics are on as far as Studio can tell: the saved
     *  switch AND no environment opt-out on the controller (DO_NOT_TRACK /
     *  MECATL_PRODUCT_METRICS, which the spawned mecated inherits; the
     *  controller never passes the out-ranking --product-metrics=true). The
     *  settings.yaml opt-out is not readable here, so true means "not
     *  disabled by Studio or the environment", not a definitive on. */
    effective: boolean;
    /** "environment" when the controller's env opts out — the switch in the
     *  UI cannot turn them back on from here. */
    source: "studio" | "environment";
  };
}

/** Decodes the controller's document; null when it is not one. */
export function readDiagnosticsOptions(
  raw: unknown,
): HarnessDiagnosticsOptions | null {
  if (!raw || typeof raw !== "object") return null;
  const body = raw as {
    logLevel?: unknown;
    quiet?: unknown;
    admin?: {
      enabled?: unknown;
      perfMcp?: unknown;
      goroutineWarnThreshold?: unknown;
    } | null;
    productMetrics?: { enabled?: unknown; dryRun?: unknown } | null;
  };
  const threshold = Number(body.admin?.goroutineWarnThreshold ?? 0);
  return {
    logLevel: typeof body.logLevel === "string" ? body.logLevel : "info",
    quiet: body.quiet === true,
    admin: {
      enabled: body.admin?.enabled === true,
      perfMcp: body.admin?.perfMcp === true,
      goroutineWarnThreshold:
        Number.isInteger(threshold) && threshold > 0 ? threshold : 0,
    },
    productMetrics: {
      // Absent = on: mecated's default, and the field a pre-feature
      // controller never wrote.
      enabled: body.productMetrics?.enabled !== false,
      dryRun: body.productMetrics?.dryRun === true,
    },
  };
}

/**
 * The saved document plus the effective product-metrics verdict. Null when
 * the controller cannot answer — external mode's 409 (the deployment owns
 * its daemon's flags) or an older controller without the route — so the
 * caller renders the managed/unsupported note rather than a form.
 */
export async function fetchHarnessDiagnosticsOptions(
  signal?: AbortSignal,
): Promise<HarnessDiagnosticsState | null> {
  const response = await fetch(`${CONTROL_API}/diagnostics-options`, {
    signal,
    cache: "no-store",
  });
  if (!response.ok) return null;
  const body = (await response.json()) as {
    options?: unknown;
    productMetrics?: { enabled?: unknown; source?: unknown } | null;
  };
  const options = readDiagnosticsOptions(body.options);
  if (!options) return null;
  const source =
    body.productMetrics?.source === "environment" ? "environment" : "studio";
  return {
    options,
    productMetrics: {
      effective:
        typeof body.productMetrics?.enabled === "boolean"
          ? body.productMetrics.enabled
          : options.productMetrics.enabled && source === "studio",
      source,
    },
  };
}

/**
 * Applies a PARTIAL document. A change to anything but `quiet` RESTARTS
 * the daemon (in-flight runs end); a start mecated refuses is rolled back
 * by the controller and surfaces here as the thrown HarnessApiError with
 * mecated's own words. Resolves to the controller's echo of the saved
 * document (null when it sent none).
 */
export async function saveHarnessDiagnosticsOptions(
  patch: HarnessDiagnosticsPatch,
): Promise<HarnessDiagnosticsOptions | null> {
  const response = await fetch(`${CONTROL_API}/diagnostics-options`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(patch),
  });
  if (!response.ok) throw await apiError(response);
  const answer = (await response.json().catch(() => null)) as {
    options?: unknown;
  } | null;
  return readDiagnosticsOptions(answer?.options);
}

/**
 * The managed daemon's DIAGNOSTICS LOG as the controller reports it
 * (`GET /logs`): Studio's analogue of mecatui's embedded-server log file.
 * mecated writes diagnostics to stderr and has no log-file flag, so the
 * controller that holds that stream appends it to an owner-only file in
 * studio/.scratch (one rotated generation at 10 MiB) and keeps a bounded
 * in-memory tail. The content is MODEL-INFLUENCED (prompt fragments, file
 * paths, provider error bodies): render it as plain text only.
 */
export interface HarnessDaemonLog {
  /** The current generation's path on the controller's machine (display). */
  path: string;
  /** The one rotated generation kept beside it. */
  rotatedPath: string;
  /** The current generation's size; 0 when nothing has been written yet. */
  sizeBytes: number;
  /** The rotation bound (mecatui's 10 MiB, mirrored). */
  maxBytes: number;
  /** The most recent complete lines, oldest first. */
  lines: string[];
  /** True when the file holds more than `lines` shows. */
  truncated: boolean;
  /** The controller-side terminal mute (`quiet`), as saved. */
  quiet: boolean;
  /** mecated's `--log-level`, as saved. */
  level: string;
  /** Whether a mecated child is alive right now. */
  running: boolean;
  /** Why the last start failed, or "" — the crash-at-start the log exists
   *  to explain. */
  startupError: string;
}

/** How many tail lines the Diagnostics page asks for. */
export const DAEMON_LOG_DEFAULT_LINES = 200;

/**
 * `GET /logs/download` through the same-origin proxy, streamed as
 * `text/plain` with a `Content-Disposition: attachment` the proxy forwards.
 * Rendered as `<a href download="mecated.log">` — a plain anchor
 * navigation passes the proxy's trust check, and the `download` attribute
 * names the file even where the header is lost.
 */
export const DAEMON_LOG_DOWNLOAD_URL = `${CONTROL_API}/logs/download`;

/**
 * The bounded tail plus the file facts. Null when the controller cannot
 * answer — external mode's 409 (the deployment writes its daemon's
 * diagnostics wherever it configured them), an older controller without
 * the route, or an unreachable controller — so the caller renders the
 * matching note rather than an empty viewer.
 */
export async function fetchHarnessDaemonLog(
  lines: number = DAEMON_LOG_DEFAULT_LINES,
  signal?: AbortSignal,
): Promise<HarnessDaemonLog | null> {
  const query = new URLSearchParams({ lines: String(lines) });
  const response = await fetch(`${CONTROL_API}/logs?${query}`, {
    signal,
    cache: "no-store",
  });
  if (!response.ok) return null;
  const body = (await response.json().catch(() => null)) as {
    path?: unknown;
    rotatedPath?: unknown;
    sizeBytes?: unknown;
    maxBytes?: unknown;
    lines?: unknown;
    truncated?: unknown;
    quiet?: unknown;
    level?: unknown;
    running?: unknown;
    startupError?: unknown;
  } | null;
  if (!body || typeof body.path !== "string") return null;
  const size = Number(body.sizeBytes ?? 0);
  const max = Number(body.maxBytes ?? 0);
  return {
    path: body.path,
    rotatedPath: typeof body.rotatedPath === "string" ? body.rotatedPath : "",
    sizeBytes: Number.isFinite(size) && size > 0 ? size : 0,
    maxBytes: Number.isFinite(max) && max > 0 ? max : 0,
    lines: Array.isArray(body.lines)
      ? body.lines.filter((line): line is string => typeof line === "string")
      : [],
    truncated: body.truncated === true,
    quiet: body.quiet === true,
    level: typeof body.level === "string" ? body.level : "info",
    running: body.running === true,
    startupError:
      typeof body.startupError === "string" ? body.startupError : "",
  };
}
