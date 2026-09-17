import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { SteerTraceEntry } from "@/features/agent/steer-trace";
import { SteerTraceLine } from "./steer-trace-line";

const entries: SteerTraceEntry[] = [
  { id: "steer-1", text: "look at the tests", decision: "accepted", at: 1 },
  {
    id: "steer-1",
    text: "look at the tests",
    decision: "drained",
    watermark: "steer-1",
    at: 2,
  },
  { id: "steer-2", text: "stop", decision: "too_late", at: 3 },
];

describe("SteerTraceLine", () => {
  it("renders one `[steer]` line per entry, oldest first, with the drain watermark", () => {
    render(<SteerTraceLine entries={entries} />);
    const list = screen.getByRole("list", {
      name: "Steer trace (developer tools)",
    });
    const lines = Array.from(list.querySelectorAll("li")).map(
      (li) => li.textContent,
    );
    expect(lines).toEqual([
      "[steer] steer-1 accepted",
      "[steer] steer-1 drained (watermark steer-1)",
      "[steer] steer-2 too_late",
    ]);
  });

  it("renders nothing while the trace is empty or withheld", () => {
    const { container, rerender } = render(<SteerTraceLine entries={[]} />);
    expect(container).toBeEmptyDOMElement();
    rerender(<SteerTraceLine />);
    expect(container).toBeEmptyDOMElement();
  });

  it("keeps the steer text out of the line and in the hover title only", () => {
    render(<SteerTraceLine entries={entries.slice(0, 1)} />);
    const line = screen.getByText("[steer] steer-1 accepted");
    expect(line).toHaveAttribute(
      "title",
      "[steer] steer-1 accepted — look at the tests",
    );
  });
});
