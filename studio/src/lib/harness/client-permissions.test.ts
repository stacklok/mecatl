import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fetchHarnessControlStatus,
  fetchHarnessPermissions,
  HarnessApiError,
  saveHarnessPermissions,
} from "./client";
import { resetHarnessClient } from "./sdk";

/**
 * The controller's permissions routes as the browser sees them through
 * /api/mecatl-control: the GET's config + operator-settings flag, the
 * status payload's saved-permissions mirror (null in external mode), and
 * the POST's exact JSON body — posture, trustProject, noShell and nothing
 * else — with a non-OK answer surfacing as a typed HarnessApiError.
 */

const jsonResponse = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("fetchHarnessPermissions", () => {
  it("reads the saved document, the trust-once grant and the operator-settings flag", async () => {
    const calls: string[] = [];
    vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
      calls.push(String(input));
      return jsonResponse(200, {
        config: { posture: "auto", trustProject: true, noShell: false },
        trustOnce: true,
        operatorSettings: true,
      });
    });
    await expect(fetchHarnessPermissions()).resolves.toEqual({
      config: {
        posture: "auto",
        trustProject: true,
        noShell: false,
        trustOnce: true,
      },
      operatorSettings: true,
    });
    expect(calls).toEqual(["/api/mecatl-control/permissions"]);
  });

  it("fills a sparse document with mecated's defaults and reads absent flags as false", async () => {
    vi.stubGlobal("fetch", async () => jsonResponse(200, { config: {} }));
    await expect(fetchHarnessPermissions()).resolves.toEqual({
      config: {
        posture: "strict",
        trustProject: false,
        noShell: false,
        trustOnce: false,
      },
      operatorSettings: false,
    });
  });

  it("returns null when the controller refuses (external mode's 409) or answers without a config", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(409, { error: "owned by the external deployment" }),
    );
    await expect(fetchHarnessPermissions()).resolves.toBeNull();
    vi.stubGlobal("fetch", async () => jsonResponse(200, { config: null }));
    await expect(fetchHarnessPermissions()).resolves.toBeNull();
  });
});

describe("fetchHarnessControlStatus permissions mirror", () => {
  it("maps the status payload's permissions and workspace", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(200, {
        mode: "managed",
        provider: "offline mock",
        running: true,
        workspace: "/srv/repo",
        permissions: {
          posture: "trusted",
          trustProject: false,
          noShell: true,
          trustOnce: false,
        },
      }),
    );
    const status = await fetchHarnessControlStatus();
    expect(status?.workspace).toBe("/srv/repo");
    expect(status?.permissions).toEqual({
      posture: "trusted",
      trustProject: false,
      noShell: true,
      trustOnce: false,
    });
  });

  it("reads external mode's permissions: null as null (the deployment owns them)", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(200, {
        mode: "external",
        provider: "external daemon",
        running: true,
        workspace: "/workspace/from-deployment",
        permissions: null,
      }),
    );
    const status = await fetchHarnessControlStatus();
    expect(status?.mode).toBe("external");
    expect(status?.permissions).toBeNull();
  });
});

describe("saveHarnessPermissions", () => {
  it("POSTs exactly the three flags as JSON", async () => {
    const requests: { url: string; init?: RequestInit }[] = [];
    vi.stubGlobal(
      "fetch",
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ url: String(input), init });
        return jsonResponse(200, { ok: true });
      },
    );
    await saveHarnessPermissions({
      posture: "yolo",
      trustProject: false,
      noShell: true,
    });
    expect(requests).toHaveLength(1);
    expect(requests[0].url).toBe("/api/mecatl-control/permissions");
    expect(requests[0].init?.method).toBe("POST");
    expect(new Headers(requests[0].init?.headers).get("content-type")).toBe(
      "application/json",
    );
    expect(JSON.parse(String(requests[0].init?.body))).toEqual({
      posture: "yolo",
      trustProject: false,
      noShell: true,
    });
  });

  it("throws a HarnessApiError carrying the controller's refusal", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(400, {
        error:
          'posture "yolo" refused: running as root (euid 0) without a declared sandbox (previous permissions restored)',
      }),
    );
    const error = await saveHarnessPermissions({
      posture: "yolo",
      trustProject: false,
      noShell: false,
    }).catch((caught) => caught);
    expect(error).toBeInstanceOf(HarnessApiError);
    expect((error as HarnessApiError).status).toBe(400);
    expect((error as HarnessApiError).message).toMatch(/refused/);
  });
});
