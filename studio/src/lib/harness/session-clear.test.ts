import { afterEach, describe, expect, it, vi } from "vitest";
import { resetHarnessClient } from "./sdk";
import {
  jsonResponse,
  problemResponse,
  sessionSnapshot,
  stubHarnessFetch,
} from "./sdk-test-stub";
import { clearHarnessSession, fetchHarnessSessionIdentity } from "./sessions";

/**
 * Pins the two client calls behind the `/clear` and `/session` built-ins as
 * the SDK spells them: the ClearSession successor RPC (`POST
 * /v1/sessions/{id}/clear`, an empty JSON body, the successor id adopted) and
 * the identity projection of the GET-session snapshot (id, title, state,
 * mode, resolved model, placement DISPLAY metadata, creation time).
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

const snapshotFor = (sessionId: string, extra?: Record<string, unknown>) =>
  jsonResponse(200, sessionSnapshot(sessionId, extra));

describe("clearHarnessSession", () => {
  it("POSTs the clear route with an empty body and adopts the successor id", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/clear")
        return jsonResponse(200, {
          session_id: "s2",
          placement: { kind: "local", label: "repo", branch: "main" },
        });
      if (request.path === "/v1/sessions/s2") return snapshotFor("s2");
      return undefined;
    });
    await expect(clearHarnessSession("s1")).resolves.toBe("s2");
    const clear = requests.find((r) => r.path === "/v1/sessions/s1/clear");
    expect(clear?.method).toBe("POST");
    // The SDK sends `{}`: the daemon decodes the body with unknown fields
    // disallowed and the browser names no placement (ADR 0291).
    expect(clear?.body ?? {}).toEqual({});
    // The successor handle is adopted: reading it issues no second GET.
    const before = requests.length;
    const identity = await fetchHarnessSessionIdentity("s2");
    expect(identity.id).toBe("s2");
    expect(requests.length).toBe(before + 1);
    expect(requests.at(-1)?.path).toBe("/v1/sessions/s2");
  });

  it("surfaces the daemon's refusal on a running source", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/clear")
        return problemResponse(
          412,
          "failed_precondition",
          "session is running",
        );
      return undefined;
    });
    await expect(clearHarnessSession("s1")).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 412,
      code: "failed_precondition",
    });
  });
});

describe("fetchHarnessSessionIdentity", () => {
  it("projects id, title, state, mode, model, placement and creation time", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1")
        return snapshotFor("s1", {
          title: "Fix the flaky test",
          state: "failed",
          mode: "accept_edits",
          created_at_unix: "1755000000",
          resolved_model: {
            provider_id: "openrouter",
            model_id: "openai/gpt-5",
            context_window: 400000,
          },
          placement: {
            kind: "worktree",
            label: "feature-x",
            branch: "feature/x",
            revision: "abc",
          },
        });
      return undefined;
    });
    await expect(fetchHarnessSessionIdentity("s1")).resolves.toEqual({
      id: "s1",
      title: "Fix the flaky test",
      state: "failed",
      mode: "acceptEdits",
      resolvedModel: {
        providerId: "openrouter",
        modelId: "openai/gpt-5",
        contextWindow: 400000,
        reasoningEffort: "",
      },
      placement: {
        kind: "worktree",
        label: "feature-x",
        branch: "feature/x",
        revision: "abc",
      },
      createdAtUnix: 1755000000,
      titleProvenance: "",
      kind: "",
      turns: 0,
      toolCalls: 0,
      limits: null,
      relationship: null,
    });
  });

  it("tolerates a daemon that echoes only the essentials", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1")
        return jsonResponse(200, { session_id: "s1", mode: "default" });
      return undefined;
    });
    const identity = await fetchHarnessSessionIdentity("s1");
    expect(identity).toMatchObject({
      id: "s1",
      title: "",
      mode: "default",
      resolvedModel: null,
      placement: null,
      createdAtUnix: 0,
    });
  });
});
