// SPDX-License-Identifier: Apache-2.0

import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/")({
  beforeLoad: () => {
    throw redirect({ search: { sessionId: undefined }, to: "/workspace/chat", replace: true });
  },
});
