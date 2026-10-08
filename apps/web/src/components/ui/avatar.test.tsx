// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Avatar, AvatarFallback, AvatarImage } from "./avatar";

afterEach(cleanup);

describe("Avatar", () => {
  it("shows the fallback while the image has not loaded", () => {
    const { container } = render(
      <Avatar>
        <AvatarImage alt="Ada" src="https://example.invalid/ada.png" />
        <AvatarFallback>AL</AvatarFallback>
      </Avatar>,
    );
    expect(container.querySelector('[data-slot="avatar"]')).not.toBeNull();
    expect(screen.getByText("AL").getAttribute("data-slot")).toBe("avatar-fallback");
    expect(screen.queryByRole("img")).toBeNull();
  });
});
