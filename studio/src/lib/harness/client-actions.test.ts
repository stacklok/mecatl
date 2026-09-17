import { PromptValidationError } from "@stacklok-oss/mecatl-sdk";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { StreamEvent } from "@/features/agent/types";
import { resetHarnessClient } from "./sdk";
import {
  dataFrame,
  jsonResponse,
  problemResponse,
  sessionSnapshot,
  sseResponse,
  stubHarnessFetch,
} from "./sdk-test-stub";
import {
  cancelHarnessRun,
  cancelHarnessSteer,
  compactHarnessSession,
  fetchHarnessSessionDetail,
  respondToHarnessApproval,
  retryHarnessRun,
  steerHarnessRun,
  streamHarnessPrompt,
} from "./sessions";

/**
 * Pins the request/response contracts of the chat-resilience client calls as
 * the SDK spells them: manual compaction (ADR 0244), the strict multimodal
 * steer + the cancel-steer route (ADR 0252), the run-scoped approve/cancel
 * controls (ADR 0249), the prompt/retry run relays, and the GET-session
 * resolved-model echo the context meter reads (B1).
 */

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

/** Answers the snapshot GET every session handle starts with. */
const snapshotFor = (sessionId: string, extra?: Record<string, unknown>) =>
  jsonResponse(200, sessionSnapshot(sessionId, extra));

describe("compactHarnessSession", () => {
  it("POSTs bodyless and reads the compacted bool", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/compact")
        return jsonResponse(200, { compacted: true });
      return undefined;
    });
    await expect(compactHarnessSession("s1")).resolves.toBe(true);
    const compact = requests.find((r) => r.path === "/v1/sessions/s1/compact");
    expect(compact?.method).toBe("POST");
    expect(compact?.body).toBeUndefined();
  });

  it("reads an empty answer as nothing-to-compact (the live daemon omits false)", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/compact")
        return jsonResponse(200, {});
      return undefined;
    });
    await expect(compactHarnessSession("s1")).resolves.toBe(false);
  });

  it("throws the typed error on a refusal", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/compact")
        return problemResponse(
          412,
          "failed_precondition",
          "session is running",
        );
      return undefined;
    });
    await expect(compactHarnessSession("s1")).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 412,
      code: "failed_precondition",
    });
  });
});

describe("steerHarnessRun", () => {
  it("sends the strict body: text, message_id, parts, and expected_run_id (ADR 0252)", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/steer")
        return jsonResponse(200, { outcome: "accepted", message_id: "m-1" });
      return undefined;
    });
    const result = await steerHarnessRun("s1", "focus", "m-1", {
      expectedRunId: "run-9",
      parts: [{ kind: "image", mime_type: "image/png", data: "aGk=" }],
    });
    expect(result).toEqual({ outcome: "accepted", messageId: "m-1" });
    const steer = requests.find((r) => r.path === "/v1/sessions/s1/steer");
    expect(steer?.method).toBe("POST");
    expect(steer?.body).toEqual({
      text: "focus",
      message_id: "m-1",
      expected_run_id: "run-9",
      parts: [{ kind: "image", mime_type: "image/png", data: "aGk=" }],
    });
  });

  it("caches the session handle: two controls cost one snapshot GET", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/steer")
        return jsonResponse(200, { outcome: "appended" });
      return undefined;
    });
    await steerHarnessRun("s1", "one", "m-2", { expectedRunId: "run-1" });
    await steerHarnessRun("s1", "two", "m-3", { expectedRunId: "run-1" });
    expect(requests.filter((r) => r.path === "/v1/sessions/s1")).toHaveLength(
      1,
    );
    // A part-less steer carries an empty parts list, never an invented one.
    const steer = requests.find((r) => r.path === "/v1/sessions/s1/steer");
    expect(steer?.body).toEqual({
      text: "one",
      message_id: "m-2",
      expected_run_id: "run-1",
      parts: [],
    });
  });

  it("surfaces the strict 409 as the typed stale_run_control error", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/steer")
        return problemResponse(
          409,
          "stale_run_control",
          "the named run already ended",
        );
      return undefined;
    });
    await expect(
      steerHarnessRun("s1", "focus", "m-3", { expectedRunId: "run-old" }),
    ).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 409,
      code: "stale_run_control",
      message: "the named run already ended",
    });
  });

  it("refuses a steer with no run to name as stale, without touching the wire", async () => {
    const { requests } = stubHarnessFetch(() => undefined);
    await expect(
      steerHarnessRun("s1", "focus", "m-4", { expectedRunId: "" }),
    ).rejects.toMatchObject({ code: "stale_run_control" });
    expect(requests).toHaveLength(0);
  });
});

