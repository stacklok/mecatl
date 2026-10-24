// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type { ExecutionTemplateInventory } from "@mecatl-studio/contracts";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatComposer, type DraftChatConfiguration } from "./chat-composer";

const initialConfiguration: DraftChatConfiguration = {
  mode: "default",
  reasoningEffort: "default",
};

function ControlledComposer({
  onConfigurationChange,
}: {
  onConfigurationChange: (configuration: DraftChatConfiguration) => void;
}) {
  const [configuration, setConfiguration] = useState(initialConfiguration);
  return (
    <>
      <ChatComposer
        configuration={configuration}
        models={[
          {
            id: "image",
            image: true,
            label: "Image model",
            providerId: "provider",
            providerLabel: "Provider",
          },
          {
            id: "text",
            image: false,
            label: "Text model",
            providerId: "provider",
            providerLabel: "Provider",
          },
        ]}
        onConfigurationChange={(next) => {
          onConfigurationChange(next);
          setConfiguration(next);
        }}
        onSend={vi.fn().mockResolvedValue(true)}
      />
      <output data-testid="draft-configuration">{JSON.stringify(configuration)}</output>
    </>
  );
}

/** Holds one execution template and can refresh the inventory without it. */
function RefreshablePicker({ item }: { item: ExecutionTemplateInventory["items"][number] }) {
  const [configuration, setConfiguration] = useState(initialConfiguration);
  const [items, setItems] = useState([item]);
  return (
    <>
      <ChatComposer
        configuration={configuration}
        executionInventory={{ inventoryRevision: "1", items }}
        executionStatus="ready"
        onConfigurationChange={setConfiguration}
        onSend={vi.fn().mockResolvedValue(false)}
      />
      <button onClick={() => setItems([])} type="button">
        Refresh without revision
      </button>
      <output data-testid="choice">{JSON.stringify(configuration.execution)}</output>
    </>
  );
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("composer configuration selectors", () => {
  it("shows loading, empty, disabled, and unavailable catalogs without blocking default or none", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    const view = render(
      <ChatComposer
        configuration={initialConfiguration}
        executionStatus="loading"
        onConfigurationChange={onChange}
        onSend={vi.fn().mockResolvedValue(false)}
      />,
    );
    expect(screen.getByText(/Loading eligible templates/)).toBeTruthy();
    view.rerender(
      <ChatComposer
        configuration={initialConfiguration}
        executionStatus="ready"
        executionInventory={{ items: [], inventoryRevision: "empty" }}
        onConfigurationChange={onChange}
        onSend={vi.fn().mockResolvedValue(false)}
      />,
    );
    expect(screen.getByText(/No eligible templates/)).toBeTruthy();
    view.rerender(
      <ChatComposer
        configuration={initialConfiguration}
        executionStatus="unavailable"
        onConfigurationChange={onChange}
        onSend={vi.fn().mockResolvedValue(false)}
      />,
    );
    expect(screen.getByText(/Template catalog unavailable/)).toBeTruthy();
    await user.selectOptions(screen.getByLabelText("Execution"), "none");
    expect(onChange).toHaveBeenCalledWith({ ...initialConfiguration, execution: { none: {} } });
    view.rerender(
      <ChatComposer
        configuration={initialConfiguration}
        executionStatus="disabled"
        onConfigurationChange={onChange}
        onSend={vi.fn().mockResolvedValue(false)}
      />,
    );
    expect(screen.getByText(/Template catalog disabled/)).toBeTruthy();
    expect(screen.getByRole("option", { name: "Deployment default" })).toBeTruthy();
  });

  it("preserves the exact revision and does not replace a stale selection with default", async () => {
    const user = userEvent.setup();
    const revision = `v1-${"a".repeat(64)}`;
    const item = {
      template: { id: "safe", revision },
      name: "Workspace",
      description: "",
      displayToken: "t",
      extensions: {},
      declaredExecutionFiles: true,
      declaredBuiltInShell: false,
    };
    render(<RefreshablePicker item={item} />);
    expect(screen.getByRole("option", { name: /declared files: true, Shell: false/ })).toBeTruthy();
    await user.selectOptions(
      screen.getByLabelText("Execution"),
      JSON.stringify(["safe", revision]),
    );
    expect(JSON.parse(screen.getByTestId("choice").textContent ?? "")).toEqual({
      template: { id: "safe", revision },
    });
    await user.click(screen.getByRole("button", { name: "Refresh without revision" }));
    expect(screen.getByRole("option", { name: /Selected template unavailable/ })).toBeTruthy();
    expect(JSON.parse(screen.getByTestId("choice").textContent ?? "")).toEqual({
      template: { id: "safe", revision },
    });
  });

  it("applies model, effort, mode, and tool choices from the narrow-screen sheet", async () => {
    Object.defineProperties(window, {
      innerHeight: { configurable: true, value: 480 },
      innerWidth: { configurable: true, value: 320 },
    });
    const user = userEvent.setup();
    const onConfigurationChange = vi.fn();
    render(<ControlledComposer onConfigurationChange={onConfigurationChange} />);

    const textarea = screen.getByRole("textbox", { name: "Message Mecatl" });
    textarea.focus();
    expect(document.activeElement).toBe(textarea);
    expect(window.innerWidth).toBe(320);
    expect(window.innerHeight).toBe(480);

    // The sheet drills into one view per setting, and each pick closes it.
    const options = screen.getByRole("button", { name: "Composer options" });
    const pick = async (row: RegExp, choice: string) => {
      await user.click(options);
      const sheet = screen.getByRole("dialog", { name: "Composer options" });
      await user.click(within(sheet).getByRole("button", { name: row }));
      await user.click(within(sheet).getByRole("button", { name: choice }));
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    };
    await pick(/^Model/, "Text model");
    await pick(/^Model/, "High");
    await pick(/^Mode\s/, "Plan Create a plan before making changes.");
    // The Execution picker sits inline in the sheet's main view.
    await user.click(options);
    const sheet = screen.getByRole("dialog", { name: "Composer options" });
    await user.selectOptions(within(sheet).getByLabelText("Execution"), "none");
    await user.click(within(sheet).getByRole("button", { name: "Close composer options" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    expect(JSON.parse(screen.getByTestId("draft-configuration").textContent ?? "")).toEqual({
      mode: "plan",
      model: { id: "text", providerId: "provider" },
      reasoningEffort: "high",
      execution: { none: {} },
    });
    expect(onConfigurationChange).toHaveBeenCalledTimes(4);
    await user.click(options);
    await user.click(screen.getByRole("button", { name: "Close composer options" }));
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Composer options" }));
  });
});
