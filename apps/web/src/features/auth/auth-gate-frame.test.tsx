// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { AuthGate } from "./auth-gate";

vi.mock("@mecatl-studio/contracts/query", () => ({
  getAuthSessionOptions: () => ({}),
  getPublicStatusOptions: () => ({}),
}));
vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ isPending: true, isError: false }),
  useQueryClient: () => ({}),
}));

describe("AuthGate frame", () => {
  it("keeps one empty global status slot and one card in the initial public shell", () => {
    const markup = renderToStaticMarkup(
      <AuthGate>
        <div data-ready-route="">Workspace route</div>
      </AuthGate>,
    );
    const slot = markup.indexOf("data-shell-global-status");
    const gradient = markup.indexOf("data-shell-gradient");
    const content = markup.indexOf("<main");
    expect(slot).toBeGreaterThanOrEqual(0);
    expect(gradient).toBeGreaterThan(slot);
    expect(content).toBeGreaterThan(gradient);
    expect(markup.match(/data-shell-global-status/g)).toHaveLength(1);
    expect(markup.match(/<h1\b/g)).toHaveLength(1);
    expect(markup).not.toContain("data-ready-route");
    expect(markup).not.toContain('role="status"');
    expect(markup).toContain("env(safe-area-inset-top)");
    expect(markup).toContain("env(safe-area-inset-bottom)");
    expect(markup.match(/<main class="([^"]+)"/)?.[1]).toMatch(/min-h-0.*flex-1/);
  });
});
