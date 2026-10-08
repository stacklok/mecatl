# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32
# SPDX-License-Identifier: Apache-2.0
#
# Mecatl Studio image — ghcr.io/stacklok/mecatl/studio: one image serving the
# SPA and /api from one origin. Published by the `publish-studio` job in
# .github/workflows/release.yml with the same cosign sign / SBOM attest /
# build-provenance steps as the Slack bot image; built no-push on every relevant
# PR by the `studio` CI job; built locally by `task studio:image`.
#
# Build context is apps/ ONLY (see .dockerignore): Studio consumes the published
# @stacklok-oss/mecatl-sdk from npm and never reaches into sdk/typescript, so the
# Go repository root is not part of this image at all.
#
# Shape (multi-stage):
#   1. build   — node:26-slim (Debian + shell + toolchain): pnpm 12.4.2 via
#                corepack (the `packageManager` pin in package.json; Node 25+
#                no longer bundles corepack, so it is installed first), a frozen
#                lockfile install, `pnpm build` (contracts generate → server
#                esbuild bundle → web Vite bundle), then `pnpm deploy --prod`
#                to prune the server package to production-only node_modules.
#   2. runtime — cgr.dev/chainguard/node pinned by DIGEST (same posture as
#                .ko.yaml's chainguard/static pin: only the `latest` tag is
#                pullable without a Chainguard contract, so pin the digest of
#                `latest`; the `latest` tag in the FROM line is informational).
#                It is the Wolfi-based runtime variant: no apk (OS package
#                manager), no pnpm/corepack, no toolchain; it does ship Node's
#                bundled npm and a busybox `sh` (verified against the pinned
#                digest — the `-dev` variant is the one that adds apk). Non-root
#                user 65532, ENTRYPOINT `/usr/bin/node`, WORKDIR /opt/studio.
#                Only the server's dist/, its pruned production node_modules,
#                its package.json (for `"type": "module"`), and the built SPA
#                are copied in — no dev dependencies, no sources.
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
#   STUDIO_WEB_DIST       where the server finds the SPA; the source-tree
#                         default (../web/dist relative to the server package)
#                         does not hold in the flattened /opt/studio layout.
#
# Runs as the base image's non-root user (65532). Because STUDIO_IMAGE=1, the
# server binds ${STUDIO_HOST:-0.0.0.0}:${STUDIO_PORT:-3100}; the TLS-terminating proxy in front of it is
# what STUDIO_PUBLIC_URL / STUDIO_TRUSTED_PROXY_HOPS describe.

# ---------------------------------------------------------------------------
# Stage 1: build (Node 26 — matches apps/package.json `engines` and .node-version)
# ---------------------------------------------------------------------------
FROM --platform=$BUILDPLATFORM node:26-slim@sha256:930557a230abacbc3f4fd9b8648abf8f4bee1e17cb72195dcdfb2f709bc85b33 AS build
ARG STUDIO_BUILD_ID=""
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0 \
    CI=1
RUN npm install --global corepack@0.36.0 && corepack enable

WORKDIR /src
# Manifests + lockfile first so the dependency layer caches across source edits.
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml .npmrc ./
COPY contracts/package.json contracts/package.json
COPY server/package.json server/package.json
COPY web/package.json web/package.json
RUN pnpm install --frozen-lockfile

COPY . .
# contracts (openapi.json + generated client) → server (esbuild → server/dist) → web (Vite → web/dist)
RUN STUDIO_BUILD_ID="${STUDIO_BUILD_ID}" pnpm build
# Prune to the server's production dependency closure. pnpm ≥ 12.2 derives a
# dedicated deploy lockfile from the shared one, so the `workspace:*` dependency
# on @mecatl-studio/contracts becomes a `file:` copy inside /out/server/node_modules
# without needing injectWorkspacePackages.
RUN pnpm --filter @mecatl-studio/server --prod deploy /out/server

# ---------------------------------------------------------------------------
# Stage 2: runtime (Chainguard node, pinned by digest)
# ---------------------------------------------------------------------------
# Multi-arch index digest of `latest` (linux/amd64 + linux/arm64); the digest is
# what is pulled. Its Node major is checked against `engines` in CI, not here.
FROM cgr.dev/chainguard/node:latest@sha256:78a8d37362503110e162d3f4e3f60362ff85429a34e0c810c68828b98c699e88

# A fresh root-owned directory: the base image's /app is world-writable, which
# would let the non-root server replace its own code. WORKDIR creates the
# directory as the current user, and the base already runs as 65532, so switch
# to root for the build steps; USER 65532 below restores the runtime user.
USER 0
WORKDIR /opt/studio
COPY --from=build /out/server/package.json ./package.json
COPY --from=build /out/server/node_modules ./node_modules
COPY --from=build /src/server/dist ./dist
COPY --from=build /src/web/dist ./web/dist

ENV STUDIO_IMAGE=1 \
    STUDIO_WEB_DIST=/opt/studio/web/dist \
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
