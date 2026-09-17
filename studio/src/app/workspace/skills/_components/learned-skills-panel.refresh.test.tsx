import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { emitRunFinished } from "@/features/agent/run-signals";
import { resetHarnessClient } from "@/lib/harness/sdk";
import { stubHarnessFetch } from "@/lib/harness/sdk-test-stub";
import { LearnedSkillsPanel } from "./learned-skills-panel";

/**
 * The Learned view's "Recent changes" list stays fresh without a reload
 * (mecatui re-lists lifecycle receipts on every run result): a run terminal
 * this page observes re-reads the list, and so does the tab coming back into
 * view; a hidden tab's visibility flip does not. The daemon is answered by
 * the SDK fetch stub.
 */

vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => ({ connected: true }),
}));

vi.mock("sonner", () => ({
  toast: { info: vi.fn(), error: vi.fn(), success: vi.fn(), warning: vi.fn() },
}));

interface Receipt {
  id: string;
  name: string;
  version: string;
  operation: string;
}

function daemon(initialReceipts: Receipt[]) {
  const receipts = [...initialReceipts];
  const stub = stubHarnessFetch((request) => {
    if (request.path.startsWith("/v1/skills/learned/changes")) {
      return { changes: receipts, next_cursor: "" };
    }
    if (request.path.startsWith("/v1/skills/learned")) {
      return {
        skills: [
          {
            id: "sk1",
            name: "triage-flakes",
            version: "v2",
            revision: "r7",
            state: "active",
            owner_agent: "explorer",
            description: "Triage flaky tests",
          },
        ],
        next_cursor: "",
      };
    }
    return undefined;
  });
  return {
    append: (receipt: Receipt) => receipts.push(receipt),
    changeRequests: () =>
      stub.requests.filter((r) =>
        r.path.startsWith("/v1/skills/learned/changes"),
      ),
  };
}

function setVisibility(state: "visible" | "hidden") {
  Object.defineProperty(document, "visibilityState", {
    value: state,
    configurable: true,
  });
  document.dispatchEvent(new Event("visibilitychange"));
}

afterEach(async () => {
  vi.unstubAllGlobals();
  await resetHarnessClient();
});

describe("LearnedSkillsPanel refresh", () => {
  it("re-reads the receipts on a run terminal and when the tab becomes visible", async () => {
    const fake = daemon([
      { id: "ch1", name: "triage-flakes", version: "v1", operation: "stage" },
    ]);
    render(<LearnedSkillsPanel />);
    await waitFor(() =>
      expect(screen.getByText("Recent changes")).toBeInTheDocument(),
    );
    expect(fake.changeRequests()).toHaveLength(1);
    expect(screen.queryByText("summarise-prs")).not.toBeInTheDocument();

    // A run ended: the list is re-read and the new receipt appears.
    fake.append({
      id: "ch2",
      name: "summarise-prs",
      version: "v1",
      operation: "activate",
    });
    await act(async () => {
      emitRunFinished({ sessionId: "s1", stop: "end_turn" });
    });
    await waitFor(() => expect(fake.changeRequests()).toHaveLength(2));
    await waitFor(() =>
      expect(screen.getByText("summarise-prs")).toBeInTheDocument(),
    );

    // Going hidden reads nothing; coming back reads once more.
    await act(async () => {
      setVisibility("hidden");
    });
    expect(fake.changeRequests()).toHaveLength(2);
    await act(async () => {
      setVisibility("visible");
    });
    await waitFor(() => expect(fake.changeRequests()).toHaveLength(3));
  });

  it("stops listening once unmounted", async () => {
    const fake = daemon([]);
    const { unmount } = render(<LearnedSkillsPanel />);
    await waitFor(() => expect(fake.changeRequests()).toHaveLength(1));
    unmount();
    emitRunFinished({ sessionId: "s1", stop: "end_turn" });
    setVisibility("visible");
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(fake.changeRequests()).toHaveLength(1);
  });
});
