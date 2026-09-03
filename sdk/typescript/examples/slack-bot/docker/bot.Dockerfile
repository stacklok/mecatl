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
FROM node:24-slim
RUN corepack enable

WORKDIR /repo/sdk/typescript
COPY . .
RUN pnpm install --frozen-lockfile && pnpm run build

WORKDIR /repo/sdk/typescript/examples/slack-bot
RUN pnpm install --frozen-lockfile

CMD ["pnpm", "run", "start"]
