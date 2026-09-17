import { describe, expect, it } from "vitest";
import { parseDeliveryNote } from "./delivery-note";

/**
 * Pins Studio's delivery-note detection to the daemon's exact renderer output
 * (`renderFireDelivery` / `renderFireStarted` in
 * internal/app/scheduler_delivery.go) and to the shared fenced-discriminator
 * contract the TUI and the relay use: fence opener, then the header prefix.
 */
describe("parseDeliveryNote", () => {
  it("parses the exact renderFireDelivery output", () => {
    expect(
      parseDeliveryNote(
        "<<<UNTRUSTED\n[scheduled task nightly (fire f-1) completed with stop reason: end_turn]\nline one\nline two\n<<<UNTRUSTED\n",
      ),
    ).toEqual({
      scheduleName: "nightly",
      fireId: "f-1",
      kind: "completed",
      stop: "end_turn",
      body: "line one\nline two",
    });
  });

  it("parses the renderFireStarted note as kind started with an empty body", () => {
    expect(
      parseDeliveryNote(
        "<<<UNTRUSTED\n[scheduled task nightly started (fire f-1)]\n<<<UNTRUSTED\n",
      ),
    ).toEqual({
      scheduleName: "nightly",
      fireId: "f-1",
      kind: "started",
      body: "",
    });
  });

  it("round-trips a schedule name with spaces and punctuation", () => {
    const parsed = parseDeliveryNote(
      "<<<UNTRUSTED\n[scheduled task Nightly digest: PRs & issues, v2! (fire sched--nightly-1700000000) completed with stop reason: error]\nboom\n<<<UNTRUSTED\n",
    );
    expect(parsed?.scheduleName).toBe("Nightly digest: PRs & issues, v2!");
    expect(parsed?.fireId).toBe("sched--nightly-1700000000");
    expect(parsed?.stop).toBe("error");
    expect(parsed?.body).toBe("boom");
  });

  it("keeps a schedule name that itself ends in 'started' on a completed note", () => {
    const parsed = parseDeliveryNote(
      "<<<UNTRUSTED\n[scheduled task deploy started (fire f-9) completed with stop reason: end_turn]\nok\n<<<UNTRUSTED\n",
    );
    expect(parsed?.scheduleName).toBe("deploy started");
    expect(parsed?.kind).toBe("completed");
  });

  it("preserves the daemon's (no output) placeholder body", () => {
    expect(
      parseDeliveryNote(
        "<<<UNTRUSTED\n[scheduled task nightly (fire f-1) completed with stop reason: end_turn]\n(no output)\n<<<UNTRUSTED\n",
      )?.body,
    ).toBe("(no output)");
  });

  it("keeps a multi-line body verbatim apart from the closing fence", () => {
    const parsed = parseDeliveryNote(
      "<<<UNTRUSTED\n[scheduled task nightly (fire f-1) completed with stop reason: end_turn]\n# heading\n\n- a\n- b\n\n<<<UNTRUSTED\n",
    );
    expect(parsed?.body).toBe("# heading\n\n- a\n- b");
  });

  it("rejects an un-fenced header (a user who typed the prefix)", () => {
    expect(
      parseDeliveryNote(
        "[scheduled task x (fire 1) completed with stop reason: end_turn]",
      ),
    ).toBeNull();
  });

  it("rejects a fenced non-delivery body", () => {
    expect(
      parseDeliveryNote("<<<UNTRUSTED\nsome fetched page text\n<<<UNTRUSTED\n"),
    ).toBeNull();
  });

  it("rejects a header with no fire id", () => {
    expect(
      parseDeliveryNote(
        "<<<UNTRUSTED\n[scheduled task nightly completed with stop reason: end_turn]\nbody\n<<<UNTRUSTED\n",
      ),
    ).toBeNull();
    expect(
      parseDeliveryNote(
        "<<<UNTRUSTED\n[scheduled task nightly (fire ) completed with stop reason: end_turn]\nbody\n<<<UNTRUSTED\n",
      ),
    ).toBeNull();
  });

  it("rejects an empty schedule name", () => {
    expect(
      parseDeliveryNote(
        "<<<UNTRUSTED\n[scheduled task  (fire f-1) completed with stop reason: end_turn]\nbody\n<<<UNTRUSTED\n",
      ),
    ).toBeNull();
  });

  it("rejects a header of an unknown shape", () => {
    expect(
      parseDeliveryNote(
        "<<<UNTRUSTED\n[scheduled task nightly (fire f-1) exploded]\nbody\n<<<UNTRUSTED\n",
      ),
    ).toBeNull();
  });

  it("rejects plain text and the empty string", () => {
    expect(parseDeliveryNote("Why does the scheduler test flake?")).toBeNull();
    expect(parseDeliveryNote("")).toBeNull();
  });
});
