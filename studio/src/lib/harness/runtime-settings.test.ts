import { afterEach, describe, expect, it, vi } from "vitest";
import {
  approveHarnessSoulBaseline,
  EMPTY_RUNTIME_SETTINGS,
  fetchHarnessRuntimeSettings,
  HARNESS_LEARNING_MODES,
  HARNESS_LEARNING_SENSITIVITIES,
  type HarnessRuntimeSettings,
  readRuntimeSettings,
  readRuntimeSettingsDoc,
  saveHarnessRuntimeSettings,
} from "./runtime-settings";

/**
 * The controller's runtime-settings routes as the browser sees them through
 * /api/mecatl-control: the GET's document decoded defensively (an older
 * controller's sparse body still yields the default shape), the PUT's exact
 * JSON body and Content-Type, the bodiless approve POST, and a refusal
 * (external mode's 409, a soul path the controller rejects) surfacing as
 * the typed error carrying the controller's own message.
 */

const jsonResponse = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const saved: HarnessRuntimeSettings = {
  learning: { mode: "review", sensitivity: "eager" },
  steer: { enabled: false },
  soul: {
    enabled: true,
    strict: true,
    file: "/home/me/.config/mecatl/team.md",
  },
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("readRuntimeSettings", () => {
  it("mirrors the controller's closed vocabularies", () => {
    expect(HARNESS_LEARNING_MODES).toEqual(["off", "review", "auto"]);
    expect(HARNESS_LEARNING_SENSITIVITIES).toEqual([
      "conservative",
      "balanced",
      "eager",
    ]);
  });

  it("decodes a saved document verbatim", () => {
    expect(readRuntimeSettings(saved)).toEqual(saved);
  });

  it("falls back to the defaults for an unknown value or a sparse body, never to a value a select cannot show", () => {
    expect(
      readRuntimeSettings({
        learning: { mode: "sometimes", sensitivity: 3 },
        steer: { enabled: "no" },
        soul: { file: 7 },
      }),
    ).toEqual(EMPTY_RUNTIME_SETTINGS);
    expect(readRuntimeSettings(null)).toEqual(EMPTY_RUNTIME_SETTINGS);
  });
});

describe("readRuntimeSettingsDoc", () => {
  it("decodes the full document", () => {
    const doc = readRuntimeSettingsDoc({
      config: saved,
      managedBy: {
        learning: "operator-settings",
        steer: "studio",
        soul: "studio",
      },
      inherited: { learning: { mode: "auto", sensitivity: "" }, steer: false },
      effective: {
        learning: { mode: "auto", sensitivity: "balanced" },
        steer: false,
      },
      soulFileDefault: "/home/me/.config/mecatl/soul.md",
      soulCandidates: [
        { path: "/home/me/.config/mecatl/soul.md", name: "soul" },
        { path: "/home/me/.config/mecatl/team.md", name: "team" },
        { bogus: true },
      ],
    });
    expect(doc.config).toEqual(saved);
    expect(doc.managedBy.learning).toBe("operator-settings");
    expect(doc.inherited).toEqual({
      learning: { mode: "auto", sensitivity: "" },
      steer: false,
    });
    expect(doc.effective).toEqual({
      learning: { mode: "auto", sensitivity: "balanced" },
      steer: false,
    });
    expect(doc.soulFileDefault).toBe("/home/me/.config/mecatl/soul.md");
    expect(doc.soulCandidates).toEqual([
      { path: "/home/me/.config/mecatl/soul.md", name: "soul" },
      { path: "/home/me/.config/mecatl/team.md", name: "team" },
    ]);
  });

  it("fills an older controller's bare document with defaults and derives the effective fold", () => {
    const doc = readRuntimeSettingsDoc({
      config: { learning: { mode: "review" } },
    });
    expect(doc.config.learning.mode).toBe("review");
    expect(doc.managedBy).toEqual({
      learning: "studio",
      steer: "studio",
      soul: "studio",
    });
    expect(doc.inherited).toEqual({
      learning: { mode: "", sensitivity: "" },
      steer: null,
    });
    expect(doc.effective).toEqual({
      learning: { mode: "review", sensitivity: "balanced" },
      steer: true,
    });
    expect(doc.soulFileDefault).toBe("");
    expect(doc.soulCandidates).toEqual([]);
  });
});

describe("fetchHarnessRuntimeSettings", () => {
  it("GETs the controller document without caching", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse(200, { config: saved, soulFileDefault: "/x/soul.md" }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const doc = await fetchHarnessRuntimeSettings();
    expect(doc.config).toEqual(saved);
    expect(doc.soulFileDefault).toBe("/x/soul.md");
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/mecatl-control/runtime-settings",
      expect.objectContaining({ cache: "no-store" }),
    );
  });

  it("surfaces external mode's 409 as the typed error with the controller's message", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(409, {
        error: "This setting is owned by the external mecated deployment.",
      }),
    );
    await expect(fetchHarnessRuntimeSettings()).rejects.toMatchObject({
      status: 409,
      message: "This setting is owned by the external mecated deployment.",
    });
  });
});

describe("saveHarnessRuntimeSettings", () => {
  it("PUTs the whole document as JSON and returns the controller's echo", async () => {
    let recorded: { method?: string; body?: unknown; type?: string } = {};
    vi.stubGlobal(
      "fetch",
      async (input: RequestInfo | URL, init?: RequestInit) => {
        recorded = {
          method: init?.method,
          body: JSON.parse(String(init?.body)),
          type: new Headers(init?.headers).get("Content-Type") ?? undefined,
        };
        expect(String(input)).toBe("/api/mecatl-control/runtime-settings");
        return jsonResponse(200, { ok: true, config: saved });
      },
    );
    await expect(saveHarnessRuntimeSettings(saved)).resolves.toEqual(saved);
    expect(recorded.method).toBe("PUT");
    expect(recorded.type).toBe("application/json");
    expect(recorded.body).toEqual(saved);
  });

  it("surfaces the controller's refusal verbatim (a soul path outside the allowed roots)", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(400, {
        error:
          "soul.file must be inside /home/me/.config/mecatl or the workspace /work",
      }),
    );
    await expect(
      saveHarnessRuntimeSettings({
        ...saved,
        soul: { ...saved.soul, file: "/etc/passwd.md" },
      }),
    ).rejects.toMatchObject({
      status: 400,
      message:
        "soul.file must be inside /home/me/.config/mecatl or the workspace /work",
    });
  });
});

describe("approveHarnessSoulBaseline", () => {
  it("POSTs the bodiless approve route", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse(200, { ok: true, restarted: true }),
    );
    vi.stubGlobal("fetch", fetchMock);
    await expect(approveHarnessSoulBaseline()).resolves.toBeUndefined();
    expect(fetchMock).toHaveBeenCalledWith("/api/mecatl-control/soul/approve", {
      method: "POST",
    });
  });

  it("surfaces the disabled-persona refusal", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(409, {
        error:
          "The persona is disabled; enable it before accepting a baseline.",
      }),
    );
    await expect(approveHarnessSoulBaseline()).rejects.toMatchObject({
      status: 409,
    });
  });
});
