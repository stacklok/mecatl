// SPDX-License-Identifier: Apache-2.0

import { expect, type OfflineBff, test } from "./fixtures";

// Between the 500px pivot and a tablet, the section nav takes 176px beside the
// content, so a fixed-width control or a two-column grid has little room.
const WIDTHS = [500, 568, 640] as const;

const SECTIONS = [
  "profile",
  "appearance",
  "agent",
  "permissions",
  "providers",
  "models",
  "mcp-tools",
  "storage",
  "memory",
  "learning",
  "diagnostics",
  "labs",
  "about",
] as const;

/**
 * Narrowest model card that keeps the name and its facts legible. Two columns
 * beside the nav gave 87–157px cards here, which split names and labels.
 */
const MIN_MODEL_CARD_WIDTH = 180;

function stub(bff: OfflineBff) {
  bff.json("GET", "/api/v1/auth/session", {
    account: "opaque-a",
    mode: "oidc",
    status: "authenticated",
  });
  bff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  bff.json("GET", "/api/v1/runtime", {
    apiMajor: 1,
    capabilities: {
      image: true,
      mcp: true,
      memory: true,
      modelSelection: true,
      posture: "auto",
      skills: true,
      steer: true,
      storageHealth: true,
      userModel: true,
    },
    connection: "online",
    features: [],
    mock: false,
    sdkVersion: "0.9.2",
    source: "external",
    studioBuildId: "v0.9.3",
  });
  bff.json("GET", "/api/v1/settings/runtime", {
    buildId: "v0.9.1-3-gabc",
    management: {
      providerConfiguration: false,
      providerConfigurationReason: "Providers are managed by the deployment.",
      routingConfiguration: false,
      routingConfigurationReason: "Routing is managed by the deployment.",
    },
    models: [
      {
        contextLimit: "200000",
        displayName: "Claude Sonnet",
        id: "claude-sonnet",
        image: true,
        providerId: "anthropic",
        reasoning: true,
      },
      {
        contextLimit: "128000",
        displayName: "GPT Mini",
        id: "gpt-mini",
        image: false,
        providerId: "openai",
        reasoning: false,
      },
    ],
    modelsReason: "",
    modelsSupported: true,
    providerEndpoint: "",
    providers: [
      {
        availableNotDefault: false,
        defaultModelAutoSelected: true,
        hint: "",
        id: "anthropic",
        modelCount: 1,
        state: "available",
      },
    ],
    serverImplementation: "mecak8s",
  });
  bff.json("GET", "/api/v1/storage/health", {
    activeJob: false,
    available: true,
    childCount: "4",
    corruptCount: "0",
    currentBytes: "5242880",
    lastFailure: false,
    mainCount: "12",
    reclaimableBytes: "1048576",
    scheduledCount: "2",
    sessionCount: "18",
    supported: true,
    unknownCount: "0",
  });
  bff.json("GET", "/api/v1/sessions", { complete: true, items: [] });
  bff.json("GET", "/api/v1/user-memory", {
    items: [{ description: "Prefers concise answers.", key: "style/answers" }],
    reason: "",
    sha256: "x",
    sizeBytes: "42",
    supported: true,
  });
}

test("settings sections fit between the 500px pivot and a tablet", async ({
  offlineBff,
  page,
}, testInfo) => {
  test.skip(testInfo.project.name !== "chromium-desktop", "sets its own viewport widths");
  await page.addInitScript(() => localStorage.setItem("studio.account", "opaque-a"));
  stub(offlineBff);

  for (const width of WIDTHS) {
    await page.setViewportSize({ width, height: 800 });
    for (const section of SECTIONS) {
      await page.goto(`/workspace/settings/${section}`);
      await expect(page.getByRole("navigation", { name: "Settings sections" })).toBeVisible();
      await expect(page.locator("main section").first()).toBeVisible();
      await page.waitForLoadState("networkidle");
      // Wait out the runtime reads so the facts, not a loading line, are measured.
      await expect(page.getByText(/^Loading\b.*…$/u)).toHaveCount(0);

      const overflow = await page.evaluate(() => {
        const scrollers = [
          document.documentElement,
          ...document.querySelectorAll<HTMLElement>("main, main .overflow-y-auto"),
        ];
        const scroll = scrollers
          .map((element) => element.scrollWidth - element.clientWidth)
          .filter((excess) => excess > 0);
        const spill: string[] = [];
        for (const card of document.querySelectorAll<HTMLElement>("main section")) {
          const right = card.getBoundingClientRect().right;
          for (const element of card.querySelectorAll<HTMLElement>("*")) {
            const box = element.getBoundingClientRect();
            if (box.width > 0 && box.right > right + 1) {
              spill.push(`${element.tagName.toLowerCase()} +${Math.round(box.right - right)}px`);
            }
          }
        }
        // Fact prose wraps between words; only IDs and paths (break-all) may split.
        const broken: string[] = [];
        for (const fact of document.querySelectorAll<HTMLElement>("main dd.break-words")) {
          fact.style.overflowWrap = "normal";
          if (fact.scrollWidth > fact.clientWidth + 1) broken.push(fact.textContent ?? "");
          fact.style.overflowWrap = "";
        }
        return { broken, scroll, spill };
      });
      expect
        .soft(overflow, `${section} at ${width}px`)
        .toEqual({ broken: [], scroll: [], spill: [] });
    }

    await page.goto("/workspace/settings/models");
    const cards = page.locator("main article");
    await expect(cards).toHaveCount(2);
    for (const card of await cards.all()) {
      const box = await card.boundingBox();
      expect
        .soft(box?.width ?? 0, `model card at ${width}px`)
        .toBeGreaterThanOrEqual(MIN_MODEL_CARD_WIDTH);
      // The name and every fact label stay on one line.
      const wrapped = await card.evaluate((article) =>
        [...article.querySelectorAll("h3, dt")]
          .filter((label) => {
            const range = document.createRange();
            range.selectNodeContents(label);
            const tops = new Set([...range.getClientRects()].map((rect) => Math.round(rect.top)));
            return tops.size > 1;
          })
          .map((label) => label.textContent),
      );
      expect.soft(wrapped, `model card labels at ${width}px`).toEqual([]);
    }
    await page.screenshot({
      fullPage: true,
      path: testInfo.outputPath(`settings-models-${width}.png`),
    });
  }
});

