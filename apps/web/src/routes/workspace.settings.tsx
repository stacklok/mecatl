// SPDX-License-Identifier: Apache-2.0

import { createFileRoute, redirect } from "@tanstack/react-router";
import { SettingsIndex } from "../features/settings/settings-index";
import { isDesktopViewport } from "../lib/use-mobile";

/**
 * The bare Settings URL. A phone gets the section index; from the 500px pivot
 * up, and outside a browser, it keeps landing on You beside the section nav.
 */
export const Route = createFileRoute("/workspace/settings")({
  beforeLoad: () => {
    if (!isDesktopViewport()) return;
    throw redirect({
      params: { section: "profile" },
      replace: true,
      search: { item: undefined },
      to: "/workspace/settings/$section",
    });
  },
  component: SettingsIndex,
});
