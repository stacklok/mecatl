import net from "node:net";
import { describe, expect, it } from "vitest";
import { normalizeDiagnosticsOptions } from "./controller-diagnostics-options.mjs";
import {
  freeLoopbackPort,
  GOROUTINE_WARN_INTERVAL_SECONDS,
  isAddressInUse,
  PERF_ADMIN_PATHS,
  PERF_MCP_PATH,
  PERF_PROXY_ROUTES,
  perfAdminPaths,
  perfProxyRoute,
  perfStatus,
} from "./controller-perf.mjs";

/**
 * The runtime-admin-surface seam the controller serves as GET /perf and
 * relays as GET /perf/metrics + /perf/vars. Pins (1) the path inventory is
 * mecated's own (four admin paths, /mcp only with --perf-mcp), (2)
 * `enabled` is the LIVE fact — a saved switch with no chosen address reads
 * as off, and the perf MCP is never reported on an off listener, (3) only
 * the two TEXT endpoints are relayed — pprof and the flight recorder are
 * links, never proxied — (4) the free-port probe hands back a port this
 * process no longer holds, and (5) the address-in-use detector recognises
 * mecated's Go wording and Node's EADDRINUSE and nothing else.
 */

const options = (
  admin: Partial<{
    enabled: boolean;
    perfMcp: boolean;
    goroutineWarnThreshold: number;
  }>,
) => normalizeDiagnosticsOptions({ admin: { enabled: false, ...admin } });

describe("perfAdminPaths", () => {
  it("lists mecated's four admin paths, plus /mcp only with the perf MCP mount", () => {
    expect(PERF_ADMIN_PATHS).toEqual([
      "/metrics",
      "/debug/pprof",
      "/debug/vars",
      "/debug/flightrecorder",
    ]);
    expect(perfAdminPaths(false)).toEqual([...PERF_ADMIN_PATHS]);
    expect(perfAdminPaths(true)).toEqual([...PERF_ADMIN_PATHS, PERF_MCP_PATH]);
    expect(PERF_MCP_PATH).toBe("/mcp");
  });
});

describe("perfStatus", () => {
  it("reports an off surface with no origin, even when the switch is saved on but no address was chosen", () => {
    expect(perfStatus(options({}), "")).toEqual({
      enabled: false,
      adminUrl: "",
      paths: [...PERF_ADMIN_PATHS],
      perfMcp: false,
      goroutineWarnThreshold: 0,
      goroutineWarnIntervalSeconds: GOROUTINE_WARN_INTERVAL_SECONDS,
    });
    const savedOnButUnspawned = perfStatus(
      options({ enabled: true, perfMcp: true }),
      "",
    );
    expect(savedOnButUnspawned.enabled).toBe(false);
    expect(savedOnButUnspawned.perfMcp).toBe(false);
    expect(savedOnButUnspawned.adminUrl).toBe("");
  });

  it("reports the live origin, the /mcp path and the alarm threshold for a spawned listener", () => {
    expect(
      perfStatus(
        options({
          enabled: true,
          perfMcp: true,
          goroutineWarnThreshold: 10_000,
        }),
        "127.0.0.1:41234",
      ),
    ).toEqual({
      enabled: true,
      adminUrl: "http://127.0.0.1:41234",
      paths: [...PERF_ADMIN_PATHS, "/mcp"],
      perfMcp: true,
      goroutineWarnThreshold: 10_000,
      goroutineWarnIntervalSeconds: 30,
    });
    expect(
      perfStatus(options({ enabled: true }), "127.0.0.1:41234").paths,
    ).toEqual([...PERF_ADMIN_PATHS]);
  });
});

describe("perfProxyRoute", () => {
  it("relays only the two text endpoints, with their content types", () => {
    expect(perfProxyRoute("/perf/metrics")).toEqual({
      path: "/metrics",
      contentType: "text/plain; charset=utf-8",
    });
    expect(perfProxyRoute("/perf/vars")).toEqual({
      path: "/debug/vars",
      contentType: "application/json; charset=utf-8",
    });
    for (const relayed of Object.values(PERF_PROXY_ROUTES)) {
      expect(relayed.path).not.toMatch(/pprof|flightrecorder|\/mcp/);
    }
  });

  it("answers null for every other pathname, prototype names included", () => {
    expect(perfProxyRoute("/perf")).toBeNull();
    expect(perfProxyRoute("/perf/pprof")).toBeNull();
    expect(perfProxyRoute("/perf/flightrecorder")).toBeNull();
    expect(perfProxyRoute("constructor")).toBeNull();
    expect(perfProxyRoute("toString")).toBeNull();
  });
});

describe("freeLoopbackPort", () => {
  it("returns a loopback port this process has already released", async () => {
    const port = await freeLoopbackPort();
    expect(Number.isInteger(port)).toBe(true);
    expect(port).toBeGreaterThan(0);
    expect(port).toBeLessThanOrEqual(65_535);
    // Released: binding it again from here must succeed.
    const server = net.createServer();
    await new Promise<void>((resolve, reject) => {
      server.once("error", reject);
      server.listen(port, "127.0.0.1", () => resolve());
    });
    await new Promise<void>((resolve) => server.close(() => resolve()));
  });
});

describe("isAddressInUse", () => {
  it("recognises mecated's Go bind failure and Node's EADDRINUSE", () => {
    expect(
      isAddressInUse(
        'mecated: listen admin "127.0.0.1:41234": listen tcp 127.0.0.1:41234: bind: address already in use',
      ),
    ).toBe(true);
    expect(isAddressInUse("Error: listen EADDRINUSE: 127.0.0.1:41234")).toBe(
      true,
    );
  });

  it("does not fire on other startup failures or on nothing", () => {
    expect(
      isAddressInUse("--perf-mcp requires --metrics-addr (the loopback…)"),
    ).toBe(false);
    expect(isAddressInUse("provider openrouter: missing api key")).toBe(false);
    expect(isAddressInUse("")).toBe(false);
    expect(isAddressInUse(undefined)).toBe(false);
  });
});
