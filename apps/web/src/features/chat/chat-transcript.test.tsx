// SPDX-License-Identifier: Apache-2.0

import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { ChatMessage } from "./chat-state";
import {
  ChatTranscript,
  isNearTranscriptBottom,
  shouldUpdateTranscriptRow,
} from "./chat-transcript";

describe("chat transcript", () => {
  it("renders markdown code and tool results in flat rows", () => {
    const messages: ChatMessage[] = [
      { content: "Please inspect this", id: "u1", role: "user" },
      {
        content:
          "A [safe link](https://example.com) and `inline`.\n\n| A | B |\n|---|---|\n| 1 | 2 |\n\n```ts\nconst answer = 42;\n```\n\n```made-up-lang\nplain text\n```\n\n![inline](data:image/png;base64,aGVsbG8=)\n\n![preview](blob:https://studio.example/image-1)\n\n![remote](https://example.com/track.png)\n\n<script>alert(1)</script>\n\n[bad](javascript:alert(1))",
        id: "a1",
        images: [
          { data: "aGVsbG8=", mimeType: "image/png", name: "Inline" },
          { mimeType: "image/png", name: "Remote", url: "https://example.com/photo.png" },
        ],
        reasoning: "I checked the file.",
        role: "assistant",
        tools: [{ args: "{}", id: "tool-1", name: "Read", output: "file contents" }],
      },
    ];
    const html = renderToStaticMarkup(
      <ChatTranscript messages={messages} showToolCalls streamingMessageId={undefined} />,
    );
    expect(html).toContain('id="chat-message-u1"');
    expect(html).toContain('id="chat-message-a1"');
    expect(html.indexOf("Please inspect")).toBeLessThan(html.indexOf("safe link"));
    expect(html).toContain("<table");
    expect(html).toContain("const answer = 42;");
    expect(html).toContain("plain text");
    expect(html).toContain("Reasoning summary");
    expect(html).toContain("Read");
    expect(html).toContain("file contents");
    expect(html).toContain("overflow-x-auto");
    expect(html).toContain("break-words");
    expect(html).toContain('src="data:image/png;base64,aGVsbG8="');
    expect(html).toMatch(/<img alt="inline"[^>]+src="data:image\/png;base64,aGVsbG8="/u);
    expect(html).toContain('src="blob:https://studio.example/image-1"');
    expect(html).toContain('href="https://example.com/photo.png"');
    expect(html).toContain("remote (external image)");
    expect(html).not.toContain('src="https://example.com/track.png"');
    expect(html).not.toContain('href="javascript:');
    expect(html).not.toContain("<script");
  });

  it("preserves reading position and settled rows during streaming", () => {
    const settled: ChatMessage = { content: "Older answer", id: "a1", role: "assistant" };
    const previous = { message: settled, streaming: false, showToolCalls: true };
    expect(shouldUpdateTranscriptRow(previous, { ...previous })).toBe(false);
    expect(
      shouldUpdateTranscriptRow(previous, {
        ...previous,
        message: { ...settled, content: "Changed" },
      }),
    ).toBe(true);
    expect(isNearTranscriptBottom({ clientHeight: 400, scrollHeight: 2000, scrollTop: 900 })).toBe(
      false,
    );
    expect(isNearTranscriptBottom({ clientHeight: 400, scrollHeight: 2000, scrollTop: 1540 })).toBe(
      true,
    );
  });

  it("omits an empty clean assistant turn after its result", () => {
    const html = renderToStaticMarkup(
      <ChatTranscript
        messages={[{ content: "", id: "empty", role: "assistant", stopReason: "end_turn" }]}
        showToolCalls
      />,
    );
    expect(html).not.toContain('id="chat-message-empty"');
  });

  it("TestStudioChatTranscriptComposer_Scenario7_AttributesRecordedDeliveryToTask", () => {
    const html = renderToStaticMarkup(
      <ChatTranscript
        messages={[
          {
            content: "<script>alert(1)</script> **literal**",
            delivery: {
              fireId: "fire-1",
              kind: "completed",
              scheduleName: "Daily <task>",
              stop: "end_turn",
            },
            id: "delivery-1",
            role: "user",
          },
        ]}
        showToolCalls
        userName="Sam"
      />,
    );
    expect(html).toContain('data-delivery-note="true"');
    expect(html).toContain('aria-label="Scheduled task Daily &lt;task&gt; message"');
    expect(html).toContain(">Scheduled task Daily &lt;task&gt;</h3>");
    expect(html).not.toContain('aria-label="Sam message"');
    expect(html).toContain("Daily &lt;task&gt;");
    expect(html).toContain("**literal**");
    expect(html).not.toContain("<script");
    expect(html).not.toContain("<<<UNTRUSTED");
  });
});
