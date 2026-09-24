// SPDX-License-Identifier: Apache-2.0

/** esbuild replaces this expression in the bundle, so runtime env cannot alter it. */
export function serverBuildDefinitions(environment: {
  readonly STUDIO_BUILD_ID?: string | undefined;
}) {
  return { "process.env.STUDIO_BUILD_ID": JSON.stringify(environment.STUDIO_BUILD_ID ?? "") };
}
