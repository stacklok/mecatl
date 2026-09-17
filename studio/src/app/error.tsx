"use client";

import { ErrorPage } from "@/components/error-page/error-page-client";

interface ErrorProps {
  error: Error & { digest?: string };
  reset: () => void;
}

export default function RootErrorPage({ error, reset }: ErrorProps) {
  return <ErrorPage error={error} reset={reset} />;
}
