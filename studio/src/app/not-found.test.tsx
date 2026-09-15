import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import NotFound from "./not-found";

describe("NotFound (root)", () => {
  it("displays a page not found heading", async () => {
    render(await NotFound());

    expect(
      screen.getByRole("heading", { name: /page not found/i }),
    ).toBeVisible();
  });

  it("displays a generic error message", async () => {
    render(await NotFound());

    expect(
      screen.getByText(/the page you're looking for doesn't exist/i),
    ).toBeVisible();
  });

  it("has a link home", async () => {
    render(await NotFound());

    const link = screen.getByRole("link", { name: /go home/i });
    expect(link).toHaveAttribute("href", "/workspace/chat");
  });

  it("does not have a back button", async () => {
    render(await NotFound());

    expect(
      screen.queryByRole("button", { name: /back/i }),
    ).not.toBeInTheDocument();
  });

  it("displays the brand header", async () => {
    render(await NotFound());

    expect(screen.getByRole("banner")).toBeVisible();
  });
});
