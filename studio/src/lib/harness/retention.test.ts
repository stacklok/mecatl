import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchHarnessControlStatus } from "./client";
import {
  EMPTY_RETENTION_SETTINGS,
  type HarnessRetentionSettings,
  readRetentionState,
  saveHarnessRetention,
} from "./retention";

/**
 * The controller-side retention mirror: `/status.retention` decodes to the
 * saved document + who manages it, external mode / an older controller
 * decode to null, and `POST /retention` sends the document verbatim and
 * surfaces the controller's 400 (the acknowledgement gate, a refused
 * restart) as the typed error.
 */

const jsonResponse = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const saved: HarnessRetentionSettings = {
  main: { maxAge: "720h", maxCount: 100 },
  child: { maxAge: null, maxCount: 250 },
  scheduled: { maxAge: "0", maxCount: null },
  sweepCadence: "30m",
  acknowledgeMainDeletion: true,
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("readRetentionState", () => {
  it("decodes the saved document and who manages it", () => {
    expect(
      readRetentionState({ settings: saved, managedBy: "operator-settings" }),
    ).toEqual({ settings: saved, managedBy: "operator-settings" });
    expect(
      readRetentionState({ settings: saved, managedBy: "studio" }),
    ).toEqual({ settings: saved, managedBy: "studio" });
  });

  it("fills a sparse or malformed document with nulls, never with numbers", () => {
    expect(
      readRetentionState({
        settings: {
          main: { maxAge: 168, maxCount: "5" },
          child: null,
          acknowledgeMainDeletion: "yes",
        },
      }),
    ).toEqual({ settings: EMPTY_RETENTION_SETTINGS, managedBy: "studio" });
  });

  it("reads external mode's null and an older controller's absence as null", () => {
    expect(readRetentionState(null)).toBeNull();
    expect(readRetentionState(undefined)).toBeNull();
    expect(readRetentionState({ managedBy: "studio" })).toBeNull();
    expect(readRetentionState("x")).toBeNull();
  });
});

describe("fetchHarnessControlStatus", () => {
  it("carries the controller's retention mirror", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(200, {
          mode: "managed",
          provider: "offline mock",
          retention: { settings: saved, managedBy: "studio" },
        }),
      ),
    );
    const status = await fetchHarnessControlStatus();
    expect(status?.retention).toEqual({ settings: saved, managedBy: "studio" });
  });

  it("reads external mode's retention: null as null", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(200, { mode: "external", retention: null }),
      ),
    );
    expect((await fetchHarnessControlStatus())?.retention).toBeNull();
  });
});

describe("saveHarnessRetention", () => {
  it("POSTs the document to /retention and returns the controller's mirror", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse(200, {
        ok: true,
        retention: { settings: saved, managedBy: "studio" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const state = await saveHarnessRetention(saved);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as unknown as [
      string,
      RequestInit,
    ];
    expect(url).toBe("/api/mecatl-control/retention");
    expect(init.method).toBe("POST");
    expect(JSON.parse(String(init.body))).toEqual(saved);
    expect(state).toEqual({ settings: saved, managedBy: "studio" });
  });

  it("surfaces the controller's refusal as the typed error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(400, {
          error:
            "Acknowledge automatic deletion of main chats before enabling main retention",
        }),
      ),
    );
    await expect(
      saveHarnessRetention({ ...saved, acknowledgeMainDeletion: false }),
    ).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 400,
      message: /Acknowledge automatic deletion/,
    });
  });
});
