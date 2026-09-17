import { render } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { InspectQueryWatcher } from "./inspect-query-watcher";

/**
 * The search's run deep link (`?inspect=<id>`) opens the read-only
 * transcript exactly once and then leaves a clean URL behind.
 */

const nav = vi.hoisted(() => ({
  params: new URLSearchParams(),
  replace: vi.fn(),
  pathname: "/workspace/chat/main-1",
}));

vi.mock("next/navigation", () => ({
  useSearchParams: () => nav.params,
  useRouter: () => ({ replace: nav.replace, push: vi.fn() }),
  usePathname: () => nav.pathname,
}));

afterEach(() => {
  nav.params = new URLSearchParams();
  nav.replace.mockReset();
});

describe("InspectQueryWatcher", () => {
  it("opens the named run once and strips the query from the URL", () => {
    nav.params = new URLSearchParams("inspect=subagent-1");
    const onInspect = vi.fn();
    const { rerender } = render(<InspectQueryWatcher onInspect={onInspect} />);
    expect(onInspect).toHaveBeenCalledTimes(1);
    expect(onInspect).toHaveBeenCalledWith("subagent-1");
    expect(nav.replace).toHaveBeenCalledWith("/workspace/chat/main-1");

    // A re-render with a new handler identity (the parent re-rendered) does
    // not reopen the same run.
    rerender(<InspectQueryWatcher onInspect={vi.fn()} />);
    expect(onInspect).toHaveBeenCalledTimes(1);
  });

  it("does nothing without the query", () => {
    const onInspect = vi.fn();
    render(<InspectQueryWatcher onInspect={onInspect} />);
    expect(onInspect).not.toHaveBeenCalled();
    expect(nav.replace).not.toHaveBeenCalled();
  });
});
