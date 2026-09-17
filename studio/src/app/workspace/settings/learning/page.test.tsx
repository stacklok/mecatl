import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  type LearningProposal,
  listLearningProposals,
} from "@/lib/harness/learning";
import type { HarnessRuntimeSettingsDoc } from "@/lib/harness/runtime-settings";
import LearningSettingsPage from "./page";

/**
 * Settings → Learning as a page: the Learning card renders FIRST whatever
 * the agent advertises, so the page is never a dead end when learning is
 * off — the "Learning is off" note sits BELOW the control that turns it on,
 * and once the agent reports proposals the note yields to the suggestions
 * list.
 */

const runtimeStatus = vi.hoisted(() => ({
  connected: true,
  mode: "managed" as "managed" | "external",
  serverCapabilities: {} as Record<string, unknown>,
  refresh: vi.fn(async () => undefined),
}));

const runtimeSettings = vi.hoisted(() => ({
  live: true,
  manageable: true,
  doc: null as HarnessRuntimeSettingsDoc | null,
  isLoading: false,
  busy: "" as "" | "save" | "approve-soul",
  error: null as string | null,
  notice: null as string | null,
  save: vi.fn(async () => true),
}));

/** The Learn-from-a-chat picker's inventory read; one finished chat. */
const sessionsMock = vi.hoisted(() => ({
  fetchAllSessions: vi.fn(),
}));

vi.mock("@/lib/harness/client", () => ({
  fetchAllSessions: sessionsMock.fetchAllSessions,
}));

vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => runtimeStatus,
}));

vi.mock("@/features/agent/hooks/use-runtime-settings", () => ({
  useRuntimeSettings: () => runtimeSettings,
}));

vi.mock("@/lib/harness/learning", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/learning")>()),
  listLearningProposals: vi.fn(async () => ({
    proposals: [],
    nextCursor: "",
  })),
}));

const doc = (): HarnessRuntimeSettingsDoc => ({
  config: {
    learning: { mode: "", sensitivity: "" },
    steer: { enabled: true },
    soul: { enabled: true, strict: false, file: "" },
  },
  managedBy: { learning: "studio", steer: "studio", soul: "studio" },
  inherited: { learning: { mode: "", sensitivity: "" }, steer: null },
  effective: {
    learning: { mode: "off", sensitivity: "balanced" },
    steer: true,
  },
  soulFileDefault: "/home/me/.config/mecatl/soul.md",
  soulCandidates: [],
});

/** True when `first` precedes `second` in document order. */
const precedes = (first: HTMLElement, second: HTMLElement) =>
  (first.compareDocumentPosition(second) & Node.DOCUMENT_POSITION_FOLLOWING) !==
  0;

beforeEach(() => {
  runtimeStatus.connected = true;
  runtimeStatus.mode = "managed";
  runtimeStatus.serverCapabilities = {};
  runtimeSettings.live = true;
  runtimeSettings.manageable = true;
  runtimeSettings.doc = doc();
  sessionsMock.fetchAllSessions.mockReset();
  sessionsMock.fetchAllSessions.mockResolvedValue({
    sessions: [
      {
        sessionId: "chat-1",
        title: "Planning the offsite",
        isChat: true,
        state: "completed",
      },
    ],
  });
});

describe("LearningSettingsPage", () => {
  it("renders the Learning card above the off note when the agent advertises neither capability", () => {
    render(<LearningSettingsPage />);
    const modeControl = screen.getByRole("button", { name: "Learning mode" });
    const note = screen.getByRole("heading", { name: "Learning is off" });
    expect(modeControl).toBeInTheDocument();
    expect(precedes(modeControl, note)).toBe(true);
    expect(
      screen.getByText(/Turn learning on above, then review/),
    ).toBeInTheDocument();
    // The downstream cards wait for the capabilities.
    expect(
      screen.queryByRole("heading", { name: "Suggestions" }),
    ).not.toBeInTheDocument();
  });

  it("keeps the Learning card first and drops the note once proposals are advertised", async () => {
    runtimeStatus.serverCapabilities = { learning_proposals: true };
    render(<LearningSettingsPage />);
    const modeControl = screen.getByRole("button", { name: "Learning mode" });
    const queue = await screen.findByRole("heading", { name: "Suggestions" });
    expect(precedes(modeControl, queue)).toBe(true);
    expect(
      screen.queryByRole("heading", { name: "Learning is off" }),
    ).not.toBeInTheDocument();
  });

  it("still offers the Learning card in external mode, as the managed note", () => {
    runtimeStatus.mode = "external";
    runtimeSettings.manageable = false;
    runtimeSettings.doc = null;
    render(<LearningSettingsPage />);
    expect(
      screen.getByText(/The agent is run somewhere else/),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: "Learning is off" }),
    ).toBeInTheDocument();
  });

  it("offers Learn from a chat when only reflection is advertised", async () => {
    runtimeStatus.serverCapabilities = { reflection: true };
    render(<LearningSettingsPage />);
    expect(
      screen.getByRole("heading", { name: "Learn from a chat" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Reviewing suggestions is not available right now."),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Find suggestions" }),
    ).toBeDisabled();
    // The picker lists finished chats; once they arrive the empty note goes.
    await waitFor(() =>
      expect(screen.queryByText("No finished chats yet.")).toBeNull(),
    );
    expect(sessionsMock.fetchAllSessions).toHaveBeenCalledTimes(1);
  });
});

