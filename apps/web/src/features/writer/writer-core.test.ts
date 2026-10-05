// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { WriterCore, type WriterTransport } from "./writer-core";

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}

let observe: ReturnType<typeof vi.fn<WriterTransport["observe"]>>;
let discuss: ReturnType<typeof vi.fn<WriterTransport["discuss"]>>;
let core: WriterCore;
const tick = async (ms: number) => {
  await vi.advanceTimersByTimeAsync(ms);
};

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(10_000);
  observe = vi.fn<WriterTransport["observe"]>().mockResolvedValue({ status: "silent" });
  discuss = vi
    .fn<WriterTransport["discuss"]>()
    .mockResolvedValue({ text: "Let's think about this." });
  core = new WriterCore({ observe, discuss }, () => {}, 1500, 60_000);
});
afterEach(() => {
  core.dispose();
  vi.useRealTimers();
});

describe("WriterCore", () => {
  it.each(["observe", "silent"] as const)(
    "notifies synchronous readers only after scheduling the next revision after %s",
    async (status) => {
      const statuses: string[] = [];
      core = new WriterCore({ observe, discuss }, () => statuses.push(core.status));
      const pending = deferred<Awaited<ReturnType<WriterTransport["observe"]>>>();
      observe.mockImplementationOnce(() => pending.promise);
      core.edit("first");
      await tick(1500);
      core.edit("next");
      pending.resolve(status === "silent" ? { status } : { status, text: "Why this claim?" });
      await tick(0);
      expect(statuses.at(-1)).toBe(
        status === "silent"
          ? "Waiting for a pause in typing"
          : "Cooling down before the next analysis",
      );
    },
  );

  it("schedules before notifying synchronous readers after a discussion", async () => {
    const statuses: string[] = [];
    core = new WriterCore({ observe, discuss }, () => statuses.push(core.status));
    const pending = deferred<Awaited<ReturnType<WriterTransport["discuss"]>>>();
    discuss.mockImplementationOnce(() => pending.promise);
    core.edit("draft");
    const sent = core.discuss("Why?");
    core.edit("revised during discussion");
    pending.resolve({ text: "What evidence would help?" });
    await sent;
    expect(statuses.at(-1)).toBe("Cooling down before the next analysis");
  });

  it("reports waiting, silent checkpoint, and forwards one model to both requests", async () => {
    expect(core.status).toContain("Start writing");
    core.setModel({ id: "m", providerId: "p" });
    core.edit("draft");
    expect(core.status).toContain("pause in typing");
    await tick(1500);
    expect(observe.mock.lastCall?.[0].model).toEqual({ id: "m", providerId: "p" });
    expect(core.status).toBe("Last checked revision 1 · no observation");
    await core.discuss("Why?");
    expect(discuss.mock.lastCall?.[0].model).toEqual({ id: "m", providerId: "p" });
    core.edit("next");
    expect(core.status).toBe("Cooling down before the next analysis");
  });

  it("starts the interruption cooldown when a slow observation actually surfaces", async () => {
    const pending = deferred<Awaited<ReturnType<WriterTransport["observe"]>>>();
    observe.mockImplementationOnce(() => pending.promise);
    core.edit("first");
    await tick(1500);
    await tick(30_000);
    pending.resolve({ status: "observe", text: "What would disprove this assumption?" });
    await tick(0);
    expect(core.observations[0]?.timestamp).toBe(Date.now());
    core.edit("next");
    await tick(59_999);
    expect(observe).toHaveBeenCalledTimes(1);
    await tick(1);
    expect(observe).toHaveBeenCalledTimes(2);
  });

  it("does not impose a cooldown after silence or a duplicate", async () => {
    core.edit("first");
    await tick(1500);
    core.edit("second");
    await tick(1500);
    expect(observe).toHaveBeenCalledTimes(2);
    observe.mockResolvedValue({ status: "observe", text: "What would disprove this assumption?" });
    core.edit("third");
    await tick(1500);
    core.edit("fourth");
    await tick(60_000);
    expect(core.observations).toHaveLength(1);
    core.edit("fifth");
    await tick(1500);
    expect(observe).toHaveBeenCalledTimes(5);
  });

  it.each(["select", "dismiss"] as const)(
    "ignores a deferred discussion after %s changes its thought",
    async (action) => {
      core.observations = [1, 2].map((revision) => ({
        id: `thought-${revision}`,
        revision: 0,
        text: `Question ${revision}?`,
        status: "active",
        timestamp: 0,
      }));
      core.select("thought-1");
      const pending = deferred<Awaited<ReturnType<WriterTransport["discuss"]>>>();
      discuss.mockImplementationOnce(() => pending.promise);
      const sent = core.discuss("Why?");
      if (action === "select") core.select("thought-2");
      else core.dismiss("thought-1");
      expect(discuss.mock.calls[0]?.[1].aborted).toBe(true);
      expect(await core.discuss("Do not overlap")).toBe(false);
      pending.resolve({ text: "Old reply" });
      expect(await sent).toBe(false);
      expect(core.discussion).toEqual([]);
      expect(core.observations[1]?.status).toBe("active");
      expect(core.error).toBe("");
    },
  );

  it("allows explicit discussion while paused, and delays observation after its reply", async () => {
    core.edit("first");
    core.setPaused(true);
    const pending = deferred<Awaited<ReturnType<WriterTransport["discuss"]>>>();
    discuss.mockImplementationOnce(() => pending.promise);
    const sent = core.discuss("Why?");
    core.setPaused(false);
    core.setPaused(true);
    expect(discuss.mock.calls[0]?.[1].aborted).toBe(false);
    core.edit("latest");
    await tick(30_000);
    expect(observe).not.toHaveBeenCalled();
    pending.resolve({ text: "What evidence would change your mind?" });
    expect(await sent).toBe(true);
    core.setPaused(false);
    await tick(59_999);
    expect(observe).not.toHaveBeenCalled();
    await tick(1);
    expect(observe.mock.lastCall?.[0].document.content).toBe("latest");
  });

  it("coalesces typing, sends exact empty checkpoint, and ignores unchanged edits and revert", async () => {
    core.edit("one");
    await tick(800);
    core.edit("two");
    await tick(1499);
    expect(observe).not.toHaveBeenCalled();
    await tick(1);
    expect(observe).toHaveBeenCalledWith(
      expect.objectContaining({
        document: { revision: 2, content: "two" },
        checkpoint: { revision: 0, content: "" },
      }),
      expect.any(AbortSignal),
    );
    core.edit("two");
    core.edit("three");
    core.edit("two");
    await tick(60_000);
    expect(observe).toHaveBeenCalledTimes(1);
    core.edit("");
    await tick(1500);
    expect(observe).toHaveBeenCalledWith(
      expect.objectContaining({
        document: { revision: 5, content: "" },
        checkpoint: { revision: 2, content: "two" },
      }),
      expect.any(AbortSignal),
    );
    core.edit("new");
    await tick(60_000);
    expect(observe).toHaveBeenLastCalledWith(
      expect.objectContaining({ checkpoint: { revision: 5, content: "" } }),
      expect.any(AbortSignal),
    );
  });

  it("does not analyze a draft reverted to the initial empty checkpoint", async () => {
    core.edit("temporary");
    core.edit("");
    await tick(60_000);
    expect(observe).not.toHaveBeenCalled();
    expect(core.document.revision).toBe(2);
  });

  it("attaches an in-flight observation to its analyzed revision and schedules the latest after cooldown", async () => {
    const pending = deferred<Awaited<ReturnType<WriterTransport["observe"]>>>();
    observe
      .mockImplementationOnce(() => pending.promise)
      .mockResolvedValue({ status: "observe", text: "Fresh insight" });
    core.edit("old");
    await tick(1500);
    core.edit("new");
    await tick(60_000);
    expect(observe).toHaveBeenCalledTimes(1);
    expect(core.status).toBe("Analyzing revision 1…");
    pending.resolve({ status: "observe", text: "Earlier insight" });
    await tick(0);
    expect(core.observations).toMatchObject([{ revision: 1, text: "Earlier insight" }]);
    expect(core.status).toBe("Cooling down before the next analysis");
    expect(observe).toHaveBeenCalledTimes(1);
    await tick(59_999);
    expect(observe).toHaveBeenCalledTimes(1);
    await tick(1);
    expect(observe).toHaveBeenCalledTimes(2);
    expect(observe.mock.calls[1]?.[0]).toMatchObject({
      document: { content: "new" },
      checkpoint: { content: "old" },
    });
    expect(core.observations).toHaveLength(2);
    core.edit("latest");
    await tick(1500);
    expect(observe).toHaveBeenCalledTimes(2);
    await tick(58_500);
    expect(observe).toHaveBeenCalledTimes(3);
  });

  it("deduplicates dismissed observations and pauses after errors until explicit retry", async () => {
    observe.mockResolvedValue({ status: "observe", text: "Same, idea!" });
    core.edit("a");
    await tick(1500);
    const first = core.observations[0];
    if (!first) throw new Error("Expected an observation");
    core.dismiss(first.id);
    core.edit("b");
    await tick(60_000);
    expect(core.observations).toHaveLength(1);
    observe.mockRejectedValueOnce(new Error("network"));
    core.edit("c");
    await tick(60_000);
    expect(core.error).toContain("failed");
    core.edit("d");
    await tick(120_000);
    expect(observe).toHaveBeenCalledTimes(3);
    core.retry();
    await tick(1500);
    expect(observe).toHaveBeenCalledTimes(4);
  });

  it("aborts and suppresses late results on pause and disposal", async () => {
    const pending = deferred<Awaited<ReturnType<WriterTransport["observe"]>>>();
    observe.mockImplementationOnce(() => pending.promise);
    core.edit("first");
    await tick(1500);
    core.setPaused(true);
    expect(observe.mock.calls[0]?.[1].aborted).toBe(true);
    pending.resolve({ status: "observe", text: "Do not show" });
    await tick(0);
    expect(core.observations).toHaveLength(0);
    core.setPaused(false);
    await tick(60_000);
    expect(observe).toHaveBeenCalledTimes(2);
    core.edit("next");
    core.dispose();
    await tick(120_000);
    expect(observe).toHaveBeenCalledTimes(2);
  });

  it("discussion waits for analysis, sends newest document, and marks observation discussed", async () => {
    observe.mockResolvedValueOnce({
      status: "observe",
      text: "What evidence supports the opening assumption?",
    });
    core.edit("opening");
    await tick(1500);
    const first = core.observations[0];
    if (!first) throw new Error("Expected an observation");
    core.select(first.id);
    const pending = deferred<Awaited<ReturnType<WriterTransport["observe"]>>>();
    observe.mockImplementationOnce(() => pending.promise);
    core.edit("revised");
    await tick(60_000);
    core.edit("latest while typing");
    const sent = core.discuss("What do you mean?");
    expect(discuss).not.toHaveBeenCalled();
    pending.resolve({ status: "silent" });
    await tick(0);
    expect(await sent).toBe(true);
    expect(discuss.mock.calls[0]?.[0]).toMatchObject({
      document: { content: "latest while typing" },
      message: "What do you mean?",
      observations: [
        {
          revision: 1,
          status: "active",
          text: "What evidence supports the opening assumption?",
          selected: true,
        },
      ],
    });
    expect(core.observations[0]?.status).toBe("discussed");
    expect(core.document.content).toBe("latest while typing");
  });

  it("bounds context, rejects oversize input without truncation, and serializes discussion", async () => {
    core.dispose();
    core = new WriterCore({ observe, discuss }, () => {}, 1, 0);
    observe.mockImplementation(async () => ({
      status: "observe",
      text: `Idea ${core.document.revision} uniquely ${core.document.revision}`,
    }));
    for (let i = 1; i <= 14; i++) {
      core.edit(`Draft ${i}`);
      await tick(1);
    }
    expect(core.observations).toHaveLength(14);
    core.edit("Draft 15");
    await tick(1);
    expect(observe.mock.lastCall?.[0].observations).toHaveLength(12);
    core.select("observation-1");
    const held = deferred<Awaited<ReturnType<WriterTransport["discuss"]>>>();
    discuss.mockImplementationOnce(() => held.promise);
    const first = core.discuss("Question");
    expect(discuss).toHaveBeenCalledTimes(1);
    expect(discuss.mock.calls[0]?.[0].observations).toHaveLength(12);
    expect(discuss.mock.calls[0]?.[0].observations[0]).toMatchObject({
      revision: 1,
      selected: true,
    });
    expect(await core.discuss("Second question")).toBe(false);
    core.setAvailable(false);
    expect(discuss.mock.calls[0]?.[1].aborted).toBe(true);
    held.resolve({ text: "Too late" });
    expect(await first).toBe(false);
    expect(core.discussion).toHaveLength(0);
    core.setAvailable(true);
    expect(await core.discuss("x".repeat(2001))).toBe(false);
    expect(core.error).toContain("2,000");
    core.edit("y".repeat(100_001));
    await tick(10);
    expect(observe).toHaveBeenCalledTimes(15);
    expect(await core.discuss("valid")).toBe(false);
    expect(core.error).toContain("100,000");
  });
});
