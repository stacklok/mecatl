// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { client as apiClient } from "@mecatl-studio/contracts/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TranscriptDialog } from "./transcript-dialog";

const fixture = {
  calls: [] as string[],
  error: undefined as { detail: string } | undefined,
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
};

beforeEach(() => {
  fixture.calls = [];
  fixture.error = undefined;
  fixture.pending = false;
  fixture.response = { complete: true, messages: [], sessionId: "scheduled-session" };
  const fetch = vi.fn<typeof globalThis.fetch>(async (input) => {
    const pathname = new URL(
      input instanceof Request ? input.url : String(input),
      window.location.href,
    ).pathname;
    if (!pathname.endsWith("/transcript")) throw new Error(`Unexpected request: ${pathname}`);
    fixture.calls.push(decodeURIComponent(pathname.split("/").at(-2) ?? ""));
    if (fixture.pending) await new Promise<never>(() => undefined);
    if (fixture.error) return Response.json(fixture.error, { status: 410 });
    return Response.json(fixture.response);
  });
  apiClient.setConfig({ baseUrl: window.location.origin, fetch });
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
