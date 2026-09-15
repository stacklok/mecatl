import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

// Pre-defined logo heights as Tailwind variants. Width is always auto so the
// operator's logo aspect ratio is preserved regardless of format or branding.
const brandLogoVariants = cva("w-auto", {
  variants: {
    size: {
      sm: "h-[19px]", // navbar, error page
      lg: "h-[30px]", // sign-in page
    },
  },
  defaultVariants: {
    size: "sm",
  },
});

interface BrandLogoProps extends VariantProps<typeof brandLogoVariants> {
  className?: string;
  loading?: "eager" | "lazy";
  fetchPriority?: "high" | "low" | "auto";
}

export function BrandLogo({
  size,
  className,
  loading,
  fetchPriority,
}: BrandLogoProps) {
  return (
    // biome-ignore lint/performance/noImgElement: next/image optimizer is not applicable here
    <img
      src="/brand/logo"
      alt={process.env.BRAND_NAME || "Stacklok"}
      className={cn(brandLogoVariants({ size }), className)}
      loading={loading}
      fetchPriority={fetchPriority}
    />
  );
}
