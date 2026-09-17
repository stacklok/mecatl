import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import {
  SEED_PROMPT_CURRENT_CHAT_NOTE,
  SEED_PROMPT_EDIT_LABEL,
  SEED_PROMPT_NEW_CHAT_NOTE,
  SEED_PROMPT_SEND_LABEL,
  SEED_PROMPT_TITLE,
  SeedPromptDialog,
} from "./seed-prompt-dialog";

/**
 * The `send=1` confirmation: it shows the exact prompt and where Send takes
 * it, Send fires once and only while nothing blocks it, Edit first (and any
 * other dismissal, e.g. Esc) hands the text to the composer instead, and a
 * wait reason disables Send and is read out.
 */
describe("SeedPromptDialog", () => {
  it("shows the exact prompt and the new-chat note, and Send confirms", async () => {
    const user = userEvent.setup();
    const onSend = vi.fn();
    const onEdit = vi.fn();
    render(
      <SeedPromptDialog
        prompt={"Line one\n  indented <b>kept</b>"}
        target="new"
        waitReason={null}
        onSend={onSend}
        onEdit={onEdit}
      />,
    );
    const dialog = screen.getByRole("dialog", { name: SEED_PROMPT_TITLE });
    expect(dialog).toHaveTextContent(SEED_PROMPT_NEW_CHAT_NOTE);
    expect(screen.getByTestId("seed-prompt-text")).toHaveTextContent(
      "Line one indented <b>kept</b>",
    );
    // The text block is verbatim (no markdown, no HTML): the pre keeps it.
    expect(screen.getByTestId("seed-prompt-text").textContent).toBe(
      "Line one\n  indented <b>kept</b>",
    );
    await user.click(
      screen.getByRole("button", { name: SEED_PROMPT_SEND_LABEL }),
    );
    expect(onSend).toHaveBeenCalledTimes(1);
    expect(onEdit).not.toHaveBeenCalled();
  });

  it("names the open chat as the target and Edit first hands the text back", async () => {
    const user = userEvent.setup();
    const onSend = vi.fn();
    const onEdit = vi.fn();
    render(
      <SeedPromptDialog
        prompt="Hello fixture"
        target="current"
        waitReason={null}
        onSend={onSend}
        onEdit={onEdit}
      />,
    );
    expect(screen.getByRole("dialog")).toHaveTextContent(
      SEED_PROMPT_CURRENT_CHAT_NOTE,
    );
    await user.click(
      screen.getByRole("button", { name: SEED_PROMPT_EDIT_LABEL }),
    );
    expect(onEdit).toHaveBeenCalledTimes(1);
    expect(onSend).not.toHaveBeenCalled();
  });

  it("Esc is Edit first: the prompt is never dropped and never sent", async () => {
    const user = userEvent.setup();
    const onSend = vi.fn();
    const onEdit = vi.fn();
    render(
      <SeedPromptDialog
        prompt="Hello fixture"
        target="new"
        waitReason={null}
        onSend={onSend}
        onEdit={onEdit}
      />,
    );
    await user.keyboard("{Escape}");
    expect(onEdit).toHaveBeenCalledTimes(1);
    expect(onSend).not.toHaveBeenCalled();
  });

  it("a wait reason disables Send and is announced", () => {
    render(
      <SeedPromptDialog
        prompt="Hello fixture"
        target="new"
        waitReason="Waiting for the daemon to connect…"
        onSend={() => {}}
        onEdit={() => {}}
      />,
    );
    expect(
      screen.getByRole("button", { name: SEED_PROMPT_SEND_LABEL }),
    ).toBeDisabled();
    expect(screen.getByRole("status")).toHaveTextContent(
      "Waiting for the daemon to connect…",
    );
    // Edit first is always available — an offline daemon loses nothing.
    expect(
      screen.getByRole("button", { name: SEED_PROMPT_EDIT_LABEL }),
    ).toBeEnabled();
  });
});
