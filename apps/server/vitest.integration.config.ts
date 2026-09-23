// SPDX-License-Identifier: Apache-2.0

import { defineConfig } from "vitest/config";

/**
 * The integration suite drives the real BFF over the real SDK against a spawned
 * `mecated --mock`. It needs the daemon binary (see STUDIO_MECATED_BIN) and is
 * therefore a separate entry point from the offline `pnpm test`.
 */
export default defineConfig({
  test: {
    include: ["test/integration/**/*.test.ts"],
    testTimeout: 60_000,
    hookTimeout: 60_000,
  },
});
