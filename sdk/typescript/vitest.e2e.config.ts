import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "node",
    fileParallelism: false,
    hookTimeout: 30_000,
    include: ["e2e/**/*.e2e.test.ts"],
    testTimeout: 30_000,
  },
});
