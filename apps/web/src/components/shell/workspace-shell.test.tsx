// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { AuthRecoveryContext } from "../../features/auth/auth-recovery-context";
import type { StatusBannerInput } from "./connection-status-banner-state";
import { WorkspaceShell } from "./workspace-shell";

vi.mock("@tanstack/react-router", () => ({ Outlet: () => <div>Route content</div> }));
vi.mock("../../features/shortcuts/shortcut-provider", () => ({
  ShortcutProvider: ({ children }: { children: ReactNode }) => children,
}));
vi.mock("../ui/tooltip", () => ({
  TooltipProvider: ({ children }: { children: ReactNode }) => children,
}));
vi.mock("./top-nav", () => ({ TopNav: () => <header data-shell-nav="">Navigation</header> }));

const baseBanner: StatusBannerInput = {
  authenticated: false,
  publicStatus: { connection: "reachable", signInRequired: true },
  publicStatusFailed: false,
  sessionCheckFailed: false,
};

function renderShell(banner: StatusBannerInput = baseBanner) {
  return renderToStaticMarkup(
    <AuthRecoveryContext.Provider
      value={{
        banner,
        loginUrl: "/api/v1/auth/login?return_to=%2Fworkspace%2Fchat",
        phase: "sign-in",
        popupIssue: null,
        retrySession: () => {},
        startPopupLogin: () => {},
      }}
    >
      <WorkspaceShell />
    </AuthRecoveryContext.Provider>,
  );
}

function between(markup: string, start: string, end: string) {
  const from = markup.indexOf(start);
  const to = markup.indexOf(end);
  expect(from).toBeGreaterThanOrEqual(0);
  expect(to).toBeGreaterThan(from);
  return markup.slice(from, to);
}

describe("WorkspaceShell", () => {
  it("shows each public recovery cause only in the global status slot", () => {
    let markup = renderShell({
      ...baseBanner,
      publicStatus: { connection: "unavailable", signInRequired: true },
    });
    expect(between(markup, "data-shell-global-status", "data-shell-gradient")).toContain(
      "The Mecatl instance is unavailable right now.",
    );
    expect(between(markup, "data-shell-transient-status", "data-shell-nav")).not.toContain(
      "The Mecatl instance is unavailable right now.",
    );
    expect(markup.match(/The Mecatl instance is unavailable right now\./g)).toHaveLength(1);

    markup = renderShell();
    expect(between(markup, "data-shell-global-status", "data-shell-gradient")).toContain(
      "Sign in to use this Mecatl workspace.",
    );
    expect(between(markup, "data-shell-transient-status", "data-shell-nav")).not.toContain(
      "Sign in to use this Mecatl workspace.",
    );

    markup = renderShell({
      ...baseBanner,
      publicStatus: { connection: "checking", signInRequired: true },
    });
    expect(markup).not.toContain("The Mecatl instance is unavailable right now.");
    expect(markup).not.toContain("Sign in to use this Mecatl workspace.");
  });

  it("keeps the route outlet below both flexible banner slots and inside the safe-area frame", () => {
    const markup = renderShell({
      ...baseBanner,
      publicStatus: { connection: "unavailable", signInRequired: true },
    });
    const route = markup.indexOf("Route content");
    expect(route).toBeGreaterThan(markup.indexOf("data-shell-nav"));
    expect(markup).toContain("env(safe-area-inset-top)");
    expect(markup).toContain("env(safe-area-inset-bottom)");
    expect(markup.match(/<main class="([^"]+)"/)?.[1]).toMatch(/min-h-0.*flex-1.*overflow-hidden/);
  });
});
