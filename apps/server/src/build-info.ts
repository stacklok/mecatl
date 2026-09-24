// SPDX-License-Identifier: Apache-2.0

// Only the bundle build defines this identifier. Source mode has no release
// stamp even when its runtime environment contains STUDIO_BUILD_ID.
declare const __STUDIO_BUILD_ID__: string | undefined;
export const studioBuildId =
  typeof __STUDIO_BUILD_ID__ === "string" && __STUDIO_BUILD_ID__ !== ""
    ? __STUDIO_BUILD_ID__
    : undefined;
