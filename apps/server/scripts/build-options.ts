// SPDX-License-Identifier: Apache-2.0

/** esbuild defines a build-only identifier that source mode cannot read from runtime env. */
export function serverBuildDefinitions(environment: {
  readonly STUDIO_BUILD_ID?: string | undefined;
}) {
  return { __STUDIO_BUILD_ID__: JSON.stringify(environment.STUDIO_BUILD_ID ?? "") };
}
