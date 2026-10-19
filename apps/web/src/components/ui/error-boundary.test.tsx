// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ErrorBoundary } from "./error-boundary";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

// React and the boundary both report a caught render error on console.error;
// `restoreMocks` undoes the stub after each test.
beforeEach(() => {
  vi.spyOn(console, "error").mockImplementation(() => {});
});

let shouldThrow = true;

function Flaky() {
  if (shouldThrow) throw new Error("Panel exploded");
  return <p>Recovered panel</p>;
}

describe("ErrorBoundary", () => {
  it("renders its children when nothing throws", () => {
    render(
      <ErrorBoundary>
        <p>Healthy panel</p>
      </ErrorBoundary>,
    );
    expect(screen.getByText("Healthy panel")).toBeTruthy();
  });

  it("shows the error and re-renders the children on Try again", async () => {
    shouldThrow = true;
    const user = userEvent.setup();
    render(
      <ErrorBoundary>
        <Flaky />
      </ErrorBoundary>,
    );
    expect(screen.getByRole("heading", { name: "Something went wrong" })).toBeTruthy();
    expect(screen.getByText("Panel exploded")).toBeTruthy();
    expect(console.error).toHaveBeenCalledWith(
      "Error caught by boundary:",
      expect.any(Error),
      expect.anything(),
    );
    shouldThrow = false;
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(screen.getByText("Recovered panel")).toBeTruthy();
  });

  it("prefers a caller-supplied fallback", () => {
    function Thrower(): never {
      throw new Error("boom");
    }
    render(
      <ErrorBoundary fallback={<p>Custom fallback</p>}>
        <Thrower />
      </ErrorBoundary>,
    );
    expect(screen.getByText("Custom fallback")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
  });
});
