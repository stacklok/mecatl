// SPDX-License-Identifier: Apache-2.0
import type { KnipConfig } from "knip";

const config: KnipConfig = {
  entry: [
    "src/app/**/{page,layout,loading,error,not-found,global-error,route}.{ts,tsx}",
    "src/**/*.test.{ts,tsx}",
  ],
  project: ["src/**/*.{ts,tsx}"],
  paths: {
    "@/*": ["src/*"],
  },
  ignore: [
    // shadcn/ui components export all variants by convention — only a subset
    // is used at any given time
    "src/components/ui/**",
    // Feature modules: internal exports consumed within the feature
    "src/features/**",
  ],
  ignoreDependencies: [
    // Tailwind v4 is imported via CSS (@import "tailwindcss"), not JS
    "tailwindcss",
    // Used by shadcn/ui Form and Label components (in src/components/ui/ which knip ignores)
    "react-hook-form",
    // Used by shadcn/ui components (in src/components/ui/ which knip ignores)
    "@radix-ui/react-avatar",
    "@radix-ui/react-checkbox",
    "@radix-ui/react-popover",
    "@radix-ui/react-progress",
    "@radix-ui/react-separator",
    "@radix-ui/react-toggle",
    "@radix-ui/react-toggle-group",
  ],
};

export default config;
