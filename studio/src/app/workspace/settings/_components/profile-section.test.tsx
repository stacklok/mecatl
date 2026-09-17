import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { memoryStorage } from "@/test/memory-storage";
import { ProfileSection } from "./profile-section";

/**
 * Settings → You. Pins that the page offers exactly the two things an
 * office user personalises about themselves — a name and a picture — in
 * plain words: (1) the card is titled "You" with the name field labelled
 * "Your name" and described as what it is (a label on your messages, not
 * something the agent is told), (2) typing a name stores it browser-side
 * so the chat can read it back, (3) the picture row offers an upload when
 * nothing is stored, and (4) no jargon or extra controls leak in.
 */

const USER_NAME_KEY = "mecatl-studio.user-name";

// The global afterEach un-stubs it, so each test starts from an empty store.
beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
});

describe("ProfileSection", () => {
  it("shows the You card with a plainly described name field and picture row", () => {
    render(<ProfileSection />);

    expect(screen.getByRole("heading", { name: "You" })).toBeInTheDocument();

    const nameInput = screen.getByLabelText("Your name");
    expect(nameInput).toHaveAttribute("placeholder", "You");
    expect(nameInput).toHaveValue("");
    expect(screen.getByText("Shown on your messages.")).toBeInTheDocument();

    expect(screen.getByText("Picture")).toBeInTheDocument();
    expect(
      screen.getByText("Shown next to your messages."),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Upload picture" }),
    ).toBeInTheDocument();
    // Nothing stored yet, so there is nothing to remove.
    expect(screen.queryByRole("button", { name: "Remove picture" })).toBeNull();
  });

  it("keeps the page to a name and a picture, in plain words", () => {
    const { container } = render(<ProfileSection />);

    expect(screen.getAllByRole("textbox")).toHaveLength(1);
    expect(screen.getAllByRole("button")).toHaveLength(1);
    expect(screen.queryByRole("switch")).toBeNull();
    expect(screen.queryByText(/What should the agent call you/)).toBeNull();
    expect(container.textContent).not.toMatch(/daemon|mecated|controller/i);
  });

  it("stores a typed name so the chat can label your messages with it", async () => {
    const user = userEvent.setup();
    render(<ProfileSection />);

    await user.type(screen.getByLabelText("Your name"), "Sam");
    expect(screen.getByLabelText("Your name")).toHaveValue("Sam");
    expect(window.localStorage.getItem(USER_NAME_KEY)).toBe("Sam");

    await user.clear(screen.getByLabelText("Your name"));
    expect(window.localStorage.getItem(USER_NAME_KEY)).toBeNull();
  });

  it("reads a previously stored name back on load", async () => {
    window.localStorage.setItem(USER_NAME_KEY, "Priya");
    render(<ProfileSection />);
    expect(await screen.findByDisplayValue("Priya")).toBeInTheDocument();
  });
});