test("the settings nav keeps the current section on screen at 1280x800", async ({
  offlineBff,
  page,
}, testInfo) => {
  test.skip(testInfo.project.name !== "chromium-desktop", "sets its own viewport size");
  await page.addInitScript(() => localStorage.setItem("studio.account", "opaque-a"));
  stub(offlineBff);
  await page.setViewportSize({ width: 1280, height: 800 });

  for (const section of SECTIONS) {
    await page.goto(`/workspace/settings/${section}`);
    const nav = page.getByRole("navigation", { name: "Settings sections" });
    const current = nav.locator('button[aria-current="page"]');
    await expect(current).toHaveCount(1);
    await page.waitForLoadState("networkidle");
    const visible = await current.evaluate((button) => {
      const box = button.getBoundingClientRect();
      const frame = button.closest("main")?.getBoundingClientRect();
      // Every clipping ancestor, the page scroller and the nav included.
      let clip = frame ?? new DOMRect(0, 0, innerWidth, innerHeight);
      for (let node = button.parentElement; node; node = node.parentElement) {
        if (getComputedStyle(node).overflowY !== "visible") {
          const rect = node.getBoundingClientRect();
          clip = new DOMRect(
            Math.max(clip.left, rect.left),
            Math.max(clip.top, rect.top),
            Math.min(clip.right, rect.right) - Math.max(clip.left, rect.left),
            Math.min(clip.bottom, rect.bottom) - Math.max(clip.top, rect.top),
          );
        }
      }
      return box.top >= clip.top - 1 && box.bottom <= clip.bottom + 1;
    });
    expect.soft(visible, `${section}: current nav item on screen`).toBe(true);
    const heights = await nav
      .locator("button")
      .evaluateAll((buttons) => buttons.map((button) => button.getBoundingClientRect().height));
    expect.soft(Math.min(...heights), `${section}: nav targets`).toBeGreaterThanOrEqual(44);
  }
});

/** WCAG relative-luminance contrast between two computed `rgb()` colors. */
function contrast(first: string, second: string): number {
  const luminance = (color: string) => {
    const [r, g, b] = (color.match(/[\d.]+/gu) ?? []).slice(0, 3).map((channel) => {
      const value = Number(channel) / 255;
      return value <= 0.03928 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * (r ?? 0) + 0.7152 * (g ?? 0) + 0.0722 * (b ?? 0);
  };
  const [light, dark] = [luminance(first), luminance(second)].sort((a, b) => b - a);
  return ((light ?? 0) + 0.05) / ((dark ?? 0) + 0.05);
}

for (const theme of ["light", "dark"] as const) {
  test(`the phone section picker reads as a control in ${theme} mode`, async ({
    offlineBff,
    page,
  }, testInfo) => {
    test.skip(testInfo.project.name !== "chromium-mobile", "the picker only shows on a phone");
    await page.addInitScript((value) => {
      localStorage.setItem("mecatl-studio-theme", value);
      localStorage.setItem("studio.account", "opaque-a");
    }, theme);
    stub(offlineBff);
    await page.goto("/workspace/settings/profile");
    const picker = page.getByRole("combobox", { name: "Settings section", exact: true });
    await expect(picker).toBeVisible();
    const colors = await picker.evaluate((select) => {
      const style = getComputedStyle(select);
      const page = getComputedStyle(select.closest("main") ?? document.body).backgroundColor;
      return {
        border: Number.parseFloat(style.borderTopWidth) >= 1 ? style.borderTopColor : page,
        page,
      };
    });
    // WCAG 1.4.11: the control's boundary needs 3:1 against what surrounds it.
    expect(contrast(colors.border, colors.page), JSON.stringify(colors)).toBeGreaterThanOrEqual(3);
  });
}
