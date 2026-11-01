# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# SPDX-License-Identifier: Apache-2.0
#
# Mecatl Studio image — ghcr.io/stacklok/mecatl/studio: one image serving the
# SPA and /api from one origin. Published by the `publish-studio` job in
# .github/workflows/release.yml with the same cosign sign / SBOM attest /
# build-provenance steps as the Slack bot image; built no-push on every relevant
# PR by the `studio` CI job; built locally by `task studio:image`.
#
# Build context is repository root so the BFF compiles the SDK from the same
# checkout: Studio, its SDK, and the daemon contract it targets are built from
# one commit, and a change spanning all three needs no intermediate SDK release.
#
# Shape (multi-stage):
#   1. build   — node:26-slim (Debian + shell + toolchain): pnpm 12.15.0 via
#                corepack (the `packageManager` pin in package.json; Node 25+
#                no longer bundles corepack, so it is installed first), a frozen
#                lockfile installs for SDK and Studio, `pnpm build` (contracts
#                generate → server bundle → web bundle), then `pnpm install --prod`
#                to keep only production dependencies in the Studio workspace.
#   2. runtime — cgr.dev/chainguard/node pinned by DIGEST (same posture as
#                .ko.yaml's chainguard/static pin: only the `latest` tag is
#                pullable without a Chainguard contract, so pin the digest of
#                `latest`; the `latest` tag in the FROM line is informational).
#                It is the Wolfi-based runtime variant: no apk (OS package
#                manager), no pnpm/corepack, no toolchain; it does ship Node's
#                bundled npm and a busybox `sh` (verified against the pinned
#                digest — the `-dev` variant is the one that adds apk). Non-root
#                user 65532, ENTRYPOINT `/usr/bin/node`, WORKDIR
#                /opt/studio/apps/server. Studio's pruned pnpm store, server
#                links and bundle, package.json, and built SPA are copied in.
#
# The build stage runs on the BUILD platform (--platform=$BUILDPLATFORM) so a
# multi-arch release (linux/amd64,linux/arm64) does not run pnpm + Vite under
# QEMU emulation. That is correct only while every PRODUCTION dependency of
# apps/server is pure JavaScript (hono, @hono/*, openid-client, zod,
# @stacklok-oss/mecatl-sdk, ...) — a native prod dependency would be installed
# for the build platform and break on the other. The dev-only natives (esbuild,
# Biome, Vite/rollup) never reach the runtime stage, so they are unaffected.
#
# Node major: one major everywhere. apps/package.json `engines` (>=26 <27) is
# the source of truth; .node-version, the CI job, the builder stage below, and
# the runtime stage all run that major. Public Chainguard only serves `latest`,
# so Renovate bumps the runtime digest in its own PR (renovate.json) and the CI
# `studio` job runs the built image and fails when its Node major differs from
# `engines` — a `latest` that moves to the next major cannot merge unnoticed.
#
# Runtime behaviour set here and relied on by apps/server:
#   STUDIO_IMAGE=1        refuses the local spawn and mock runtime modes (a
#                         container never ships mecated) and requires
#                         STUDIO_ALLOW_UNAUTHENTICATED=1 for static-token /
#                         no-auth runtimes.
#   STUDIO_WEB_DIST       explicit location of the SPA in the image.
#
# Runs as the base image's non-root user (65532). Because STUDIO_IMAGE=1, the
# server binds ${STUDIO_HOST:-0.0.0.0}:${STUDIO_PORT:-3100}; the TLS-terminating proxy in front of it is
# what STUDIO_PUBLIC_URL / STUDIO_TRUSTED_PROXY_HOPS describe.

# ---------------------------------------------------------------------------
# Stage 1: build (Node 26 — matches apps/package.json `engines` and .node-version)
# ---------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM node:26-slim@sha256:e4db4c914bf8ad241bd24804fc25d272d89bad97e68cdc8a1414862790a02c39 AS build
ARG STUDIO_BUILD_ID=""
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0 \
    CI=1
RUN npm install --global corepack@0.36.0 && corepack enable

WORKDIR /src
COPY sdk/typescript/package.json sdk/typescript/pnpm-lock.yaml sdk/typescript/pnpm-workspace.yaml sdk/typescript/tsconfig*.json sdk/typescript/
RUN cd sdk/typescript && pnpm install --frozen-lockfile
COPY sdk/typescript/src sdk/typescript/src
RUN cd sdk/typescript && pnpm exec tsc -p tsconfig.build.json

WORKDIR /src/apps
# Manifests + lockfile first so the dependency layer caches across source edits.
COPY apps/package.json apps/pnpm-lock.yaml apps/pnpm-workspace.yaml apps/.npmrc ./
COPY apps/contracts/package.json contracts/package.json
COPY apps/server/package.json server/package.json
COPY apps/web/package.json web/package.json
RUN pnpm install --frozen-lockfile

COPY apps/ .
# contracts (openapi.json + generated client) → server (esbuild → server/dist) → web (Vite → web/dist)
RUN STUDIO_BUILD_ID="${STUDIO_BUILD_ID}" pnpm build
# Reinstall production dependencies in place: pnpm deploy refuses the
# checked-out SDK because its source lives outside the apps workspace.
RUN pnpm install --prod --frozen-lockfile

# ---------------------------------------------------------------------------
# Stage 2: runtime (Chainguard node, pinned by digest)
# ---------------------------------------------------------------------------
# Multi-arch index digest of `latest` (linux/amd64 + linux/arm64); the digest is
# what is pulled. Its Node major is checked against `engines` in CI, not here.
FROM cgr.dev/chainguard/node:latest@sha256:88c4f6e6941bdd4fb9039d98ad05384e1bfe5693dd31ccc8eb2f0c8522bdb40e

# A fresh root-owned directory: the base image's /app is world-writable, which
# would let the non-root server replace its own code. WORKDIR creates the
# directory as the current user, and the base already runs as 65532, so switch
# to root for the build steps; USER 65532 below restores the runtime user.
USER 0
WORKDIR /opt/studio/apps/server
COPY --from=build /src/apps/server/package.json ./package.json
COPY --from=build /src/apps/node_modules ../node_modules
COPY --from=build /src/apps/server/node_modules ./node_modules
COPY --from=build /src/apps/server/dist ./dist
COPY --from=build /src/apps/web/dist ../web/dist

ENV STUDIO_IMAGE=1 \
    STUDIO_WEB_DIST=/opt/studio/apps/web/dist \
    NODE_ENV=production

# Early access: the UI and its HTTP surface may change between releases without
# a deprecation period. Stated on the image so a deployment can see it.
LABEL org.stacklok.mecatl.studio.stability="early-access"


# The base image already runs as 65532 (its `node` user); stated explicitly so a
# future base bump cannot silently change it.
USER 65532
EXPOSE 3100

# The base ENTRYPOINT is /usr/bin/node, so CMD is just the script.
CMD ["dist/index.js"]
