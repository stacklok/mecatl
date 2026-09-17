import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ErrorPageLayout } from "./error-page";

describe("ErrorPageLayout", () => {
  it("displays the title", () => {
    render(
      <ErrorPageLayout title="Test Error">Error description</ErrorPageLayout>,
    );

    expect(screen.getByRole("heading", { name: /test error/i })).toBeVisible();
  });

  it("displays children as description", () => {
    render(
      <ErrorPageLayout title="Error">
        This is the error message
      </ErrorPageLayout>,
    );

    expect(screen.getByText(/this is the error message/i)).toBeVisible();
  });

  it("displays action buttons when provided", () => {
    render(
      <ErrorPageLayout
        title="Error"
        actions={<button type="button">Click me</button>}
      >
        Description
      </ErrorPageLayout>,
    );

    expect(screen.getByRole("button", { name: /click me/i })).toBeVisible();
  });
});
