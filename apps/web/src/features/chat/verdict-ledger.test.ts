import { describe, expect, it } from "vitest";
import { VerdictLedger } from "./verdict-ledger";

const ask = { askId: "a1", runId: "r1", sessionId: "s1" };

describe("VerdictLedger", () => {
  it("refuses a second begin while in flight (double-click sends one POST)", () => {
    const ledger = new VerdictLedger();
    expect(ledger.begin(ask)).toBe(true);
    expect(ledger.begin(ask)).toBe(false);
  });

  it("keeps an uncertain ask blocked until a stream event resets it", () => {
    const ledger = new VerdictLedger();
    ledger.begin(ask);
    ledger.markUncertain(ask);
    expect(ledger.blocks(ask)).toBe(true);
    expect(ledger.begin(ask)).toBe(false);
    ledger.reset(ask);
    expect(ledger.phase(ask)).toBe("idle");
    expect(ledger.begin(ask)).toBe(true);
  });

  it("tracks asks independently", () => {
    const ledger = new VerdictLedger();
    ledger.begin(ask);
    expect(ledger.blocks({ ...ask, askId: "a2" })).toBe(false);
  });
});
