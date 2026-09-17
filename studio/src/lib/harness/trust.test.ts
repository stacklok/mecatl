import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchHarnessControlStatus, HarnessApiError } from "./client";
import { resetHarnessClient } from "./sdk";
import { stubHarnessFetch } from "./sdk-test-stub";
import {
  fetchDaemonSoulTrust,
  readTrustState,
  trustWorkspace,
  trustWorkspaceOnce,
} from "./trust";

/**
 * Project trust as the browser reads and grants it: the `/status.trust`
 * mirror of the controller's own registry (null in external mode and on an
 * older controller), the two BODYLESS grant POSTs and their exact paths,
 * and the daemon-side cross-check — a PROJECT soul the daemon reports
 * trusted means the daemon admitted the project from a registry Studio
 * cannot see.
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

describe("readTrustState", () => {
  it("reads the four fields and defaults a missing source to none", () => {
    expect(
      readTrustState({
        hasAuthority: true,
        decision: "drifted",
        source: "none",
        anchor: "abc",
      }),
    ).toEqual({
      hasAuthority: true,
      decision: "drifted",
      source: "none",
      anchor: "abc",
    });
    expect(readTrustState({ decision: "trusted", source: "posture" })).toEqual({
      hasAuthority: false,
      decision: "trusted",
      source: "posture",
      anchor: "",
    });
    expect(readTrustState({ decision: "once", source: "bogus" })?.source).toBe(
      "none",
    );
  });

  it("is null for external mode's trust: null, an older controller and an unknown decision", () => {
    expect(readTrustState(null)).toBeNull();
    expect(readTrustState(undefined)).toBeNull();
    expect(readTrustState("trusted")).toBeNull();
    expect(readTrustState({ hasAuthority: true })).toBeNull();
    expect(readTrustState({ decision: "maybe" })).toBeNull();
  });
});

describe("fetchHarnessControlStatus trust mirror", () => {
  it("maps /status.trust", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(200, {
        mode: "managed",
        provider: "offline mock",
        running: true,
        workspace: "/srv/repo",
        trust: {
          hasAuthority: true,
          decision: "untrusted",
          source: "none",
          anchor: "0".repeat(64),
        },
      }),
    );
    const status = await fetchHarnessControlStatus();
    expect(status?.trust).toEqual({
      hasAuthority: true,
      decision: "untrusted",
      source: "none",
      anchor: "0".repeat(64),
    });
  });

  it("reads external mode's trust: null (and an older controller's absence) as null", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(200, {
        mode: "external",
        provider: "external daemon",
        running: true,
        trust: null,
      }),
    );
    expect((await fetchHarnessControlStatus())?.trust).toBeNull();
    vi.stubGlobal("fetch", async () =>
      jsonResponse(200, { mode: "managed", provider: "mock", running: true }),
    );
    expect((await fetchHarnessControlStatus())?.trust).toBeNull();
  });
});

describe("the grant routes", () => {
  it("trustWorkspace POSTs bodyless to /permissions/trust", async () => {
    const requests: { url: string; init?: RequestInit }[] = [];
    vi.stubGlobal(
      "fetch",
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ url: String(input), init });
        return jsonResponse(200, { ok: true });
      },
    );
    await trustWorkspace();
    expect(requests).toEqual([
      {
        url: "/api/mecatl-control/permissions/trust",
        init: { method: "POST" },
      },
    ]);
  });

  it("trustWorkspaceOnce POSTs bodyless to /permissions/trust-once", async () => {
    const requests: { url: string; init?: RequestInit }[] = [];
    vi.stubGlobal(
      "fetch",
      async (input: RequestInfo | URL, init?: RequestInit) => {
        requests.push({ url: String(input), init });
        return jsonResponse(200, { ok: true });
      },
    );
    await trustWorkspaceOnce();
    expect(requests).toEqual([
      {
        url: "/api/mecatl-control/permissions/trust-once",
        init: { method: "POST" },
      },
    ]);
  });

  it("surfaces a refusal (external mode's 409) as a HarnessApiError", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(409, {
        error: "This setting is owned by the external mecated deployment.",
      }),
    );
    for (const grant of [trustWorkspace, trustWorkspaceOnce]) {
      const error = await grant().catch((caught) => caught);
      expect(error).toBeInstanceOf(HarnessApiError);
      expect((error as HarnessApiError).status).toBe(409);
    }
  });
});

describe("fetchDaemonSoulTrust", () => {
  it("reads a trusted PROJECT soul (the daemon admitted the project) off GET /v1/soul", async () => {
    const stub = stubHarnessFetch((request) =>
      request.path === "/v1/soul"
        ? { soul: { present: true, provenance: 2, trusted: true } }
        : undefined,
    );
    await expect(fetchDaemonSoulTrust()).resolves.toEqual({
      projectSoul: true,
      trusted: true,
    });
    expect(stub.last().method).toBe("GET");
  });

  it("reads a dropped untrusted project soul, and a user soul as not a project soul", async () => {
    stubHarnessFetch((request) =>
      request.path === "/v1/soul"
        ? { soul: { present: false, provenance: 2, trusted: false } }
        : undefined,
    );
    await expect(fetchDaemonSoulTrust()).resolves.toEqual({
      projectSoul: true,
      trusted: false,
    });
    await resetHarnessClient();
    stubHarnessFetch((request) =>
      request.path === "/v1/soul"
        ? { soul: { present: true, provenance: 1, trusted: true } }
        : undefined,
    );
    await expect(fetchDaemonSoulTrust()).resolves.toEqual({
      projectSoul: false,
      trusted: true,
    });
  });

  it("is null when the daemon cannot answer — never a thrown error, never a guess", async () => {
    stubHarnessFetch(() => undefined);
    await expect(fetchDaemonSoulTrust()).resolves.toBeNull();
    await resetHarnessClient();
    vi.stubGlobal("fetch", async () => {
      throw new TypeError("Failed to fetch");
    });
    await expect(fetchDaemonSoulTrust()).resolves.toBeNull();
  });
});
