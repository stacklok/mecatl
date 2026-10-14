// SPDX-License-Identifier: Apache-2.0

import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

/**
 * Studio's layouts pivot at 500px (`min-[500px]:` / `max-[499px]:`), like the
 * prototype's. A `sm:` or `md:` class (or `max-sm:` / `max-md:`, behind any
 * other variant) anywhere in `src` needs a reason; each one left is listed
 * here with it. Shared primitives in `components/ui` keep their upstream
 * shadcn classes and are not scanned.
 */

const src = fileURLToPath(new URL("..", import.meta.url));

/** Intentional `sm:` / `md:` classes, by file, with the reason each stays. */
const allowed: Record<string, { classes: string[]; reason: string }> = {
  "components/shell/storage-health-banner.tsx": {
    classes: ["sm:inline"],
    reason: "The prototype's banner shows its detail line from sm: on the same element.",
  },
  "features/chat/session-inspection.tsx": {
    classes: ["sm:max-w-lg"],
    reason: "Dialog width, matching shadcn's DialogContent and the prototype's dialogs.",
  },
  "features/knowledge/learned-skills.tsx": {
    classes: ["sm:max-w-2xl"],
    reason: "Dialog width, the prototype's learned-skill dialog's own class.",
  },
  "features/shortcuts/shortcut-reference.tsx": {
    classes: ["sm:grid-cols-2", "sm:grid-cols-2"],
    reason: "The prototype's shortcut and feature grids use sm: on the same elements.",
  },
};

/** Owned by open work that takes the sweep itself; see #2194. */
const pending = ["features/schedules/"];

function sources(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) return sources(path);
    return /\.tsx?$/u.test(entry.name) && !/\.test\.tsx?$/u.test(entry.name) ? [path] : [];
  });
}

/** The source files the guard reads, relative to `src`: all of it but the shared primitives. */
function scanned(): string[] {
  return sources(src)
    .map((file) => relative(src, file))
    .filter(
      (path) =>
        !path.startsWith("components/ui/") && !pending.some((prefix) => path.startsWith(prefix)),
    );
}

/** The viewport breakpoint variants the guard looks for, alone or after other variants. */
const viewportVariants = new Set(["sm", "md", "min-sm", "min-md", "max-sm", "max-md"]);

/**
 * The classes in a source text that carry a viewport `sm:` / `md:` variant,
 * in any position (`dark:sm:flex`, `[&>svg]:md:size-4`) and in its `min-`
 * and `max-` forms (`min-sm:` compiles to the same media query as `sm:`). A
 * container query (`@md:`, `@max-md:`) reads its container, not the viewport,
 * so it passes.
 */
function breakpointClasses(text: string): string[] {
  return text.split(/[\s"'`{}()]+/u).filter((token) =>
    token
      .split(":")
      .slice(0, -1)
      .some((part) => viewportVariants.has(part)),
  );
}

describe("the breakpoint guard", () => {
  it("finds a viewport breakpoint behind any variant, and in its min- and max- forms", () => {
    expect(
      breakpointClasses(
        'cn("sm:flex", "dark:md:grid hover:sm:block", "[&>svg]:sm:size-4 max-sm:hidden max-md:p-2 min-sm:flex min-md:grid")',
      ),
    ).toEqual([
      "sm:flex",
      "dark:md:grid",
      "hover:sm:block",
      "[&>svg]:sm:size-4",
      "max-sm:hidden",
      "max-md:p-2",
      "min-sm:flex",
      "min-md:grid",
    ]);
  });

  it("leaves container queries, the 500px pivot, and look-alike tokens alone", () => {
    expect(
      breakpointClasses(
        '"@max-md:hidden @sm:flex @min-[72rem]/chat-header:inline min-[500px]:flex max-[499px]:hidden rounded-md text-sm" // README.md: see',
      ),
    ).toEqual([]);
  });

  it("reads every source outside the shared primitives", () => {
    const files = scanned();
    for (const path of [
      "features/chat/chat-workspace.tsx",
      "components/shell/storage-health-banner.tsx",
      "components/sortable-head.tsx",
      "lib/use-mobile.ts",
      "routes/__root.tsx",
      "main.tsx",
    ]) {
      expect(files).toContain(path);
    }
    expect(files.some((path) => path.startsWith("components/ui/"))).toBe(false);
    expect(files.some((path) => path.startsWith("features/schedules/"))).toBe(false);
    expect(files.some((path) => /\.test\.tsx?$/u.test(path))).toBe(false);
  });
});

it("pivots at 500px, with every remaining sm: or md: class listed and explained", () => {
  const found: Record<string, string[]> = {};
  for (const path of scanned()) {
    const classes = breakpointClasses(readFileSync(join(src, path), "utf8"));
    if (classes.length > 0) found[path] = classes;
  }
  expect(found).toEqual(
    Object.fromEntries(Object.entries(allowed).map(([path, entry]) => [path, entry.classes])),
  );
});
