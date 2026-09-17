import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { SessionInventoryWalk } from "@/features/agent/hooks/use-agent-sessions";
import { SessionInventoryStatus } from "./session-inventory-status";

/**
 * The sidebar's inventory status line: progress + Cancel while the visible
 * walk runs, the error + Retry when a page failed, the honest extent hint +
 * Load again after a bounded or cancelled walk, and nothing at all when the
 * last walk completed cleanly.
 */

const idle: SessionInventoryWalk = {
  inFlight: false,
  pages: 0,
  rows: 0,
  complete: null,
  cancelled: false,
};

describe("SessionInventoryStatus", () => {
  it("renders nothing before the first walk and after a complete one", () => {
    const { container, rerender } = render(
      <SessionInventoryStatus
        walk={idle}
        error={null}
        onCancel={() => {}}
        onRetry={() => {}}
      />,
    );
    expect(container).toBeEmptyDOMElement();

    rerender(
      <SessionInventoryStatus
        walk={{ ...idle, pages: 3, rows: 240, complete: true }}
        error={null}
        onCancel={() => {}}
        onRetry={() => {}}
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the running walk's progress with a Cancel that stops it", () => {
    const onCancel = vi.fn();
    render(
      <SessionInventoryStatus
        walk={{ ...idle, inFlight: true, pages: 2, rows: 137 }}
        error={null}
        onCancel={onCancel}
        onRetry={() => {}}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(
      "Loading chats… 137 so far (page 2)",
    );
    fireEvent.click(screen.getByRole("button", { name: "Stop loading chats" }));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("reads plainly before the first page lands", () => {
    render(
      <SessionInventoryStatus
        walk={{ ...idle, inFlight: true }}
        error={null}
        onCancel={() => {}}
        onRetry={() => {}}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(/^Loading chats…/);
    expect(screen.getByRole("status")).not.toHaveTextContent("page");
  });

  it("shows the error with a Retry", () => {
    const onRetry = vi.fn();
    render(
      <SessionInventoryStatus
        walk={idle}
        error="store unavailable"
        onCancel={() => {}}
        onRetry={onRetry}
      />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent("store unavailable");
    fireEvent.click(
      screen.getByRole("button", { name: "Retry loading chats" }),
    );
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("says the inventory is larger after a page-bounded walk, with Load again", () => {
    const onRetry = vi.fn();
    render(
      <SessionInventoryStatus
        walk={{ ...idle, pages: 25, rows: 2500, complete: false }}
        error={null}
        onCancel={() => {}}
        onRetry={onRetry}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(
      "Loaded the first 2500 chats — the inventory is larger.",
    );
    fireEvent.click(screen.getByRole("button", { name: "Load chats again" }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("says loading stopped after a cancel, never claiming the inventory is larger", () => {
    render(
      <SessionInventoryStatus
        walk={{ ...idle, pages: 1, rows: 1, complete: false, cancelled: true }}
        error={null}
        onCancel={() => {}}
        onRetry={() => {}}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(
      "Loading stopped — 1 chat loaded so far.",
    );
    expect(
      screen.getByRole("button", { name: "Load chats again" }),
    ).toBeInTheDocument();
  });

  it("prefers the running walk over a stale error or hint", () => {
    render(
      <SessionInventoryStatus
        walk={{ ...idle, inFlight: true, pages: 1, rows: 4, complete: false }}
        error="old failure"
        onCancel={() => {}}
        onRetry={() => {}}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("Loading chats…");
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
