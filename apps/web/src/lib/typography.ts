// SPDX-License-Identifier: Apache-2.0

import { cn } from "./utils";

export function pageTitleClass(...extra: Parameters<typeof cn>): string {
  return cn("text-page-title", ...extra);
}
