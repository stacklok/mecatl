"use client";

import { useEffect } from "react";
import { Button } from "@/components/ui/button";
import { ErrorPageLayout } from "./error-page";

interface ErrorPageProps {
  error: Error & { digest?: string };
  reset: () => void;
}

export function ErrorPage({ error, reset }: ErrorPageProps) {
  useEffect(() => {
    console.error(error);
  }, [error]);

  return (
    <ErrorPageLayout
      title="Something went wrong"
      actions={
        <Button onClick={reset} variant="default" className="rounded-full">
          Try again
        </Button>
      }
    >
      An unexpected error occurred. Please try again.
    </ErrorPageLayout>
  );
}
