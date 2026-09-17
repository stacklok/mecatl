import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { StorageHealth } from "@/lib/harness/storage";
import {
  describeSessionMix,
  StorageHealthCard,
  storageHealthStatus,
} from "./storage-health-card";

/**
 * The Storage card's health summary: a HEALTHY store renders too (unlike
 * the workspace banner), with the saved count and its plain-words mix, the
 * space used ("Not available" when the store cannot size itself), what a clean-up
 * could free, the Needs-attention pill with a plain reason — never the raw
 * backend text — and a Refresh that re-reads. The clean-up block passed as
 * children renders beneath the summary but not while offline. Unsupported,
 * offline and unread states render notes.
 */

const healthy: StorageHealth = {
  available: true,
  unavailableReason: "",
  sessionCount: 12,
  corruptCount: 0,
  v1Count: 2,
  v2Count: 10,
  mainCount: 5,
  childCount: 4,
  scheduledCount: 2,
  unknownCount: 1,
  fileCount: 31,
  currentBytes: 20_480,
  reclaimableBytes: null,
  policy: null,
  lastSweepAt: null,
  nextSweepAt: null,
  lastFailure: "",
  activeJob: "",
};

describe("storageHealthStatus", () => {
  it("is Healthy (naming running maintenance), Needs attention for unreadable / unopenable / failed", () => {
    expect(storageHealthStatus(healthy)).toEqual({
      label: "Healthy",
      detail: "",
    });
    expect(storageHealthStatus({ ...healthy, activeJob: "cleanup" })).toEqual({
      label: "Healthy",
      detail: "Storage maintenance is running.",
    });
    expect(
      storageHealthStatus({
        ...healthy,
        available: false,
        unavailableReason: "permission denied",
      }),
    ).toEqual({
      label: "Needs attention",
      detail: "The agent cannot read its saved chats right now.",
    });
    expect(storageHealthStatus({ ...healthy, corruptCount: 1 }).detail).toBe(
      "1 saved item can no longer be opened.",
    );
    expect(storageHealthStatus({ ...healthy, corruptCount: 3 }).detail).toBe(
      "3 saved items can no longer be opened.",
    );
    expect(
      storageHealthStatus({ ...healthy, lastFailure: "sweep: disk full" }),
    ).toEqual({
      label: "Needs attention",
      detail: "A recent automatic clean-up did not finish.",
    });
  });

  it("never surfaces the raw reason or failure text", () => {
    const unreadable = storageHealthStatus({
      ...healthy,
      available: false,
      unavailableReason: "permission denied",
    });
    expect(unreadable.detail).not.toContain("permission denied");
    const failed = storageHealthStatus({
      ...healthy,
      lastFailure: "sweep: disk full",
    });
    expect(failed.detail).not.toContain("disk full");
  });
});

describe("describeSessionMix", () => {
  it("lists the non-zero kinds in plain words, singular when one", () => {
    expect(describeSessionMix(healthy)).toBe(
      "5 chats · 4 agent runs · 2 scheduled runs · 1 other",
    );
    expect(
      describeSessionMix({
        ...healthy,
        mainCount: 1,
        childCount: 0,
        scheduledCount: 1,
        unknownCount: 0,
      }),
    ).toBe("1 chat · 1 scheduled run");
    expect(
      describeSessionMix({
        ...healthy,
        mainCount: 0,
        childCount: 0,
        scheduledCount: 0,
        unknownCount: 0,
      }),
    ).toBe("");
  });
});