describe("cancelHarnessSteer", () => {
  it("POSTs the ADR-0252 cancel-steer route scoped to the run", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/cancel-steer")
        return jsonResponse(200, { outcome: "retracted" });
      return undefined;
    });
    await expect(cancelHarnessSteer("s1", "run-9")).resolves.toBe("retracted");
    const cancel = requests.find(
      (r) => r.path === "/v1/sessions/s1/cancel-steer",
    );
    expect(cancel?.method).toBe("POST");
    expect(cancel?.body).toEqual({ expected_run_id: "run-9", message_id: "" });
  });

  it("answers none_pending for an unknown run — nothing could be waiting", async () => {
    const { requests } = stubHarnessFetch(() => undefined);
    await expect(cancelHarnessSteer("s1", "")).resolves.toBe("none_pending");
    expect(requests).toHaveLength(0);
  });
});

describe("respondToHarnessApproval / cancelHarnessRun", () => {
  it("sends the run-scoped approve body (ADR 0249)", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/approve")
        return new Response(null, { status: 204 });
      return undefined;
    });
    await respondToHarnessApproval("s1", "ask-1", "allow_always", "run-9");
    const approve = requests.find((r) => r.path === "/v1/sessions/s1/approve");
    expect(approve?.method).toBe("POST");
    expect(approve?.body).toEqual({
      allow: true,
      ask_id: "ask-1",
      expected_run_id: "run-9",
      verdict: "allow_always",
    });
  });

  it("relays a stale approve as the typed code the dialog branches on", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/approve")
        return problemResponse(409, "stale_run_control", "run ended");
      return undefined;
    });
    await expect(
      respondToHarnessApproval("s1", "ask-1", "deny", "run-old"),
    ).rejects.toMatchObject({ status: 409, code: "stale_run_control" });
    await expect(
      respondToHarnessApproval("s1", "ask-1", "deny", ""),
    ).rejects.toMatchObject({ code: "stale_run_control" });
  });

  it("cancels the named run and swallows a stale refusal (fire-and-forget), reporting the outcome", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/cancel")
        return problemResponse(409, "stale_run_control", "run ended");
      return undefined;
    });
    // A stale refusal never throws — and says the run had already ended, so
    // the turn is not labelled cancelled.
    await expect(cancelHarnessRun("s1", "run-9")).resolves.toBe("stale");
    const cancel = requests.find((r) => r.path === "/v1/sessions/s1/cancel");
    expect(cancel?.body).toEqual({ expected_run_id: "run-9" });
    // No run id → nothing to name → no request.
    await expect(cancelHarnessRun("s1", "")).resolves.toBe("stale");
    expect(
      requests.filter((r) => r.path === "/v1/sessions/s1/cancel"),
    ).toHaveLength(1);
  });

  it("reports an accepted cancel, and an unknown outcome for a transport fault", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/cancel")
        return new Response(null, { status: 204 });
      return undefined;
    });
    await expect(cancelHarnessRun("s1", "run-9")).resolves.toBe("cancelled");
    await resetHarnessClient();
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/cancel")
        return problemResponse(500, "internal", "boom");
      return undefined;
    });
    await expect(cancelHarnessRun("s1", "run-9")).resolves.toBe("unknown");
  });
});

