// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

// Pins for #2299's transcript change: what the run log's per-fire transcript
// control must keep while it moves from a chat link to a read-only dialog.
// Each test passes before and after the change.

import { cleanup, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { fire, mountSchedules, resetApi, schedule } from "./schedules-test-kit";

afterEach(() => {
  cleanup();
  resetApi();
});

const FIRES = [
  fire({ firedAt: "2026-10-07T09:00:00Z", id: "with-session", sessionId: "session-1" }),
  fire({
    error: "Provider unavailable",
    firedAt: "2026-10-06T09:00:00Z",
    id: "without-session",
    outcome: "failed",
    sessionId: "",
  }),
];

/** The interactive element behind each "View transcript" label in a container. */
function transcriptControls(container: HTMLElement) {
  return within(container)
    .queryAllByText("View transcript")
    .map((label) => label.closest<HTMLElement>("a[href], button"));
}

describe("the run log's transcript control", () => {
  it("offers one keyboard-reachable View transcript per fire that has a session, on the desktop table", async () => {
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules/Alpha", FIRES);
    const table = await screen.findByRole("table");
    const rows = within(table).getAllByRole("row").slice(1);
    const [withSession, withoutSession] = rows as [HTMLElement, HTMLElement];
    const controls = transcriptControls(withSession);
    expect(controls).toHaveLength(1);
    expect(controls[0]).toBeTruthy();
    expect(controls[0]?.getAttribute("tabindex")).not.toBe("-1");
    expect(transcriptControls(withoutSession)).toEqual([]);
    expect(within(withoutSession).getByText("—")).toBeTruthy();
  });

  it("offers the same control on each phone card that has a session", async () => {
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules/Alpha", FIRES);
    await screen.findByRole("table");
    const cards = [
      ...(document.querySelector("[data-schedule-fire-mobile-list]")?.children ?? []),
    ] as HTMLElement[];
    expect(cards).toHaveLength(2);
    expect(transcriptControls(cards[0] as HTMLElement)).toHaveLength(1);
    expect(transcriptControls(cards[1] as HTMLElement)).toEqual([]);
  });
});
