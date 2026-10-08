// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from "./empty";

afterEach(cleanup);

describe("Empty", () => {
  it("composes media, title, description and content slots", () => {
    const { container } = render(
      <Empty>
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <svg aria-hidden data-testid="art" />
          </EmptyMedia>
          <EmptyTitle>No schedules yet</EmptyTitle>
          <EmptyDescription>Schedules run a prompt on a timer.</EmptyDescription>
        </EmptyHeader>
        <EmptyContent>
          <button type="button">New schedule</button>
        </EmptyContent>
      </Empty>,
    );
    const slot = (name: string) => container.querySelector(`[data-slot="${name}"]`);
    expect(slot("empty")).not.toBeNull();
    expect(slot("empty-title")?.textContent).toBe("No schedules yet");
    expect(slot("empty-description")?.textContent).toBe("Schedules run a prompt on a timer.");
    const media = slot("empty-icon");
    expect(media?.getAttribute("data-variant")).toBe("icon");
    expect(media?.className).toContain("bg-muted");
    expect(media?.contains(screen.getByTestId("art"))).toBe(true);
    expect(
      slot("empty-content")?.contains(screen.getByRole("button", { name: "New schedule" })),
    ).toBe(true);
  });

  it("defaults the media slot to the transparent variant", () => {
    const { container } = render(<EmptyMedia>icon</EmptyMedia>);
    const media = container.querySelector('[data-slot="empty-icon"]');
    expect(media?.getAttribute("data-variant")).toBe("default");
    expect(media?.className).toContain("bg-transparent");
  });
});
