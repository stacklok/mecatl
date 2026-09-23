// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import {
  connectionBannerMessages,
  connectionBannerState,
  type RuntimeResponse,
} from "./connection-status-banner-state";

function runtime(connection: RuntimeResponse["connection"]) {
  return {
    data: { connection } as RuntimeResponse,
    isError: false,
    isPending: false,
  };
}

describe("connectionBannerState", () => {
  it("the connection banner state maps connected, reconnecting, and unavailable", () => {
    expect(connectionBannerState({ isError: false, isPending: true })).toBe("hidden");
    expect(connectionBannerState(runtime("online"))).toBe("hidden");
    expect(connectionBannerState(runtime("reconnecting"))).toEqual({ kind: "reconnecting" });
    // A 503 or a fetch failure surfaces as a query error.
    expect(connectionBannerState({ isError: true, isPending: false })).toEqual({
      kind: "unavailable",
    });
    for (const connection of ["connecting", "offline", "unauthorized", "incompatible"] as const) {
      expect(connectionBannerState(runtime(connection))).toEqual({ kind: "unavailable" });
    }
    expect(connectionBannerMessages.reconnecting).not.toBe(connectionBannerMessages.unavailable);
  });
});
