import { describe, expect, it } from "vitest";
import {
  DEFAULT_DIAGNOSTICS_OPTIONS,
  diagnosticsOptionArgs,
  diagnosticsRestartRequired,
  LOG_LEVELS,
  MAX_GOROUTINE_WARN_THRESHOLD,
  mergeDiagnosticsOptions,
  normalizeDiagnosticsOptions,
  productMetricsEnvOptOut,
} from "./controller-diagnostics-options.mjs";

/**
 * The diagnostics-options seam the controller spawns mecated with: the flag
 * grammar (`--log-level`, the EXPLICIT `--metrics-addr` in both directions,
 * `--perf-mcp` only with a real listener, `--goroutine-warn-threshold`,
 * `--product-metrics=false` / `--product-metrics-dry-run`), the strict
 * validation a POST body meets, the partial-merge rules, and which changes
 * restart the daemon (everything except the controller-side `quiet`).
 */

const defaults = normalizeDiagnosticsOptions({});

describe("normalizeDiagnosticsOptions", () => {
  it("fills an empty document with mecated's defaults", () => {
    expect(defaults).toEqual({
      logLevel: "info",
      quiet: false,
      admin: { enabled: false, perfMcp: false, goroutineWarnThreshold: 0 },
      productMetrics: { enabled: true, dryRun: false },
    });
    expect(defaults).toEqual(DEFAULT_DIAGNOSTICS_OPTIONS);
    expect(LOG_LEVELS).toEqual(["debug", "info", "warn", "error"]);
  });

  it("accepts every known log level, case-folded, and rejects a fifth", () => {
    for (const level of LOG_LEVELS)
      expect(normalizeDiagnosticsOptions({ logLevel: level }).logLevel).toBe(
        level,
      );
    expect(normalizeDiagnosticsOptions({ logLevel: " WARN " }).logLevel).toBe(
      "warn",
    );
    const error = (() => {
      try {
        normalizeDiagnosticsOptions({ logLevel: "verbose" });
      } catch (caught) {
        return caught as Error & { statusCode?: number };
      }
      return null;
    })();
    expect(error?.statusCode).toBe(400);
    expect(error?.message).toMatch(/Unknown log level "verbose"/);
  });

  it("refuses the perf MCP mount without the admin listener", () => {
    expect(() =>
      normalizeDiagnosticsOptions({ admin: { perfMcp: true } }),
    ).toThrow(/enable the admin listener first/);
    expect(
      normalizeDiagnosticsOptions({ admin: { enabled: true, perfMcp: true } })
        .admin,
    ).toEqual({ enabled: true, perfMcp: true, goroutineWarnThreshold: 0 });
  });

  it("bounds the goroutine threshold to a non-negative integer", () => {
    expect(
      normalizeDiagnosticsOptions({ admin: { goroutineWarnThreshold: 10_000 } })
        .admin.goroutineWarnThreshold,
    ).toBe(10_000);
    for (const bad of [-1, 1.5, "10", MAX_GOROUTINE_WARN_THRESHOLD + 1, null])
      expect(() =>
        normalizeDiagnosticsOptions({ admin: { goroutineWarnThreshold: bad } }),
      ).toThrow(/whole number from 0/);
  });

  it("rejects non-boolean switches and ignores unknown keys from a newer file", () => {
    expect(() => normalizeDiagnosticsOptions({ quiet: "yes" })).toThrow(
      /quiet must be true or false/,
    );
    expect(() =>
      normalizeDiagnosticsOptions({ productMetrics: { enabled: 1 } }),
    ).toThrow(/productMetrics.enabled must be true or false/);
    expect(
      normalizeDiagnosticsOptions({ futureKnob: true, quiet: true }),
    ).toEqual({ ...defaults, quiet: true });
  });
});

describe("mergeDiagnosticsOptions", () => {
  it("applies a partial body per nested key and leaves the rest", () => {
    const current = normalizeDiagnosticsOptions({
      logLevel: "debug",
      admin: { enabled: true, goroutineWarnThreshold: 500 },
    });
    const merged = normalizeDiagnosticsOptions(
      mergeDiagnosticsOptions(current, {
        admin: { perfMcp: true },
        productMetrics: { dryRun: true },
      }),
    );
    expect(merged).toEqual({
      logLevel: "debug",
      quiet: false,
      admin: { enabled: true, perfMcp: true, goroutineWarnThreshold: 500 },
      productMetrics: { enabled: true, dryRun: true },
    });
  });

  it("rejects an unknown key rather than silently ignoring a typo", () => {
    expect(() =>
      mergeDiagnosticsOptions(defaults, { logLevle: "debug" }),
    ).toThrow(/Unknown diagnostics option "logLevle"/);
    expect(() =>
      mergeDiagnosticsOptions(defaults, { admin: { perfMCP: true } }),
    ).toThrow(/Unknown diagnostics option "admin.perfMCP"/);
    expect(() => mergeDiagnosticsOptions(defaults, [])).toThrow(
      /must be a JSON object/,
    );
    // The posture is the permissions document's, never this one's.
    expect(() =>
      mergeDiagnosticsOptions(defaults, { posture: "yolo" }),
    ).toThrow(/Unknown diagnostics option "posture"/);
  });
});

