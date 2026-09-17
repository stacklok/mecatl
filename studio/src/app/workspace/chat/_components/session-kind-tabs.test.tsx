import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StorageMaintenanceLink } from "./session-kind-tabs";

describe("StorageMaintenanceLink", () => {
  it("points at the Storage settings page", () => {
    render(<StorageMaintenanceLink />);
    expect(
      screen.getByRole("link", { name: "Storage & maintenance" }),
    ).toHaveAttribute("href", "/workspace/settings/storage");
  });
});
