import { afterEach, describe, expect, it, vi } from "vitest";
import { createHarnessDebugSession } from "./debug";
import { resetHarnessClient } from "./sdk";
import {
  jsonResponse,
  problemResponse,
  type RecordedRequest,
  stubHarnessFetch,
} from "./sdk-test-stub";
import {
  createHarnessSession,
  createThreadHarnessSession,
  forkHarnessSessionToModel,
  ThreadSourceBusyError,
} from "./sessions";

/**
 * Compatibility pin (requirement H4): the daemon strictly decodes the
 * POST /v1/sessions create body — an unknown or mis-cased key (protojson
 * camelCase included) is a 400 naming the field. These tests freeze the exact
 * field sets the SDK emits for Studio's create paths so a stray key fails
 * HERE, not as a baffling runtime 400. Forks ride their own route
 * (`POST /v1/sessions/{id}/fork`) with their own field set; the create body
 * never carries a source id or a workspace (rule 2: placement is
 * server-owned, ADR 0291).
 */

const CREATE_ALLOWED = new Set(["mode", "model_id", "provider_id"]);
const FORK_ALLOWED = new Set(["title", "model_id", "provider_id"]);

/** The debug create (ADR 0254) is pinned as its OWN exact set. */
const DEBUG_ALLOWED = [
  "debug_mcp_servers",
  "debug_target_session_id",
  "mode",
  "profile",
] as const;

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

function captureCreates(): {
  requests: RecordedRequest[];
  body: () => Record<string, unknown>;
} {
  const { requests } = stubHarnessFetch((request) => {
    if (request.method === "POST")
      return jsonResponse(200, { session_id: "s-1" });
    return undefined;
  });
  return {
    requests,
    body: () => {
      const create = requests.find((r) => r.method === "POST");
      if (!create) throw new Error("no create request captured");
      return create.body as Record<string, unknown>;
    },
  };
}

describe("session create bodies stay inside the daemon's strict field set", () => {
  it("plain create with a model pick", async () => {
    const captured = captureCreates();
    await createHarnessSession("plan", {
      modelId: "m",
      providerId: "openrouter",
    });
    expect(captured.requests[0].path).toBe("/v1/sessions");
    const body = captured.body();
    const keys = Object.keys(body);
    expect(keys.every((k) => CREATE_ALLOWED.has(k))).toBe(true);
    // snake_case, never protojson camelCase; mode as the daemon's word.
    expect(keys.some((k) => /[A-Z]/.test(k))).toBe(false);
    expect(body).toEqual({
      mode: "plan",
      model_id: "m",
      provider_id: "openrouter",
    });
  });

  it("auto-routed create omits the model fields entirely", async () => {
    const captured = captureCreates();
    await createHarnessSession("default");
    expect(Object.keys(captured.body()).sort()).toEqual(["mode"]);
  });

  it("spells accept-edits the way the daemon's modeFromString reads it", async () => {
    const captured = captureCreates();
    await createHarnessSession("accept_edits");
    expect(captured.body()).toEqual({ mode: "accept_edits" });
  });

  it("thread create is a titled fork of the parent", async () => {
    const captured = captureCreates();
    await createThreadHarnessSession("parent-1", "Thread: x");
    expect(captured.requests[0].path).toBe("/v1/sessions/parent-1/fork");
    const keys = Object.keys(captured.body());
    expect(keys.every((k) => FORK_ALLOWED.has(k))).toBe(true);
    expect(captured.body()).toEqual({ title: "Thread: x" });
  });

  it("model-switch fork carries the model pick and the source title", async () => {
    const captured = captureCreates();
    await forkHarnessSessionToModel(
      "src-1",
      { modelId: "m", providerId: "openrouter" },
      "Title",
    );
    expect(captured.requests[0].path).toBe("/v1/sessions/src-1/fork");
    const keys = Object.keys(captured.body());
    expect(keys.every((k) => FORK_ALLOWED.has(k))).toBe(true);
    expect(captured.body()).toEqual({
      title: "Title",
      model_id: "m",
      provider_id: "openrouter",
    });
  });

  it("a busy fork source (412) surfaces as ThreadSourceBusyError", async () => {
    stubHarnessFetch(() =>
      problemResponse(412, "failed_precondition", "source session is running"),
    );
    await expect(
      createThreadHarnessSession("parent-1", "Thread: x"),
    ).rejects.toBeInstanceOf(ThreadSourceBusyError);
  });

  it("debug create (ADR 0254) stays inside its own exact set", async () => {
    const captured = captureCreates();
    await createHarnessDebugSession("target-1", { mcpServers: ["fetch"] });
    const body = captured.body();
    expect(Object.keys(body).sort()).toEqual([...DEBUG_ALLOWED]);
    expect(body.profile).toBe("no-fs");
    // Placement is server-owned: no workspace key, empty or otherwise.
    expect(Object.hasOwn(body, "workspace")).toBe(false);
  });
});
