// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { InputSearch } from "./input-search";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

function Harness() {
  const [value, setValue] = useState("");
  return <InputSearch aria-label="Search sessions" onChange={setValue} value={value} />;
}

describe("InputSearch", () => {
  it("reports typed text and clears it with the clear button", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const input = screen.getByRole("textbox", { name: "Search sessions" }) as HTMLInputElement;
    expect(input.placeholder).toBe("Search");
    expect(screen.queryByRole("button", { name: "Clear search" })).toBeNull();
    await user.type(input, "deploy");
    expect(input.value).toBe("deploy");
    await user.click(screen.getByRole("button", { name: "Clear search" }));
    expect(input.value).toBe("");
    expect(screen.queryByRole("button", { name: "Clear search" })).toBeNull();
  });
});
