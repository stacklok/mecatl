// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { RuntimeStatusLine } from "./settings-card";

// Pins the runtime status line's announcements before its failure banner
// moves onto the Alert primitive: a failed read is one assertive alert with
// the message, and loading and notices stay polite status regions.

afterEach(cleanup);

describe("runtime status line", () => {
  it("announces a failed read as a single alert carrying the message", () => {
    render(<RuntimeStatusLine state={{ kind: "error", text: "Runtime unavailable: timeout" }} />);
    const alerts = screen.getAllByRole("alert");
    expect(alerts).toHaveLength(1);
    expect(alerts[0]?.textContent).toBe("Runtime unavailable: timeout");
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("keeps loading and notices as polite status lines", () => {
    const { rerender } = render(
      <RuntimeStatusLine state={{ kind: "loading", text: "Loading…" }} />,
    );
    expect(screen.getByRole("status").textContent).toBe("Loading…");
    expect(screen.queryByRole("alert")).toBeNull();
    rerender(<RuntimeStatusLine state={{ kind: "notice", text: "Offline." }} />);
    expect(screen.getByRole("status").textContent).toBe("Offline.");
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