describe("streamHarnessPrompt", () => {
  it("POSTs the prompt with SDK-encoded media parts and relays every event", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/prompt")
        return sseResponse([
          dataFrame({ type: "turn.start", run_id: "run-1" }),
          dataFrame({ type: "message.delta", text: "hi", run_id: "run-1" }),
          dataFrame({
            type: "result",
            run_id: "run-1",
            result: { stop: "end_turn", text: "hi" },
          }),
        ]);
      return undefined;
    });
    const events: StreamEvent[] = [];
    const started: string[] = [];
    await streamHarnessPrompt(
      "s1",
      "hello",
      [{ kind: "image", mime_type: "image/png", data: "aGk=" }],
      (event) => events.push(event),
      undefined,
      { onRunStarted: (runId) => started.push(runId) },
    );
    const prompt = requests.find((r) => r.path === "/v1/sessions/s1/prompt");
    expect(prompt?.method).toBe("POST");
    expect(prompt?.body).toEqual({
      text: "hello",
      parts: [{ kind: "image", mime_type: "image/png", data: "aGk=", url: "" }],
    });
    expect(started).toEqual(["run-1"]);
    expect(events).toEqual([
      { type: "token", text: "hi", runId: "run-1" },
      {
        type: "run_result",
        stop: "end_turn",
        text: "hi",
        errorText: "",
        permanent: false,
        runId: "run-1",
      },
    ]);
  });

  it("fails loudly when the stream closes before the terminal result", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/prompt")
        return sseResponse([
          dataFrame({ type: "message.delta", text: "hi", run_id: "run-1" }),
        ]);
      return undefined;
    });
    await expect(
      streamHarnessPrompt("s1", "hello", [], () => undefined),
    ).rejects.toThrow("closed before Mecatl returned a final result");
  });

  it("refuses a media part the session's modalities reject with the SDK's TYPED error, before any prompt request", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1")
        return snapshotFor("s1", {
          session_capabilities: { image: false, audio: false },
        });
      return undefined;
    });
    const attempt = streamHarnessPrompt(
      "s1",
      "look",
      [{ kind: "image", mime_type: "image/png", data: "aGk=" }],
      () => undefined,
    );
    await expect(attempt).rejects.toBeInstanceOf(PromptValidationError);
    await expect(attempt).rejects.toMatchObject({ reason: "capability" });
    expect(requests.some((r) => r.path === "/v1/sessions/s1/prompt")).toBe(
      false,
    );
  });
});

describe("retryHarnessRun", () => {
  it("POSTs the retry route bodyless and relays the SSE exactly like a prompt (B2.2)", async () => {
    const { requests } = stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/retry")
        return sseResponse([
          dataFrame({
            type: "model.retry",
            run_id: "run-2",
            model_retry: { retry_disposition: 2 },
          }),
          dataFrame({
            type: "message.delta",
            text: "resumed",
            run_id: "run-2",
          }),
          dataFrame({
            type: "result",
            run_id: "run-2",
            result: { stop: "end_turn", text: "done" },
          }),
        ]);
      return undefined;
    });
    const events: StreamEvent[] = [];
    await retryHarnessRun("s1", (event) => events.push(event));
    const retry = requests.find((r) => r.path === "/v1/sessions/s1/retry");
    expect(retry?.method).toBe("POST");
    expect(retry?.body).toBeUndefined();
    expect(events).toEqual([
      { type: "notice", text: "Retrying the failed step…", runId: "run-2" },
      { type: "token", text: "resumed", runId: "run-2" },
      {
        type: "run_result",
        stop: "end_turn",
        text: "done",
        errorText: "",
        permanent: false,
        runId: "run-2",
      },
    ]);
  });

  it("surfaces the 409 ineligibility as the typed code", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      if (request.path === "/v1/sessions/s1/retry")
        return problemResponse(
          409,
          "failed_step_retry_ineligible",
          "retry is not eligible",
        );
      return undefined;
    });
    await expect(retryHarnessRun("s1", () => {})).rejects.toMatchObject({
      name: "HarnessApiError",
      status: 409,
      code: "failed_step_retry_ineligible",
    });
  });
});

