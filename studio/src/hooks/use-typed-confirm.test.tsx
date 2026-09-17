import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { useTypedConfirm } from "./use-typed-confirm";

/**
 * The typed confirmation: the confirm button stays disabled until the field
 * holds the phrase EXACTLY — case-sensitive, untrimmed — and every other way
 * out resolves false.
 */

function Harness({ onResult }: { onResult: (confirmed: boolean) => void }) {
  const { confirmTyped, TypedConfirmDialog } = useTypedConfirm();
  return (
    <>
      <button
        type="button"
        onClick={() =>
          void confirmTyped({
            title: "Delete 3 stored sessions permanently?",
            description: "This cannot be undone.",
            phrase: "CLEAN UP",
            confirmText: "Clean up",
          }).then(onResult)
        }
      >
        open
      </button>
      {TypedConfirmDialog}
    </>
  );
}

async function openDialog(onResult = vi.fn()) {
  const user = userEvent.setup();
  render(<Harness onResult={onResult} />);
  await user.click(screen.getByRole("button", { name: "open" }));
  const dialog = await screen.findByRole("alertdialog");
  const input = screen.getByRole("textbox", {
    name: /Type CLEAN UP to confirm/,
  });
  const confirm = screen.getByRole("button", { name: "Clean up" });
  return { user, dialog, input, confirm, onResult };
}

describe("useTypedConfirm", () => {
  it("enables confirm only for the exact phrase — case and whitespace included", async () => {
    const { user, input, confirm, onResult } = await openDialog();
    expect(
      screen.getByText("Delete 3 stored sessions permanently?"),
    ).toBeInTheDocument();
    expect(confirm).toBeDisabled();

    await user.type(input, "clean up");
    expect(confirm).toBeDisabled();

    await user.clear(input);
    await user.type(input, "CLEAN UP ");
    expect(confirm).toBeDisabled();

    await user.clear(input);
    await user.type(input, "CLEAN UP");
    expect(confirm).toBeEnabled();

    await user.click(confirm);
    expect(onResult).toHaveBeenCalledWith(true);
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("Enter confirms only once the phrase matches", async () => {
    const { user, input, onResult } = await openDialog();
    await user.type(input, "CLEAN{Enter}");
    expect(onResult).not.toHaveBeenCalled();
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();

    await user.type(input, " UP{Enter}");
    expect(onResult).toHaveBeenCalledWith(true);
  });

  it("Cancel resolves false", async () => {
    const { user, input, onResult } = await openDialog();
    await user.type(input, "CLEAN UP");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onResult).toHaveBeenCalledWith(false);
    expect(screen.queryByRole("alertdialog")).toBeNull();
  });

  it("Escape resolves false and a re-open starts with an empty field", async () => {
    const { user, input, onResult } = await openDialog();
    await user.type(input, "CLEAN UP");
    await user.keyboard("{Escape}");
    expect(onResult).toHaveBeenCalledWith(false);

    await user.click(screen.getByRole("button", { name: "open" }));
    const again = await screen.findByRole("textbox", {
      name: /Type CLEAN UP to confirm/,
    });
    expect(again).toHaveValue("");
    expect(screen.getByRole("button", { name: "Clean up" })).toBeDisabled();
  });
});
