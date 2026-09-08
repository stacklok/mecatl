# Image for the Slack bot example. Originally a local-dev/`docker compose up`
# convenience only; also the source of the CI-published
# ghcr.io/stacklok/mecatl/slack-bot image (.github/workflows/release.yml) now
# that a real deployment (stacklok/infra's staging2) runs it. The bot's code
# still lives under sdk/typescript/examples/ and isn't a published npm
# package (see ../README.md) — only the container image is released.
#
# Build context is sdk/typescript/ (see docker-compose.yml and release.yml),
# since the bot depends on the sibling SDK via "link:../.." and both must be
# present with the same relative layout for that symlink to resolve inside
# the image.
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

WORKDIR /repo/sdk/typescript
COPY . .
RUN pnpm install --frozen-lockfile && pnpm run build

WORKDIR /repo/sdk/typescript/examples/slack-bot
RUN pnpm install --frozen-lockfile

CMD ["pnpm", "run", "start"]
