// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { clearUserScopedStorage } from "../../lib/account-storage";
import {
  ChatComposer,
  type ComposerModelOption,
  type DraftChatConfiguration,
} from "./chat-composer";

afterEach(() => {
  cleanup();
  clearUserScopedStorage();
  window.localStorage.clear();
  window.sessionStorage.clear();
});

const configuration: DraftChatConfiguration = { mode: "default", reasoningEffort: "default" };

function option(id: string, label: string, effortLevels?: ComposerModelOption["effortLevels"]) {
  return {
    ...(effortLevels ? { effortLevels } : {}),
    id,
    image: false,
    label,
    providerId: "provider",
    providerLabel: "Provider",
    reasoning: false,
  };
}

const lowToHigh = option("lowhigh", "Low-high model", {
  known: true,
  levels: ["low", "medium", "high"],
});
const effortless = option("none", "Effortless model", { known: true, levels: [] });
const gateway = option("gateway", "Gateway model", { known: false, levels: [] });
const models = [lowToHigh, effortless, gateway];

/** Picks a menu item the way a keyboard user does; happy-dom drops Radix's pointer selection. */
async function choose(user: ReturnType<typeof userEvent.setup>, item: HTMLElement) {
  act(() => item.focus());
  await user.keyboard("{Enter}");
}

async function effortItems(user: ReturnType<typeof userEvent.setup>, pill: RegExp) {
  await user.click(screen.getByRole("button", { name: pill }));
  await user.hover(screen.getByRole("menuitem", { name: /^Effort/ }));
  await screen.findByRole("menuitem", { name: "Auto" });
  return screen
    .getAllByRole("menuitem")
    .map((item) => item.textContent)
    .filter((text) => ["Auto", "Low", "Medium", "High", "Extra high", "Max"].includes(text ?? ""));
}

