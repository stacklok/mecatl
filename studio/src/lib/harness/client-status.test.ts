import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchHarnessControlStatus } from "./client";

/**
 * `GET /api/mecatl-control/status` → `HarnessControlStatus`: the parser
 * reads the controller's `workspace` / `defaultWorkspace` labels (the
 * managed daemon's spawn root and the controller's fallback — display only,
 * Studio rule 2), tolerates their absence on an older controller or the
 * external stub, and reports null — never a half-filled document — when the
 * controller does not answer.
 */

const statusBody = (extra: Record<string, unknown>) => ({
  mode: "managed",
  provider: "offline mock",
  isMock: true,
  running: true,
  configuredProviders: [],
  selectedProvider: "mock",
  authFile: "/home/dev/.config/mecatl/auth.yaml",
  ...extra,
});

function stubStatus(body: unknown, status = 200) {
  const fetchMock = vi.fn(
    async (_input: string | URL | Request, _init?: RequestInit) =>
      new Response(JSON.stringify(body), {
        status,
        headers: { "content-type": "application/json" },
      }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("fetchHarnessControlStatus", () => {
  it("parses the workspace root and the controller's default", async () => {
    const fetchMock = stubStatus(
      statusBody({
        workspace: "/home/dev/other-repo",
        defaultWorkspace: "/srv/checkout/mecatl",
      }),
    );
    const status = await fetchHarnessControlStatus();
    expect(fetchMock.mock.calls[0]?.[0]).toBe("/api/mecatl-control/status");
    expect(status?.mode).toBe("managed");
    expect(status?.workspace).toBe("/home/dev/other-repo");
    expect(status?.defaultWorkspace).toBe("/srv/checkout/mecatl");
  });

  it("tolerates a controller or stub that reports neither", async () => {
    stubStatus(statusBody({ mode: "external", provider: "external daemon" }));
    const status = await fetchHarnessControlStatus();
    expect(status?.mode).toBe("external");
    expect(status?.workspace).toBe("");
    expect(status?.defaultWorkspace).toBeUndefined();
  });

  it("ignores a non-string label rather than rendering it", async () => {
    stubStatus(statusBody({ workspace: 42, defaultWorkspace: null }));
    const status = await fetchHarnessControlStatus();
    expect(status?.workspace).toBe("");
    expect(status?.defaultWorkspace).toBeUndefined();
  });

  it("returns null when the controller does not answer", async () => {
    stubStatus({ error: "gone" }, 503);
    expect(await fetchHarnessControlStatus()).toBeNull();
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );
    expect(await fetchHarnessControlStatus()).toBeNull();
  });
});
