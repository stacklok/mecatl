// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import type { ChatMessage } from "./chat-state";
import {
  reconcileRecordedMessages,
  shouldRefreshTranscriptAfterInventory,
} from "./use-delivery-follow";

const previous = { id: "chat-a", updatedAt: "2026-09-24T12:00:00.000Z" };
const advanced = { id: "chat-a", updatedAt: "2026-09-24T12:00:20.000Z" };

describe("delivery follow", () => {
  it("merges a recorded delivery and its reply after inventory changes", () => {
    expect(
      shouldRefreshTranscriptAfterInventory({
        connected: true,
        idle: true,
        next: advanced,
        previous,
        sessionId: "chat-a",
        visible: true,
      }),
    ).toBe(true);
    const liveStart: ChatMessage = {
      content: "Task started",
      delivery: { fireId: "fire-1", kind: "started", scheduleName: "Daily" },
      id: "live-start",
      role: "user",
      tools: [{ args: "{}", id: "tool-1", name: "Read", output: "live detail" }],
    };
    const liveAnswer: ChatMessage = {
      content: "The scheduled answer",
      id: "live-answer",
      reasoning: "Looked at the file",
      role: "assistant",
      tools: [{ args: "{}", id: "tool-1", name: "Read", output: "live detail" }],
    };
    const current = [liveStart, liveAnswer];
    const transcript: ChatMessage[] = [
      {
        content: "Task started",
        delivery: { fireId: "fire-1", kind: "started", scheduleName: "Daily" },
        id: "transcript-start",
        role: "user",
      },
      {
        content: "The scheduled answer",
        id: "transcript-answer",
        role: "assistant",
        tools: [{ args: "{}", id: "tool-1", name: "Read", output: "saved detail" }],
      },
      {
        content: "Task completed",
        delivery: { fireId: "fire-1", kind: "completed", scheduleName: "Daily", stop: "end_turn" },
        id: "transcript-complete",
        role: "user",
      },
      { content: "An ordinary user message", id: "ordinary", role: "user" },
      { content: "Another client's answer", id: "other-answer", role: "assistant" },
    ];
    const merged = reconcileRecordedMessages(current, transcript);
    expect(merged).toEqual([liveStart, liveAnswer, transcript[2], transcript[3], transcript[4]]);
    expect(merged[0]).toBe(liveStart);
    expect(merged[1]).toBe(liveAnswer);
    expect(reconcileRecordedMessages(merged, transcript)).toBe(merged);
    expect(reconcileRecordedMessages([], transcript)).toEqual(transcript);
  });

  it("matches repeated ordinary turns by occurrence and keeps saved order", () => {
    const first = { content: "Repeat", id: "live-first", role: "user" };
    const second = { content: "Repeat", id: "live-second", role: "user" };
    const saved = [
      { content: "Repeat", id: "saved-first", role: "user" },
      { content: "New answer", id: "saved-answer", role: "assistant" },
      { content: "Repeat", id: "saved-second", role: "user" },
    ];
    const merged = reconcileRecordedMessages([first, second], saved);
    expect(merged).toEqual([first, saved[1], second]);
    expect(reconcileRecordedMessages(merged, saved)).toBe(merged);
  });

  it("matches renamed image prompts by occurrence and keeps the live attachment", () => {
    const first: ChatMessage = {
      content: "",
      id: "live-first",
      images: [{ data: "Zmlyc3Q=", id: "upload-first", mimeType: "image/png", name: "first.png" }],
      role: "user",
    };
    const second: ChatMessage = {
      content: "",
      id: "live-second",
      images: [
        { data: "c2Vjb25k", id: "upload-second", mimeType: "image/png", name: "second.png" },
      ],
      role: "user",
    };
    const saved: ChatMessage[] = [
      {
        content: "",
        id: "transcript-0",
        images: [
          { data: "Zmlyc3Q=", id: "transcript-0-image-0", mimeType: "image/png", name: "Image 1" },
        ],
        role: "user",
      },
      { content: "First answer", id: "transcript-1", role: "assistant" },
      {
        content: "",
        id: "transcript-2",
        images: [
          { data: "c2Vjb25k", id: "transcript-2-image-0", mimeType: "image/png", name: "Image 1" },
        ],
        role: "user",
      },
    ];

    const merged = reconcileRecordedMessages([first, second], saved);
    expect(merged).toEqual([first, saved[1], second]);
    expect(merged.filter((message) => message.role === "user")).toHaveLength(2);
    expect(merged[0]).toBe(first);
    expect(merged[2]).toBe(second);
    expect(reconcileRecordedMessages(merged, saved)).toBe(merged);
  });

  it("keeps another client's image before a matching live image-only prompt", () => {
    const live: ChatMessage = {
      content: "",
      id: "live-b",
      images: [{ data: "aW1hZ2UgQg==", id: "upload-b", mimeType: "image/png", name: "b.png" }],
      role: "user",
    };
    const saved: ChatMessage[] = [
      {
        content: "",
        id: "transcript-a",
        images: [
          {
            data: "aW1hZ2UgQQ==",
            id: "transcript-a-image",
            mimeType: "image/png",
            name: "Image 1",
          },
        ],
        role: "user",
      },
      {
        content: "",
        id: "transcript-b",
        images: [
          {
            data: "aW1hZ2UgQg==",
            id: "transcript-b-image",
            mimeType: "image/png",
            name: "Image 1",
          },
        ],
        role: "user",
      },
    ];

    const merged = reconcileRecordedMessages([live], saved);
    expect(merged).toEqual([saved[0], live]);
    expect(merged[1]).toBe(live);
    expect(reconcileRecordedMessages(merged, saved)).toBe(merged);
  });

  it("matches URL-only saved images without treating a different URL as the live image", () => {
    const live: ChatMessage = {
      content: "",
      id: "live-b",
      images: [
        { id: "upload-b", mimeType: "image/png", name: "b.png", url: "https://example.test/b.png" },
      ],
      role: "user",
    };
    const saved: ChatMessage[] = [
      {
        content: "",
        id: "transcript-a",
        images: [{ mimeType: "image/png", name: "Image 1", url: "https://example.test/a.png" }],
        role: "user",
      },
      {
        content: "",
        id: "transcript-b",
        images: [{ mimeType: "image/png", name: "Image 1", url: "https://example.test/b.png" }],
        role: "user",
      },
    ];

    expect(reconcileRecordedMessages([live], saved)).toEqual([saved[0], live]);
  });

  it("fills a matched live row with its recorded tool result", () => {
    const live: ChatMessage = {
      content: "Answer",
      id: "live-answer",
      reasoning: "Live reasoning",
      role: "assistant",
      tools: [{ args: "{}", id: "read-1", name: "Read" }],
    };
    const saved: ChatMessage = {
      content: "Answer",
      id: "saved-answer",
      role: "assistant",
      tools: [{ args: "{}", id: "read-1", name: "Read", output: "File contents" }],
    };
    expect(reconcileRecordedMessages([live], [saved])).toEqual([{ ...live, tools: saved.tools }]);
  });

  it("keeps a matched row's render ID unique when a saved turn arrives before it", () => {
    const existing = { content: "Earlier answer", id: "transcript-0", role: "assistant" };
    const saved = [
      { content: "New prompt", id: "transcript-0", role: "user" },
      { content: "Earlier answer", id: "transcript-1", role: "assistant" },
    ];
    const merged = reconcileRecordedMessages([existing], saved);
    expect(merged.map((message) => message.content)).toEqual(["New prompt", "Earlier answer"]);
    expect(merged[1]).toBe(existing);
    expect(new Set(merged.map((message) => message.id)).size).toBe(2);
    expect(reconcileRecordedMessages(merged, saved)).toBe(merged);
  });

  it("keeps an unmatched live turn until it appears in saved history", () => {
    const live: ChatMessage = {
      content: "The just-finished answer",
      id: "live-answer",
      reasoning: "Live detail",
      role: "assistant",
    };
    expect(reconcileRecordedMessages([live], [])).toEqual([live]);
    expect(
      reconcileRecordedMessages(
        [live],
        [{ content: "The just-finished answer", id: "transcript-0", role: "assistant" }],
      ),
    ).toEqual([live]);
  });

  it("completes a partial live assistant after the same saved prompt", () => {
    const prompt = { content: "Summarize", id: "live-prompt", role: "user" };
    const partial: ChatMessage = {
      content: "The answer is",
      id: "live-answer",
      reasoning: "Live reasoning",
      role: "assistant",
    };
    const saved = [
      { content: "Summarize", id: "transcript-0", role: "user" },
      { content: "The answer is complete", id: "transcript-1", role: "assistant" },
    ];
    const merged = reconcileRecordedMessages([prompt, partial], saved);
    expect(merged).toEqual([prompt, { ...partial, content: "The answer is complete" }]);
    expect(new Set(merged.map((message) => message.id)).size).toBe(2);
  });

  it("checks only an idle, visible, connected row for the same open chat", () => {
    const facts = {
      connected: true,
      idle: true,
      next: advanced,
      previous,
      sessionId: "chat-a",
      visible: true,
    };
    expect(shouldRefreshTranscriptAfterInventory({ ...facts, idle: false })).toBe(false);
    expect(shouldRefreshTranscriptAfterInventory({ ...facts, visible: false })).toBe(false);
    expect(shouldRefreshTranscriptAfterInventory({ ...facts, connected: false })).toBe(false);
    expect(shouldRefreshTranscriptAfterInventory({ ...facts, sessionId: "chat-b" })).toBe(false);
    expect(shouldRefreshTranscriptAfterInventory({ ...facts, previous: advanced })).toBe(false);
    expect(
      shouldRefreshTranscriptAfterInventory({
        ...facts,
        next: { ...advanced, updatedAt: "2026-09-24T11:59:00.000Z" },
      }),
    ).toBe(false);
    expect(shouldRefreshTranscriptAfterInventory({ ...facts, previous: undefined })).toBe(false);
  });
});
