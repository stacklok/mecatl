// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { Label } from "./label";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

describe("Label", () => {
  it("names its control and focuses it when clicked", async () => {
    const user = userEvent.setup();
    render(
      <>
        <Label htmlFor="name">Name</Label>
        <input id="name" />
      </>,
    );
    const label = screen.getByText("Name");
    expect(label.tagName).toBe("LABEL");
    expect(label.getAttribute("data-slot")).toBe("label");
    const input = screen.getByRole("textbox", { name: "Name" });
    await user.click(label);
    expect(document.activeElement).toBe(input);
  });
});
