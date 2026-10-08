// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Label } from "./label";
import { RadioGroup, RadioGroupItem } from "./radio-group";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

describe("RadioGroup", () => {
  it("selects exactly one option at a time", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(
      <RadioGroup aria-label="Size" defaultValue="small" onValueChange={onValueChange}>
        <RadioGroupItem id="small" value="small" />
        <Label htmlFor="small">Small</Label>
        <RadioGroupItem id="large" value="large" />
        <Label htmlFor="large">Large</Label>
      </RadioGroup>,
    );
    expect(screen.getByRole("radiogroup", { name: "Size" }).getAttribute("data-slot")).toBe(
      "radio-group",
    );
    const small = screen.getByRole("radio", { name: "Small" });
    const large = screen.getByRole("radio", { name: "Large" });
    expect(small.getAttribute("aria-checked")).toBe("true");
    expect(large.getAttribute("aria-checked")).toBe("false");
    await user.click(large);
    expect(onValueChange).toHaveBeenCalledWith("large");
    expect(small.getAttribute("aria-checked")).toBe("false");
    expect(large.getAttribute("aria-checked")).toBe("true");
  });
});
