import type { Event as SdkEvent } from "@stacklok-oss/mecatl-sdk";
import { describe, expect, it } from "vitest";
import { translateEvent } from "./events";

/**
 * Pins the translation of the MCP browser-authorization lifecycle
 * (`authorization.required` / `authorization.resolved`): both render as their
 * own StreamEvents — never the "not rendered yet" notice — and the protobuf
 * Timestamp expiry becomes epoch milliseconds.
 */

function sdkEvent(kind: string, payload: unknown, runId = "run-1"): SdkEvent {
  return {
    kind,
    payload,
    runId,
    seq: BigInt(7),
    text: "",
    turn: 1,
    usage: undefined,
  } as unknown as SdkEvent;
}

const authorization = (overrides: Record<string, unknown> = {}) => ({
  authorizationId: "auth-1",
  callId: "call-1",
  displayName: "GitHub MCP",
  expiresAt: { seconds: BigInt(1_800_000_000), nanos: 500_000_000 },
  status: "pending",
  ...overrides,
});

describe("translateEvent — MCP browser authorization", () => {
  it("renders authorization.required as a parked authorization with the expiry in milliseconds", () => {
    expect(
      translateEvent(sdkEvent("authorization.required", authorization()), "s1"),
    ).toEqual([
      {
        type: "authorization",
        authorizationId: "auth-1",
        callId: "call-1",
        displayName: "GitHub MCP",
        status: "pending",
        expiresAt: 1_800_000_000_500,
        runId: "run-1",
      },
    ]);
  });

  it("leaves the expiry absent when the daemon set none (the zero timestamp is unset, not 1970)", () => {
    const [absent] = translateEvent(
      sdkEvent(
        "authorization.required",
        authorization({ expiresAt: undefined }),
      ),
      "s1",
    );
    expect(absent).toMatchObject({ type: "authorization" });
    expect((absent as { expiresAt?: number }).expiresAt).toBeUndefined();
    const [zero] = translateEvent(
      sdkEvent(
        "authorization.required",
        authorization({ expiresAt: { seconds: BigInt(0), nanos: 0 } }),
      ),
      "s1",
    );
    expect((zero as { expiresAt?: number }).expiresAt).toBeUndefined();
  });

  it("renders authorization.resolved with the terminal status passed through", () => {
    expect(
      translateEvent(
        sdkEvent(
          "authorization.resolved",
          authorization({ status: "granted" }),
          "run-2",
        ),
        "s1",
      ),
    ).toEqual([
      {
        type: "authorization_resolved",
        authorizationId: "auth-1",
        displayName: "GitHub MCP",
        status: "granted",
        runId: "run-2",
      },
    ]);
  });

  it("never degrades either kind to the unrendered-event notice", () => {
    for (const kind of ["authorization.required", "authorization.resolved"]) {
      const events = translateEvent(sdkEvent(kind, authorization()), "s1");
      expect(events.some((event) => event.type === "notice")).toBe(false);
    }
  });
});
