// SPDX-License-Identifier: Apache-2.0

import type { Page } from "@playwright/test";
import { expect, type OfflineBff, test } from "./fixtures";

/**
 * No page scrolls sideways on a phone. Each surface is measured at 320, 360,
 * and 390 CSS pixels: the document and every scroll region that is not an
 * intentional horizontal scroller (a class with `overflow-auto`,
 * `overflow-x-auto`, or their `scroll` forms, such as a code block or a wide
 * table) must fit the viewport's width.
 */

const WIDTHS = [320, 360, 390] as const;

const capabilities = {
  copyId: true,
  copyIdReason: "",
  delete: true,
  deleteReason: "",
  fork: true,
  forkReason: "",
  inspect: true,
  inspectReason: "",
  publicChat: true,
  publicChatReason: "",
  rename: true,
  renameReason: "",
  viewTranscript: true,
  viewTranscriptReason: "",
};

const longTitle = "Refactor the provider settings page and its deeply nested model inventory";

function stub(bff: OfflineBff) {
  bff.json("GET", "/api/v1/auth/session", {
    account: "phone-overflow",
    mode: "oidc",
    status: "authenticated",
  });
  bff.json("GET", "/api/v1/status", { connection: "reachable", signInRequired: false });
  bff.json("GET", "/api/v1/runtime", {
    apiMajor: 1,
    capabilities: {
      image: true,
      learnedSkills: true,
      memory: true,
      modelSelection: true,
      posture: "managed",
      scheduling: true,
      skills: true,
    },
    connection: "online",
    features: [],
    mock: false,
    source: "external",
  });
  bff.json("GET", "/api/v1/settings/runtime", {
    buildId: "v0.9.1",
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
        id: "claude-sonnet-with-a-long-model-identifier",
        image: true,
        providerId: "anthropic",
        reasoning: true,
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
  bff.json("GET", "/api/v1/sessions", {
    complete: true,
    items: [
      {
        capabilities,
        createdAt: "2026-10-09T08:00:00Z",
        debugTargetSessionId: "",
        id: "s1",
        kind: "main",
        modelId: "claude-sonnet",
        state: "idle",
        title: longTitle,
        titleProvenance: "",
        titleRevision: "0",
        turns: 1,
        updatedAt: "2026-10-09T08:00:00Z",
      },
    ],
  });
  bff.json("GET", "/api/v1/sessions/s1", {
    capabilities: { image: true, manualCompaction: false, modelSelection: false },
    id: "s1",
    kind: "main",
    mode: "default",
    state: "idle",
    usage: {
      cacheReadTokens: "0",
      cacheWriteTokens: "0",
      inputTokens: "0",
      outputTokens: "0",
      reasoningTokens: "0",
    },
  });
  bff.json("GET", "/api/v1/sessions/s1/transcript", {
    complete: true,
    messages: [
      {
        images: [],
        role: "user",
        text: "Tighten the imports in apps/web/src/features/settings/provider/provider-models-page.tsx.",
        toolCalls: [],
      },
      {
        images: [],
        role: "assistant",
        text: 'Updated the import. The page now reads `cn` from `@/lib/utils`:\n\n```ts\nimport { cn } from "@/lib/utils"; // a deliberately long line that a code block scrolls sideways\n```',
        toolCalls: [],
      },
    ],
    sessionId: "s1",
  });
  bff.on("GET", "/api/v1/sessions/s1/activity", () => ({
    body: "",
    contentType: "text/event-stream",
  }));
  bff.json("GET", "/api/v1/skills", {
    items: [
      {
        activeVersion: "",
        agentOwned: false,
        description:
          "Reviews a pull request against the repository's conventions and reports findings.",
        name: "review-pull-request-against-repository-conventions",
        ownerAgent: "",
      },
    ],
    reason: "",
    supported: true,
  });
  bff.json("GET", "/api/v1/schedules", {
    items: [
      {
        enabled: true,
        fireCount: 3,
        lastFireAt: "2026-10-09T07:00:00Z",
        lastFireSessionId: "",
        maxFires: 0,
        mode: "plan",
        modelId: "claude-sonnet",
        mutating: false,
        name: "nightly-digest-of-the-repository-activity",
        nextFireAt: "2026-10-10T07:00:00Z",
        oneShotMaxRetries: 0,
        oneShotRetry: false,
        owner: "phone-overflow",
        profile: "all",
        prompt: "Summarise yesterday's merged pull requests and open issues.",
        providerId: "anthropic",
        status: "scheduled",
        trigger: { expression: "0 7 * * *", kind: "cron", timezone: "Europe/Rome" },
      },
    ],
    reason: "",
    supported: true,
  });
  bff.json("GET", "/api/v1/user-memory", {
    items: [{ description: "Prefers concise answers.", key: "style/answers" }],
    reason: "",
    sha256: "x",
    sizeBytes: "42",
    supported: true,
  });
}

/** The scroll regions wider than their box, and the document when it is wider than the viewport. */
function sidewaysScroll(page: Page) {
  return page.evaluate(() => {
    const intentional = /(^|\s)(overflow-(auto|scroll|x-auto|x-scroll))(\s|$)/u;
    const offenders: string[] = [];
    const root = document.documentElement;
    if (root.scrollWidth > root.clientWidth + 1) {
      offenders.push(`document: ${root.scrollWidth} > ${root.clientWidth}`);
    }
    for (const element of document.body.querySelectorAll<HTMLElement>("*")) {
      const style = getComputedStyle(element);
      if (style.overflowX !== "auto" && style.overflowX !== "scroll") continue;
      if (intentional.test(element.getAttribute("class") ?? "")) continue;
      if (element.closest("pre")) continue;
      if (element.scrollWidth > element.clientWidth + 1) {
        const label =
          element.getAttribute("aria-label") ??
          (element.getAttribute("class") ?? "").split(/\s+/u).slice(0, 6).join(" ");
        offenders.push(
          `${element.tagName.toLowerCase()} [${label}]: ${element.scrollWidth} > ${element.clientWidth}`,
        );
      }
    }
    return offenders;
  });
}

const surfaces: ReadonlyArray<{
  name: string;
  path: string;
  ready: (page: Page) => Promise<void>;
  /** Widths a page owned by other open work still overflows at, with the issue that fixes it. */
  pending?: { owner: string; widths: readonly number[] };
}> = [
  {
    name: "chat draft",
    path: "/workspace/chat",
    ready: async (page) => {
      await expect(page.getByRole("heading", { name: "What can I help you with?" })).toBeVisible();
      await expect(
        page.getByRole("button", { name: "Draft a plan for a new feature" }),
      ).toBeVisible();
      await expect(page.getByRole("button", { name: /^Continue/u })).toBeVisible();
    },
  },
  {
    name: "chat transcript",
    path: "/workspace/chat?sessionId=s1",
    ready: async (page) => {
      await expect(page.getByText(/^Updated the import\./u)).toBeVisible();
    },
  },
  {
    name: "settings",
    path: "/workspace/settings/models",
    ready: async (page) => {
      await expect(page.getByRole("heading", { level: 1, name: "Settings" })).toBeVisible();
      await expect(page.getByText("Claude Sonnet").first()).toBeVisible();
    },
  },
  {
    name: "skills",
    path: "/workspace/skills",
    ready: async (page) => {
      await expect(page.getByRole("heading", { level: 1, name: "Skills" })).toBeVisible();
      await expect(
        page
          .getByText(/review pull request/iu)
          .filter({ visible: true })
          .first(),
      ).toBeVisible();
    },
  },
  {
    name: "schedules",
    path: "/workspace/schedules",
    // The page header's title and Schedule task button share one unwrapped row; the
    // schedules workspace belongs to #2194, which takes the fix.
    pending: { owner: "#2194", widths: [320] },
    ready: async (page) => {
      await expect(
        page
          .getByText("nightly-digest-of-the-repository-activity")
          .filter({ visible: true })
          .first(),
      ).toBeVisible();
    },
  },
  {
    name: "memory",
    path: "/workspace/settings/memory",
    ready: async (page) => {
      await expect(page.getByText("Prefers concise answers.").first()).toBeVisible();
    },
  },
  {
    name: "shortcuts",
    path: "/workspace/shortcuts",
    ready: async (page) => {
      await expect(page.getByRole("heading", { name: "Keyboard shortcuts" })).toBeVisible();
    },
  },
];

test.describe("phone widths", () => {
  test.skip(
    ({ isMobile }) => !isMobile,
    "The widths below are phone widths; the mobile project emulates the phone.",
  );

  for (const surface of surfaces) {
    for (const width of WIDTHS) {
      test(`${surface.name} has no sideways scroll at ${width}px`, async ({ offlineBff, page }) => {
        test.fixme(
          surface.pending?.widths.includes(width) ?? false,
          `Overflows at ${width}px until ${surface.pending?.owner} lands.`,
        );
        await page.addInitScript(() => localStorage.setItem("studio.account", "phone-overflow"));
        stub(offlineBff);
        await page.setViewportSize({ height: 844, width });
        await page.goto(surface.path);
        await surface.ready(page);
        expect(await sidewaysScroll(page)).toEqual([]);
      });
    }
  }
});

test.describe("500px pivot", () => {
  test.skip(({ isMobile }) => !isMobile, "One run is enough; the mobile project sets the width.");

  // Exactly one of the Skills inventory's two lists shows on each side of the pivot. Tailwind
  // v4's `max-[N]` is `width < N`, so the table's `max-[500px]:hidden` is the exact complement
  // of the phone list's `min-[500px]:hidden`; `max-[499px]` would show both lists at 499px.
  for (const width of [499, 500]) {
    test(`the skills inventory shows one list at ${width}px`, async ({ offlineBff, page }) => {
      await page.addInitScript(() => localStorage.setItem("studio.account", "phone-overflow"));
      stub(offlineBff);
      await page.setViewportSize({ height: 844, width });
      await page.goto("/workspace/skills");
      await expect(page.getByRole("heading", { level: 1, name: "Skills" })).toBeVisible();
      await expect(
        page.getByRole("link", { name: /Review Pull Request/u }).filter({ visible: true }),
      ).toHaveCount(1);
    });
  }
});
