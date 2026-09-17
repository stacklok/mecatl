import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resetHarnessClient } from "@/lib/harness/sdk";
import {
  jsonResponse,
  type RecordedRequest,
  stubHarnessFetch,
} from "@/lib/harness/sdk-test-stub";
import MemoryDetailPage from "./page";

/**
 * The memory detail page performs the TUI's `enter` step: a key-scoped read
 * whose VALUE leads the page, whose writer/origin replace the label the page
 * used to hard-code ("Learned in conversation"), whose source session links
 * to the chat, and whose bounded history renders — or is reported as
 * unavailable from this store/driver. It stays read-only (memory rule 8).
 */

const { params, runtime, routerPush } = vi.hoisted(() => ({
  params: { memoryId: "prefers-tabs" },
  /** The runtime probe's state; tests flip it to cover the connecting
   *  window and an unreachable daemon. Reset to connected after each. */
  runtime: {
    state: "connected" as "connecting" | "connected" | "offline",
  },
  routerPush: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  useParams: () => params,
  useRouter: () => ({ push: routerPush }),
}));

// The real memory hooks over the stubbed SDK path; only the runtime status
// (the `connected` gate every load reads) is mocked.
vi.mock("@/features/agent", async () => {
  const hooks = await import("@/features/agent/hooks/use-agent-memory");
  return {
    useAgentMemory: hooks.useAgentMemory,
    useMemoryEntryDetail: hooks.useMemoryEntryDetail,
  };
});

vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => ({
    state: runtime.state,
    connected: runtime.state === "connected",
    features: new Set<string>(),
    serverCapabilities: {},
  }),
}));

