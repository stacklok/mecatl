// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

// #2299 / #1852: a past fire's transcript opens in a read-only dialog that
// reads the session detail first and keeps the inspection gate.

import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { formatDate } from "../../lib/formatters";
import {
  type FakeRequest,
  fire,
  jsonResponse,
  mountSchedules,
  problem,
  resetApi,
  schedule,
} from "./schedules-test-kit";

afterEach(() => {
  cleanup();
  resetApi();
});

const FIRED = "2026-10-07T09:00:00Z";
const FIRES = [fire({ firedAt: FIRED, id: "f1", sessionId: "s1" })];

const CAPABILITIES = {
  copyId: true,
  copyIdReason: "",
  delete: false,
  deleteReason: "",
  fork: false,
  forkReason: "",
  inspect: true,
  inspectReason: "",
  publicChat: false,
  publicChatReason: "inspect_only_kind",
  rename: false,
  renameReason: "",
  viewTranscript: true,
  viewTranscriptReason: "",
};

function summary(capabilities: Partial<typeof CAPABILITIES> = {}) {
  return {
    capabilities: { ...CAPABILITIES, ...capabilities },
    createdAt: FIRED,
    debugTargetSessionId: "",
    id: "s1",
    kind: "scheduled",
    modelId: "",
    state: "idle",
    title: "Alpha · Oct 7",
    titleProvenance: "operator",
    titleRevision: "1",
    turns: 1,
    updatedAt: FIRED,
  };
}

const DETAIL = {
  capabilities: {
    audio: false,
    builtInShell: false,
    executionFiles: false,
    image: false,
    manualCompaction: false,
    modelSelection: false,
  },
  id: "s1",
  kind: "scheduled",
  mode: "plan",
  state: "idle",
  usage: {
    cacheReadTokens: "0",
    cacheWriteTokens: "0",
    inputTokens: "0",
    outputTokens: "0",
    reasoningTokens: "0",
  },
};

const TRANSCRIPT = {
  complete: true,
  messages: [
    { images: [], role: "user", text: "Digest started.", toolCalls: [] },
    {
      images: [],
      role: "assistant",
      text: "Six PRs merged overnight.",
      toolCalls: [{ args: "{}", id: "c1", name: "Bash" }],
    },
  ],
  sessionId: "s1",
};

interface Session {
  connection?: "online" | "offline";
  detail?: () => Response;
  sessions?: unknown[];
}

function sessionApi({
  connection = "online",
  detail = () => jsonResponse(DETAIL),
  sessions = [summary()],
}: Session = {}) {
  return (request: FakeRequest) => {
    if (request.method !== "GET") return undefined;
    if (request.path === "/api/v1/runtime") {
      return jsonResponse({ capabilities: { scheduling: true }, connection });
    }
    if (request.path === "/api/v1/sessions")
      return jsonResponse({ complete: true, items: sessions });
    if (request.path === "/api/v1/sessions/s1") return detail();
    if (request.path === "/api/v1/sessions/s1/transcript") return jsonResponse(TRANSCRIPT);
    return undefined;
  };
}

function sessionReads(requests: FakeRequest[]) {
  return requests
    .filter((request) => request.path.startsWith("/api/v1/sessions/"))
    .map((request) => request.path);
}

async function openTranscript(user: ReturnType<typeof userEvent.setup>) {
  const table = await screen.findByRole("table");
  const button = within(table).getByRole("button", { name: "View transcript" });
  await user.click(button);
  return { button, dialog: await screen.findByRole("dialog") };
}