describe("Effort choices from per-model levels", () => {
  it("offers a draft Auto plus its model's levels and predicts the level an unaccepted pick sends", async () => {
    const user = userEvent.setup();
    render(
      <ChatComposer
        configuration={{
          ...configuration,
          model: { id: "lowhigh", providerId: "provider" },
          reasoningEffort: "xhigh",
        }}
        models={models}
        onConfigurationChange={vi.fn()}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );

    expect(await effortItems(user, /^Model Low-high model · Extra high/)).toEqual([
      "Auto",
      "Low",
      "Medium",
      "High",
    ]);
    expect(
      screen.getByText("This model doesn't accept Extra high; requests use High."),
    ).toBeTruthy();
    // The old caveat belongs to servers without per-model levels.
    expect(screen.queryByText(/reports no reasoning support/)).toBeNull();
  });

  it("offers Auto alone for a model known to take no effort", async () => {
    const user = userEvent.setup();
    render(
      <ChatComposer
        configuration={{ ...configuration, model: { id: "none", providerId: "provider" } }}
        models={models}
        onConfigurationChange={vi.fn()}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );

    expect(await effortItems(user, /^Model Effortless model/)).toEqual(["Auto"]);
    expect(screen.getByText("This model has no effort levels.")).toBeTruthy();
  });

  it("offers every tier for unknown levels and lists models again when Effort opens", async () => {
    const user = userEvent.setup();
    const onRefreshModels = vi.fn();
    const view = render(
      <ChatComposer
        configuration={{ ...configuration, model: { id: "gateway", providerId: "provider" } }}
        models={models}
        onConfigurationChange={vi.fn()}
        onRefreshModels={onRefreshModels}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );

    expect(await effortItems(user, /^Model Gateway model/)).toEqual([
      "Auto",
      "Low",
      "Medium",
      "High",
      "Extra high",
      "Max",
    ]);
    expect(
      screen.getByText("This model's effort levels are unknown; the provider may refuse a level."),
    ).toBeTruthy();
    expect(onRefreshModels).toHaveBeenCalledOnce();
    await user.keyboard("{Escape}");

    // A model whose levels are known needs no new listing.
    view.rerender(
      <ChatComposer
        configuration={{ ...configuration, model: { id: "lowhigh", providerId: "provider" } }}
        models={models}
        onConfigurationChange={vi.fn()}
        onRefreshModels={onRefreshModels}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    await effortItems(user, /^Model Low-high model/);
    expect(onRefreshModels).toHaveBeenCalledOnce();
  });

  it("shows a live chat's request, names the applied level, and forks with the request", async () => {
    const user = userEvent.setup();
    const onForkModel = vi.fn();
    render(
      <ChatComposer
        canForkModel
        liveModel={{
          appliedReasoningEffort: "default",
          id: "none",
          providerId: "provider",
          reasoningEffort: "xhigh",
        }}
        // The chat's model is hidden from the pickers but keeps its levels.
        modelInventory={models}
        models={[lowToHigh, gateway]}
        onForkModel={onForkModel}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );

    expect(await effortItems(user, /^Model none · Extra high/)).toEqual(["Auto"]);
    expect(screen.getByText("This model has no effort levels.")).toBeTruthy();
    await choose(user, screen.getByRole("menuitem", { name: "Auto" }));
    expect(onForkModel).toHaveBeenLastCalledWith({ id: "none", providerId: "provider" }, "default");

    await user.click(screen.getByRole("button", { name: /^Model none/ }));
    await user.hover(screen.getByRole("menuitem", { name: /^Model none/ }));
    await choose(user, await screen.findByRole("menuitem", { name: "Low-high model" }));
    // The request travels unchanged; the server picks the level the new model takes.
    expect(onForkModel).toHaveBeenLastCalledWith(
      { id: "lowhigh", providerId: "provider" },
      "xhigh",
    );
  });

  it("names the level a live chat's requests use when the model lowers its request", async () => {
    const user = userEvent.setup();
    render(
      <ChatComposer
        canForkModel
        liveModel={{
          appliedReasoningEffort: "high",
          id: "lowhigh",
          providerId: "provider",
          reasoningEffort: "max",
        }}
        models={models}
        onForkModel={vi.fn()}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );

    await effortItems(user, /^Model Low-high model · Max/);
    expect(screen.getByText("This model doesn't accept Max; requests use High.")).toBeTruthy();
  });

  it("offers the same choices and note in the phone sheet", async () => {
    const user = userEvent.setup();
    const onRefreshModels = vi.fn();
    const view = render(
      <ChatComposer
        configuration={{ ...configuration, model: { id: "lowhigh", providerId: "provider" } }}
        models={models}
        onConfigurationChange={vi.fn()}
        onRefreshModels={onRefreshModels}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Composer options" }));
    let sheet = screen.getByRole("dialog", { name: "Composer options" });
    await user.click(within(sheet).getByRole("button", { name: /^Model/ }));
    const effort = within(sheet).getByRole("group", { name: "Effort" });
    expect(
      within(effort)
        .getAllByRole("button")
        .map((button) => button.textContent),
    ).toEqual(["Auto", "Low", "Medium", "High"]);
    expect(onRefreshModels).not.toHaveBeenCalled();
    await user.keyboard("{Escape}");

    view.rerender(
      <ChatComposer
        configuration={{ ...configuration, model: { id: "gateway", providerId: "provider" } }}
        models={models}
        onConfigurationChange={vi.fn()}
        onRefreshModels={onRefreshModels}
        onSend={vi.fn().mockResolvedValue(true)}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Composer options" }));
    sheet = screen.getByRole("dialog", { name: "Composer options" });
    await user.click(within(sheet).getByRole("button", { name: /^Model/ }));
    expect(
      within(sheet).getByText(
        "This model's effort levels are unknown; the provider may refuse a level.",
      ),
    ).toBeTruthy();
    expect(onRefreshModels).toHaveBeenCalledOnce();
  });
});
