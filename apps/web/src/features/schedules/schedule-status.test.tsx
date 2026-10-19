// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ScheduleStatusBadge, ScheduleStatusDot, ScheduleStatusLabel } from "./schedule-status";

const statuses = ["claimed", "completed", "paused", "running", "scheduled"] as const;
const labels = {
  claimed: "Claimed",
  completed: "Completed",
  paused: "Paused",
  running: "Running",
  scheduled: "Scheduled",
} as const;

describe("ScheduleStatusBadge", () => {
  it("gives every status its own look, so Completed never reads as Scheduled", () => {
    const classes = statuses.map((status) => {
      const { container, unmount } = render(<ScheduleStatusBadge status={status} />);
      const badge = container.firstElementChild;
      expect(badge?.textContent).toBe(labels[status]);
      const className = badge?.className ?? "";
      unmount();
      return className;
    });
    expect(new Set(classes).size).toBe(statuses.length);
  });

  it("pulses only while a fire is live", () => {
    for (const status of statuses) {
      const { container, unmount } = render(<ScheduleStatusBadge status={status} />);
      const live = status === "claimed" || status === "running";
      expect(Boolean(container.querySelector(".animate-pulse"))).toBe(live);
      unmount();
    }
  });
});

describe("ScheduleStatusDot", () => {
  it("names the status for hover and screen readers, and hides the dot itself", () => {
    for (const status of statuses) {
      const { container, unmount } = render(<ScheduleStatusDot status={status} />);
      const root = container.firstElementChild;
      expect(root?.getAttribute("title")).toBe(labels[status]);
      expect(root?.querySelector(".sr-only")?.textContent).toBe(labels[status]);
      const dot = root?.querySelector("[data-schedule-status-dot]");
      expect(dot?.getAttribute("aria-hidden")).toBe("true");
      expect(dot?.classList.contains("animate-pulse")).toBe(
        status === "claimed" || status === "running",
      );
      unmount();
    }
  });

  it("draws a different dot for every status, with a hollow one for Completed", () => {
    const dots = statuses.map((status) => {
      const { container, unmount } = render(<ScheduleStatusDot status={status} />);
      const className =
        container
          .querySelector("[data-schedule-status-dot]")
          ?.className.replace("animate-pulse", "") ?? "";
      unmount();
      return className.trim();
    });
    expect(new Set(dots).size).toBe(statuses.length);
    expect(dots[1]).toContain("border");
  });
});

describe("ScheduleStatusLabel", () => {
  it("shows the label beside a dot that screen readers skip", () => {
    const { container } = render(<ScheduleStatusLabel status="paused" />);
    expect(container.textContent).toBe("Paused");
    expect(container.querySelector("[title]")).toBeNull();
    expect(container.querySelector("[data-schedule-status-dot]")?.getAttribute("aria-hidden")).toBe(
      "true",
    );
  });
});
