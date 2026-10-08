// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TranscriptDialog } from "./transcript-dialog";

const fixture = vi.hoisted(() => ({
  calls: [] as string[],
  error: undefined as Error | { detail: string } | undefined,
  pending: false,
  response: {
    complete: true,
    messages: [] as Array<{
      delivery?: {
        fireId: string;
        kind: "completed" | "started";
        scheduleName: string;
        stop?: string;
      };
      images: Array<{ data?: string; mimeType: string; name: string; url?: string }>;
      role: string;
      text: string;
      toolCalls: Array<{ args: string; id: string; name: string }>;
      toolResult?: { callId: string; content: string; isError: boolean };
    }>,
    sessionId: "scheduled-session",
  },
}));

vi.mock("@mecatl-studio/contracts/query", () => ({
  getSessionTranscriptOptions: ({ path }: { path: { sessionId: string } }) => ({
    queryFn: async () => {
      fixture.calls.push(path.sessionId);
      if (fixture.pending) await new Promise<never>(() => undefined);
      if (fixture.error) throw fixture.error;
      return fixture.response;
    },
    queryKey: ["transcript", path.sessionId],
  }),
}));

beforeEach(() => {
  fixture.calls = [];
  fixture.error = undefined;
  fixture.pending = false;
  fixture.response = { complete: true, messages: [], sessionId: "scheduled-session" };
});

afterEach(cleanup);

function renderDialog() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <TranscriptDialog
        onOpenChange={() => undefined}
        open
        sessionId="scheduled-session"
        title="Daily report — Oct 7"
      />
    </QueryClientProvider>,
  );
}

describe("TranscriptDialog", () => {
  it("shows loading while the transcript read is pending", () => {
    fixture.pending = true;
    renderDialog();
    expect(screen.getByRole("status").textContent).toContain("Reading transcript…");
  });

  it("renders an incomplete transcript with role labels and tool details", async () => {
    fixture.response = {
      complete: false,
      messages: [
        {
          images: [],
          role: "user",
          text: "Build the report",
          toolCalls: [],
        },
        {
          images: [],
          role: "assistant",
          text: "Done",
          toolCalls: [{ args: '{"path":"report.txt"}', id: "tool-1", name: "Write" }],
          toolResult: { callId: "tool-1", content: "saved", isError: false },
        },
      ],
      sessionId: "scheduled-session",
    };

    renderDialog();

    expect(await screen.findByText(/transcript is incomplete/i)).toBeTruthy();
    expect(screen.getByRole("article", { name: "You message" }).textContent).toContain(
      "Build the report",
    );
    const assistant = screen.getByRole("article", { name: "Mecatl message" });
    expect(within(assistant).getByText("Tool: Write")).toBeTruthy();
    expect(within(assistant).getByText("saved")).toBeTruthy();
    expect(fixture.calls).toEqual(["scheduled-session"]);
    expect(screen.queryByRole("textbox")).toBeNull();
    for (const name of ["Retry", "Fork", "Rename", "Delete", "Send message"]) {
      expect(screen.queryByRole("button", { name })).toBeNull();
    }
  });

  it("shows incompleteness even when no messages were retained", async () => {
    fixture.response = { complete: false, messages: [], sessionId: "scheduled-session" };
    renderDialog();
    expect(await screen.findByText(/transcript is incomplete/i)).toBeTruthy();
    expect(screen.getByText("This execution has no recorded messages.")).toBeTruthy();
  });

  it("renders an explicit empty state", async () => {
    renderDialog();
    expect(await screen.findByText("This execution has no recorded messages.")).toBeTruthy();
  });

  it("surfaces an RFC problem detail without mutation controls", async () => {
    fixture.error = { detail: "Transcript retention expired." };
    renderDialog();
    expect((await screen.findByRole("alert")).textContent).toContain(
      "Transcript retention expired.",
    );
    expect(screen.queryByRole("textbox")).toBeNull();
  });
});
