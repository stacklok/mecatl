// SPDX-License-Identifier: Apache-2.0

// The server build replaces this expression with the image's release tag.
// Source-mode/local builds have no stamp and omit the response field.
export const studioBuildId = process.env.STUDIO_BUILD_ID || undefined;