describe("diagnosticsOptionArgs", () => {
  it("passes only the explicit no-admin-listener flag for the defaults", () => {
    expect(diagnosticsOptionArgs(defaults)).toEqual(["--metrics-addr="]);
  });

  it("emits the log level only when it differs from info", () => {
    expect(
      diagnosticsOptionArgs(normalizeDiagnosticsOptions({ logLevel: "debug" })),
    ).toEqual(["--log-level", "debug", "--metrics-addr="]);
  });

  it("binds the admin listener to the controller's address, with perf MCP and the goroutine alarm", () => {
    const options = normalizeDiagnosticsOptions({
      admin: { enabled: true, perfMcp: true, goroutineWarnThreshold: 10_000 },
    });
    expect(
      diagnosticsOptionArgs(options, { adminAddr: "127.0.0.1:41234" }),
    ).toEqual([
      "--metrics-addr",
      "127.0.0.1:41234",
      "--perf-mcp",
      "--goroutine-warn-threshold",
      "10000",
    ]);
  });

  it("never passes --perf-mcp without a listener address (mecated would refuse the start)", () => {
    const options = normalizeDiagnosticsOptions({
      admin: { enabled: true, perfMcp: true, goroutineWarnThreshold: 7 },
    });
    expect(diagnosticsOptionArgs(options)).toEqual([
      "--metrics-addr=",
      "--goroutine-warn-threshold",
      "7",
    ]);
  });

  it("turns product metrics off or dry-run, never on (the environment opt-out stays the operator's)", () => {
    expect(
      diagnosticsOptionArgs(
        normalizeDiagnosticsOptions({ productMetrics: { enabled: false } }),
      ),
    ).toEqual(["--metrics-addr=", "--product-metrics=false"]);
    expect(
      diagnosticsOptionArgs(
        normalizeDiagnosticsOptions({ productMetrics: { dryRun: true } }),
      ),
    ).toEqual(["--metrics-addr=", "--product-metrics-dry-run"]);
    expect(diagnosticsOptionArgs(defaults)).not.toContain(
      "--product-metrics=true",
    );
  });

  it("yields no flag for the controller-side quiet switch", () => {
    expect(
      diagnosticsOptionArgs(normalizeDiagnosticsOptions({ quiet: true })),
    ).toEqual(["--metrics-addr="]);
  });
});

describe("diagnosticsRestartRequired", () => {
  it("is false for a quiet-only change and true for every flag-bearing field", () => {
    expect(
      diagnosticsRestartRequired(
        defaults,
        normalizeDiagnosticsOptions({ quiet: true }),
      ),
    ).toBe(false);
    for (const patch of [
      { logLevel: "warn" },
      { admin: { enabled: true } },
      { admin: { enabled: true, perfMcp: true } },
      { admin: { goroutineWarnThreshold: 1 } },
      { productMetrics: { enabled: false } },
      { productMetrics: { dryRun: true } },
    ])
      expect(
        diagnosticsRestartRequired(
          defaults,
          normalizeDiagnosticsOptions(patch),
        ),
        JSON.stringify(patch),
      ).toBe(true);
  });
});

describe("productMetricsEnvOptOut", () => {
  it("mirrors mecated's precedence: MECATL_PRODUCT_METRICS, then DO_NOT_TRACK's off-spellings", () => {
    expect(productMetricsEnvOptOut({})).toBe(false);
    expect(productMetricsEnvOptOut({ DO_NOT_TRACK: "1" })).toBe(true);
    expect(productMetricsEnvOptOut({ DO_NOT_TRACK: "anything" })).toBe(true);
    expect(productMetricsEnvOptOut({ DO_NOT_TRACK: "0" })).toBe(false);
    expect(productMetricsEnvOptOut({ DO_NOT_TRACK: "FALSE" })).toBe(false);
    expect(productMetricsEnvOptOut({ MECATL_PRODUCT_METRICS: "false" })).toBe(
      true,
    );
    expect(productMetricsEnvOptOut({ MECATL_PRODUCT_METRICS: "0" })).toBe(true);
    // An explicit TRUE override out-ranks the generic convention.
    expect(
      productMetricsEnvOptOut({
        MECATL_PRODUCT_METRICS: "true",
        DO_NOT_TRACK: "1",
      }),
    ).toBe(false);
    // An unparseable override falls through to DO_NOT_TRACK.
    expect(
      productMetricsEnvOptOut({
        MECATL_PRODUCT_METRICS: "maybe",
        DO_NOT_TRACK: "1",
      }),
    ).toBe(true);
  });
});
