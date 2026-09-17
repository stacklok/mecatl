import { describe, expect, it } from "vitest";
import {
  createLogRing,
  DEFAULT_TAIL_LINES,
  LOG_FILE_NAME,
  MAX_LOG_FILE_BYTES,
  MAX_TAIL_LINES,
  parseTailLines,
  ROTATED_LOG_FILE_NAME,
  shouldRotate,
  tailIsTruncated,
} from "./controller-log.mjs";

/**
 * The controller's daemon-log seam (the managed-mode analogue of mecatui's
 * embedded-server diagnostics file): the bounded ring of COMPLETE tail
 * lines the Diagnostics page reads, the 10 MiB one-generation rotation
 * boundary mirrored from cmd/mecatui/diaglog.go, and the `?lines=` query
 * grammar of GET /logs.
 */

describe("createLogRing", () => {
  it("holds back a partial trailing line until its newline arrives", () => {
    const ring = createLogRing();
    ring.append("time=1 level=INFO msg=ready\ntime=2 lev");
    expect(ring.lines(10)).toEqual(["time=1 level=INFO msg=ready"]);
    ring.append("el=WARN msg=slow\n");
    expect(ring.lines(10)).toEqual([
      "time=1 level=INFO msg=ready",
      "time=2 level=WARN msg=slow",
    ]);
    expect(ring.length).toBe(2);
    expect(ring.dropped).toBe(0);
  });

  it("returns the most recent n lines, oldest first", () => {
    const ring = createLogRing();
    ring.append("a\nb\nc\nd\n");
    expect(ring.lines(2)).toEqual(["c", "d"]);
    expect(ring.lines(10)).toEqual(["a", "b", "c", "d"]);
    expect(ring.lines(0)).toEqual([]);
  });

  it("evicts the oldest complete lines once the byte budget is exceeded", () => {
    // Budget for ~3 six-byte lines ("xxxxx" + newline).
    const ring = createLogRing(18);
    ring.append("11111\n22222\n33333\n");
    expect(ring.dropped).toBe(0);
    ring.append("44444\n");
    expect(ring.lines(10)).toEqual(["22222", "33333", "44444"]);
    expect(ring.dropped).toBe(1);
    expect(ring.bytes).toBe(18);
  });

  it("keeps a single line longer than the whole budget rather than truncating it", () => {
    const ring = createLogRing(8);
    ring.append("short\n");
    ring.append(`${"x".repeat(40)}\n`);
    expect(ring.lines(10)).toEqual(["x".repeat(40)]);
    expect(ring.dropped).toBe(1);
  });

  it("ignores empty and non-string chunks", () => {
    const ring = createLogRing();
    ring.append("");
    // @ts-expect-error a Buffer the caller forgot to stringify is not text
    ring.append(Buffer.from("nope\n"));
    expect(ring.length).toBe(0);
  });
});

describe("tailIsTruncated", () => {
  it("is true when the ring evicted lines or holds more than requested", () => {
    const ring = createLogRing();
    ring.append("a\nb\nc\n");
    expect(tailIsTruncated(ring, 3)).toBe(false);
    expect(tailIsTruncated(ring, 2)).toBe(true);
    const evicting = createLogRing(4);
    evicting.append("a\nb\nc\n");
    expect(tailIsTruncated(evicting, 100)).toBe(true);
  });
});

describe("shouldRotate", () => {
  it("rotates only when the append would push the generation past 10 MiB", () => {
    expect(MAX_LOG_FILE_BYTES).toBe(10 * 1024 * 1024);
    expect(shouldRotate(0, 1)).toBe(false);
    expect(shouldRotate(MAX_LOG_FILE_BYTES - 10, 10)).toBe(false);
    expect(shouldRotate(MAX_LOG_FILE_BYTES - 10, 11)).toBe(true);
    expect(shouldRotate(MAX_LOG_FILE_BYTES, 1)).toBe(true);
    // A custom bound, and an empty file that never rotates even for a
    // chunk larger than the bound (it lands in a fresh generation whole).
    expect(shouldRotate(5, 10, 12)).toBe(true);
    expect(shouldRotate(0, 1_000, 12)).toBe(false);
    expect(shouldRotate(Number.NaN, 1)).toBe(false);
  });

  it("names the two generations the controller keeps", () => {
    expect(LOG_FILE_NAME).toBe("mecated.log");
    expect(ROTATED_LOG_FILE_NAME).toBe("mecated.log.1");
  });
});

describe("parseTailLines", () => {
  it("defaults when absent, clamps to [1, max], and refuses a non-integer", () => {
    expect(parseTailLines(undefined)).toBe(DEFAULT_TAIL_LINES);
    expect(parseTailLines(null)).toBe(DEFAULT_TAIL_LINES);
    expect(parseTailLines("")).toBe(DEFAULT_TAIL_LINES);
    expect(parseTailLines("50")).toBe(50);
    expect(parseTailLines(" 7 ")).toBe(7);
    expect(parseTailLines("0")).toBe(1);
    expect(parseTailLines("999999")).toBe(MAX_TAIL_LINES);
    expect(parseTailLines("10", { max: 5 })).toBe(5);
    for (const bad of ["ten", "1.5", "-3", "1e3"]) {
      expect(() => parseTailLines(bad)).toThrow(
        expect.objectContaining({ statusCode: 400 }),
      );
    }
  });
});
