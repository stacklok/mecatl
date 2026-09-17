import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ScheduleRow, ScheduleSpecDraft } from "@/lib/protocol";
import { useAgentCron } from "./use-agent-cron";

/**
 * Pins the rename sequencing: the daemon has no rename on the wire, so a
 * new-name save is re-create → (pause, when the old entry was paused) →
 * delete-old, with fail-safe failure arms — a failed create leaves the old
 * schedule untouched, and a failed delete reports that both entries exist.
 */

const { calls, harnessScheduleAction, listScheduleRows, saveHarnessSchedule } =
  vi.hoisted(() => {
    const calls: string[] = [];
    return {
      calls,
      harnessScheduleAction: vi.fn(async (name: string, action: string) => {
        calls.push(`${action}:${name}`);
      }),
      listScheduleRows: vi.fn(async () => []),
      saveHarnessSchedule: vi.fn(
        async (draft: { name: string }, opts: { update: boolean }) => {
          calls.push(`${opts.update ? "update" : "create"}:${draft.name}`);
        },
      ),
    };
  });

vi.mock("@/lib/harness/client", () => ({
  harnessScheduleAction,
  listScheduleRows,
  saveHarnessSchedule,
}));

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => ({ connected: true }),
}));

function makeRow(overrides: Partial<ScheduleRow>): ScheduleRow {
  return {
    name: "old-name",
    prompt: "do the thing",
    cron: "0 9 * * *",
    oneShotAt: null,
    timezone: "",
    profile: "",
    mode: 2,
    mutating: false,
    maxFires: 0,
    limits: { maxTurns: 0, maxToolCalls: 0, maxConsecutiveFailures: 0 },
    oneShotRetry: false,
    oneShotMaxRetries: 0,
    enabled: true,
    fireCount: 3,
    nextFireAt: null,
    lastFireAt: null,
    fireStage: "idle",
    lastFireSessionId: "",
    owner: "",
    carried: {
      selectorProvider: "openrouter",
      selectorModel: "some/model",
      misfire: 1,
      singleton: true,
      carryContext: false,
      fireTimeoutSeconds: 90,
      parts: [],
    },
    ...overrides,
  };
}

const draft: ScheduleSpecDraft = {
  name: "new-name",
  prompt: "do the thing",
  trigger: { kind: "cron", cron: "0 9 * * *", timezone: "" },
  profile: "",
  mode: 2,
  mutating: false,
  maxFires: 0,
  limits: { maxTurns: 0, maxToolCalls: 0, maxConsecutiveFailures: 0 },
  oneShotRetry: false,
  oneShotMaxRetries: 0,
};

async function settledHook() {
  const rendered = renderHook(() => useAgentCron());
  await waitFor(() => expect(rendered.result.current.isLoading).toBe(false));
  calls.length = 0;
  return rendered;
}

describe("renameAndUpdateFromDraft", () => {
  beforeEach(() => {
    calls.length = 0;
  });

  it("creates the new name (carrying the stored spec) then deletes the old", async () => {
    const previous = makeRow({ enabled: true });
    const { result } = await settledHook();

    await act(async () => {
      await result.current.renameAndUpdateFromDraft(draft, previous);
    });

    expect(calls).toEqual(["create:new-name", "delete:old-name"]);
    expect(saveHarnessSchedule).toHaveBeenCalledWith(draft, {
      update: false,
      carried: previous.carried,
    });
    expect(result.current.error).toBeNull();
  });

  it("mirrors a paused state onto the new name before deleting the old", async () => {
    const previous = makeRow({ enabled: false });
    const { result } = await settledHook();

    await act(async () => {
      await result.current.renameAndUpdateFromDraft(draft, previous);
    });

    expect(calls).toEqual([
      "create:new-name",
      "pause:new-name",
      "delete:old-name",
    ]);
  });

  it("leaves the old schedule untouched when the create fails", async () => {
    saveHarnessSchedule.mockRejectedValueOnce(new Error("name is taken"));
    const previous = makeRow({ enabled: false });
    const { result } = await settledHook();

    await act(async () => {
      await expect(
        result.current.renameAndUpdateFromDraft(draft, previous),
      ).rejects.toThrow("name is taken");
    });

    // No pause, no delete: the failure arm never touches the old entry.
    expect(calls).toEqual([]);
    expect(result.current.error).toBe("name is taken");
  });

  it("reports that both entries exist when the delete fails", async () => {
    harnessScheduleAction.mockImplementationOnce(async (name, action) => {
      calls.push(`${action}:${name}`);
      throw new Error("store is read-only");
    });
    const previous = makeRow({ enabled: true });
    const { result } = await settledHook();

    await act(async () => {
      await expect(
        result.current.renameAndUpdateFromDraft(draft, previous),
      ).rejects.toThrow(/both entries exist/);
    });

    expect(calls).toEqual(["create:new-name", "delete:old-name"]);
    expect(result.current.error).toContain('"new-name"');
    expect(result.current.error).toContain('delete "old-name" manually');
    expect(result.current.error).toContain("store is read-only");
  });
});

/**
 * The fire session's TOOL PROFILE (the spec's `profile`, ADR 0291) reaches
 * the create body from both authoring paths: the quick-create takes it as an
 * input (default "" = all tools), and the full form's draft carries whatever
 * the form picked, untouched.
 */
describe("tool profile on create", () => {
  beforeEach(() => {
    calls.length = 0;
  });

  it("quick-create carries the picked no-fs profile", async () => {
    const { result } = await settledHook();
    await act(async () => {
      await result.current.createJob({
        name: "nofs-digest",
        schedule: "0 9 * * *",
        instruction: "summarise",
        profile: "no-fs",
      });
    });
    expect(saveHarnessSchedule).toHaveBeenCalledWith(
      expect.objectContaining({ name: "nofs-digest", profile: "no-fs" }),
      { update: false },
    );
  });

  it("quick-create without a pick sends the all-tools default", async () => {
    const { result } = await settledHook();
    await act(async () => {
      await result.current.createJob({
        name: "plain-digest",
        schedule: "0 9 * * *",
        instruction: "summarise",
      });
    });
    expect(saveHarnessSchedule).toHaveBeenCalledWith(
      expect.objectContaining({ name: "plain-digest", profile: "" }),
      { update: false },
    );
  });

  it("the full form's draft carries its profile through createFromDraft verbatim", async () => {
    const { result } = await settledHook();
    const noFs: ScheduleSpecDraft = { ...draft, profile: "no-fs" };
    await act(async () => {
      await result.current.createFromDraft(noFs);
    });
    expect(saveHarnessSchedule).toHaveBeenCalledWith(noFs, { update: false });
    expect(calls).toEqual(["create:new-name"]);
  });
});
