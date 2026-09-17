import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resetHarnessClient } from "@/lib/harness/sdk";
import {
  problemResponse,
  type RecordedRequest,
  stubHarnessFetch,
} from "@/lib/harness/sdk-test-stub";
import { emitRunFinished } from "../run-signals";
import {
  LEARNED_SKILLS_VIEW_HREF,
  receiptNoticeText,
  useLearnedSkillChangeNotices,
  walkLearnedSkillReceipts,
} from "./use-learned-skill-change-notices";

/**
 * Post-run learned-skill receipt notices (mecatui's ListSkillChangesCmd on
 * every ResultMsg + its status line): the mount seeds on the newest receipt
 * WITHOUT a toast, each observed run terminal lists forward from that id
 * and announces only what is new, the action opens the Learned view, a
 * pruned cursor re-seeds silently, and a daemon without the capability is
 * never asked. The daemon is answered by the SDK fetch stub; sonner and the
 * router are mocked at the module boundary.
 */

const mocks = vi.hoisted(() => ({
  toastInfo: vi.fn(),
  push: vi.fn(),
  runtime: {
    connected: true,
    serverCapabilities: { learned_skills: true } as Record<string, unknown>,
  },
}));

vi.mock("sonner", () => ({
  toast: { info: mocks.toastInfo, error: vi.fn(), success: vi.fn() },
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: mocks.push, replace: vi.fn() }),
}));

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => mocks.runtime,
}));

interface Receipt {
  id: string;
  name: string;
  operation: string;
}

const receipt = (id: string, name = "triage-flakes"): Receipt => ({
  id,
  name,
  operation: "activate",
});

const cursorOf = (request: RecordedRequest) =>
  new URL(request.url, "http://studio.test").searchParams.get("cursor") ?? "";

/**
 * A receipt log in daemon order (oldest first) answering `?cursor=` the way
 * the daemon does: the page AFTER the named receipt; an unknown cursor is a
 * 400 `skill_cursor` problem. One page per request (the log is small).
 */
function receiptLog(initial: Receipt[]) {
  const log = [...initial];
  const stub = stubHarnessFetch((request) => {
    if (!request.path.startsWith("/v1/skills/learned/changes")) return;
    const cursor = cursorOf(request);
    let start = 0;
    if (cursor) {
      const at = log.findIndex((entry) => entry.id === cursor);
      if (at < 0) {
        return problemResponse(400, "skill_cursor", "stale cursor");
      }
      start = at + 1;
    }
    return { changes: log.slice(start), next_cursor: "" };
  });
  const changeRequests = () =>
    stub.requests.filter((r) =>
      r.path.startsWith("/v1/skills/learned/changes"),
    );
  return {
    append: (...entries: Receipt[]) => log.push(...entries),
    prune: (count: number) => log.splice(0, count),
    changeRequests,
  };
}

async function runEnds() {
  emitRunFinished({ sessionId: "s1", stop: "end_turn" });
  // Let the serialized walk settle.
  await new Promise((resolve) => setTimeout(resolve, 0));
}

beforeEach(() => {
  mocks.runtime.connected = true;
  mocks.runtime.serverCapabilities = { learned_skills: true };
});

afterEach(async () => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
  await resetHarnessClient();
});

describe("receiptNoticeText", () => {
  it("counts receipts with the plural spelled out", () => {
    expect(receiptNoticeText(1)).toBe(
      "1 learned-skill change receipt available",
    );
    expect(receiptNoticeText(3)).toBe(
      "3 learned-skill change receipts available",
    );
  });
});

describe("walkLearnedSkillReceipts", () => {
  it("follows the daemon's cursor to the end of the log and reports the newest id", async () => {
    const pages: Record<string, unknown> = {
      "": { changes: [receipt("ch1"), receipt("ch2")], next_cursor: "ch2" },
      ch2: { changes: [receipt("ch3")], next_cursor: "" },
    };
    const stub = stubHarnessFetch((request) => pages[cursorOf(request)]);
    await expect(walkLearnedSkillReceipts("")).resolves.toEqual({
      count: 3,
      last: "ch3",
      complete: true,
    });
    expect(stub.requests.map(cursorOf)).toEqual(["", "ch2"]);
    // An empty log keeps the starting id.
    stubHarnessFetch(() => ({ changes: [], next_cursor: "" }));
    await expect(walkLearnedSkillReceipts("ch3")).resolves.toEqual({
      count: 0,
      last: "ch3",
      complete: true,
    });
  });
});

