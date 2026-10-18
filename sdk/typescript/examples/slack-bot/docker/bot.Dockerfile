# syntax=docker/dockerfile:1@sha256:4edf897a3ffa55b89f906fc8cc78afdb3f1834cc9c7083565e611a8a7d5fe99e
# Release and local-development image for the Slack bot example.
# Build context is sdk/typescript: build the in-tree SDK before installing the
# bot's file:../.. dependency. Only production artifacts reach the runtime.
#
# Chainguard's free Node images expose moving latest tags. Renovate's native
# Dockerfile manager uses these tags to refresh each multi-architecture digest;
# the digest still pins the build. Keep latest-dev for the builder and latest for
# the runtime so updates cannot silently swap the development/runtime variants.
# https://docs.renovatebot.com/docker/#digest-updating
FROM cgr.dev/chainguard/node:latest-dev@sha256:85a6741a8a56d833f13700d8868de253257fdd4f37e6e8fdd755e3fabbbddf1d AS builder

WORKDIR /home/node/src
COPY --chown=65532:65532 . .

RUN corepack pnpm@11.25.0 install --frozen-lockfile \
    && corepack pnpm@11.25.0 run build
RUN cd examples/slack-bot \
    && corepack pnpm@11.25.0 install --frozen-lockfile \
    && corepack pnpm@11.25.0 run build

FROM builder AS production-dependencies
RUN cd examples/slack-bot && corepack pnpm@11.25.0 prune --prod
RUN corepack pnpm@11.25.0 prune --prod

FROM cgr.dev/chainguard/node:latest@sha256:13c04f76802e60540acb246335d5ef01be929219756841f99cfbbf7d077143d6

# The current runtime image includes BusyBox. Remove its single executable (all
# applet links, including /bin/sh, then become inert) before dropping privileges.
USER 0
RUN ["/bin/busybox", "rm", "/bin/busybox"]
USER 65532

WORKDIR /app/sdk/typescript
COPY --from=production-dependencies /home/node/src/package.json ./package.json
COPY --from=production-dependencies /home/node/src/dist ./dist
COPY --from=production-dependencies /home/node/src/node_modules ./node_modules
COPY --from=production-dependencies /home/node/src/examples/slack-bot/package.json ./examples/slack-bot/package.json
COPY --from=production-dependencies /home/node/src/examples/slack-bot/dist ./examples/slack-bot/dist
COPY --from=production-dependencies /home/node/src/examples/slack-bot/node_modules ./examples/slack-bot/node_modules

WORKDIR /app/sdk/typescript/examples/slack-bot
ENTRYPOINT ["/usr/bin/node"]
CMD ["dist/index.js"]
