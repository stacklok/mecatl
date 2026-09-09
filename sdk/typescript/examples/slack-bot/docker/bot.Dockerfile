# syntax=docker/dockerfile:1
# Image for the Slack bot example. Originally a local-dev/`docker compose up`
# convenience only; also the source of the CI-published
# ghcr.io/stacklok/mecatl/slack-bot image (.github/workflows/release.yml) now
# that a real deployment (stacklok/infra's staging2) runs it. The bot's code
# still lives under sdk/typescript/examples/ and isn't a published npm
# package (see ../README.md) — only the container image is released.
#
# Build context is THIS example directory itself (see docker-compose.yml and
# release.yml) — the bot now depends on the PUBLISHED @stacklok/mecatl-sdk
# (GitHub Packages), not a sibling-source symlink, so the SDK's own source
# tree no longer needs to be present in the build context.
#
# @stacklok/mecatl-sdk has no public npm registry yet, only GitHub Packages —
# TODO: once it does, drop the --mount=type=secret below and the npm_token
# build secret entirely; a plain `pnpm install --frozen-lockfile` will work
# with no auth. Until then, the install step needs a `read:packages`-scoped
# token, supplied as a BuildKit secret (never as a build ARG/ENV, which would
# leak it into image layer history). It is written to a throwaway user-level
# npmrc for the duration of one RUN, then removed before that layer commits —
# pnpm deliberately refuses to expand env vars in the committed project-level
# .npmrc (see that file's own comment), so this can't just be an ARG
# substituted into the checked-in file.
#
# NOT a hardened base image (deliberately, for now). node:24-slim is Debian
# + a shell + apt + a full toolchain, which is exactly what this single-stage
# build needs: corepack/pnpm/build run INSIDE the final image below, and a
# hardened runtime image (Chainguard's default tag, Docker Hardened Images'
# default tag) is minimal/distroless-style and almost certainly has none of
# that. Adopting one is a multi-stage rewrite, not a one-line FROM swap:
# a builder stage on a "-dev"/full variant (has the shell + toolchain) would
# produce dist/ + a pruned node_modules, then a second, minimal runtime
# stage would COPY just those artifacts in and run `node dist/index.js`
# directly, no corepack/pnpm/shell needed at runtime at all.
#
# Two hardened options were evaluated, favoring Chainguard when this gets
# picked up:
#   - cgr.dev/chainguard/node: free, no registry login to pull. mecatl
#     already depends on this vendor for every OTHER released image -
#     mecated/mecak8s/mecatui build on cgr.dev/chainguard/static (.ko.yaml)
#     - so adopting it here is zero new vendor relationship and zero new CI
#     credentials. The one real constraint: only the `latest`/`latest-dev`
#     tags are pullable for free; a specific pinned version tag (`node:24`,
#     etc.) requires contacting Chainguard's sales. Workaround, matching
#     .ko.yaml's own existing pattern: pin the DIGEST of `latest` at build
#     time, not the moving tag - reproducible, no sales conversation needed.
#   - Docker Hardened Images (docker.com/products/hardened-images): also
#     genuinely free (Apache 2.0, no paywalled catalog) and has a Node.js
#     image, but pulling from dhi.io requires `docker login dhi.io` with a
#     Docker account even on the free tier - a new credential that would
#     need to be provisioned and stored as a GitHub secret in this repo's
#     CI. Marketed as "change one line in your Dockerfile"; the registry
#     login requirement is the part that pitch leaves out.
#
# Tracked as a deliberate follow-up, not blocking: stacklok/mecatl#1054's
# PR description has the same writeup for anyone picking this up.
FROM node:24-slim
RUN corepack enable

WORKDIR /app
COPY . .
RUN --mount=type=secret,id=npm_token,required=true \
    export NODE_AUTH_TOKEN="$(cat /run/secrets/npm_token)" \
    && export NPM_CONFIG_USERCONFIG=/tmp/npmrc-build-only \
    && printf '//npm.pkg.github.com/:_authToken=%s\n' "$NODE_AUTH_TOKEN" > "$NPM_CONFIG_USERCONFIG" \
    && pnpm install --frozen-lockfile \
    && rm -f "$NPM_CONFIG_USERCONFIG"

CMD ["pnpm", "run", "start"]
