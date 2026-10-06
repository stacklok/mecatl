// SPDX-License-Identifier: Apache-2.0

import { createFileRoute, redirect } from "@tanstack/react-router";

export const Route = createFileRoute("/workspace/settings")({
  beforeLoad: () => {
    throw redirect({
      params: { section: "profile" },
      replace: true,
      search: { item: undefined },
      to: "/workspace/settings/$section",
    });
  },
});
