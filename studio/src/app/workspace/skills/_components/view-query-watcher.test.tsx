import { render } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { SkillsViewQueryWatcher } from "./view-query-watcher";

/**
 * The `?view=` deep link into the skills page: the learning review queue
 * links a materialized proposal to `/workspace/skills?view=learned`, and the
 * watcher hands that view to the page exactly once, rendering nothing.
 */

const query = vi.hoisted(() => ({ current: new URLSearchParams() }));

vi.mock("next/navigation", () => ({
  useSearchParams: () => query.current,
}));

beforeEach(() => {
  query.current = new URLSearchParams();
});

describe("SkillsViewQueryWatcher", () => {
  it("reports the named view once and renders nothing", () => {
    query.current = new URLSearchParams("view=learned");
    const onView = vi.fn();
    const { container, rerender } = render(
      <SkillsViewQueryWatcher onView={onView} />,
    );
    expect(container).toBeEmptyDOMElement();
    expect(onView).toHaveBeenCalledTimes(1);
    expect(onView).toHaveBeenCalledWith("learned");
    // A re-render with the same query does not fire again.
    rerender(<SkillsViewQueryWatcher onView={onView} />);
    expect(onView).toHaveBeenCalledTimes(1);
  });

  it("stays silent without a view", () => {
    const onView = vi.fn();
    render(<SkillsViewQueryWatcher onView={onView} />);
    expect(onView).not.toHaveBeenCalled();
  });
});
