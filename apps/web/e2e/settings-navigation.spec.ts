// SPDX-License-Identifier: Apache-2.0

import { expect, type OfflineBff, test } from "./fixtures";

/**
 * Settings navigation at real widths, with the app's own CSS and media
 * queries: Settings opens on You beside the section nav on a desktop, and
 * every section stays reachable on a phone with its name announced. The phone
 * check opens a section with whichever control the page offers, so it holds
 * for the native picker and for the section index that replaces it (#2261).
 */

// Studio's slugs in route order, with the label a person sees.
const SECTIONS = [
  ["profile", "You"],
  ["appearance", "Personalise"],
  ["agent", "Agent"],
  ["permissions", "Permissions"],
  ["providers", "Providers"],
  ["models", "Models"],
  ["mcp-tools", "MCP tools"],
  ["storage", "Storage"],
  ["memory", "Memory"],
  ["learning", "Learning"],
  ["diagnostics", "Diagnostics"],
  ["labs", "Labs"],
  ["about", "About"],
] as const;

function stub(bff: OfflineBff) {
  bff.json("GET", "/api/v1/auth/session", {
    account: "opaque-a",
    mode: "oidc",
    status: "authenticated",
  });
  bff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  bff.json("GET", "/api/v1/runtime", {
    apiMajor: 1,
    capabilities: { mcp: true, memory: true, posture: "auto", steer: true, storageHealth: true },
    connection: "online",
    features: [],
    mock: false,
    sdkVersion: "0.9.2",
    source: "external",
    studioBuildId: "v0.9.3",
  });
  bff.json("GET", "/api/v1/settings/runtime", {
    buildId: "v0.9.1",
    management: {
      providerConfiguration: false,
      providerConfigurationReason: "Providers are managed by the deployment.",
      routingConfiguration: false,
      routingConfigurationReason: "Routing is managed by the deployment.",
    },
    models: [],
    modelsReason: "",
    modelsSupported: true,
    providerEndpoint: "",
    providers: [],
    serverImplementation: "mecated",
  });
  bff.json("GET", "/api/v1/storage/health", { supported: false });
  bff.json("GET", "/api/v1/sessions", { complete: true, items: [] });
  bff.json("GET", "/api/v1/user-memory", {
    items: [],
    reason: "",
    sha256: "x",
    sizeBytes: "0",
    supported: true,
  });
}

test("Settings opens on You beside the section nav on a desktop", async ({
  offlineBff,
  page,
}, testInfo) => {
  test.skip(testInfo.project.name !== "chromium-desktop", "the section nav shows from 500px");
  await page.addInitScript(() => localStorage.setItem("studio.account", "opaque-a"));
  stub(offlineBff);

  await page.goto("/workspace/settings");
  await expect(page).toHaveURL(/\/workspace\/settings\/profile$/u);
  const nav = page.getByRole("navigation", { name: "Settings sections" });
  await expect(nav).toBeVisible();
  await expect(page.getByRole("heading", { level: 1 })).toHaveText("Settings");
  for (const [slug, label] of SECTIONS) {
    await nav.getByRole("button", { name: label, exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/workspace/settings/${slug}$`, "u"));
    await expect(nav.locator('[aria-current="page"]')).toHaveText(label);
  }

  // The top nav's Settings entry lands on You again.
  await page
    .getByRole("navigation", { name: "Main navigation" })
    .getByRole("link", { name: "Settings" })
    .click();
  await expect(page).toHaveURL(/\/workspace\/settings\/profile$/u);
  await expect(nav.locator('[aria-current="page"]')).toHaveText("You");
});

test("every section is reachable on a phone from Settings, and its page names it", async ({
  offlineBff,
  page,
}, testInfo) => {
  test.skip(testInfo.project.name !== "chromium-mobile", "phone navigation shows below 500px");
  await page.addInitScript(() => localStorage.setItem("studio.account", "opaque-a"));
  stub(offlineBff);
  await page.goto("/workspace/chat");

  const main = page.getByRole("main");
  const picker = main.getByRole("combobox", { name: "Settings section", exact: true });
  for (const [slug, label] of SECTIONS) {
    await page
      .getByRole("navigation", { name: "Main navigation" })
      .getByRole("link", { name: "Settings" })
      .click();
    const link = main.getByRole("link", { name: label, exact: true });
    await expect(picker.or(link).first()).toBeVisible();
    if (await picker.isVisible()) await picker.selectOption({ label });
    else await link.click();
    await expect(page).toHaveURL(new RegExp(`/workspace/settings/${slug}$`, "u"));
    // The page names the section: as the picker's value, or as its heading.
    if (await picker.isVisible()) await expect(picker).toHaveValue(slug);
    else await expect(main.getByRole("heading", { level: 1 })).toHaveText(label);
  }
});
