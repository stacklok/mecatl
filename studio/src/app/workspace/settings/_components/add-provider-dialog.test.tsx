import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ComponentProps } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { AddProviderDialog } from "./add-provider-dialog";

/**
 * The guided add: pick a provider, copy its snippet into the agent's key
 * file, re-check, restart. Rule 3 holds throughout: the dialog renders no
 * input at all — the only field is the provider picker — and the snippet
 * carries the `<YOUR_KEY>` placeholder, never a value. The custom-gateway
 * form and the gateway base-URL override are gone.
 */

const known = [
  {
    name: "anthropic",
    label: "Anthropic",
    testable: true,
    snippet: "providers:\n  anthropic:\n    api_key: <YOUR_KEY>\n",
    note: "An Anthropic API key.",
  },
  {
    name: "openrouter",
    label: "OpenRouter",
    testable: true,
    snippet: "providers:\n  openrouter:\n    api_key: <YOUR_KEY>\n",
    note: "Create a key at openrouter.ai/keys.",
  },
];

const reload = vi.fn(async () => []);
const restartDaemon = vi.fn(async () => {});

type Props = ComponentProps<typeof AddProviderDialog>;

function renderDialog(overrides: Partial<Props> = {}) {
  return render(
    <AddProviderDialog
      known={known}
      configured={["openrouter"]}
      authFile="/home/op/.config/mecatl/auth.yaml"
      reload={reload}
      restartDaemon={restartDaemon}
      restarting={false}
      {...overrides}
    />,
  );
}

/** Radix Select focuses the selected item on open from a timer, i.e. a
 *  state update outside the click's act scope; letting that timer fire
 *  inside an awaited act keeps vitest-fail-on-console quiet. */
async function flushTimers() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

async function openAndPick(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole("button", { name: /Add provider/ }));
  await user.click(await screen.findByRole("combobox"));
  await user.click(await screen.findByRole("option", { name: /Anthropic/ }));
  await flushTimers();
}

beforeEach(() => {
  reload.mockReset();
  reload.mockResolvedValue([]);
  restartDaemon.mockClear();
});

describe("AddProviderDialog", () => {
  it("lists the built-in providers only, marking the ones already added", async () => {
    const user = userEvent.setup();
    renderDialog();
    await user.click(screen.getByRole("button", { name: /Add provider/ }));
    expect(await screen.findByRole("dialog")).toHaveTextContent(
      "Studio never sees your key. You add it to the agent’s own key file in three quick steps.",
    );
    await user.click(screen.getByRole("combobox"));
    expect(
      await screen.findByRole("option", { name: /Anthropic/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("option", { name: "OpenRouter (already added)" }),
    ).toHaveAttribute("aria-disabled", "true");
    expect(
      screen.queryByRole("option", { name: /Custom gateway/ }),
    ).not.toBeInTheDocument();
  });

  it("shows the snippet and the plain steps for a picked provider, with no input anywhere", async () => {
    const user = userEvent.setup();
    renderDialog();
    await openAndPick(user);
    expect(
      screen.getByText("2. Add this to the agent’s key file"),
    ).toBeInTheDocument();
    expect(screen.getByText(/An Anthropic API key\./)).toBeInTheDocument();
    expect(screen.getByText(/api_key: <YOUR_KEY>/)).toBeInTheDocument();
    expect(
      screen.getByText("3. Save the file, then re-check"),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Copy snippet" }),
    ).toBeInTheDocument();
    // Rule 3: nothing to type a key into — and no gateway URL field either.
    expect(document.querySelectorAll("input, textarea")).toHaveLength(0);
    expect(
      screen.queryByText(/Route through a gateway/),
    ).not.toBeInTheDocument();
    expect(screen.queryByText(/daemon/)).not.toBeInTheDocument();
  });

  it("names the key file plainly when the agent did not report where it is", async () => {
    const user = userEvent.setup();
    renderDialog({ authFile: "" });
    await openAndPick(user);
    expect(
      screen.getByText(/the person who set up the agent knows where it is/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/~\/\.config/)).not.toBeInTheDocument();
  });

  it("Re-check says when the key is not there yet, then offers the restart once it is", async () => {
    const user = userEvent.setup();
    renderDialog();
    await openAndPick(user);

    reload.mockResolvedValueOnce([
      {
        name: "anthropic",
        configured: true,
        keyPresent: false,
        source: "auth.yaml",
        testable: true,
      },
    ] as never);
    await user.click(screen.getByRole("button", { name: "Re-check" }));
    expect(
      await screen.findByText(
        "Not found yet. Save the file on the agent’s computer, then try again.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Save and restart" }),
    ).not.toBeInTheDocument();

    reload.mockResolvedValueOnce([
      {
        name: "anthropic",
        configured: true,
        keyPresent: true,
        source: "auth.yaml",
        testable: true,
      },
    ] as never);
    await user.click(screen.getByRole("button", { name: "Re-check" }));
    expect(await screen.findByText(/found\./)).toHaveTextContent(
      "Anthropic found. Changes restart the agent. Anything running will stop.",
    );
    expect(
      screen.queryByRole("button", { name: "Re-check" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save and restart" }));
    expect(restartDaemon).toHaveBeenCalledTimes(1);
  });

  it("copies the exact snippet to the clipboard", async () => {
    // user-event installs its own clipboard stub on setup; ours goes on top.
    const user = userEvent.setup();
    const writeText = vi.fn(async () => {});
    Object.defineProperty(window.navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    renderDialog();
    await openAndPick(user);
    await user.click(screen.getByRole("button", { name: "Copy snippet" }));
    expect(writeText).toHaveBeenCalledWith(known[0].snippet);
    expect(
      await screen.findByRole("button", { name: "Copied" }),
    ).toBeInTheDocument();
  });
});
