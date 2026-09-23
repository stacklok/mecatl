// SPDX-License-Identifier: Apache-2.0

import { configDefaults, defineConfig } from "vitest/config";

/** The offline suite: SDK fakes only. The integration suite has its own config. */
export default defineConfig({
  test: {
    exclude: [...configDefaults.exclude, "test/integration/**"],
  },
});
