// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { locateQuote, WriterCore, type WriterTransport } from "./writer-core";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((yes) => {
    resolve = yes;
  });
  return { promise, resolve };
}

let observe: ReturnType<typeof vi.fn<WriterTransport["observe"]>>;
let discuss: ReturnType<typeof vi.fn<WriterTransport["discuss"]>>;
let core: WriterCore;
const tick = async (ms: number) => vi.advanceTimersByTimeAsync(ms);

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(10_000);
  observe = vi.fn<WriterTransport["observe"]>().mockResolvedValue({ status: "silent" });
  discuss = vi.fn<WriterTransport["discuss"]>().mockResolvedValue({ text: "Think about it." });
  core = new WriterCore({ observe, discuss }, () => {});
});
afterEach(() => {
  core.dispose();
  vi.useRealTimers();
});

describe("WriterCore", () => {
  it.each(["pause", "dispose"] as const)("suppresses late observation after %s", async (action) => {
    const held = deferred<Awaited<ReturnType<WriterTransport["observe"]>>>();
    const changed = vi.fn();
    core.dispose();
    core = new WriterCore({ observe, discuss }, changed);
    observe.mockReturnValueOnce(held.promise);
    core.edit("A meaningful draft for analysis.");
    await tick(1500);
    if (action === "pause") core.setPaused(true);
    else core.dispose();
    const calls = changed.mock.calls.length;
    expect(observe.mock.calls[0]?.[1].aborted).toBe(true);
    held.resolve({ status: "observe", text: "Must not appear" });
    await tick(60_000);
    expect(core.observations).toEqual([]);
    expect(core.checkpoint).toEqual({ revision: 0, content: "" });
    expect(core.lastChecked).toBeUndefined();
    expect(observe).toHaveBeenCalledOnce();
    if (action === "dispose") expect(changed).toHaveBeenCalledTimes(calls);
  });

  it.each(["cancellation", "reply"] as const)(
    "rejects a thread switch during %s, including switch-away-and-back",
    async (phase) => {
      core.restore({
        document: { revision: 1, content: "Draft" },
        brief: "",
        generalDiscussion: [],
        observations: [
          { id: "thread", revision: 1, text: "Why?", status: "open", timestamp: 1, discussion: [] },
        ],
      });
      core.select("thread");
      const observation = deferred<Awaited<ReturnType<WriterTransport["observe"]>>>();
      const reply = deferred<Awaited<ReturnType<WriterTransport["discuss"]>>>();
      observe.mockReturnValueOnce(observation.promise);
      discuss.mockReturnValueOnce(reply.promise);
      const reading = phase === "cancellation" ? core.readNow() : Promise.resolve();
      const pending = core.discuss("Thread question");
      if (phase === "cancellation") expect(discuss).not.toHaveBeenCalled();
      else expect(discuss).toHaveBeenCalledOnce();
      core.select(undefined);
      core.select("thread");
      observation.resolve({ status: "observe", text: "Canceled analysis" });
      reply.resolve({ text: "Late reply" });
      await reading;
      expect(await pending).toBe(false);
      expect(core.discussion).toEqual([]);
      expect(core.generalDiscussion).toEqual([]);
      expect(discuss).toHaveBeenCalledTimes(phase === "cancellation" ? 0 : 1);
    },
  );

  it("keeps an in-flight observation on its original revision and schedules the latest edit", async () => {
    const held = deferred<Awaited<ReturnType<WriterTransport["observe"]>>>();
    observe.mockReturnValueOnce(held.promise);
    core.edit("First draft under analysis.");
    await tick(1500);
    core.edit("Latest draft with different evidence.");
    held.resolve({ status: "observe", text: "Original revision question?" });
    await tick(0);
    expect(core.observations[0]?.revision).toBe(1);
    expect(core.checkpoint).toEqual({ revision: 1, content: "First draft under analysis." });
    expect(core.document.revision).toBe(2);
    await tick(30_000);
    expect(observe).toHaveBeenCalledTimes(2);
    expect(observe.mock.lastCall?.[0].document).toEqual(core.document);
    expect(observe.mock.lastCall?.[0].checkpoint?.revision).toBe(1);
  });

  it("sends the selected model in both request types and removes it for deployment default", async () => {
    core.edit("Draft");
    const model = { id: "model", providerId: "provider" };
    core.setModel(model);
    await core.readNow();
    await core.discuss("Why?");
    expect(observe.mock.lastCall?.[0].model).toEqual(model);
    expect(discuss.mock.lastCall?.[0].model).toEqual(model);
    core.setModel();
    await core.readNow();
    await core.discuss("Why again?");
    expect(observe.mock.lastCall?.[0]).not.toHaveProperty("model");
    expect(discuss.mock.lastCall?.[0]).not.toHaveProperty("model");
  });

  it("sends at most twelve discussion entries while retaining the full local thread", async () => {
    core.setPaused(true);
    core.edit("Draft");
    for (let index = 0; index < 8; index++)
      expect(await core.discuss(`Question ${index}`)).toBe(true);
    expect(discuss.mock.lastCall?.[0].discussion).toHaveLength(12);
    expect(discuss.mock.lastCall?.[0].discussion[0]?.text).toBe("Question 1");
    expect(core.discussion).toHaveLength(16);
    expect(core.discussion[0]?.text).toBe("Question 0");
    await core.readNow();
    expect(observe.mock.lastCall?.[0].discussion).toEqual(core.discussion.slice(-12));
    expect(core.snapshot().generalDiscussion).toHaveLength(16);
  });

  it("never sends an over-100k document via automatic, explicit, retry, or discussion requests", async () => {
    core.edit("x".repeat(100_001));
    await tick(100_000);
    await core.readNow();
    await core.retry();
    expect(await core.discuss("Why?")).toBe(false);
    expect(observe).not.toHaveBeenCalled();
    expect(discuss).not.toHaveBeenCalled();
    core.edit("x".repeat(100_000));
    await core.readNow();
    expect(await core.discuss("Why?")).toBe(true);
    expect(observe).toHaveBeenCalledOnce();
    expect(discuss).toHaveBeenCalledOnce();
  });

  it("requires explicit discussion retry while On request and retains only successful messages", async () => {
    core.setPaused(true);
    core.edit("Draft");
    discuss.mockRejectedValueOnce(new Error("transient"));
    expect(await core.discuss("Retry this question")).toBe(false);
    await tick(100_000);
    expect(discuss).toHaveBeenCalledOnce();
    expect(observe).not.toHaveBeenCalled();
    expect(core.discussion).toEqual([]);
    expect(await core.discuss("Retry this question")).toBe(true);
    expect(core.discussion).toHaveLength(2);
    expect(core.error).toBe("");
    expect(core.paused).toBe(true);
  });
  it("uses a quiet interval even after silence, defers trivial edits, and checks during continuous typing", async () => {
    core.edit("A first meaningful draft ends here.");
    await tick(1500);
    expect(observe).toHaveBeenCalledTimes(1);
    core.edit("A first meaningful draft ends here. x");
    await tick(29_999);
    expect(observe).toHaveBeenCalledTimes(1);
    await tick(15_001);
    expect(observe).toHaveBeenCalledTimes(2);
    core.edit("More meaningful writing in the next section.");
    for (let index = 0; index < 10; index++) {
      await tick(1000);
      core.edit(`More meaningful writing in the next section. ${index}`);
    }
    await tick(35_000);
    expect(observe).toHaveBeenCalledTimes(3);
  });

  it("describes freshness without exposing internal revisions", async () => {
    core.edit("A meaningful draft for analysis.");
    expect(core.status).toBe("Waiting for a pause in typing");
    await tick(1500);
    expect(core.status).toBe("Checked · no new observations");
    core.edit("A newer draft for analysis.");
    core.setPaused(true);
    expect(core.status).toBe("Automatic checks paused · earlier draft checked");
  });

  it("applies an author brief only to future checks; old-brief in-flight results cannot become current", async () => {
    const held = deferred<Awaited<ReturnType<WriterTransport["observe"]>>>();
    observe.mockImplementationOnce(() => held.promise);
    core.edit("This is a meaningful first draft.");
    await tick(1500);
    expect(core.setBrief("Audience: operators; focus on cost")).toBe(true);
    held.resolve({ status: "observe", text: "Old brief question" });
    await tick(0);
    expect(core.observations).toHaveLength(0);
    expect(core.checkpoint.content).toBe("");
    await core.readNow();
    expect(observe.mock.lastCall?.[0]).toMatchObject({
      brief: "Audience: operators; focus on cost",
    });
    expect(observe.mock.lastCall?.[0].checkpoint).toBeUndefined();
    core.setBrief("Audience: operators; focus on clarity");
    expect(core.status).toContain("Brief changed");
    expect(observe).toHaveBeenCalledTimes(2);
    await core.readNow();
    expect(observe).toHaveBeenCalledTimes(3);
  });

  it("keeps independent general and observation threads, and only confirmed decisions enter context", async () => {
    observe.mockResolvedValueOnce({
      status: "observe",
      text: "What are the costs?",
      quotes: ["Costs"],
    });
    core.edit("Costs are not estimated in this draft.");
    await tick(1500);
    const first = core.observations[0];
    if (!first) throw new Error("Expected observation");
    expect(await core.discuss("General question")).toBe(true);
    core.select(first.id);
    expect(core.discussion).toEqual([]);
    expect(await core.discuss("Thread question")).toBe(true);
    expect(first.status).toBe("open");
    expect(core.confirmDecision(first.id, "Deployment costs outside scope")).toBe(true);
    core.setObservationStatus(first.id, "addressed");
    core.select(undefined);
    expect(core.discussion[0]?.text).toBe("General question");
    core.select(first.id);
    expect(core.discussion[0]?.text).toBe("Thread question");
    await core.readNow();
    expect(observe.mock.lastCall?.[0].observations).toMatchObject([
      { status: "addressed", decision: "Deployment costs outside scope", selected: true },
    ]);
    core.setObservationStatus(first.id, "not-relevant");
    core.setObservationStatus(first.id, "open");
    expect(first.status).toBe("open");
  });

  it("allows Ask Writer and Read this now without an observation and while paused", async () => {
    core.setPaused(true);
    core.edit("A draft can be discussed directly.");
    await tick(100_000);
    expect(observe).not.toHaveBeenCalled();
    expect(await core.discuss("What is missing?")).toBe(true);
    expect(discuss.mock.lastCall?.[0].discussion).toEqual([]);
    await core.readNow();
    expect(observe).toHaveBeenCalledOnce();
    expect(core.paused).toBe(true);
  });

  it("serializes explicit discussion against in-flight observation and rejects a switched thread's reply", async () => {
    const held = deferred<Awaited<ReturnType<WriterTransport["observe"]>>>();
    observe.mockImplementationOnce(() => held.promise);
    core.edit("A first meaningful draft ends here.");
    await tick(1500);
    core.edit("A second meaningful draft ends here.");
    const sent = core.discuss("Why?");
    expect(observe.mock.calls[0]?.[1].aborted).toBe(true);
    expect(discuss).not.toHaveBeenCalled();
    held.resolve({ status: "observe", text: "Do not surface" });
    await tick(0);
    expect(await sent).toBe(true);
    expect(discuss.mock.calls[0]?.[0].document.content).toContain("second");
    expect(core.observations).toHaveLength(0);
    const reply = deferred<Awaited<ReturnType<WriterTransport["discuss"]>>>();
    discuss.mockImplementationOnce(() => reply.promise);
    const pending = core.discuss("Again?");
    core.select(undefined);
    core.setAvailable(false);
    reply.resolve({ text: "late" });
    expect(await pending).toBe(false);
  });

  it("bounds outgoing context without deleting stored history and rejects oversize messages", async () => {
    core.restore({
      document: { revision: 15, content: "draft" },
      brief: "",
      generalDiscussion: [],
      observations: Array.from({ length: 15 }, (_, index) => ({
        id: `id-${index}`,
        revision: index,
        text: `Thought ${index}?`,
        status: "open" as const,
        timestamp: index,
        discussion: [],
      })),
    });
    core.select("id-0");
    expect(core.status).toContain("Draft restored");
    await tick(60_000);
    expect(observe).not.toHaveBeenCalled();
    await core.readNow();
    expect(observe.mock.lastCall?.[0].observations).toHaveLength(12);
    expect(observe.mock.lastCall?.[0].observations[0]).toMatchObject({
      text: "Thought 0?",
      selected: true,
    });
    expect(core.observations).toHaveLength(15);
    expect(await core.discuss("x".repeat(2001))).toBe(false);
  });

  it("retains old confirmed decisions independently of recent observations and enforces the decision bound", async () => {
    core.restore({
      document: { revision: 101, content: "draft" },
      brief: "",
      generalDiscussion: [],
      observations: Array.from({ length: 101 }, (_, index) => ({
        id: `id-${index}`,
        revision: index,
        text: `Thought ${index}?`,
        status: "open" as const,
        timestamp: index,
        discussion: [],
      })),
    });
    expect(core.confirmDecision("id-0", "Keep the first assumption")).toBe(true);
    await core.readNow();
    expect(observe.mock.lastCall?.[0].observations).toHaveLength(12);
    expect(observe.mock.lastCall?.[0].observations).not.toContainEqual(
      expect.objectContaining({ text: "Thought 0?" }),
    );
    expect(observe.mock.lastCall?.[0].decisions).toEqual([
      { text: "Thought 0?", decision: "Keep the first assumption" },
    ]);
    expect(core.confirmDecision("id-0", "")).toBe(true);
    await core.readNow();
    expect(observe.mock.lastCall?.[0]).not.toHaveProperty("decisions");
    for (let index = 0; index < 100; index++)
      expect(core.confirmDecision(`id-${index}`, `Decision ${index}`)).toBe(true);
    expect(core.confirmDecision("id-100", "Another decision")).toBe(false);
    expect(core.error).toContain("Decision limit reached");
    expect(core.observations[100]?.decision).toBeUndefined();
    expect(core.confirmDecision("id-0", "Updated decision")).toBe(true);
    expect(core.confirmDecision("id-0", "")).toBe(true);
    expect(core.confirmDecision("id-100", "Another decision")).toBe(true);
    await core.readNow();
    expect(observe.mock.lastCall?.[0].decisions).toHaveLength(100);
  });

  it("retries a failed explicit read while automatic observation remains paused", async () => {
    core.setPaused(true);
    core.edit("Draft requiring explicit analysis");
    observe.mockRejectedValueOnce(new Error("transient"));
    await core.readNow();
    expect(core.error).toContain("Retry");
    await core.retry();
    expect(observe).toHaveBeenCalledTimes(2);
    expect(core.error).toBe("");
    expect(core.paused).toBe(true);
  });

  it("resolves only unique current quotes and flags removed or ambiguous passages", () => {
    expect(locateQuote("alpha beta", "beta")).toEqual({ from: 6, to: 10 });
    expect(locateQuote("alpha alpha", "alpha")).toBeUndefined();
    expect(locateQuote("beta", "alpha")).toBeUndefined();
  });
});
