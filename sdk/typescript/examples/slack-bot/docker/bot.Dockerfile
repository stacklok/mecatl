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
# Chainguard's free Node images expose moving latest tags, so both stages are
# pinned by multi-architecture digest, matching the convention in .ko.yaml.
FROM cgr.dev/chainguard/node@sha256:dcb7cf99cf3eaf95bad12812e4233a2b534e464a277611287c3392d2171d662c AS builder

WORKDIR /app
COPY --chown=65532:65532 . .
RUN --mount=type=secret,id=npm_token,required=true,uid=65532 \
    export NODE_AUTH_TOKEN="$(cat /run/secrets/npm_token)" \
    && export NPM_CONFIG_USERCONFIG=/tmp/npmrc-build-only \
    && printf '//npm.pkg.github.com/:_authToken=%s\n' "$NODE_AUTH_TOKEN" > "$NPM_CONFIG_USERCONFIG" \
    && corepack pnpm@11.25.0 install --frozen-lockfile \
    && rm -f "$NPM_CONFIG_USERCONFIG" \
    && corepack pnpm@11.25.0 run build

FROM builder AS production-dependencies
RUN corepack pnpm@11.25.0 prune --prod

FROM cgr.dev/chainguard/node@sha256:753a66014b1310b8f93c76d4cac41d039958b9a86dd44a245289d6cb85455582

# The current runtime image includes BusyBox. Remove its single executable (all
# applet links, including /bin/sh, then become inert) before dropping privileges.
USER 0
RUN ["/bin/busybox", "rm", "/bin/busybox"]
USER 65532

WORKDIR /app
COPY --from=production-dependencies /app/package.json ./package.json
COPY --from=production-dependencies /app/dist ./dist
COPY --from=production-dependencies /app/node_modules ./node_modules

ENTRYPOINT ["/usr/bin/node"]
CMD ["dist/index.js"]