describe("the run log's transcript dialog", () => {
  it("replays the saved transcript read-only, after reading the session detail", async () => {
    const user = userEvent.setup();
    const { requests, router } = mountSchedules(
      [schedule({ name: "Alpha" })],
      "/workspace/schedules/Alpha",
      FIRES,
      sessionApi(),
    );
    const { dialog } = await openTranscript(user);

    expect(dialog.getAttribute("aria-labelledby")).toBeTruthy();
    expect(screen.getByRole("dialog", { name: `Alpha — ${formatDate(FIRED)}` })).toBe(dialog);
    expect(await within(dialog).findByText("Six PRs merged overnight.")).toBeTruthy();
    expect(within(dialog).getByText("Digest started.")).toBeTruthy();
    expect(within(dialog).getByText("1 tool call")).toBeTruthy();
    expect(within(dialog).getByText("Saved transcript · s1")).toBeTruthy();
    expect(within(dialog).queryByRole("textbox")).toBeNull();
    expect(sessionReads(requests)).toEqual([
      "/api/v1/sessions/s1",
      "/api/v1/sessions/s1/transcript",
    ]);
    expect(router.history.location.pathname).toBe("/workspace/schedules/Alpha");
  });

  it("closes with Escape and returns focus to its View transcript button", async () => {
    const user = userEvent.setup();
    mountSchedules(
      [schedule({ name: "Alpha" })],
      "/workspace/schedules/Alpha",
      FIRES,
      sessionApi(),
    );
    const { button, dialog } = await openTranscript(user);
    await within(dialog).findByText("Six PRs merged overnight.");
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    await waitFor(() => expect(document.activeElement).toBe(button));
  });

  it("opens from a phone card too", async () => {
    const user = userEvent.setup();
    mountSchedules(
      [schedule({ name: "Alpha" })],
      "/workspace/schedules/Alpha",
      FIRES,
      sessionApi(),
    );
    await screen.findByRole("table");
    const card = document.querySelector("[data-schedule-fire-mobile-list]") as HTMLElement;
    await user.click(within(card).getByRole("button", { name: "View transcript" }));
    expect(await screen.findByText("Six PRs merged overnight.")).toBeTruthy();
  });

  it("shows the daemon's reason and never reads a transcript the session does not grant", async () => {
    const user = userEvent.setup();
    const { requests } = mountSchedules(
      [schedule({ name: "Alpha" })],
      "/workspace/schedules/Alpha",
      FIRES,
      sessionApi({
        sessions: [
          summary({ viewTranscript: false, viewTranscriptReason: "Transcript retention expired." }),
        ],
      }),
    );
    const { dialog } = await openTranscript(user);
    expect(await within(dialog).findByText("Transcript retention expired.")).toBeTruthy();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(sessionReads(requests)).not.toContain("/api/v1/sessions/s1/transcript");
  });

  it("needs the inspect capability as well, as the inspection dialog does", async () => {
    const user = userEvent.setup();
    const { requests } = mountSchedules(
      [schedule({ name: "Alpha" })],
      "/workspace/schedules/Alpha",
      FIRES,
      sessionApi({ sessions: [summary({ inspect: false })] }),
    );
    const { dialog } = await openTranscript(user);
    expect(await within(dialog).findByText("Transcript unavailable.")).toBeTruthy();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(sessionReads(requests)).not.toContain("/api/v1/sessions/s1/transcript");
  });

  it("reads nothing about the session while the runtime is offline", async () => {
    const user = userEvent.setup();
    const { requests } = mountSchedules(
      [schedule({ name: "Alpha" })],
      "/workspace/schedules/Alpha",
      FIRES,
      sessionApi({ connection: "offline" }),
    );
    const { dialog } = await openTranscript(user);
    expect(await within(dialog).findByText("Transcript unavailable while offline.")).toBeTruthy();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(sessionReads(requests)).toEqual([]);
  });

  it("treats a session missing from the caller's inventory as unavailable", async () => {
    const user = userEvent.setup();
    const { requests } = mountSchedules(
      [schedule({ name: "Alpha" })],
      "/workspace/schedules/Alpha",
      FIRES,
      sessionApi({ sessions: [] }),
    );
    const { dialog } = await openTranscript(user);
    expect(await within(dialog).findByText("Transcript unavailable.")).toBeTruthy();
    expect(within(dialog).getByText("Saved transcript")).toBeTruthy();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(sessionReads(requests)).not.toContain("/api/v1/sessions/s1/transcript");
  });

  it("does not read the transcript when the session detail fails, and offers Retry", async () => {
    const user = userEvent.setup();
    let failDetail = true;
    const { requests } = mountSchedules(
      [schedule({ name: "Alpha" })],
      "/workspace/schedules/Alpha",
      FIRES,
      sessionApi({
        detail: () => (failDetail ? problem("session store down", 503) : jsonResponse(DETAIL)),
      }),
    );
    const { dialog } = await openTranscript(user);
    expect(await within(dialog).findByText("Saved transcript could not be read.")).toBeTruthy();
    expect(sessionReads(requests)).toEqual(["/api/v1/sessions/s1"]);

    failDetail = false;
    await user.click(within(dialog).getByRole("button", { name: "Retry" }));
    expect(await within(dialog).findByText("Six PRs merged overnight.")).toBeTruthy();
  });
});