describe("useLearnedSkillChangeNotices", () => {
  it("never asks a daemon without the learned_skills capability", async () => {
    mocks.runtime.serverCapabilities = {};
    const log = receiptLog([receipt("ch1")]);
    renderHook(() => useLearnedSkillChangeNotices());
    await runEnds();
    expect(log.changeRequests()).toHaveLength(0);
    expect(mocks.toastInfo).not.toHaveBeenCalled();
  });

  it("seeds on the newest receipt without announcing, then announces only what a run added", async () => {
    const log = receiptLog([receipt("ch1"), receipt("ch2")]);
    renderHook(() => useLearnedSkillChangeNotices());
    await waitFor(() => expect(log.changeRequests()).toHaveLength(1));
    expect(cursorOf(log.changeRequests()[0])).toBe("");
    expect(mocks.toastInfo).not.toHaveBeenCalled();

    // A run that produced nothing new stays quiet.
    await runEnds();
    await waitFor(() => expect(log.changeRequests()).toHaveLength(2));
    expect(cursorOf(log.changeRequests()[1])).toBe("ch2");
    expect(mocks.toastInfo).not.toHaveBeenCalled();

    // One new receipt: one toast, whose action opens the Learned view.
    log.append(receipt("ch3", "summarise-prs"));
    await runEnds();
    await waitFor(() => expect(mocks.toastInfo).toHaveBeenCalledTimes(1));
    const [text, options] = mocks.toastInfo.mock.calls[0] as [
      string,
      { action: { label: string; onClick: () => void } },
    ];
    expect(text).toBe("1 learned-skill change receipt available");
    expect(options.action.label).toBe("Open Skills");
    options.action.onClick();
    expect(mocks.push).toHaveBeenCalledWith(LEARNED_SKILLS_VIEW_HREF);
    expect(LEARNED_SKILLS_VIEW_HREF).toBe("/workspace/skills?view=learned");

    // The cursor advanced: two more receipts read as two, not three.
    log.append(receipt("ch4"), receipt("ch5"));
    await runEnds();
    await waitFor(() => expect(mocks.toastInfo).toHaveBeenCalledTimes(2));
    expect(mocks.toastInfo.mock.calls[1][0]).toBe(
      "2 learned-skill change receipts available",
    );
    expect(cursorOf(log.changeRequests().at(-1) as RecordedRequest)).toBe(
      "ch3",
    );
  });

  it("re-seeds silently when the daemon no longer knows the remembered receipt", async () => {
    const log = receiptLog([receipt("ch1"), receipt("ch2")]);
    renderHook(() => useLearnedSkillChangeNotices());
    await waitFor(() => expect(log.changeRequests()).toHaveLength(1));

    // The bounded history pruned everything up to and including ch2.
    log.prune(2);
    log.append(receipt("ch3"));
    await runEnds();
    // The stale cursor was refused: no toast, no guess.
    await waitFor(() => expect(log.changeRequests()).toHaveLength(2));
    expect(mocks.toastInfo).not.toHaveBeenCalled();

    // The next run terminal re-seeds from the start (still no toast) …
    await runEnds();
    await waitFor(() => expect(log.changeRequests()).toHaveLength(3));
    expect(cursorOf(log.changeRequests()[2])).toBe("");
    expect(mocks.toastInfo).not.toHaveBeenCalled();

    // … and announcing resumes from the re-seeded position.
    log.append(receipt("ch4"));
    await runEnds();
    await waitFor(() => expect(mocks.toastInfo).toHaveBeenCalledTimes(1));
    expect(mocks.toastInfo.mock.calls[0][0]).toBe(
      "1 learned-skill change receipt available",
    );
  });

  it("stops listening once unmounted", async () => {
    const log = receiptLog([receipt("ch1")]);
    const { unmount } = renderHook(() => useLearnedSkillChangeNotices());
    await waitFor(() => expect(log.changeRequests()).toHaveLength(1));
    unmount();
    log.append(receipt("ch2"));
    await runEnds();
    expect(log.changeRequests()).toHaveLength(1);
    expect(mocks.toastInfo).not.toHaveBeenCalled();
  });
});
