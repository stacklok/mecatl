// SPDX-License-Identifier: Apache-2.0

import type { Locator, Page, TestInfo } from "@playwright/test";
import { expect, type OfflineBff, test } from "./fixtures";

// The composer area's pressed controls are Toggles: the dictation buttons, the
// phone sheet's Mode and Model rows, and its Effort pills. These journeys pin
// what a person sees of them in a real browser: the glyph size the phone rule
// in styles.css draws for a Toggle (20px on phones, as for a Button, and 16px
// on desktop), and how each control's fill and ink relate across rest, hover,
// pressed and pressed-hover, in light and in dark.

const PHONE = 20;
const DESKTOP = 16;

function serve(bff: OfflineBff) {
  bff.json("GET", "/api/v1/auth/session", {
    account: "composer-toggles",
    mode: "oidc",
    status: "authenticated",
  });
  bff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  bff.json("GET", "/api/v1/runtime", {
    capabilities: { image: false, modelSelection: true, posture: "managed" },
    connection: "online",
    features: [],
  });
  bff.json("GET", "/api/v1/settings/runtime", {
    buildId: "test",
    management: {
      providerConfiguration: false,
      providerConfigurationReason: "",
      routingConfiguration: false,
      routingConfigurationReason: "",
    },
    models: ["Deep model", "Plain model"].map((displayName) => ({
      contextLimit: "200000",
      displayName,
      id: displayName.split(" ")[0]?.toLowerCase(),
      image: false,
      providerDisplayName: "Provider",
      providerId: "provider",
      reasoning: true,
    })),
    modelsReason: "",
    modelsSupported: true,
    providerEndpoint: "",
    providers: [],
    serverImplementation: "test",
  });
  bff.json("GET", "/api/v1/sessions", { complete: true, items: [] });
}

/** A browser that can dictate, so the dictation Toggle presses. */
async function fakeDictation(page: Page, theme: "light" | "dark" = "light") {
  await page.emulateMedia({ colorScheme: theme });
  await page.addInitScript((value) => {
    localStorage.setItem("mecatl-studio-theme", value);
    class FakeRecognition {
      onresult = null;
      onerror = null;
      onend: (() => void) | null = null;
      continuous = false;
      interimResults = false;
      lang = "";
      start() {}
      stop() {
        this.onend?.();
      }
    }
    Object.defineProperty(window, "SpeechRecognition", {
      configurable: true,
      value: FakeRecognition,
    });
  }, theme);
}

async function glyphSize(control: Locator) {
  await expect(control).toBeVisible();
  return control.locator("svg.lucide").evaluate((svg) => {
    const box = svg.getBoundingClientRect();
    return { height: Math.round(box.height), width: Math.round(box.width) };
  });
}

function expected(testInfo: TestInfo) {
  const size = testInfo.project.name === "chromium-mobile" ? PHONE : DESKTOP;
  return { height: size, width: size };
}

interface Look {
  background: string;
  border: string;
  color: string;
  radius: number;
}

async function look(control: Locator): Promise<Look> {
  return control.evaluate((element) => {
    const style = getComputedStyle(element);
    return {
      background: style.backgroundColor,
      border: style.borderTopColor,
      color: style.color,
      radius: Number.parseFloat(style.borderTopLeftRadius),
    };
  });
}

/** The control's look at rest and under the pointer. */
async function lookAndHover(page: Page, control: Locator) {
  await page.mouse.move(0, 0);
  const rest = await look(control);
  await control.hover();
  const hover = await look(control);
  await page.mouse.move(0, 0);
  return { hover, rest };
}

const CLEAR = "rgba(0, 0, 0, 0)";

test("a Toggle's glyph size at phone and desktop widths", async ({
  offlineBff,
  page,
}, testInfo) => {
  serve(offlineBff);
  await fakeDictation(page);
  await page.goto("/workspace/chat");
  const dictation = page.getByRole("button", { name: "Start dictation" });
  expect(await glyphSize(dictation)).toEqual(expected(testInfo));
  await dictation.click();
  expect(await glyphSize(page.getByRole("button", { name: "Stop dictation" }))).toEqual(
    expected(testInfo),
  );

  // Any Toggle's bare glyph follows the same rule: the canvas's view switch.
  await page.getByRole("button", { name: "Open local canvas" }).click();
  for (const name of ["Preview", "Edit"]) {
    expect(await glyphSize(page.getByRole("button", { name }))).toEqual(expected(testInfo));
  }
});