describe("fetchHarnessSessionDetail", () => {
  it("decodes the resolved_model echo and the capabilities object (B1)", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1")
        return snapshotFor("s1", {
          resolved_model: {
            provider_id: "openrouter",
            model_id: "openai/gpt-5",
            context_window: 400000,
          },
          capabilities: { manual_compaction: true },
        });
      return undefined;
    });
    const detail = await fetchHarnessSessionDetail("s1");
    expect(detail.resolvedModel).toEqual({
      providerId: "openrouter",
      modelId: "openai/gpt-5",
      contextWindow: 400000,
      reasoningEffort: "",
    });
    expect(detail.capabilities).toMatchObject({ manualCompaction: true });
  });

  it("reads the EFFECTIVE reasoning-effort tier off resolved_model.reasoning_effort", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1")
        return snapshotFor("s1", {
          resolved_model: {
            provider_id: "openrouter",
            model_id: "openai/gpt-5",
            context_window: 400000,
            reasoning_effort: "medium",
          },
        });
      return undefined;
    });
    const detail = await fetchHarnessSessionDetail("s1");
    expect(detail.resolvedModel?.reasoningEffort).toBe("medium");
  });

  it("tolerates a daemon that echoes neither field", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1")
        return jsonResponse(200, { session_id: "s1", mode: "default" });
      return undefined;
    });
    await expect(fetchHarnessSessionDetail("s1")).resolves.toEqual({
      resolvedModel: null,
      placement: null,
      capabilities: {},
      tokenUsage: null,
      sessionCapabilities: null,
    });
  });

  it("projects the placement's display metadata for the header badge (ADR 0291)", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1")
        return snapshotFor("s1", {
          placement: {
            kind: "git-worktree",
            label: "feature-x",
            branch: "feature/x",
            revision: "0123456789abcdef",
          },
        });
      return undefined;
    });
    const detail = await fetchHarnessSessionDetail("s1");
    expect(detail.placement).toEqual({
      kind: "git-worktree",
      label: "feature-x",
      branch: "feature/x",
      revision: "0123456789abcdef",
    });
  });

  it("reports no placement when the snapshot names neither a label nor a kind", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1")
        return snapshotFor("s1", {
          placement: { kind: "", label: "", branch: "main", revision: "" },
        });
      return undefined;
    });
    const detail = await fetchHarnessSessionDetail("s1");
    expect(detail.placement).toBeNull();
  });

  it("maps session_capabilities to the composer's image/audio gate (proto field 21)", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1")
        return snapshotFor("s1", {
          session_capabilities: { image: true, audio: false },
        });
      return undefined;
    });
    const detail = await fetchHarnessSessionDetail("s1");
    expect(detail.sessionCapabilities).toEqual({ image: true, audio: false });
  });

  it("carries the debug binding, its MCP servers and mounted tools off an AI-debug snapshot (ADR 0254)", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/dbg-1")
        return snapshotFor("dbg-1", {
          kind: "debug",
          relationship: { debug_target_session_id: "target-1" },
          debug_mcp_servers: ["github", ""],
          debug_mcp_tools: ["mcp__github__create_issue", ""],
        });
      return undefined;
    });
    const detail = await fetchHarnessSessionDetail("dbg-1");
    expect(detail.debugTargetSessionId).toBe("target-1");
    expect(detail.debugMcpServers).toEqual(["github"]);
    expect(detail.debugMcpTools).toEqual(["mcp__github__create_issue"]);
  });

  it("omits every debug field on an ordinary session", async () => {
    stubHarnessFetch((request) => {
      if (request.path === "/v1/sessions/s1") return snapshotFor("s1");
      return undefined;
    });
    const detail = await fetchHarnessSessionDetail("s1");
    expect(detail).not.toHaveProperty("debugTargetSessionId");
    expect(detail).not.toHaveProperty("debugMcpServers");
    expect(detail).not.toHaveProperty("debugMcpTools");
  });
});
