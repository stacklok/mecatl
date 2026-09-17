/**
 * The controller's RUNTIME ADMIN SURFACE helpers — the pure half of how
 * Studio exposes mecated's loopback admin listener (`--metrics-addr`:
 * /metrics, /debug/pprof, /debug/vars, /debug/flightrecorder, plus /mcp
 * when `--perf-mcp` is on) in managed mode. Shared by the controller
 * (`scripts/local-controller.mjs`) and its vitest suite, the same
 * dual-import pattern as controller-diagnostics-options.mjs, so the route
 * inventory the browser renders cannot drift from what the controller
 * actually relays.
 *
 * Two facts shape everything here. (1) mecated's ready file names only
 * `http_address`, so the controller must CHOOSE the admin port itself:
 * `freeLoopbackPort` probes (and releases) one just before the spawn; the
 * probe-to-bind window is a real, rare race, which the controller closes
 * with one re-probe + re-spawn when the failed start's stderr says
 * `address already in use` (`isAddressInUse`). (2) The browser's CSP is
 * `connect-src 'self'`, so it can never fetch the loopback listener: the
 * two TEXT endpoints are RELAYED by the controller (`PERF_PROXY_ROUTES`),
 * while the binary, potentially large pprof and flight-recorder endpoints
 * stay plain links that only resolve in a browser on the daemon's host.
 *
 * Nothing here is a credential — but `/metrics` can embed prompt text and
 * file paths, which is why every route stays behind the studio header.
 */

import net from "node:net";

/** The paths mecated mounts on the admin listener, in the order its own
 *  "admin server listening" line reports them (cmd/mecated/main.go
 *  `adminPaths`). */
export const PERF_ADMIN_PATHS = Object.freeze([
  "/metrics",
  "/debug/pprof",
  "/debug/vars",
  "/debug/flightrecorder",
]);

/** Where `--perf-mcp` mounts the read-only perf MCP server. */
export const PERF_MCP_PATH = "/mcp";

/** mecated's `--goroutine-warn-interval` default; the controller never
 *  passes the flag, so this is how often the alarm samples. */
export const GOROUTINE_WARN_INTERVAL_SECONDS = 30;

/** The paths served for a listener with or without the perf MCP mount. */
export function perfAdminPaths(perfMcp) {
  return perfMcp ? [...PERF_ADMIN_PATHS, PERF_MCP_PATH] : [...PERF_ADMIN_PATHS];
}

/**
 * The `GET /perf` payload (and `/status.perf`'s superset): whether the
 * CURRENT child was spawned with the admin listener, the origin the
 * controller chose for it, the paths mounted there, and the two knobs that
 * ride it. `enabled` is the LIVE fact — a saved `admin.enabled` with no
 * chosen address (a spawn that never happened) reads as off.
 *
 * @param {{admin: {enabled: boolean, perfMcp: boolean, goroutineWarnThreshold: number}}} options
 * @param {string} adminAddr the `host:port` the child was spawned with, or ""
 */
export function perfStatus(options, adminAddr) {
  const enabled = options.admin.enabled && adminAddr !== "";
  const perfMcp = enabled && options.admin.perfMcp;
  return {
    enabled,
    adminUrl: enabled ? `http://${adminAddr}` : "",
    paths: perfAdminPaths(perfMcp),
    perfMcp,
    goroutineWarnThreshold: options.admin.goroutineWarnThreshold,
    goroutineWarnIntervalSeconds: GOROUTINE_WARN_INTERVAL_SECONDS,
  };
}

/**
 * The controller routes that RELAY an admin endpoint, and what they relay.
 * Text only: /metrics (Prometheus exposition) and /debug/vars (expvar
 * JSON). /debug/pprof and /debug/flightrecorder are deliberately absent —
 * binary and potentially large, they are links, never proxied.
 */
export const PERF_PROXY_ROUTES = Object.freeze({
  "/perf/metrics": Object.freeze({
    path: "/metrics",
    contentType: "text/plain; charset=utf-8",
  }),
  "/perf/vars": Object.freeze({
    path: "/debug/vars",
    contentType: "application/json; charset=utf-8",
  }),
});

/** The relay for a controller pathname, or null when it is not one. */
export function perfProxyRoute(pathname) {
  return Object.hasOwn(PERF_PROXY_ROUTES, pathname)
    ? PERF_PROXY_ROUTES[pathname]
    : null;
}

/** How long the controller waits on the admin listener before answering 502. */
export const PERF_PROXY_TIMEOUT_MS = 5_000;

/**
 * A loopback port the kernel just handed out and this process released
 * again — the address to pass as `--metrics-addr` on the next spawn. The
 * ready file cannot report the admin address, so the controller picks it;
 * the window between this close and mecated's bind is the race
 * `isAddressInUse` lets the controller retry across.
 *
 * @returns {Promise<number>}
 */
export function freeLoopbackPort() {
  return new Promise((resolvePort, reject) => {
    const probe = net.createServer();
    probe.once("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const address = probe.address();
      const port = address && typeof address === "object" ? address.port : 0;
      probe.close((error) => {
        if (error) reject(error);
        else if (!port) reject(new Error("no loopback port was assigned"));
        else resolvePort(port);
      });
    });
  });
}

/**
 * Whether a failed start's stderr says a listener could not bind because
 * the address was taken — Go's `bind: address already in use` (mecated
 * wraps it as `listen admin "127.0.0.1:N": …`) or Node's EADDRINUSE.
 *
 * @param {unknown} text
 */
export function isAddressInUse(text) {
  return /address already in use|EADDRINUSE/i.test(String(text ?? ""));
}
