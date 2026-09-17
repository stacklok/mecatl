import { afterEach, describe, expect, it, vi } from "vitest";
import {
  EMPTY_DAEMON_DEFAULTS,
  fetchHarnessControlStatus,
  fetchHarnessDaemonDefaults,
  HarnessApiError,
  readDaemonDefaults,
  saveHarnessDaemonDefaults,
  validateDaemonDefaults,
} from "./client";
import { resetHarnessClient } from "./sdk";

/**
 * The controller's daemon-defaults routes as the browser sees them through
 * /api/mecatl-control: the GET's normalised document, the status payload's
 * mirror (null in external mode / on an older controller), and the PUT's
 * exact JSON body — the document WITHOUT the controller-owned active
 * provider — with a refusal surfacing as a typed HarnessApiError carrying
 * the controller's (and mecated's) own message.
 */

const jsonResponse = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const saved = {
  ...EMPTY_DAEMON_DEFAULTS,
  models: { openrouter: { defaultModel: "org/model", subagentModel: "" } },
  reasoningEffort: "high",
  activeProvider: "openrouter",
};

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("fetchHarnessDaemonDefaults", () => {
  it("reads and normalises the saved document", async () => {
    const calls: string[] = [];
    vi.stubGlobal("fetch", async (input: RequestInfo | URL) => {
      calls.push(String(input));
      return jsonResponse(200, { defaults: saved, managedBy: "studio" });
    });
    await expect(fetchHarnessDaemonDefaults()).resolves.toEqual(saved);
    expect(calls).toEqual(["/api/mecatl-control/daemon-defaults"]);
  });

  it("throws the controller's refusal (external mode's 409) as a HarnessApiError", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(409, {
        error: "This setting is owned by the external mecated deployment.",
      }),
    );
    await expect(fetchHarnessDaemonDefaults()).rejects.toBeInstanceOf(
      HarnessApiError,
    );
    await expect(fetchHarnessDaemonDefaults()).rejects.toThrow(
      /external mecated deployment/,
    );
  });
});

describe("readDaemonDefaults", () => {
  it("returns null for a missing or malformed payload and the empty document for {}", () => {
    expect(readDaemonDefaults(undefined)).toBeNull();
    expect(readDaemonDefaults(null)).toBeNull();
    expect(readDaemonDefaults("nope")).toBeNull();
    expect(readDaemonDefaults([])).toBeNull();
    expect(readDaemonDefaults({})).toEqual(EMPTY_DAEMON_DEFAULTS);
    // An out-of-grammar document reads as absent, never half-typed.
    expect(readDaemonDefaults({ reasoningEffort: "ultra" })).toBeNull();
  });
});

describe("fetchHarnessControlStatus", () => {
  it("mirrors daemonDefaults, settingsFile and the ToolHive base URL off /status", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(200, {
        mode: "managed",
        provider: "OpenRouter",
        daemonDefaults: saved,
        settingsFile: "/home/op/.config/mecatl/settings.yaml",
        toolhiveGateway: {
          available: true,
          active: false,
          baseURL: "http://127.0.0.1:14000/v1",
        },
      }),
    );
    const status = await fetchHarnessControlStatus();
    expect(status?.daemonDefaults).toEqual(saved);
    expect(status?.settingsFile).toBe("/home/op/.config/mecatl/settings.yaml");
    expect(status?.toolhiveGateway).toEqual({
      available: true,
      active: false,
      baseURL: "http://127.0.0.1:14000/v1",
    });
  });

  it("reads an absent mirror (external mode, older controller) as null", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(200, { mode: "external", provider: "external daemon" }),
    );
    const status = await fetchHarnessControlStatus();
    expect(status?.daemonDefaults).toBeNull();
    expect(status?.settingsFile).toBeUndefined();
  });
});

describe("saveHarnessDaemonDefaults", () => {
  it("PUTs the document without activeProvider and returns the controller's echo", async () => {
    let recorded: { method?: string; body?: unknown; type?: string } = {};
    vi.stubGlobal(
      "fetch",
      async (input: RequestInfo | URL, init?: RequestInit) => {
        recorded = {
          method: init?.method,
          body: JSON.parse(String(init?.body)),
          type: new Headers(init?.headers).get("Content-Type") ?? undefined,
        };
        expect(String(input)).toBe("/api/mecatl-control/daemon-defaults");
        return jsonResponse(200, { ok: true, defaults: saved });
      },
    );
    await expect(
      saveHarnessDaemonDefaults({ ...saved, activeProvider: "anthropic" }),
    ).resolves.toEqual(saved);
    expect(recorded.method).toBe("PUT");
    expect(recorded.type).toBe("application/json");
    expect(recorded.body).not.toHaveProperty("activeProvider");
    expect(recorded.body).toMatchObject({
      models: { openrouter: { defaultModel: "org/model", subagentModel: "" } },
      reasoningEffort: "high",
    });
  });

  it("surfaces mecated's fail-fast refusal verbatim", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(400, {
        error:
          'OpenRouter could not start. mecated: --default-model "nope": not catalogued for the default provider "openrouter"',
      }),
    );
    await expect(saveHarnessDaemonDefaults(saved)).rejects.toThrow(
      /not catalogued for the default provider/,
    );
  });
});

describe("validateDaemonDefaults", () => {
  it("applies the controller's grammar client-side and drops the active provider", () => {
    expect(
      validateDaemonDefaults({ ...saved, reasoningEffort: " Low " }),
    ).toEqual({ ...saved, reasoningEffort: "low", activeProvider: null });
    expect(() =>
      validateDaemonDefaults({ ...saved, slots: { router: "fast" } }),
    ).toThrow(/reserved for model routing/);
  });
});
