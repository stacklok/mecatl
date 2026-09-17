import react from "@vitejs/plugin-react";
import tsconfigPaths from "vite-tsconfig-paths";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [tsconfigPaths(), react()],
  test: {
    globals: true,
    environment: "jsdom",
    setupFiles: ["./vitest.setup.ts"],
    exclude: [
      "**/node_modules/**",
      "**/dist/**",
      "tests/e2e/**",
      "tests/*.test.mjs",
    ],
    coverage: {
      provider: "v8",
      reporter: ["text", "lcov", "json"],
      reportsDirectory: "./coverage",
      exclude: [
        "node_modules/**",
        "**/*.d.ts",
        "**/*.test.{ts,tsx}",
        "vitest.setup.ts",
        "vitest.config.mts",
        "next.config.ts",
        "postcss.config.mjs",
        "scripts/**",
        "tests/**",
      ],
    },
  },
});
