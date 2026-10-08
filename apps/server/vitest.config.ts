// SPDX-License-Identifier: Apache-2.0

import { configDefaults, defineConfig } from "vitest/config";

/** The offline suite: SDK fakes only. The integration suite has its own config. */
export default defineConfig({
  test: {
    // DECISION: no test isolation, in both apps. See the NO-ISOLATE RULES in
    // ../web/src/test-setup.ts; biome bans the patterns that break under it.
    isolate: false,
    exclude: [...configDefaults.exclude, "test/integration/**"],
  },
});
