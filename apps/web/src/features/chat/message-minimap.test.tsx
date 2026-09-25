// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ChatMessage } from "./chat-state";
import { ChatTranscript } from "./chat-transcript";
import type { DelegationActivity } from "./delegation-card";
import { MessageMinimap } from "./message-minimap";

afterEach(cleanup);

const rows: ChatMessage[] = Array.from({ length: 30 }, (_, index) => ({
  content: `Message ${index + 1}`,
  id: `row-${index + 1}`,
  role: index % 2 === 0 ? "user" : "assistant",
}));

function mount(messages = rows, delegationsByMessageId?: Record<string, DelegationActivity[]>) {
  const scrollportRef = createRef<HTMLDivElement>();
  const navigate = vi.fn();
  const view = render(
    <div ref={scrollportRef}>
      <ChatTranscript
        delegationsByMessageId={delegationsByMessageId}
        messages={messages}
        onOpenActivity={() => {}}
        showToolCalls
      />
      <MessageMinimap
        delegationsByMessageId={delegationsByMessageId}
        messages={messages}
        onNavigate={navigate}
        scrollportRef={scrollportRef}
        sessionId="chat-a"
      />
    </div>,
  );
  const scrollport = scrollportRef.current;
  if (!scrollport) throw new Error("Scrollport missing");
  Object.defineProperties(scrollport, {
    clientHeight: { configurable: true, value: 400 },
    scrollHeight: { configurable: true, value: 5000 },
    scrollTop: { configurable: true, value: 2100, writable: true },
  });
  scrollport.getBoundingClientRect = () => ({ top: 100 }) as DOMRect;
  const scrollTo = vi.fn((options: ScrollToOptions) => {
    scrollport.scrollTop = Number(options.top);
  });
  Object.defineProperty(scrollport, "scrollTo", { configurable: true, value: scrollTo });
  return { navigate, scrollport, scrollportRef, scrollTo, view };
}

describe("message minimap", () => {
  it("navigates to an assistant row visible only through delegated activity", () => {
    const messages: ChatMessage[] = [
      { content: "Inspect", id: "prompt", role: "user" },
      { content: "", id: "activity-row", role: "assistant" },
    ];
    const delegationsByMessageId: Record<string, DelegationActivity[]> = {
      "activity-row": [
        {
          childId: "child-a",
          family: "subagent",
          historyIncomplete: false,
          key: "run-a/call-a",
          parentCallId: "call-a",
          runId: "run-a",
          sessionId: "chat-a",
          startObserved: true,
          state: "running",
          trace: { entries: [], omitted: 0 },
        },
      ],
    };
    const { navigate, scrollTo } = mount(messages, delegationsByMessageId);
    const activityRow = document.getElementById("chat-message-activity-row");
    if (!activityRow) throw new Error("Activity-only transcript row missing");
    expect(screen.getByRole("button", { name: /Subagent child-a/u })).toBeTruthy();
    activityRow.getBoundingClientRect = () => ({ top: -1940 }) as DOMRect;
    fireEvent.click(screen.getByRole("button", { name: /^Jump to message 2: assistant$/u }));
    expect(scrollTo).toHaveBeenCalledWith({ behavior: "smooth", top: 60 });
    expect(navigate).toHaveBeenCalledWith("activity-row");
  });

  it("scrolls an early minimap target into its own row", () => {
    const { scrollport, scrollTo } = mount();
    const early = document.getElementById("chat-message-row-2");
    if (!early) throw new Error("Early row missing");
    early.getBoundingClientRect = () => ({ top: -1940 }) as DOMRect;
    fireEvent.click(screen.getByRole("button", { name: /^Jump to message 2:/u }));
    expect(scrollTo).toHaveBeenCalledWith({ behavior: "smooth", top: 60 });
    expect(scrollport.scrollTop).toBe(60);
    expect(document.documentElement.scrollTop).toBe(0);
    expect(
      screen.getByRole("button", { name: /^Jump to message 2:/u }).getAttribute("aria-current"),
    ).toBe("location");
  });

  it("keeps manual minimap position during stream updates", () => {
    const { navigate, scrollport, scrollportRef, view } = mount();
    const first = document.getElementById("chat-message-row-1");
    if (!first) throw new Error("First row missing");
    first.getBoundingClientRect = () => ({ top: -1980 }) as DOMRect;
    fireEvent.click(screen.getByRole("button", { name: /^Jump to message 1:/u }));
    expect(navigate).toHaveBeenCalledWith("row-1");
    expect(scrollport.scrollTop).toBe(20);
    const oldRow = document.getElementById("chat-message-row-1");
    const updated = [...rows, { content: "Streaming", id: "live", role: "assistant" }];
    view.rerender(
      <div ref={scrollportRef}>
        <ChatTranscript messages={updated} showToolCalls streamingMessageId="live" />
        <MessageMinimap
          messages={updated}
          onNavigate={navigate}
          scrollportRef={scrollportRef}
          sessionId="chat-a"
          streamingMessageId="live"
        />
      </div>,
    );
    expect(document.getElementById("chat-message-row-1")).toBe(oldRow);
    expect(scrollport.scrollTop).toBe(20);
    expect(screen.getAllByRole("button", { name: /Jump to message/u })).toHaveLength(31);
    expect(
      screen.getByRole("button", { name: /^Jump to message 1:/u }).getAttribute("aria-current"),
    ).toBe("location");
    view.rerender(
      <div ref={scrollportRef}>
        <ChatTranscript
          messages={[{ content: "Other chat", id: "row-1", role: "user" }]}
          showToolCalls
        />
        <MessageMinimap
          messages={[{ content: "Other chat", id: "row-1", role: "user" }]}
          onNavigate={navigate}
          scrollportRef={scrollportRef}
          sessionId="chat-b"
        />
      </div>,
    );
    expect(screen.queryByRole("button", { name: /^Jump to message 2:/u })).toBeNull();
    expect(
      screen.getByRole("button", { name: /^Jump to message 1:/u }).hasAttribute("aria-current"),
    ).toBe(false);
  });

  it("supports minimap keyboard and mobile navigation", async () => {
    const user = userEvent.setup();
    const { navigate } = mount(rows.slice(0, 2));
    const first = screen.getByRole("button", { name: /^Jump to message 1:/u });
    const second = screen.getByRole("button", { name: /^Jump to message 2:/u });
    expect(first.getAttribute("type")).toBe("button");
    expect(second.getAttribute("type")).toBe("button");
    await user.tab();
    expect(document.activeElement).toBe(first);
    await user.keyboard("{Enter}");
    expect(navigate).toHaveBeenCalledWith("row-1");
    await user.tab();
    expect(document.activeElement).toBe(second);
    await user.keyboard(" ");
    expect(navigate).toHaveBeenCalledWith("row-2");
    expect(second.className).toContain("focus-visible:");
    expect(screen.getByRole("navigation", { name: "Message minimap" }).className).toContain(
      "max-w-",
    );
    fireEvent.click(first);
    expect(first.getAttribute("aria-current")).toBe("location");
  });
});
