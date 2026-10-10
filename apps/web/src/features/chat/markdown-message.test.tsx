// SPDX-License-Identifier: Apache-2.0

import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { MarkdownMessage } from "./markdown-message";

/** The `<pre>` panels in rendered markup, each with its inner HTML. */
function codePanels(html: string): string[] {
  return [...html.matchAll(/<pre[^>]*>([\s\S]*?)<\/pre>/gu)].map((match) => match[1] ?? "");
}

describe("MarkdownMessage code", () => {
  it("renders an unlabelled fence as a dark-panel code block, not as inline code", () => {
    const html = renderToStaticMarkup(
      <MarkdownMessage>{"Run this:\n\n```\ngo test ./...\n```"}</MarkdownMessage>,
    );
    const [panel] = codePanels(html);
    expect(panel).toContain("go test ./...");
    // The inline chip's light background would put near-white text on near-white.
    expect(panel).not.toContain("bg-zinc-100");
    // The block takes the panel's dark tone, like a labelled fence does.
    expect(panel).toContain("data-highlighted");
    expect(panel).toContain("text-[color:var(--sd)]");
    expect(panel).not.toContain("text-[color:var(--sl)]");
  });

  it("renders an indented code block on the dark panel too", () => {
    const html = renderToStaticMarkup(
      <MarkdownMessage>{"Output:\n\n    exit status 1\n"}</MarkdownMessage>,
    );
    const [panel] = codePanels(html);
    expect(panel).toContain("exit status 1");
    expect(panel).not.toContain("bg-zinc-100");
    expect(panel).toContain("text-[color:var(--sd)]");
  });

  it("keeps labelled fences highlighted and inline code as a light chip", () => {
    const html = renderToStaticMarkup(
      <MarkdownMessage>
        {"Call `claim` here:\n\n```go\ndone := make(chan error, 1)\n```"}
      </MarkdownMessage>,
    );
    const [panel] = codePanels(html);
    expect(panel).toContain("done := make(chan error, 1)");
    expect(panel).toContain("text-[color:var(--sd)]");
    expect(html).toMatch(/<code class="[^"]*bg-zinc-100[^"]*">claim<\/code>/u);
    // react-markdown's hast node never leaks onto the DOM as an attribute.
    expect(html).not.toContain('node="');
  });
});
