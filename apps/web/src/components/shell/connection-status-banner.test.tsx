// SPDX-License-Identifier: Apache-2.0

import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { AuthRecoveryContext } from "../../features/auth/auth-recovery-context";
import { ConnectionStatusBanner } from "./connection-status-banner";
import { statusBannerState } from "./connection-status-banner-state";

const base = {
  authenticated: false,
  publicStatus: { connection: "reachable" as const, signInRequired: true },
  publicStatusFailed: false,
  sessionCheckFailed: false,
};

describe("public status banner", () => {
  it("public status banners prioritize outages before sign-in", () => {
    expect(statusBannerState({ ...base, publicStatusFailed: true })).toBe("bff-unavailable");
    expect(
      statusBannerState({
        ...base,
        publicStatus: { ...base.publicStatus, connection: "unavailable" },
      }),
    ).toBe("daemon-unavailable");
    expect(statusBannerState({ ...base, sessionCheckFailed: true })).toBe("session-check-failed");
    expect(statusBannerState(base)).toBe("sign-in");
    expect(
      statusBannerState({
        ...base,
        publicStatus: { connection: "checking", signInRequired: true },
      }),
    ).toBe("hidden");
    expect(statusBannerState({ ...base, publicStatus: undefined })).toBe("hidden");
  });

  it("anonymous banners never request storage health", () => {
    const input = {
      ...base,
      publicStatus: { connection: "unavailable" as const, signInRequired: true },
    };
    // No QueryClientProvider is present: a detailed runtime or storage query
    // inside the real banner would throw during this render.
    const html = renderToStaticMarkup(
      <AuthRecoveryContext.Provider
        value={{
          banner: input,
          loginUrl: "/api/v1/auth/login?flow=popup",
          phase: "sign-in",
          popupIssue: null,
          retrySession: () => {},
          startPopupLogin: () => {},
        }}
      >
        <ConnectionStatusBanner />
      </AuthRecoveryContext.Provider>,
    );
    expect(html).toContain("Mecatl instance is unavailable");
    expect(Object.keys(input.publicStatus).sort()).toEqual(["connection", "signInRequired"]);
  });

  it("draws the band and its actions in the warning contrast tokens", () => {
    const html = renderToStaticMarkup(
      <AuthRecoveryContext.Provider
        value={{
          banner: base,
          loginUrl: "/api/v1/auth/login?flow=popup",
          phase: "ready",
          popupIssue: "blocked",
          retrySession: () => {},
          startPopupLogin: () => {},
        }}
      >
        <ConnectionStatusBanner />
      </AuthRecoveryContext.Provider>,
    );
    const band = html.match(/<div class="([^"]+)" role="status"/)?.[1] ?? "";
    expect(band.split(" ")).toEqual(
      expect.arrayContaining(["bg-warning", "text-warning-foreground"]),
    );
    expect(band).toContain("min-h-11");
    const actions = [...html.matchAll(/<(?:button|a)\b[^>]*class="([^"]+)"/g)].map(
      ([, cls]) => cls ?? "",
    );
    expect(actions).toHaveLength(3);
    for (const cls of actions) {
      expect(cls.split(" ")).toEqual(
        expect.arrayContaining([
          "text-warning-foreground",
          "focus-visible:ring-warning-foreground",
          "focus-visible:ring-offset-warning",
        ]),
      );
    }
    expect(html).toContain("Sign in again");
    expect(html).toContain("Retry sign-in");
    expect(html).toContain("Open sign-in in a new tab");
  });
});