for (const theme of ["light", "dark"] as const) {
  test(`the composer's Toggles keep their looks in ${theme}`, async ({
    offlineBff,
    page,
  }, testInfo) => {
    // Hover styles need a pointer that hovers, so the phone sheet is measured
    // at phone width in the desktop project.
    test.skip(testInfo.project.name === "chromium-mobile", "hover needs a hovering pointer");
    serve(offlineBff);
    await fakeDictation(page, theme);
    await page.goto("/workspace/chat");
    // Measure end states, not a colour transition's first frame.
    await page.addStyleTag({ content: "*, *::before, *::after { transition: none !important; }" });

    // Dictation: ghost icon button ink, brand tint while listening.
    const start = page.getByRole("button", { name: "Start dictation" });
    const off = await lookAndHover(page, start);
    await start.click();
    const on = await lookAndHover(page, page.getByRole("button", { name: "Stop dictation" }));
    expect(off.rest.radius).toBeGreaterThan(1000);
    expect(off.rest.background).toBe(CLEAR);
    expect(off.hover.background).not.toBe(CLEAR);
    expect(off.hover.color).not.toBe(off.rest.color);
    expect(on.rest.background).not.toBe(CLEAR);
    expect(on.rest.color).not.toBe(off.rest.color);
    expect(on.hover.background).not.toBe(on.rest.background);
    expect(on.hover.color).toBe(off.hover.color);
    await page.getByRole("button", { name: "Stop dictation" }).click();

    await page.setViewportSize({ height: 844, width: 390 });
    const options = page.getByRole("button", { name: "Composer options" });
    const sheet = page.getByRole("dialog", { name: "Composer options" });

    // Mode rows: no fill while chosen; the hover fill is the same either way.
    await options.click();
    await sheet.getByRole("button", { name: /^Mode\s/ }).click();
    const manual = await lookAndHover(page, sheet.getByRole("button", { name: /^Manual/ }));
    const plan = await lookAndHover(page, sheet.getByRole("button", { name: /^Plan/ }));
    await expect(sheet.getByRole("button", { name: /^Manual/ })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(manual.rest).toEqual(plan.rest);
    expect(manual.hover).toEqual(plan.hover);
    expect(manual.rest.radius).toBe(0);
    expect(manual.rest.background).toBe(CLEAR);
    expect(manual.hover.background).not.toBe(CLEAR);
    expect(manual.hover.color).toBe(manual.rest.color);
    await page.keyboard.press("Escape");

    // Model rows: a zinc fill while chosen. Hovering the chosen row shows the
    // hover fill in light and keeps the zinc fill in dark.
    await options.click();
    await sheet.getByRole("button", { name: /^Model/ }).click();
    const chosen = sheet.getByRole("button", { exact: true, name: "Default" });
    await expect(chosen).toHaveAttribute("aria-pressed", "true");
    const model = await lookAndHover(page, chosen);
    const other = await lookAndHover(page, sheet.getByRole("button", { name: "Deep model" }));
    expect(model.rest.radius).toBe(0);
    expect(other.rest.radius).toBe(0);
    expect(other.rest.background).toBe(CLEAR);
    expect(other.hover.background).not.toBe(CLEAR);
    expect(other.hover.color).toBe(other.rest.color);
    expect(model.rest.background).not.toBe(CLEAR);
    expect(model.rest.background).not.toBe(other.hover.background);
    expect(model.rest.color).toBe(other.rest.color);
    expect(model.hover.color).toBe(model.rest.color);
    expect(model.hover.background).toBe(
      theme === "dark" ? model.rest.background : other.hover.background,
    );

    // Effort pills: no hover fill; a brand border, fill and ink while chosen.
    const effort = sheet.getByRole("group", { name: "Effort" });
    const auto = effort.getByRole("button", { name: "Auto" });
    await expect(auto).toHaveAttribute("aria-pressed", "true");
    const pressed = await lookAndHover(page, auto);
    const pill = await lookAndHover(page, effort.getByRole("button", { name: "Low" }));
    expect(pressed.rest.radius).toBeGreaterThan(1000);
    expect(pill.rest.radius).toBeGreaterThan(1000);
    expect(pill.rest.background).toBe(CLEAR);
    expect(pill.hover).toEqual(pill.rest);
    expect(pressed.hover).toEqual(pressed.rest);
    expect(pressed.rest.background).not.toBe(CLEAR);
    expect(pressed.rest.border).not.toBe(pill.rest.border);
    expect(pressed.rest.color).not.toBe(pill.rest.color);
    expect(pressed.rest.color).toBe(on.rest.color);
  });
}
