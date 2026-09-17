import { describe, expect, it } from "vitest";
import type { AgentMessage } from "../types";
import {
  failedRehydrateState,
  isPermanentFailure,
  promptBelongsTo,
  REHYDRATED_FAILURE_DETAIL,
  RETRY_PRECOMMIT_EXPLANATION,
  recoverableDraft,
  retryFailureExplanation,
  retryRoute,
  retryStartFailure,
  shouldAutoRetry,
  trimFailedExchange,
  unmarkFailedRehydrate,
} from "./failed-step-retry";
import { reduceWatchEvent } from "./use-agent-chat";

/**
 * The failed-step retry decisions (ADR 0239), pinned pure: ONE automatic
 * retry only on retryable + precommit, the daemon asked first for every
 * non-permanent disposition (the unknown one a reload leaves behind
 * included), the rehydrated failed marking, and the scoped prompt.
 */

describe("shouldAutoRetry", () => {
  const eligible = {
    disposition: "retryable" as const,
    streamProgress: "precommit" as const,
    alreadyRetried: false,
    permanent: false,
  };

  it("retries exactly the retryable + precommit shape, once", () => {
    expect(shouldAutoRetry(eligible)).toBe(true);
    expect(shouldAutoRetry({ ...eligible, alreadyRetried: true })).toBe(false);
  });

  it("never retries a permanent failure, whichever field says so", () => {
    expect(shouldAutoRetry({ ...eligible, permanent: true })).toBe(false);
    expect(shouldAutoRetry({ ...eligible, disposition: "permanent" })).toBe(
      false,
    );
  });

  it("leaves visible output, unknown progress, and untyped failures alone", () => {
    expect(shouldAutoRetry({ ...eligible, streamProgress: "visible" })).toBe(
      false,
    );
    expect(shouldAutoRetry({ ...eligible, streamProgress: "complete" })).toBe(
      false,
    );
    expect(shouldAutoRetry({ ...eligible, streamProgress: "unknown" })).toBe(
      false,
    );
    expect(shouldAutoRetry({ ...eligible, streamProgress: undefined })).toBe(
      false,
    );
    expect(shouldAutoRetry({ ...eligible, disposition: "unknown" })).toBe(
      false,
    );
    expect(shouldAutoRetry({ ...eligible, disposition: undefined })).toBe(
      false,
    );
  });
});

describe("retryRoute", () => {
  it("asks the daemon for retryable, unknown, and untyped failures", () => {
    expect(retryRoute("retryable")).toBe("endpoint");
    expect(retryRoute("unknown")).toBe("endpoint");
    expect(retryRoute(undefined)).toBe("endpoint");
  });

  it("re-sends directly for a permanent failure", () => {
    expect(retryRoute("permanent")).toBe("resend");
  });

  it("re-sends directly when nothing failed (a cancel has no failed step)", () => {
    expect(retryRoute("retryable", false)).toBe("resend");
    expect(retryRoute(undefined, false)).toBe("resend");
  });
});

describe("retry strip texts", () => {
  it("explains a retry that stopped before the model was called", () => {
    expect(
      retryFailureExplanation({ streamProgress: "precommit", error: "x" }),
    ).toBe(RETRY_PRECOMMIT_EXPLANATION);
    expect(RETRY_PRECOMMIT_EXPLANATION).toContain(
      "before the model was called",
    );
  });

  it("keeps the daemon's own text for every other stop", () => {
    expect(
      retryFailureExplanation({ streamProgress: "visible", error: "boom" }),
    ).toBe("boom");
    expect(retryFailureExplanation({ error: "boom" })).toBe("boom");
    expect(retryFailureExplanation({ error: "" })).toBe(
      "The run failed without a specific error.",
    );
  });

  it("names a retry that could not start", () => {
    expect(retryStartFailure("socket hang up")).toBe(
      "Retry could not start: socket hang up",
    );
  });
});

describe("isPermanentFailure", () => {
  it("reads either wire field", () => {
    expect(isPermanentFailure({ permanent: true })).toBe(true);
    expect(
      isPermanentFailure({ permanent: false, retryDisposition: "permanent" }),
    ).toBe(true);
    expect(
      isPermanentFailure({ permanent: false, retryDisposition: "retryable" }),
    ).toBe(false);
    expect(isPermanentFailure({ permanent: false })).toBe(false);
  });
});

