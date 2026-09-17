import Link from "next/link";
import { ErrorPageLayout } from "@/components/error-page/error-page";
import { Button } from "@/components/ui/button";

export default async function NotFound() {
  return (
    <div className="flex flex-col h-screen">
      <header className="flex h-16 w-full shrink-0 items-center border-b border-border bg-sidebar px-6">
        <Link href="/workspace/chat" className="flex shrink-0 items-center">
          {/* The stock wordmark asset is white, so it is rendered as a mask
              tinted with the theme's --logo token (same as the shell sidebar). */}
          <span
            aria-hidden="true"
            className="block h-[17px] w-[108px] bg-logo"
            style={{
              WebkitMaskImage: "url(/stacklok-logo.svg)",
              maskImage: "url(/stacklok-logo.svg)",
              WebkitMaskRepeat: "no-repeat",
              maskRepeat: "no-repeat",
              WebkitMaskSize: "contain",
              maskSize: "contain",
              WebkitMaskPosition: "left center",
              maskPosition: "left center",
            }}
          />
          <span className="sr-only">Stacklok</span>
        </Link>
      </header>
      <main className="flex flex-col flex-1 overflow-hidden px-4 py-5">
        <ErrorPageLayout
          title="Page Not Found"
          actions={
            <Button asChild variant="action" className="rounded-full">
              <Link href="/workspace/chat">Go home</Link>
            </Button>
          }
        >
          The page you're looking for doesn't exist or has been moved.
        </ErrorPageLayout>
      </main>
    </div>
  );
}
