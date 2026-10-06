// SPDX-License-Identifier: Apache-2.0

import { createRootRoute, Outlet } from "@tanstack/react-router";

export const Route = createRootRoute({
  component: RootLayout,
});

function RootLayout() {
  return <Outlet />;
}
