// SPDX-License-Identifier: Apache-2.0

import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { expect, it } from "vitest";

/**
 * Studio's layouts pivot at 500px (`min-[500px]:` / `max-[499px]:`), like the
 * prototype's. A `sm:` or `md:` class in a feature or the shell needs a reason;
 * each one left is listed here with it. Shared primitives in `components/ui`
 * keep their upstream shadcn classes and are not scanned.
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

it("pivots at 500px, with every remaining sm: or md: class listed and explained", () => {
  const found: Record<string, string[]> = {};
  for (const file of [
    ...sources(join(src, "features")),
    ...sources(join(src, "components/shell")),
  ]) {
    const path = relative(src, file);
    if (pending.some((prefix) => path.startsWith(prefix))) continue;
    const classes = [...readFileSync(file, "utf8").matchAll(/(?<=[\s"'`{(])(?:sm|md):[^\s"'`]+/gu)];
    if (classes.length > 0) found[path] = classes.map(([name]) => name);
  }
  expect(found).toEqual(
    Object.fromEntries(Object.entries(allowed).map(([path, entry]) => [path, entry.classes])),
  );
});
