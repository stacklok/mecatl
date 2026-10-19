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

describe("MarkdownMessage lists", () => {
  it("keeps an ordered list's start number", () => {
    const html = renderToStaticMarkup(
      <MarkdownMessage>{"1. first\n\n```\nstep\n```\n\n2. second\n3. third"}</MarkdownMessage>,
    );
    const lists = [...html.matchAll(/<ol[^>]*>/gu)].map((match) => match[0]);
    expect(lists).toHaveLength(2);
    expect(lists[0]).not.toContain("start=");
    expect(lists[1]).toContain('start="2"');
    expect(lists[1]).toContain("list-decimal");
  });

  it("renders a task list with its checkboxes and no bullets", () => {
    const html = renderToStaticMarkup(
      <MarkdownMessage>{"- [x] done\n- [ ] open\n\nNext:\n\n- plain"}</MarkdownMessage>,
    );
    const lists = [...html.matchAll(/<ul[^>]*>/gu)].map((match) => match[0]);
    expect(lists).toHaveLength(2);
    expect(lists[0]).toContain("contains-task-list");
    expect(lists[0]).not.toContain("list-disc");
    expect(lists[1]).toContain("list-disc");
    expect(html).toMatch(/<li class="[^"]*task-list-item[^"]*">/u);
    expect(html.match(/type="checkbox"/gu)).toHaveLength(2);
    expect(html).not.toContain('node="');
  });
});
