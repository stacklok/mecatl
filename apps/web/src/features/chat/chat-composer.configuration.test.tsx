// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen, within } from "@testing-library/react";
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

    await user.click(screen.getByRole("button", { name: "Chat options" }));
    const sheet = screen.getByRole("dialog", { name: "Chat options" });
    await user.selectOptions(within(sheet).getByLabelText("Model"), '["provider","text"]');
    await user.selectOptions(within(sheet).getByLabelText("Effort"), "high");
    await user.selectOptions(within(sheet).getByLabelText("Mode"), "plan");
    await user.selectOptions(within(sheet).getByLabelText("Tools"), "noFilesystem");

    expect(JSON.parse(screen.getByTestId("draft-configuration").textContent ?? "")).toEqual({
      mode: "plan",
      model: { id: "text", providerId: "provider" },
      reasoningEffort: "high",
      toolAccess: "noFilesystem",
    });
    expect(onConfigurationChange).toHaveBeenCalledTimes(4);
    await user.click(within(sheet).getByRole("button", { name: "Close chat options" }));
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Chat options" }));
  });
});