// ── the rehydrated failed marking ────────────────────────────────────────────

const user = (id: string, content: string, extra: Partial<AgentMessage> = {}) =>
  ({ id, role: "user", content, timestamp: 0, ...extra }) as AgentMessage;
const assistant = (
  id: string,
  content: string,
  extra: Partial<AgentMessage> = {},
) =>
  ({ id, role: "assistant", content, timestamp: 0, ...extra }) as AgentMessage;

describe("failedRehydrateState", () => {
  it("marks the trailing assistant turn failed and extracts ITS prompt, not an older one", () => {
    const marked = failedRehydrateState([
      user("u1", "first question"),
      assistant("a1", "first answer"),
      user("u2", "second question"),
      assistant("a2", ""),
    ]);
    expect(marked).not.toBeNull();
    expect(marked?.appended).toBe(false);
    expect(marked?.markedId).toBe("a2");
    expect(marked?.lastPrompt).toBe("second question");
    expect(marked?.messages[3]).toMatchObject({
      failed: true,
      failureDetail: REHYDRATED_FAILURE_DETAIL,
    });
    // Earlier turns are untouched.
    expect(marked?.messages[1].failed).toBeUndefined();
  });

  it("fails the turn's still-running tool calls too", () => {
    const marked = failedRehydrateState([
      user("u1", "go"),
      assistant("a1", "", {
        toolCalls: [
          { callId: "c1", name: "Read", input: "", status: "running" },
          { callId: "c2", name: "Read", input: "", status: "completed" },
        ],
      }),
    ]);
    expect(marked?.messages[1].toolCalls?.map((call) => call.status)).toEqual([
      "failed",
      "completed",
    ]);
  });

  it("appends a failed bubble when the run died before any assistant text was recorded", () => {
    const marked = failedRehydrateState([user("u1", "do it")]);
    expect(marked?.appended).toBe(true);
    expect(marked?.messages).toHaveLength(2);
    expect(marked?.messages[1]).toMatchObject({
      id: "u1-failed",
      role: "assistant",
      failed: true,
      failureDetail: REHYDRATED_FAILURE_DETAIL,
    });
    expect(marked?.lastPrompt).toBe("do it");
  });

  it("skips a mid-run steer to find the turn's genuine prompt", () => {
    const marked = failedRehydrateState([
      user("u1", "the prompt"),
      user("u2", "also check tests", { steered: true }),
      assistant("a1", ""),
    ]);
    expect(marked?.lastPrompt).toBe("the prompt");
  });

  it("does not treat a scheduled task's delivery note as a resendable prompt", () => {
    const marked = failedRehydrateState([
      user("u1", "digest", {
        delivery: {
          scheduleName: "nightly",
          fireId: "f-1",
          kind: "completed",
          stop: "end_turn",
        },
      }),
      assistant("a1", ""),
    ]);
    expect(marked?.lastPrompt).toBeNull();
    expect(
      failedRehydrateState([
        user("u1", "digest", {
          delivery: {
            scheduleName: "nightly",
            fireId: "f-1",
            kind: "started",
            stop: "",
          },
        }),
      ]),
    ).toBeNull();
  });

  it("returns null when there is nothing to mark", () => {
    expect(failedRehydrateState([])).toBeNull();
  });

  it("keeps an existing failure detail over the generic one", () => {
    const marked = failedRehydrateState([
      assistant("a1", "", { failed: true, failureDetail: "boom" }),
    ]);
    expect(marked?.messages[0].failureDetail).toBe("boom");
  });
});

describe("unmarkFailedRehydrate", () => {
  it("removes an appended bubble", () => {
    const marked = failedRehydrateState([user("u1", "do it")]);
    if (!marked) throw new Error("expected a mark");
    expect(unmarkFailedRehydrate(marked.messages, marked)).toEqual([
      user("u1", "do it"),
    ]);
  });

  it("clears the marking on an existing turn, and only the generic one", () => {
    const marked = failedRehydrateState([
      user("u1", "go"),
      assistant("a1", ""),
    ]);
    if (!marked) throw new Error("expected a mark");
    const cleared = unmarkFailedRehydrate(marked.messages, marked);
    expect(cleared[1].failed).toBeUndefined();
    expect(cleared[1].failureDetail).toBeUndefined();
    // A real recorded failure is not an artefact of the marking.
    const real = [assistant("a1", "", { failed: true, failureDetail: "boom" })];
    expect(
      unmarkFailedRehydrate(real, { markedId: "a1", appended: false }),
    ).toEqual(real);
  });
});

