import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { BrandLogo } from "./brand-logo";

describe("BrandLogo", () => {
  describe("when BRAND_NAME is set", () => {
    beforeEach(() => {
      vi.stubEnv("BRAND_NAME", "Acme Corp");
    });

    it("uses BRAND_NAME as the alt attribute on the rendered <img>", () => {
      render(<BrandLogo />);
      expect(screen.getByRole("img", { name: "Acme Corp" })).toBeVisible();
    });
  });

  describe("when BRAND_NAME is unset", () => {
    beforeEach(() => {
      vi.stubEnv("BRAND_NAME", "");
    });

    it('defaults the alt attribute to "Stacklok"', () => {
      render(<BrandLogo />);
      expect(screen.getByRole("img", { name: "Stacklok" })).toBeVisible();
    });
  });

  it("always uses /brand/logo as the src (served by the force-static route handler)", () => {
    render(<BrandLogo />);
    expect(screen.getByRole("img")).toHaveAttribute("src", "/brand/logo");
  });
});
