// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Alert, AlertDescription, AlertTitle } from "./alert";

afterEach(cleanup);

describe("Alert", () => {
  it("announces its title and description as an alert", () => {
    render(
      <Alert>
        <AlertTitle>Connection lost</AlertTitle>
        <AlertDescription>Retrying in 5 seconds.</AlertDescription>
      </Alert>,
    );
    const alert = screen.getByRole("alert");
    expect(alert.getAttribute("data-slot")).toBe("alert");
    expect(alert.className).toContain("bg-card");
    expect(alert.querySelector('[data-slot="alert-title"]')?.textContent).toBe("Connection lost");
    expect(alert.querySelector('[data-slot="alert-description"]')?.textContent).toBe(
      "Retrying in 5 seconds.",
    );
  });

  it("applies the destructive variant", () => {
    render(<Alert variant="destructive">Failed</Alert>);
    const alert = screen.getByRole("alert");
    expect(alert.className).toContain("text-destructive");
    expect(alert.className).not.toContain("text-card-foreground");
  });
});
