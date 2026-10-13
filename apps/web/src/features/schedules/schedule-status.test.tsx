// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ScheduleStatusBadge } from "./schedule-status";

describe("ScheduleStatusBadge", () => {
  it("gives every status its own look, so Completed never reads as Scheduled", () => {
    const statuses = ["claimed", "completed", "paused", "running", "scheduled"] as const;
    const classes = statuses.map((status) => {
      const { container, unmount } = render(<ScheduleStatusBadge status={status} />);
      const badge = container.firstElementChild;
      expect(badge?.textContent).toBe(status);
      const className = badge?.className ?? "";
      unmount();
      return className;
    });
    expect(new Set(classes).size).toBe(statuses.length);
  });
});