afterEach(async () => {
  runtime.state = "connected";
  routerPush.mockReset();
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

const INDEX = {
  entries: [{ key: "prefers-tabs", description: "The operator prefers tabs." }],
  size_bytes: 64,
  sha256: "f".repeat(64),
};

const UPDATED_AT = 1_755_000_000;
const PRIOR_AT = 1_754_000_000;

const DETAIL = {
  current: {
    key: "prefers-tabs",
    value: "Tabs, width 4",
    description: "The operator prefers tabs.",
    version: "3",
    status: "active",
    writer: "agent",
    origin: "reflection",
    source_session_id: "session-fixture-1",
    source_proposal_id: "proposal-7",
    updated_at: { seconds: UPDATED_AT },
  },
  history: [
    { version: "2", status: "superseded", updated_at: { seconds: PRIOR_AT } },
  ],
  history_available: true,
};

function keyOf(request: RecordedRequest): string | null {
  return new URL(request.url, "http://studio").searchParams.get("key");
}

/** Serves the index for the key-less read and `detail` for the exact key. */
function stubDaemon(detail: unknown | undefined) {
  return stubHarnessFetch((request) =>
    keyOf(request) === "prefers-tabs" && detail !== undefined
      ? { ...INDEX, detail }
      : INDEX,
  );
}

/** The value cell of the Details row labelled `label`. */
function factValue(label: string): HTMLElement {
  const labelEl = screen.getByText(label, { selector: "span" });
  const value = labelEl.nextElementSibling;
  if (!(value instanceof HTMLElement))
    throw new Error(`fact "${label}" has no value cell`);
  return value;
}

describe("memory detail page", () => {
  it("leads with the fact VALUE and shows daemon-derived provenance rows", async () => {
    const stub = stubDaemon(DETAIL);
    render(<MemoryDetailPage />);

    const value = await screen.findByText("Tabs, width 4");
    expect(value.tagName).toBe("PRE");
    expect(
      within(screen.getByRole("region", { name: "Value" })).getByText(
        "The operator prefers tabs.",
      ),
    ).toBeInTheDocument();

    expect(factValue("Key")).toHaveTextContent("prefers-tabs");
    expect(factValue("Status")).toHaveTextContent("active");
    expect(factValue("Version")).toHaveTextContent("3");
    expect(factValue("Writer")).toHaveTextContent("agent");
    expect(factValue("Origin")).toHaveTextContent("reflection");
    expect(screen.queryByText("Learned in conversation")).toBeNull();
    expect(screen.queryByText("Source")).toBeNull();

    const updated = within(factValue("Updated")).getByText(/ago$/);
    expect(updated).toHaveAttribute(
      "title",
      new Date(UPDATED_AT * 1000).toISOString(),
    );

    // Both reads went out: the key-less index and the exact-key detail.
    expect(stub.requests.map(keyOf)).toEqual(
      expect.arrayContaining([null, "prefers-tabs"]),
    );
  });

  it("links the source session to its chat and the proposal to Learning", async () => {
    stubDaemon(DETAIL);
    render(<MemoryDetailPage />);
    await screen.findByText("Tabs, width 4");

    expect(
      screen.getByRole("link", { name: "session-fixture-1" }),
    ).toHaveAttribute("href", "/workspace/chat/session-fixture-1");
    expect(screen.getByRole("link", { name: "proposal-7" })).toHaveAttribute(
      "href",
      "/workspace/settings/learning",
    );
  });

  it("lists the bounded revisions newest-first as version · status · date", async () => {
    stubDaemon(DETAIL);
    render(<MemoryDetailPage />);
    await screen.findByText("Tabs, width 4");

    const history = screen.getByRole("region", { name: "History" });
    expect(history).toHaveTextContent("1 bounded revision");
    const rows = within(history).getAllByRole("listitem");
    expect(rows).toHaveLength(1);
    expect(rows[0]).toHaveTextContent(
      `2 · superseded · ${new Date(PRIOR_AT * 1000).toISOString().slice(0, 10)}`,
    );
  });

  it("reports history the store/driver could not supply, never as 'no revisions'", async () => {
    stubDaemon({ ...DETAIL, history: [], history_available: false });
    render(<MemoryDetailPage />);
    await screen.findByText("Tabs, width 4");

    const history = screen.getByRole("region", { name: "History" });
    expect(history).toHaveTextContent(
      "History unavailable from this store/driver.",
    );
    expect(history).not.toHaveTextContent("bounded revision");
  });

  it("says the value is unavailable when the key no longer matches an entry", async () => {
    stubDaemon(undefined);
    render(<MemoryDetailPage />);
    expect(
      await screen.findByText(
        "Value unavailable (the key no longer matches an entry).",
      ),
    ).toBeInTheDocument();
    // The index description still frames the fact; the Key row stays.
    expect(factValue("Key")).toHaveTextContent("prefers-tabs");
    expect(screen.queryByRole("region", { name: "History" })).toBeNull();
  });

  it("surfaces a detail-read refusal as an error, not as content", async () => {
    stubHarnessFetch((request) =>
      keyOf(request) === "prefers-tabs"
        ? jsonResponse(503, { code: "unavailable", error: "store offline" })
        : INDEX,
    );
    render(<MemoryDetailPage />);
    expect(await screen.findByText(/^Value unavailable: /)).toBeInTheDocument();
  });

  it("stays read-only: the footer names the agent's memory tools, no editor", async () => {
    stubDaemon(DETAIL);
    render(<MemoryDetailPage />);
    await screen.findByText("Tabs, width 4");
    expect(
      screen.getByText(
        "Read-only — ask the agent to use ForgetUserMemory or UndoUserMemory.",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("textbox")).toBeNull();
  });

  it("treats the connecting window as loading, never as a missing fact", () => {
    // The runtime probe has not settled, so no index read has gone out and
    // an empty index means "not read yet". This first render used to reach
    // Next's terminal notFound() and 404 the route on every fresh load.
    runtime.state = "connecting";
    const stub = stubDaemon(DETAIL);
    render(<MemoryDetailPage />);
    expect(screen.getByText("Loading…")).toBeInTheDocument();
    expect(screen.queryByText("Memory not found")).toBeNull();
    expect(stub.requests).toHaveLength(0);
  });

  it("reports a fact the settled index lacks as missing, in-page and recoverable", async () => {
    stubHarnessFetch(() => ({ ...INDEX, entries: [] }));
    render(<MemoryDetailPage />);
    expect(await screen.findByText("Memory not found")).toBeInTheDocument();
    expect(
      screen.getByText(/the agent may have forgotten or renamed it\./),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Back to Memory" }));
    expect(routerPush).toHaveBeenCalledWith("/workspace/settings/memory");
  });

  it("names an unreachable daemon instead of claiming the fact is gone", () => {
    runtime.state = "offline";
    const stub = stubDaemon(DETAIL);
    render(<MemoryDetailPage />);
    expect(screen.getByText("Memory not found")).toBeInTheDocument();
    expect(
      screen.getByText(
        "Studio can't reach the agent, so this fact can't be looked up right now.",
      ),
    ).toBeInTheDocument();
    expect(stub.requests).toHaveLength(0);
  });
});
