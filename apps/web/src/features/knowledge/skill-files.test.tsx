// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, expect, it, vi } from "vitest";
import { langForFileName, SkillFiles } from "./skill-files";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

let root: Root | undefined;

afterEach(async () => {
  await act(async () => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});

async function renderFiles(respond: () => Response) {
  vi.stubGlobal("fetch", async () => respond());
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () =>
    root?.render(
      <QueryClientProvider
        client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
      >
        <SkillFiles name="deploy" />
      </QueryClientProvider>,
    ),
  );
  await act(async () => new Promise((resolve) => setTimeout(resolve, 20)));
  return container;
}

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), {
    headers: { "Content-Type": status === 200 ? "application/json" : "application/problem+json" },
    status,
  });

it("lists every file in the order given, with its size and text", async () => {
  const container = await renderFiles(() =>
    json({
      files: [
        { content: "# Deploy", name: "SKILL.md", size: 8, unavailable: "" },
        { content: "API notes", name: "references/api.md", size: 2048, unavailable: "" },
      ],
      omitted: 0,
    }),
  );
  const names = [...container.querySelectorAll("span.font-mono")].map((n) => n.textContent);
  expect(names).toEqual(["SKILL.md", "references/api.md"]);
  expect(container.textContent).toContain("8 B");
  expect(container.textContent).toContain("2.0 KB");
  expect(container.textContent).toContain("# Deploy");
  expect(container.textContent).toContain("API notes");
});

it("shows a file that could not be loaded with its reason, without hiding the others", async () => {
  const container = await renderFiles(() =>
    json({
      files: [
        { content: "# Deploy", name: "SKILL.md", size: 8, unavailable: "" },
        {
          content: "",
          name: "data/blob.bin",
          size: 3,
          unavailable: "This is a binary file, so it can't be shown here.",
        },
      ],
      omitted: 0,
    }),
  );
  expect(container.textContent).toContain("data/blob.bin");
  expect(container.textContent).toContain("This is a binary file");
  expect(container.textContent).toContain("# Deploy");
});

it("renders file text as text, never as markup", async () => {
  const container = await renderFiles(() =>
    json({
      files: [
        {
          content: "<b>bold</b><img src=x onerror=alert(1)>",
          name: "SKILL.md",
          size: 40,
          unavailable: "",
        },
      ],
    }),
  );
  expect(container.querySelector("pre")?.classList.contains("scroll-fade-y")).toBe(true);
  expect(container.querySelector("pre b")).toBeNull();
  expect(container.querySelector("pre img")).toBeNull();
  expect(container.querySelector("pre")?.textContent).toContain("<b>bold</b>");
});

it("shows the BFF's message when the request fails", async () => {
  const container = await renderFiles(() =>
    json({ code: "skill_not_found", detail: "That skill is gone.", status: 404, title: "x" }, 404),
  );
  expect(container.textContent).toContain("That skill is gone.");
});

it("says how many more files exist when the BFF omitted some", async () => {
  const file = { content: "x", name: "SKILL.md", size: 1, unavailable: "" };
  const many = await renderFiles(() => json({ files: [file], omitted: 3 }));
  expect(many.textContent).toContain("3 more files aren't shown.");
  await act(async () => root?.unmount());
  document.body.replaceChildren();
  const one = await renderFiles(() => json({ files: [file], omitted: 1 }));
  expect(one.textContent).toContain("1 more file isn't shown.");
});

it("chooses a highlight language from the extension and falls back to plain text", () => {
  expect(langForFileName("SKILL.md")).toBe("md");
  expect(langForFileName("references/api.md")).toBe("md");
  expect(langForFileName("scripts/run.sh")).toBe("sh");
  expect(langForFileName("data/config.JSON")).toBe("json");
  expect(langForFileName("Makefile")).toBe("text");
  expect(langForFileName("notes.unknownext")).toBe("text");
});
