// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { PageShell } from "./page-shell";

function render(element: React.ReactElement): HTMLElement {
  const host = document.createElement("div");
  host.innerHTML = renderToStaticMarkup(element);
  return host.firstElementChild as HTMLElement;
}

describe("PageShell", () => {
  it("scrolls a full-width, left-aligned page instead of a centered column", () => {
    const shell = render(
      <PageShell>
        <h1>Settings</h1>
      </PageShell>,
    );
    expect(shell.className).toContain("overflow-y-auto");
    expect(shell.className).not.toMatch(/mx-auto|max-w-/u);
    const content = shell.firstElementChild as HTMLElement;
    expect(content.className).not.toMatch(/mx-auto/u);
    expect(content.textContent).toBe("Settings");
  });

  it("lets a page cap its own reading width without centering it", () => {
    const shell = render(<PageShell className="max-w-3xl">body</PageShell>);
    const content = shell.firstElementChild as HTMLElement;
    expect(content.classList.contains("max-w-3xl")).toBe(true);
    expect(content.className).not.toMatch(/mx-auto/u);
  });
});
