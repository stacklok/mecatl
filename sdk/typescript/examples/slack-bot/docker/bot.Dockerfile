# syntax=docker/dockerfile:1@sha256:572cebbc2b15e6f84c077742952674889cb63cd5e9b80abdffcbe587b6f19183
# Release and local-development image for the Slack bot example.
# Build context is sdk/typescript: build the in-tree SDK before installing the
# bot's file:../.. dependency. Only production artifacts reach the runtime.
#
# Chainguard's free Node images expose moving latest tags. Renovate's native
# Dockerfile manager uses these tags to refresh each multi-architecture digest;
# the digest still pins the build. Keep latest-dev for the builder and latest for
# the runtime so updates cannot silently swap the development/runtime variants.
# https://docs.renovatebot.com/docker/#digest-updating
FROM cgr.dev/chainguard/node:latest-dev@sha256:ae340fe00130911512a268fe0c74ab75a710d67ae5c4b0e0e8a121eb36e915ad AS builder

WORKDIR /home/node/src
COPY --chown=65532:65532 . .

RUN corepack pnpm@12.23.0 install --frozen-lockfile \
    && corepack pnpm@12.23.0 run build
RUN cd examples/slack-bot \
    && corepack pnpm@12.23.0 install --frozen-lockfile \
    && corepack pnpm@12.23.0 run build

FROM builder AS production-dependencies
RUN cd examples/slack-bot && corepack pnpm@12.23.0 prune --prod
RUN corepack pnpm@12.23.0 prune --prod

FROM cgr.dev/chainguard/node:latest@sha256:f93d51ac85f04aed13ade2d7864d014671023764e5e6e13564e5e767fbf77fa5

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
