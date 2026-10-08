// SPDX-License-Identifier: Apache-2.0

import { defineConfig } from "@hey-api/openapi-ts";

export default defineConfig({
  input: "./openapi.json",
  output: {
    header: (context) => ["// SPDX-License-Identifier: Apache-2.0", ...context.defaultValue],
    path: "./src/generated",
  },
  plugins: [
    "@hey-api/client-fetch",
    {
      name: "@tanstack/react-query",
    },
  ],
});
