// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { ChatMessage } from "./chat-state";
import { ChatTranscript } from "./chat-transcript";

afterEach(cleanup);

describe("mounted chat transcript", () => {
  it("does not render a settled row again as the live answer grows", () => {
    let settledContentReads = 0;
    const settled: ChatMessage = {
      get content() {
        settledContentReads += 1;
        return "Earlier answer";
      },
      id: "settled",
      role: "assistant",
    };
    const view = render(
      <ChatTranscript
        messages={[settled, { content: "First", id: "live", role: "assistant" }]}
        showToolCalls
        streamingMessageId="live"
      />,
    );
    expect(screen.getByText("Earlier answer")).toBeTruthy();
    const readsAfterMount = settledContentReads;
    expect(readsAfterMount).toBeGreaterThan(0);

    for (const content of ["First second", "First second third", "Complete answer"]) {
      view.rerender(
        <ChatTranscript
          messages={[settled, { content, id: "live", role: "assistant" }]}
          showToolCalls
          streamingMessageId="live"
        />,
      );
      expect(screen.getByText(content)).toBeTruthy();
      expect(settledContentReads).toBe(readsAfterMount);
    }
    expect(screen.getAllByText("Earlier answer")).toHaveLength(1);
  });
});
