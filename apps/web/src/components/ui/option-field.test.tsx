// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { getRuntimeOptions } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { InterfaceSettings } from "../../features/settings/interface-settings";
import { appearanceStore, useTheme } from "../../lib/theme";
import { OptionField } from "./option-field";

const choices = [
  { value: "light", label: "Light", description: "Always light." },
  { value: "dark", label: "Dark", description: "Always dark." },
  { value: "system", label: "System", description: "Follow this device." },
];

function ThemePicker() {
  const theme = useTheme();
  return (
    <OptionField
      label="Theme"
      onChange={(next) => theme.setTheme(next as "light" | "dark" | "system")}
      options={choices}
      value={theme.theme}
    />
  );
}

function renderSettings(steer?: boolean) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  queryClient.setQueryData(getRuntimeOptions().queryKey, {
    capabilities: { image: false, posture: "managed", ...(steer === undefined ? {} : { steer }) },
    connection: "online",
  } as never);
  return render(
    <QueryClientProvider client={queryClient}>
      <InterfaceSettings />
    </QueryClientProvider>,
  );
}

function setWidth(width: number) {
  Object.defineProperty(window, "innerWidth", { configurable: true, value: width });
  fireEvent(window, new Event("resize"));
}

beforeEach(() => {
  window.matchMedia = (query: string) =>
    ({
      matches: query.includes("max-width") && window.innerWidth < 500,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => true,
    }) as MediaQueryList;
  window.HTMLElement.prototype.scrollIntoView = () => {};
  window.HTMLElement.prototype.hasPointerCapture = () => false;
  window.HTMLElement.prototype.setPointerCapture = () => {};
  window.HTMLElement.prototype.releasePointerCapture = () => {};
  appearanceStore.setTheme("light");
  appearanceStore.setPalette("default");
});

afterEach(() => {
  cleanup();
  appearanceStore.setTheme("system");
  appearanceStore.setPalette("default");
});

describe("OptionField", () => {
  it("supports keyboard selection and focus restoration", async () => {
    setWidth(500);
    render(<ThemePicker />);
    const trigger = screen.getByRole("button", { name: "Theme: Light" });
    trigger.focus();
    fireEvent.keyDown(trigger, { key: "ArrowDown" });
    const dark = await screen.findByRole("menuitemradio", { name: /Dark/ });
    expect(screen.getByRole("menuitemradio", { name: /Light/ }).getAttribute("aria-checked")).toBe(
      "true",
    );
    fireEvent.keyDown(dark, { key: "End" });
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("menuitemradio", { name: /System/ })),
    );
    fireEvent.keyDown(screen.getByRole("menuitemradio", { name: /System/ }), { key: "Home" });
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("menuitemradio", { name: /Light/ })),
    );
    fireEvent.keyDown(screen.getByRole("menuitemradio", { name: /Light/ }), { key: "ArrowDown" });
    await waitFor(() => expect(document.activeElement).toBe(dark));
    fireEvent.keyDown(dark, { key: "Enter" });
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("button", { name: "Theme: Dark" })),
    );
    expect(appearanceStore.getSnapshot().theme).toBe("dark");
    const selectedTrigger = screen.getByRole("button", { name: "Theme: Dark" });
    fireEvent.keyDown(selectedTrigger, { key: "ArrowDown" });
    fireEvent.keyDown(await screen.findByRole("menu"), { key: "Escape" });
    await waitFor(() => expect(document.activeElement).toBe(selectedTrigger));
  });

  it("switches the option surface at the 500px pivot", async () => {
    setWidth(499);
    render(<ThemePicker />);
    const trigger = screen.getByRole("button", { name: "Theme: Light" });
    fireEvent.click(trigger);
    expect(await screen.findByRole("dialog", { name: "Theme" })).toBeTruthy();
    expect(screen.getByRole("radio", { name: /Dark/ })).toBeTruthy();
    expect((screen.getByRole("radio", { name: /Light/ }) as HTMLInputElement).checked).toBe(true);
    expect(screen.queryByRole("menu")).toBeNull();

    setWidth(500);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.queryByRole("menu")).toBeNull();
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("button", { name: "Theme: Light" })),
    );
    fireEvent.keyDown(screen.getByRole("button", { name: "Theme: Light" }), { key: "ArrowDown" });
    expect(await screen.findByRole("menu")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();

    setWidth(499);
    await waitFor(() => expect(screen.queryByRole("menu")).toBeNull());
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole("button", { name: "Theme: Light" })),
    );
    fireEvent.click(screen.getByRole("button", { name: "Theme: Light" }));
    fireEvent.click(await screen.findByRole("radio", { name: /Dark/ }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(appearanceStore.getSnapshot().theme).toBe("dark");
    const selectedTrigger = screen.getByRole("button", { name: "Theme: Dark" });
    expect(document.activeElement).toBe(selectedTrigger);
    fireEvent.click(selectedTrigger);
    fireEvent.keyDown(await screen.findByRole("dialog", { name: "Theme" }), { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.activeElement).toBe(selectedTrigger);
  });

  it("keeps theme and palette selection independent in Appearance", async () => {
    setWidth(500);
    renderSettings();
    fireEvent.keyDown(screen.getByRole("button", { name: "Theme: Light" }), { key: "ArrowDown" });
    fireEvent.click(await screen.findByRole("menuitemradio", { name: /Dark/ }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Theme: Dark" })).toBeTruthy());
    fireEvent.keyDown(screen.getByRole("button", { name: "Palette: Default" }), {
      key: "ArrowDown",
    });
    const solar = await screen.findByRole("menuitemradio", { name: /Solar/ });
    expect(solar.querySelector('[aria-hidden="true"]')).toBeTruthy();
    fireEvent.click(solar);
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Palette: Solar" })).toBeTruthy(),
    );
    expect(screen.getByRole("button", { name: "Theme: Dark" })).toBeTruthy();
    expect(appearanceStore.getSnapshot()).toMatchObject({ theme: "dark", palette: "solar" });
    expect(document.documentElement.dataset.palette).toBe("solar");
  });
  it("offers Steer agent unless the daemon cannot steer", async () => {
    setWidth(500);
    renderSettings(true);
    fireEvent.keyDown(screen.getByRole("button", { name: /Messages while the agent works/ }), {
      key: "ArrowDown",
    });
    expect(await screen.findByRole("menuitemradio", { name: /Steer agent/ })).toBeTruthy();
  });

  it("hides Steer agent and shows Queue for a stored steer, leaving storage alone", async () => {
    setWidth(500);
    window.localStorage.setItem("studio.chat.composer.enterToSend", "steer");
    renderSettings(false);
    const trigger = screen.getByRole("button", { name: /Messages while the agent works/ });
    expect(trigger.textContent).toContain("Queue message");
    fireEvent.keyDown(trigger, { key: "ArrowDown" });
    expect(await screen.findByRole("menuitemradio", { name: /Queue message/ })).toBeTruthy();
    expect(screen.queryByRole("menuitemradio", { name: /Steer agent/ })).toBeNull();
    expect(window.localStorage.getItem("studio.chat.composer.enterToSend")).toBe("steer");
  });
});
