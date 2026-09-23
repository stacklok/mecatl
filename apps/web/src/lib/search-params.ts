// SPDX-License-Identifier: Apache-2.0

import { stringifySearchWith } from "@tanstack/react-router";

/** Search values are opaque IDs; JSON coercion changes hand-written detail URLs. */
export const stringSearchParams = {
  parseSearch: (search: string) => Object.fromEntries(new URLSearchParams(search)),
  stringifySearch: stringifySearchWith(JSON.stringify),
};
