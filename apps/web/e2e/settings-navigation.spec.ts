// SPDX-License-Identifier: Apache-2.0

import { expect, type OfflineBff, test } from "./fixtures";

/**
 * Settings navigation at real widths, with the app's own CSS and media
 * queries: Settings opens on You beside the section nav on a desktop, and
 * every section stays reachable on a phone with its name announced. The phone
 * check opens a section with whichever control the page offers, so it holds
 * for the native picker and for the section index that replaces it (#2261).
 * The index's groups, the back bar, and the switch at the pivot follow.
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

// The prototype's groups for the phone index, with Studio's sections in route order.
const GROUPS = [
  ["Preferences", ["You", "Personalise"]],
  [
    "Agent runtime",
    [
      "Agent",
      "Permissions",
      "Providers",
      "Models",
      "MCP tools",
      "Storage",
      "Memory",
      "Learning",
      "Diagnostics",
    ],
  ],
  ["Experimental", ["Labs"]],
  ["Support", ["About"]],
] as const;

test("a phone opens Settings on the section index, in the prototype's groups", async ({
  offlineBff,
  page,
}, testInfo) => {
  test.skip(testInfo.project.name !== "chromium-mobile", "the index shows below 500px");
  await page.addInitScript(() => localStorage.setItem("studio.account", "opaque-a"));
  stub(offlineBff);

  await page.goto("/workspace/settings");
  await expect(page).toHaveURL(/\/workspace\/settings$/u);
  const main = page.getByRole("main");
  await expect(main.getByRole("heading", { level: 1 })).toHaveText("Settings");
  const index = main.getByRole("navigation", { name: "Settings sections" });
  await expect(index).toBeVisible();
  for (const [group, labels] of GROUPS) {
    const rows = index.getByRole("list", { name: group }).getByRole("link");
    await expect(rows).toHaveText([...labels]);
    for (const row of await rows.all()) {
      const box = await row.boundingBox();
      expect(box?.height ?? 0, `${group}: row height`).toBeGreaterThanOrEqual(44);
    }
  }
  await expect(page.locator("select")).toHaveCount(0);
});

test("a phone section page's back bar names the section and returns to the index", async ({
  offlineBff,
  page,
}, testInfo) => {
  test.skip(testInfo.project.name !== "chromium-mobile", "the back bar shows below 500px");
  await page.addInitScript(() => localStorage.setItem("studio.account", "opaque-a"));
  stub(offlineBff);

  await page.goto("/workspace/settings/agent");
  const main = page.getByRole("main");
  await expect(main.getByRole("heading", { level: 1 })).toHaveText("Agent");
  await expect(main.getByRole("navigation", { name: "Settings sections" })).toBeHidden();
  await expect(page.locator("select")).toHaveCount(0);
  const back = main.getByRole("link", { name: "Back to Settings", exact: true });
  const box = await back.boundingBox();
  expect(box?.width ?? 0).toBeGreaterThanOrEqual(44);
  expect(box?.height ?? 0).toBeGreaterThanOrEqual(44);

  await back.click();
  await expect(page).toHaveURL(/\/workspace\/settings$/u);
  await expect(main.getByRole("heading", { level: 1 })).toHaveText("Settings");
  await expect(main.getByRole("link", { name: "Agent", exact: true })).toBeVisible();
});

test("crossing the 500px pivot swaps the phone index and back bar for the section nav", async ({
  offlineBff,
  page,
}, testInfo) => {
  test.skip(testInfo.project.name !== "chromium-desktop", "sets its own viewport widths");
  await page.addInitScript(() => localStorage.setItem("studio.account", "opaque-a"));
  stub(offlineBff);
  const main = page.getByRole("main");
  const nav = main.getByRole("navigation", { name: "Settings sections" });
  const back = main.getByRole("link", { name: "Back to Settings", exact: true });

  await page.setViewportSize({ width: 499, height: 800 });
  await page.goto("/workspace/settings");
  await expect(page).toHaveURL(/\/workspace\/settings$/u);
  await expect(nav.getByRole("link", { name: "You", exact: true })).toBeVisible();

  // Widened past the pivot, the index gives way to You beside the section nav.
  await page.setViewportSize({ width: 500, height: 800 });
  await expect(page).toHaveURL(/\/workspace\/settings\/profile$/u);
  await expect(nav.getByRole("button", { name: "You", exact: true })).toHaveAttribute(
    "aria-current",
    "page",
  );
  await expect(back).toHaveCount(0);
  await expect(main.getByRole("heading", { level: 1 })).toHaveText("Settings");

  // Narrowed again, the same section page takes the back bar.
  await page.setViewportSize({ width: 499, height: 800 });
  await expect(back).toBeVisible();
  await expect(nav).toBeHidden();
  await expect(main.getByRole("heading", { level: 1 })).toHaveText("You");
});
