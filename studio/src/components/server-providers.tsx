import { NuqsAdapter } from "nuqs/adapters/next/app";
import type { ReactNode } from "react";

interface ServerProvidersProps {
  children: ReactNode;
}

export function ServerProviders({ children }: ServerProvidersProps) {
  return <NuqsAdapter>{children}</NuqsAdapter>;
}
