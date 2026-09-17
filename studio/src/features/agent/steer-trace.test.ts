import { describe, expect, it } from "vitest";
import {
  appendSteerTrace,
  formatSteerTraceLine,
  STEER_TRACE_LIMIT,
  type SteerTraceEntry,
  traceDrainedSteers,
} from "./steer-trace";

const entry = (
  id: string,
  decision: SteerTraceEntry["decision"] = "accepted",
): SteerTraceEntry => ({ id, text: `text ${id}`, decision, at: 1 });

describe("appendSteerTrace", () => {
  it("appends in order without mutating the input", () => {
    const trace = [entry("steer-1")];
    const next = appendSteerTrace(trace, entry("steer-2", "held"));
    expect(next.map((e) => e.id)).toEqual(["steer-1", "steer-2"]);
    expect(trace).toHaveLength(1);
  });

  it("keeps the bound at STEER_TRACE_LIMIT, dropping the oldest first", () => {
    let trace: SteerTraceEntry[] = [];
    for (let i = 0; i < STEER_TRACE_LIMIT + 5; i += 1) {
      trace = appendSteerTrace(trace, entry(`steer-${i}`));
    }
    expect(trace).toHaveLength(STEER_TRACE_LIMIT);
    expect(trace[0]?.id).toBe("steer-5");
    expect(trace.at(-1)?.id).toBe(`steer-${STEER_TRACE_LIMIT + 4}`);
  });
});

describe("traceDrainedSteers", () => {
  it("records one drained line per split-off steer, each stamped with the watermark", () => {
    const trace = traceDrainedSteers(
      [entry("steer-1"), entry("steer-2")],
      [
        { id: "steer-1", text: "first" },
        { id: "steer-2", text: "second" },
      ],
      "steer-2",
      7,
    );
    expect(trace.slice(2)).toEqual([
      {
        id: "steer-1",
        text: "first",
        decision: "drained",
        watermark: "steer-2",
        at: 7,
      },
      {
        id: "steer-2",
        text: "second",
        decision: "drained",
        watermark: "steer-2",
        at: 7,
      },
    ]);
  });

  it("leaves one line under the watermark when the echo matched nothing locally", () => {
    const trace = traceDrainedSteers([], [], "steer-9", 3);
    expect(trace).toEqual([
      {
        id: "steer-9",
        text: "",
        decision: "drained",
        watermark: "steer-9",
        at: 3,
      },
    ]);
    expect(traceDrainedSteers([], [], "", 3)[0]?.id).toBe("-");
  });

  it("still honours the bound", () => {
    const seed = Array.from({ length: STEER_TRACE_LIMIT }, (_, i) =>
      entry(`steer-${i}`),
    );
    const trace = traceDrainedSteers(
      seed,
      [
        { id: "a", text: "" },
        { id: "b", text: "" },
      ],
      "b",
      1,
    );
    expect(trace).toHaveLength(STEER_TRACE_LIMIT);
    expect(trace.slice(-2).map((e) => e.id)).toEqual(["a", "b"]);
  });
});

describe("formatSteerTraceLine", () => {
  it("reads `[steer] <id> <decision>` with the watermark when there is one", () => {
    expect(formatSteerTraceLine(entry("steer-1", "too_late"))).toBe(
      "[steer] steer-1 too_late",
    );
    expect(
      formatSteerTraceLine({ ...entry("steer-1", "drained"), watermark: "w" }),
    ).toBe("[steer] steer-1 drained (watermark w)");
  });
});