describe("StorageHealthCard", () => {
  it("renders a healthy store's pill, the saved count with its mix, the space used and what can be freed", () => {
    render(
      <StorageHealthCard
        live
        supported
        health={{ ...healthy, reclaimableBytes: 4_096 }}
        onRefresh={vi.fn()}
      />,
    );
    expect(screen.getByTestId("storage-health-status")).toHaveTextContent(
      "Healthy",
    );
    expect(screen.queryByTestId("storage-health-detail")).toBeNull();
    expect(screen.getByTestId("storage-health-sessions")).toHaveTextContent(
      "125 chats · 4 agent runs · 2 scheduled runs · 1 other",
    );
    expect(screen.getByTestId("storage-health-size")).toHaveTextContent(
      "20 KB",
    );
    expect(screen.getByTestId("storage-health-reclaimable")).toHaveTextContent(
      "4 KB",
    );
    // The technical rows are gone for good.
    expect(screen.queryByText("Files")).toBeNull();
    expect(screen.queryByText("Layout")).toBeNull();
    expect(screen.queryByText("Corrupt families")).toBeNull();
    expect(screen.queryByText("Active job")).toBeNull();
    expect(screen.queryByText("Last failure")).toBeNull();
  });

  it("shows Unknown for a size the store cannot report and omits what can be freed when unknown", () => {
    render(
      <StorageHealthCard
        live
        supported
        health={{ ...healthy, currentBytes: null, reclaimableBytes: null }}
        onRefresh={vi.fn()}
      />,
    );
    expect(screen.getByTestId("storage-health-size")).toHaveTextContent(
      "Not available",
    );
    expect(screen.queryByTestId("storage-health-reclaimable")).toBeNull();
  });

  it("names running maintenance and a failed clean-up in the status line", () => {
    const { unmount } = render(
      <StorageHealthCard
        live
        supported
        health={{ ...healthy, activeJob: "cleanup" }}
        onRefresh={vi.fn()}
      />,
    );
    expect(screen.getByTestId("storage-health-status")).toHaveTextContent(
      "Healthy",
    );
    expect(screen.getByTestId("storage-health-detail")).toHaveTextContent(
      "Storage maintenance is running.",
    );
    unmount();

    render(
      <StorageHealthCard
        live
        supported
        health={{ ...healthy, lastFailure: "cleanup: backend timeout" }}
        onRefresh={vi.fn()}
      />,
    );
    expect(screen.getByTestId("storage-health-status")).toHaveTextContent(
      "Needs attention",
    );
    expect(screen.getByTestId("storage-health-detail")).toHaveTextContent(
      "A recent automatic clean-up did not finish.",
    );
  });

  it("an unreadable store shows Needs attention with a plain reason and no counts", () => {
    render(
      <StorageHealthCard
        live
        supported
        health={{
          ...healthy,
          available: false,
          unavailableReason: "permission denied",
        }}
        onRefresh={vi.fn()}
      />,
    );
    expect(screen.getByTestId("storage-health-status")).toHaveTextContent(
      "Needs attention",
    );
    expect(screen.getByTestId("storage-health-detail")).toHaveTextContent(
      "The agent cannot read its saved chats right now.",
    );
    expect(screen.queryByTestId("storage-health-sessions")).toBeNull();
  });

  it("Refresh re-reads; an unread health shows a note with the same button", async () => {
    const user = userEvent.setup();
    const onRefresh = vi.fn();
    render(
      <StorageHealthCard live supported health={null} onRefresh={onRefresh} />,
    );
    expect(
      screen.getByText("Storage details are not available right now."),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Refresh" }));
    expect(onRefresh).toHaveBeenCalledTimes(1);
  });

  it("renders the clean-up block beneath the summary, but not while offline", () => {
    const { unmount } = render(
      <StorageHealthCard live supported health={healthy} onRefresh={vi.fn()}>
        <p>clean-up block</p>
      </StorageHealthCard>,
    );
    expect(screen.getByText("clean-up block")).toBeInTheDocument();
    unmount();

    render(
      <StorageHealthCard
        live={false}
        supported
        health={healthy}
        onRefresh={vi.fn()}
      >
        <p>clean-up block</p>
      </StorageHealthCard>,
    );
    expect(
      screen.getByText(/The agent is offline, so these settings/),
    ).toBeInTheDocument();
    expect(screen.queryByText("clean-up block")).toBeNull();
  });

  it("says so when the agent cannot report on its storage, and still renders the clean-up block", () => {
    render(
      <StorageHealthCard
        live
        supported={false}
        health={null}
        onRefresh={vi.fn()}
      >
        <p>clean-up block</p>
      </StorageHealthCard>,
    );
    expect(
      screen.getByText("This agent cannot report on its storage."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Refresh" })).toBeNull();
    expect(screen.getByText("clean-up block")).toBeInTheDocument();
  });
});
