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
