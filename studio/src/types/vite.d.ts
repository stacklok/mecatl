// Ambient types for Vite's import.meta.glob used in tests/build.
interface ImportMeta {
  glob: (
    pattern: string,
    options?: { import?: string },
  ) => Record<string, () => Promise<unknown>>;
}

// Static image imports (used by Next.js Image component).
// Declared here so tsc can resolve them without a prior `next build`.
declare module "*.png" {
  import type { StaticImageData } from "next/image";
  const content: StaticImageData;
  export default content;
}
