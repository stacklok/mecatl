import { afterEach, describe, expect, it, vi } from "vitest";
import {
  EMPTY_DAEMON_OPTIONS,
  fetchHarnessDaemonOptions,
  readDaemonOptions,
  readDaemonOptionsDoc,
  saveHarnessDaemonOptions,
} from "./daemon-options";
import { HarnessApiError } from "./errors";

/**
 * The controller's daemon-options routes as the browser sees them through
 * /api/mecatl-control: the GET's decoded document (defaults filled for an
 * older controller), the PUT's exact JSON body — the WHOLE document, the
 * browser never composes a flag — and a refusal (external mode's 409, a
 * directory outside the allowed roots' 400, mecated's own refusal after the
 * controller rolled back) surfacing as a typed HarnessApiError carrying
 * the controller's message.
 */

const jsonResponse = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });

const saved = {
  ...EMPTY_DAEMON_OPTIONS,
  skills: { enabled: false, dir: "" },
  mcp: { ...EMPTY_DAEMON_OPTIONS.mcp, toolhiveGroup: "team" },
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("fetchHarnessDaemonOptions", () => {
  it("reads the document, its defaults and the effective directories", async () => {
    const calls: [string, RequestInit | undefined][] = [];
    vi.stubGlobal(
      "fetch",
      async (input: RequestInfo | URL, init?: RequestInit) => {
        calls.push([String(input), init]);
        return jsonResponse(200, {
          options: saved,
          defaults: {
            skillsDir: "/repo/.mecatl/skills",
            memoryDir: "/repo/.scratch/studio-memory",
            userModelDir: "/home/me/.config/mecatl/usermodel",
            commandDirs: [".mecatl/commands", ".claude/commands"],
          },
          effective: {
            skillsDir: "/repo/.mecatl/skills",
            memoryDir: "/repo/.scratch/studio-memory",
            userModelDir: "",
            commandsDir: "",
          },
          allowedRoots: ["/repo", "/home/me/.config/mecatl"],
        });
      },
    );
    const doc = await fetchHarnessDaemonOptions();
    expect(calls.map(([url]) => url)).toEqual([
      "/api/mecatl-control/daemon-options",
    ]);
    expect(calls[0][1]?.method).toBeUndefined();
    expect(doc.options).toEqual(saved);
    expect(doc.defaults.userModelDir).toBe("/home/me/.config/mecatl/usermodel");
    expect(doc.effective.commandsDir).toBe("");
    expect(doc.allowedRoots).toEqual(["/repo", "/home/me/.config/mecatl"]);
  });

  it("throws the controller's refusal (external mode's 409) as a HarnessApiError", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(409, {
        error: "This setting is owned by the external mecated deployment.",
      }),
    );
    await expect(fetchHarnessDaemonOptions()).rejects.toBeInstanceOf(
      HarnessApiError,
    );
    await expect(fetchHarnessDaemonOptions()).rejects.toThrow(
      /external mecated deployment/,
    );
  });
});

describe("readDaemonOptions / readDaemonOptionsDoc", () => {
  it("fills a missing or malformed payload with the defaults", () => {
    expect(readDaemonOptions(undefined)).toEqual(EMPTY_DAEMON_OPTIONS);
    expect(readDaemonOptions("nope")).toEqual(EMPTY_DAEMON_OPTIONS);
    expect(
      readDaemonOptions({
        userModel: { enabled: "yes", reviewInterval: 0 },
        mcp: { toolhiveGroup: 4 },
      }),
    ).toEqual(EMPTY_DAEMON_OPTIONS);
    expect(
      readDaemonOptions({ userModel: { reviewInterval: 7, dir: "/x" } })
        .userModel,
    ).toEqual({ enabled: true, dir: "/x", reviewInterval: 7 });
  });

  it("gives an older controller's bare document the default placeholders", () => {
    const doc = readDaemonOptionsDoc({ options: saved });
    expect(doc.options).toEqual(saved);
    expect(doc.defaults.commandDirs).toEqual([
      ".mecatl/commands",
      ".claude/commands",
    ]);
    expect(doc.defaults.skillsDir).toBe("");
    expect(doc.allowedRoots).toEqual([]);
  });
});

describe("saveHarnessDaemonOptions", () => {
  it("PUTs the whole document as JSON and adopts the controller's echo", async () => {
    let recorded: { url: string; init: RequestInit | undefined } | null = null;
    vi.stubGlobal(
      "fetch",
      async (input: RequestInfo | URL, init?: RequestInit) => {
        recorded = { url: String(input), init };
        return jsonResponse(200, {
          ok: true,
          options: { ...saved, commands: { enabled: true, dir: "" } },
        });
      },
    );
    const echoed = await saveHarnessDaemonOptions(saved);
    expect(recorded).not.toBeNull();
    const call = recorded as unknown as {
      url: string;
      init: RequestInit | undefined;
    };
    expect(call.url).toBe("/api/mecatl-control/daemon-options");
    expect(call.init?.method).toBe("PUT");
    expect(call.init?.headers).toEqual({ "Content-Type": "application/json" });
    expect(JSON.parse(String(call.init?.body))).toEqual(saved);
    expect(echoed.commands).toEqual({ enabled: true, dir: "" });
  });

  it("surfaces a confined-directory refusal with the controller's message", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse(400, {
        error:
          "skills.dir must be inside the workspace /repo or /home/me/.config/mecatl",
      }),
    );
    await expect(
      saveHarnessDaemonOptions({
        ...saved,
        skills: { enabled: true, dir: "/etc" },
      }),
    ).rejects.toThrow(/skills\.dir must be inside the workspace/);
  });
});