describe("promptBelongsTo", () => {
  const held = { sessionId: "s1", serial: 1, text: "go" };

  it("accepts the bound chat's prompt and a draft's (minted on that send)", () => {
    expect(promptBelongsTo(held, "s1")).toBe(true);
    expect(promptBelongsTo({ ...held, sessionId: null }, "s9")).toBe(true);
  });

  it("refuses another chat's prompt and an empty hold", () => {
    expect(promptBelongsTo(held, "s2")).toBe(false);
    expect(promptBelongsTo(null, "s1")).toBe(false);
  });
});

describe("recoverableDraft", () => {
  it("hands back a text-only prompt", () => {
    expect(recoverableDraft({ text: "go" })).toEqual({ text: "go" });
    expect(recoverableDraft({ text: "go", files: [] })).toEqual({ text: "go" });
  });

  it("never replays media or an empty prompt into the composer", () => {
    const png = new File(["x"], "x.png", { type: "image/png" });
    expect(recoverableDraft({ text: "go", files: [png] })).toBeNull();
    expect(recoverableDraft({ text: "   " })).toBeNull();
  });
});

describe("trimFailedExchange", () => {
  const user = (id: string, content: string): AgentMessage => ({
    id,
    role: "user",
    content,
    timestamp: 0,
  });
  const assistant = (
    id: string,
    over: Partial<AgentMessage> = {},
  ): AgentMessage => ({
    id,
    role: "assistant",
    content: "",
    timestamp: 0,
    ...over,
  });

  it("removes a trailing failed assistant bubble and the user bubble that carried the prompt", () => {
    const earlier = [user("u1", "first"), assistant("a1", { content: "ok" })];
    const messages = [
      ...earlier,
      user("u2", "hello"),
      assistant("a2", { failed: true, failureDetail: "409" }),
    ];
    expect(trimFailedExchange(messages, "hello")).toEqual(earlier);
    // The input is never mutated.
    expect(messages).toHaveLength(4);
  });

  it("drops every trailing empty assistant bubble before the user bubble", () => {
    const messages = [
      user("u1", "hello"),
      assistant("a1"),
      assistant("a2", { failed: true }),
    ];
    expect(trimFailedExchange(messages, "hello")).toEqual([]);
  });

  it("leaves an unrelated tail alone", () => {
    const answered = [user("u1", "hello"), assistant("a1", { content: "ok" })];
    expect(trimFailedExchange(answered, "hello")).toEqual(answered);
    // A failed bubble under a DIFFERENT prompt: the bubble goes, the prompt
    // it belongs to stays.
    const other = [user("u1", "other"), assistant("a1", { failed: true })];
    expect(trimFailedExchange(other, "hello")).toEqual([user("u1", "other")]);
  });

  it("handles an empty transcript", () => {
    expect(trimFailedExchange([], "hello")).toEqual([]);
  });
});

describe("reduceWatchEvent failed terminals", () => {
  it("stamps failurePermanent from a permanent run_result", () => {
    const messages = reduceWatchEvent(
      [{ id: "m1", role: "assistant", content: "", timestamp: 0 }],
      {
        type: "run_result",
        stop: "error",
        text: "",
        errorText: "context window exceeded",
        permanent: true,
        retryDisposition: "permanent",
      },
      () => "n",
    );
    expect(messages[0]).toMatchObject({ failed: true, failurePermanent: true });
  });

  it("leaves failurePermanent false on a retryable run_result", () => {
    const messages = reduceWatchEvent(
      [{ id: "m1", role: "assistant", content: "", timestamp: 0 }],
      {
        type: "run_result",
        stop: "error",
        text: "",
        errorText: "upstream 503",
        permanent: false,
        retryDisposition: "retryable",
        streamProgress: "precommit",
      },
      () => "n",
    );
    expect(messages[0]).toMatchObject({
      failed: true,
      failurePermanent: false,
    });
  });
});
