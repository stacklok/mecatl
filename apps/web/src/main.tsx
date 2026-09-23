// SPDX-License-Identifier: Apache-2.0

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createRouter, RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { RootErrorBoundary, studioRouterOptions } from "./components/error-page/error-routes";
import { AuthGate } from "./features/auth/auth-gate";
import { installCsrfInterceptor, installRecoveryInterceptor } from "./lib/api-client";
import { stringSearchParams } from "./lib/search-params";
import { initializeTheme } from "./lib/theme";
import { routeTree } from "./routeTree.gen";
import "./styles.css";

initializeTheme();
installCsrfInterceptor();
installRecoveryInterceptor();

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      staleTime: 30_000,
    },
  },
});

const router = createRouter({
  ...studioRouterOptions,
  routeTree,
  ...stringSearchParams,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

const rootElement = document.getElementById("root");

if (!rootElement) {
  throw new Error("Missing application root element");
}

createRoot(rootElement).render(
  <StrictMode>
    <RootErrorBoundary>
      <QueryClientProvider client={queryClient}>
        <AuthGate>
          <RouterProvider router={router} />
        </AuthGate>
      </QueryClientProvider>
    </RootErrorBoundary>
  </StrictMode>,
);