/**
 * The suggestions list's paging and refresh: every list request carries the
 * page size, the Deferred pill sends the agent's exact status token, Load
 * more appends the next page by cursor, and Refresh restarts from the first
 * page.
 */
describe("Suggestions paging", () => {
  const listMock = vi.mocked(listLearningProposals);
  const proposal = (id: string, status = "staged"): LearningProposal => ({
    id,
    version: "1",
    status,
    kind: "fact",
    key: `key/${id}`,
    value: `value of ${id}`,
    description: "",
    title: "",
    body: "",
    triggers: [],
    evidence: [
      {
        sessionId: "s1",
        locator: "tool",
        ordinal: 1,
        eventSeq: 1,
        toolCallId: "",
        digest: "d",
        available: true,
        availability: "",
        preview: "",
      },
    ],
    evidenceCount: 1,
    decisions: [],
    promotion: null,
    createdAtUnix: 0,
    updatedAtUnix: 0,
    projectScoped: false,
    promotionAvailable: true,
    promotionUnavailableReason: "",
    learnedSkillId: "",
  });

  beforeEach(() => {
    runtimeStatus.serverCapabilities = { learning_proposals: true };
    listMock.mockReset();
    listMock.mockResolvedValue({ proposals: [], nextCursor: "" });
  });

  it("requests the first page with the page size and the Deferred pill's exact token", async () => {
    const user = userEvent.setup();
    render(<LearningSettingsPage />);
    await waitFor(() => expect(listMock).toHaveBeenCalledTimes(1));
    expect(listMock.mock.calls[0]?.[0]).toEqual({
      status: "staged",
      limit: 50,
    });
    expect(
      await screen.findByText("Nothing waiting for review."),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Deferred" }));
    await waitFor(() => expect(listMock).toHaveBeenCalledTimes(2));
    expect(listMock.mock.calls[1]?.[0]).toEqual({
      status: "deferred_unsupported",
      limit: 50,
    });
    expect(screen.getByRole("button", { name: "Deferred" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(
      await screen.findByText("No deferred suggestions."),
    ).toBeInTheDocument();
  });

  it("labels the promoted filter Approved and sends the promoted token", async () => {
    const user = userEvent.setup();
    render(<LearningSettingsPage />);
    await waitFor(() => expect(listMock).toHaveBeenCalledTimes(1));
    await user.click(screen.getByRole("button", { name: "Approved" }));
    await waitFor(() => expect(listMock).toHaveBeenCalledTimes(2));
    expect(listMock.mock.calls[1]?.[0]).toEqual({
      status: "promoted",
      limit: 50,
    });
    expect(
      await screen.findByText("No approved suggestions."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Promoted" })).toBeNull();
  });

  it("appends the next page through Load more, sending the cursor", async () => {
    listMock
      .mockResolvedValueOnce({
        proposals: [proposal("p1")],
        nextCursor: "cursor-2",
      })
      .mockResolvedValueOnce({
        // The agent may repeat a row that moved between pages; it is
        // listed once.
        proposals: [proposal("p1"), proposal("p2")],
        nextCursor: "",
      });
    const user = userEvent.setup();
    render(<LearningSettingsPage />);
    const list = await screen.findByRole("list");
    expect(within(list).getAllByRole("listitem")).toHaveLength(1);
    const more = screen.getByRole("button", { name: "Load more" });
    await user.click(more);
    await waitFor(() =>
      expect(within(list).getAllByRole("listitem")).toHaveLength(2),
    );
    expect(listMock.mock.calls[1]?.[0]).toEqual({
      status: "staged",
      cursor: "cursor-2",
      limit: 50,
    });
    expect(screen.getByText("key/p2")).toBeInTheDocument();
    // The last page carried no cursor: nothing more to load.
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("hides Load more when the first page is the whole queue", async () => {
    listMock.mockResolvedValueOnce({
      proposals: [proposal("p1")],
      nextCursor: "",
    });
    render(<LearningSettingsPage />);
    await screen.findByText("key/p1");
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("Refresh reloads from the first page", async () => {
    listMock
      .mockResolvedValueOnce({
        proposals: [proposal("p1")],
        nextCursor: "cursor-2",
      })
      .mockResolvedValueOnce({
        proposals: [proposal("p3")],
        nextCursor: "",
      });
    const user = userEvent.setup();
    render(<LearningSettingsPage />);
    await screen.findByText("key/p1");
    await user.click(
      screen.getByRole("button", { name: "Refresh suggestions" }),
    );
    await screen.findByText("key/p3");
    expect(screen.queryByText("key/p1")).toBeNull();
    // A refresh restarts the walk: no cursor, same page size.
    expect(listMock).toHaveBeenCalledTimes(2);
    expect(listMock.mock.calls[1]?.[0]).toEqual({
      status: "staged",
      limit: 50,
    });
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });
});
