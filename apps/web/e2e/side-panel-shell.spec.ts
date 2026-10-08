// SPDX-License-Identifier: Apache-2.0

import { expect, test } from "./fixtures";

test("canvas panel stays in the viewport at mobile and desktop widths", async ({
  offlineBff,
  page,
}, testInfo) => {
  offlineBff.json("GET", "/api/v1/auth/session", {
    account: "opaque-a",
    mode: "oidc",
    status: "authenticated",
  });
  offlineBff.json("GET", "/api/v1/status", {
    connection: "reachable",
    signInRequired: false,
  });
  offlineBff.json("GET", "/api/v1/runtime", {
    capabilities: { image: false, posture: "managed" },
    connection: "online",
  });
  offlineBff.json("GET", "/api/v1/settings/runtime", {
    models: [],
    modelsSupported: false,
  });
  offlineBff.json("GET", "/api/v1/sessions", { complete: true, items: [] });

  await page.goto("/workspace/chat");
  const trigger = page.getByRole("button", { name: "Open local canvas" });
  await expect(trigger).toBeVisible();
  await trigger.click();
  const panel = page.getByRole("complementary", { name: "Local canvas" });
  await expect(panel).toBeVisible();
  await expect(page.getByRole("button", { name: "Close panel" })).toBeFocused();

  for (const width of [320, 500, 1280]) {
    await page.setViewportSize({ width, height: 800 });
    const bounds = await panel.boundingBox();
    expect(bounds).not.toBeNull();
    expect(bounds?.x ?? -1).toBeGreaterThanOrEqual(0);
    expect((bounds?.x ?? 0) + (bounds?.width ?? 0)).toBeLessThanOrEqual(width + 1);
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(
      width,
    );
    if (width < 760) expect(bounds?.width).toBeGreaterThanOrEqual(width - 2);
  }

  const resize = page.getByRole("button", { name: "Resize panel" });
  await resize.focus();
  await resize.press("ArrowLeft");
  await expect(panel).toHaveCSS("width", "268px");
  await resize.press("ArrowRight");
  await expect(panel).toHaveCSS("width", "256px");
  const handle = await resize.boundingBox();
  expect(handle).not.toBeNull();
  const handleX = (handle?.x ?? 0) + (handle?.width ?? 0) / 2;
  const handleY = (handle?.y ?? 0) + (handle?.height ?? 0) / 2;
  await page.mouse.move(handleX, handleY);
  await page.mouse.down();
  await page.mouse.move(handleX - 40, handleY);
  await page.mouse.up();
  await expect(panel).toHaveCSS("width", "296px");

  await page.setViewportSize({ width: 320, height: 800 });
  const backdrop = page.getByRole("button", { name: "Dismiss panel backdrop" });
  if (testInfo.project.name === "chromium-mobile") {
    await backdrop.tap({ position: { x: 10, y: 10 } });
  } else {
    await backdrop.click({ position: { x: 10, y: 10 } });
  }
  await expect(panel).toHaveCount(0);
  await expect(trigger).toBeFocused();
  await trigger.click();
  await expect(panel).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(panel).toHaveCount(0);
});
