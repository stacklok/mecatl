// SPDX-License-Identifier: Apache-2.0

import tailwindcss from "@tailwindcss/vite";
import { tanstackRouter } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [tanstackRouter({ target: "react" }), react(), tailwindcss()],
  // Pin the transform to this package's tsconfig. The workspace `contracts`
  // sources are bundled from their own directory, and per-file tsconfig
  // discovery would otherwise pick up that package's tsconfig instead.
  tsconfig: "./tsconfig.json",
  server: {
    host: "127.0.0.1",
    port: 18473,
    proxy: {
      "/api": {
        changeOrigin: false,
        target: "http://127.0.0.1:3100",
      },
      "/oauth/callback": {
        changeOrigin: false,
        target: "http://127.0.0.1:3100",
      },
    },
    strictPort: true,
  },
});
