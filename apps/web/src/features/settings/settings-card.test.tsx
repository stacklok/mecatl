// SPDX-License-Identifier: Apache-2.0

import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Note, SettingsCard } from "./settings-card";

describe("SettingsCard", () => {
  it("renders an eyebrow title without an icon tile and dissolves its chrome on mobile", () => {
    const html = renderToStaticMarkup(
      <SettingsCard title="Storage">
        <p>Body</p>
      </SettingsCard>,
    );
    expect(html).toMatch(/^<section class="rounded-xl border bg-card p-5 [^"]*max-\[499px\]:p-0">/);
    expect(html).toContain("max-[499px]:border-0");
    expect(html).toContain("max-[499px]:bg-transparent");
    expect(html).toMatch(/<h2 class="[^"]*uppercase[^"]*max-\[499px\]:hidden">Storage<\/h2>/);
    expect(html).not.toContain("<svg");
    expect(html).toContain("<p>Body</p>");
  });
});

describe("Note", () => {
  it("renders muted explanatory copy with an optional live-region role", () => {
    expect(renderToStaticMarkup(<Note>Managed here.</Note>)).toBe(
      '<p class="text-sm text-muted-foreground">Managed here.</p>',
    );
    expect(renderToStaticMarkup(<Note role="status">Loading…</Note>)).toContain('role="status"');
  });
});
