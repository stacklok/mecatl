// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatComposer, type DraftChatConfiguration } from "./chat-composer";

const initialConfiguration: DraftChatConfiguration = {
  mode: "default",
  reasoningEffort: "default",
  toolAccess: "all",
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
          { id: "image", image: true, label: "Image model", providerId: "provider" },
          { id: "text", image: false, label: "Text model", providerId: "provider" },
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

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("composer configuration selectors", () => {
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
    await pick(/^Tools/, "No filesystem No file or shell tools; other tools stay available.");

    expect(JSON.parse(screen.getByTestId("draft-configuration").textContent ?? "")).toEqual({
      mode: "plan",
      model: { id: "text", providerId: "provider" },
      reasoningEffort: "high",
      toolAccess: "noFilesystem",
    });
    expect(onConfigurationChange).toHaveBeenCalledTimes(4);
    await user.click(options);
    await user.click(screen.getByRole("button", { name: "Close composer options" }));
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Composer options" }));
  });
});
