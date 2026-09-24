// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { AuthGate } from "./auth-gate";

const query = vi.hoisted(() => ({ state: "ready" as "ready" | "sign-in" | "error" | "checking" }));

vi.mock("@mecatl-studio/contracts/query", () => ({ getAuthSessionOptions: () => ({}) }));
vi.mock("@tanstack/react-query", () => ({
  useQuery: () => {
    if (query.state === "checking") return { isPending: true, isError: false };
    if (query.state === "error") return { isPending: false, isError: true, refetch: vi.fn() };
    return {
      data: { mode: "oidc", status: query.state === "sign-in" ? "anonymous" : "authenticated" },
      isPending: false,
      isError: false,
    };
  },
}));
vi.mock("../../lib/account-storage", () => ({ reconcileAccount: vi.fn() }));

function renderGate() {
  return renderToStaticMarkup(
    <AuthGate>
      <div data-ready-route="">Workspace route</div>
    </AuthGate>,
  );
}

describe("AuthGate frame", () => {
  it("passes the ready route through without adding a second global status slot", () => {
    query.state = "ready";
    const markup = renderGate();
    expect(markup).toContain('data-ready-route=""');
    expect(markup).not.toContain("data-shell-global-status");
  });

  it.each(["checking", "error", "sign-in"] as const)(
    "reserves a full-width global status slot above the %s view",
    (state) => {
      query.state = state;
      const markup = renderGate();
      const slot = markup.indexOf("data-shell-global-status");
      const gradient = markup.indexOf("data-shell-gradient");
      const content = markup.indexOf("<main");
      expect(slot).toBeGreaterThanOrEqual(0);
      expect(gradient).toBeGreaterThan(slot);
      expect(content).toBeGreaterThan(gradient);
      expect(markup.match(/data-shell-global-status/g)).toHaveLength(1);
      expect(markup).toMatch(/class="[^"]*w-full shrink-0[^"]*" data-shell-global-status/);
      expect(markup).toContain("env(safe-area-inset-top)");
      expect(markup).toContain("env(safe-area-inset-bottom)");
      expect(markup.match(/<main class="([^"]+)"/)?.[1]).toMatch(/min-h-0.*flex-1/);
    },
  );
});
