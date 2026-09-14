import { expect, test } from "vitest";
import { BoundedByteTail, reportableStderr, validateExtraArguments } from "../src/spawn-common.js";

test("spawn stderr capture retains exactly the last 64 KiB across arbitrary chunks", () => {
  const tail = new BoundedByteTail();
  const first = new Uint8Array(70_000).map((_, index) => index % 251);
  tail.append(first);
  expect(tail.value()).toEqual(first.subarray(first.length - 64 * 1024));
  first.fill(0); // Capture owns its bytes, not a view of a reusable reader buffer.
  expect(tail.value().at(-1)).toBe(69_999 % 251);
  const next = new Uint8Array([255, 254, 253]);
  const before = tail.value().slice();
  tail.append(next);
  expect(tail.value()).toEqual(new Uint8Array([...before.subarray(3), ...next]));
  expect(tail.value()).toHaveLength(64 * 1024);
  tail.append(new Uint8Array());
  expect(tail.value()).toHaveLength(64 * 1024);
});

test("spawn stderr reporting bounds bytes and preserves each runtime's UTF-8 decoder", () => {
  const bytes = new Uint8Array([0xef, 0xbb, 0xbf, 0x61, 0xff, 0x0d, 0x0a]);
  expect(reportableStderr(bytes, (value) => Buffer.from(value).toString("utf8"))).toBe(
    "\uFEFFa\uFFFD\r\n",
  );
  expect(reportableStderr(bytes, (value) => new TextDecoder().decode(value))).toBe("a\uFFFD\r\n");
  const overlongLine = new TextEncoder().encode("x".repeat(4 * 1024 + 1));
  expect(reportableStderr(overlongLine, (value) => new TextDecoder().decode(value))).toBe("");
  const exactLines = new TextEncoder().encode(`discard\n${"x\n".repeat(2 * 1024)}`);
  expect(reportableStderr(exactLines, (value) => new TextDecoder().decode(value))).toBe(
    "x\n".repeat(2 * 1024),
  );
});

test.each(["--http-addr", "-http-addr", "--http-addr=remote", "-http-addr=remote"])(
  "spawn reserves the Go flag spelling %s",
  (flag) => {
    expect(() => validateExtraArguments([flag], ["--http-addr"])).toThrow(
      "Extra argument --http-addr collides with an SDK-owned flag",
    );
  },
);

test("spawn argument validation keeps runtime-specific reserved flags explicit", () => {
  expect(() => validateExtraArguments(["--lifetime-stdin"], ["--lifetime-pipe-fd"])).not.toThrow();
  expect(() =>
    validateExtraArguments(["-lifetime-stdin=true"], ["--lifetime-pipe-fd", "--lifetime-stdin"]),
  ).toThrow("Extra argument --lifetime-stdin collides with an SDK-owned flag");
  expect(() => validateExtraArguments(["http-addr", "--mock"], ["--http-addr"])).not.toThrow();
});
