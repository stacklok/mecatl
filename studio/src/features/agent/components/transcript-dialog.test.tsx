import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { SessionTranscript } from "@/lib/protocol";
import { TranscriptDialog } from "./transcript-dialog";

/**
 * The read-only transcript viewer (the TUI's `v` viewer): a failed load shows
 * the error WITH a Retry that re-drives the same session's load (no
 * close-and-reopen), the relationship subtitle renders under the label, and
 * a child run offers its parent chat as a link.
 */

const mocks = vi.hoisted(() => ({
  fetchSessionTranscriptMessages: vi.fn(),
}));

vi.mock("@/lib/harness/client", () => ({
  fetchSessionTranscriptMessages: mocks.fetchSessionTranscriptMessages,
}));

const transcript = (
  messages: SessionTranscript["messages"],
  complete = true,
): SessionTranscript => ({ sessionId: "child-1", complete, messages });

afterEach(() => {
  mocks.fetchSessionTranscriptMessages.mockReset();
});

describe("TranscriptDialog", () => {
  it("shows a failed load's error with Retry, and a second attempt replaces it with the transcript", async () => {
    mocks.fetchSessionTranscriptMessages
      .mockRejectedValueOnce(new Error("store unavailable"))
      .mockResolvedValueOnce(
        transcript([
          { role: "user", text: "Scan the tests", toolCalls: [] },
          { role: "assistant", text: "Two flaky tests found.", toolCalls: [] },
        ]),
      );
    render(
      <TranscriptDialog
        sessionId="child-1"
        label="subagent"
        onClose={() => {}}
      />,
    );

    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent("store unavailable"),
    );
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    await waitFor(() =>
      expect(screen.getByText("Two flaky tests found.")).toBeInTheDocument(),
    );
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(mocks.fetchSessionTranscriptMessages).toHaveBeenCalledTimes(2);
    expect(mocks.fetchSessionTranscriptMessages.mock.calls[1][0]).toBe(
      "child-1",
    );
  });

  it("renders the relationship subtitle and offers the parent chat as a link", async () => {
    mocks.fetchSessionTranscriptMessages.mockResolvedValue(transcript([]));
    const onOpenParent = vi.fn();
    render(
      <TranscriptDialog
        sessionId="child-1"
        label="Scan the tests"
        subtitle="Subagent of main-1 · call c7"
        parentSessionId="main-1"
        onOpenParent={onOpenParent}
        onClose={() => {}}
      />,
    );

    expect(
      screen.getByText("Subagent of main-1 · call c7"),
    ).toBeInTheDocument();
    expect(screen.getByText("child-1")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open parent chat" }));
    expect(onOpenParent).toHaveBeenCalledWith("main-1");
    await waitFor(() =>
      expect(
        screen.getByText(/holds no replayable conversation/),
      ).toBeInTheDocument(),
    );
  });

  it("offers no parent link without a parent, and warns when the transcript is unproven", async () => {
    mocks.fetchSessionTranscriptMessages.mockResolvedValue(
      transcript(
        [{ role: "assistant", text: "partial", toolCalls: [] }],
        false,
      ),
    );
    render(
      <TranscriptDialog
        sessionId="fire-1"
        label="nightly"
        onOpenParent={() => {}}
        onClose={() => {}}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Open parent chat" }),
    ).not.toBeInTheDocument();
    await waitFor(() =>
      expect(
        screen.getByText(/could not be proven complete/),
      ).toBeInTheDocument(),
    );
  });
});
