import { afterEach, describe, expect, it, vi } from "vitest";
import {
  fetchHarnessControlStatus,
  HarnessApiError,
  readStorageState,
  saveHarnessStorageSettings,
  storageSettingsBody,
} from "./client";
import { resetHarnessClient } from "./sdk";

/**
 * The controller's session-store surface as the browser sees it through
 * /api/mecatl-control: the status payload's `storage` mirror (null in
 * external mode and against an older controller), and the POST /storage
 * body — persistence plus the location ONLY when one was given, never an
 * empty string — with a non-OK answer surfacing as a typed HarnessApiError.
 */

const jsonResponse = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const managedStatus = (storage: unknown) => ({
  mode: "managed",
  provider: "offline mock",
  isMock: true,
  running: true,
  storage,
});

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("readStorageState", () => {
  it("decodes a durable store", () => {
    expect(
      readStorageState({
        persistence: "durable",
        dir: "/repo/.scratch/studio-sessions",
        storeDir: ".scratch/studio-sessions",
        defaultDir: "/repo/.scratch/studio-sessions",
        defaultPersistence: "durable",
        managedBy: "studio",
      }),
    ).toEqual({
      persistence: "durable",
      dir: "/repo/.scratch/studio-sessions",
      storeDir: ".scratch/studio-sessions",
      defaultDir: "/repo/.scratch/studio-sessions",
      defaultPersistence: "durable",
    });
  });

  it("decodes in-memory with an empty resolved dir and fills absent strings", () => {
    expect(readStorageState({ persistence: "memory" })).toEqual({
      persistence: "memory",
      dir: "",
      storeDir: "",
      defaultDir: "",
      defaultPersistence: "durable",
    });
  });

  it("is null for external mode, an older controller, or a document with no known mode", () => {
    expect(readStorageState(null)).toBeNull();
    expect(readStorageState(undefined)).toBeNull();
    expect(readStorageState("durable")).toBeNull();
    expect(readStorageState({ persistence: "disk", dir: "/x" })).toBeNull();
  });
});

describe("fetchHarnessControlStatus storage mirror", () => {
  it("carries the controller's storage document", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(
        200,
        managedStatus({
          persistence: "durable",
          dir: "/repo/.scratch/studio-sessions",
          storeDir: ".scratch/studio-sessions",
          defaultDir: "/repo/.scratch/studio-sessions",
          defaultPersistence: "durable",
        }),
      ),
    );
    const status = await fetchHarnessControlStatus();
    expect(status?.storage).toEqual({
      persistence: "durable",
      dir: "/repo/.scratch/studio-sessions",
      storeDir: ".scratch/studio-sessions",
      defaultDir: "/repo/.scratch/studio-sessions",
      defaultPersistence: "durable",
    });
  });

  it("reads external mode's storage: null and an older controller's absence as null", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(200, { mode: "external", storage: null }),
    );
    expect((await fetchHarnessControlStatus())?.storage).toBeNull();
    vi.stubGlobal("fetch", async () =>
      jsonResponse(200, { mode: "managed", provider: "offline mock" }),
    );
    expect((await fetchHarnessControlStatus())?.storage).toBeNull();
  });
});

describe("storageSettingsBody", () => {
  it("sends the trimmed location when one was given and OMITS it when blank", () => {
    expect(
      storageSettingsBody({ persistence: "durable", storeDir: "  /data/s " }),
    ).toEqual({ persistence: "durable", storeDir: "/data/s" });
    expect(
      storageSettingsBody({ persistence: "memory", storeDir: "  " }),
    ).toEqual({ persistence: "memory" });
    expect(storageSettingsBody({ persistence: "durable" })).toEqual({
      persistence: "durable",
    });
  });
});

describe("saveHarnessStorageSettings", () => {
  it("POSTs the exact JSON body to /storage and returns the controller's echoed storage", async () => {
    const calls: Array<{ url: string; init?: RequestInit }> = [];
    vi.stubGlobal(
      "fetch",
      async (input: RequestInfo | URL, init?: RequestInit) => {
        calls.push({ url: String(input), init });
        return jsonResponse(200, {
          ok: true,
          storage: {
            persistence: "memory",
            dir: "",
            storeDir: "/data/s",
            defaultDir: "/repo/.scratch/studio-sessions",
            defaultPersistence: "durable",
          },
        });
      },
    );
    const echoed = await saveHarnessStorageSettings({
      persistence: "memory",
      storeDir: "/data/s",
    });
    expect(calls).toHaveLength(1);
    expect(calls[0].url).toBe("/api/mecatl-control/storage");
    expect(calls[0].init?.method).toBe("POST");
    expect(calls[0].init?.headers).toEqual({
      "Content-Type": "application/json",
    });
    expect(JSON.parse(String(calls[0].init?.body))).toEqual({
      persistence: "memory",
      storeDir: "/data/s",
    });
    expect(echoed?.persistence).toBe("memory");
    expect(echoed?.storeDir).toBe("/data/s");
  });

  it("never sends an empty storeDir — the key is omitted", async () => {
    let body = "";
    vi.stubGlobal(
      "fetch",
      async (_input: RequestInfo | URL, init?: RequestInit) => {
        body = String(init?.body);
        return jsonResponse(200, { ok: true });
      },
    );
    await saveHarnessStorageSettings({ persistence: "durable", storeDir: "" });
    expect(JSON.parse(body)).toEqual({ persistence: "durable" });
  });

  it("surfaces a refusal (rollback 400, external 409) as a typed HarnessApiError with the server's words", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(400, {
        error:
          "EACCES: permission denied, mkdir '/root/x' (previous session store restored)",
      }),
    );
    await expect(
      saveHarnessStorageSettings({
        persistence: "durable",
        storeDir: "/root/x",
      }),
    ).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 400,
      message: expect.stringMatching(/previous session store restored/),
    });
    vi.stubGlobal("fetch", async () =>
      jsonResponse(409, {
        error: "This setting is owned by the external mecated deployment.",
      }),
    );
    const refused = await saveHarnessStorageSettings({
      persistence: "memory",
    }).catch((error: unknown) => error);
    expect(refused).toBeInstanceOf(HarnessApiError);
    expect((refused as HarnessApiError).status).toBe(409);
  });
});
