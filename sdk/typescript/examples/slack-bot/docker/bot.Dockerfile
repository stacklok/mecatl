# Local dev/demo image for the Slack bot example, built for `docker compose
# up` only — this example isn't a published package (see ../README.md).
#
# Build context is sdk/typescript/ (see docker-compose.yml), since the bot
# depends on the sibling SDK via "link:../.." and both must be present with
# the same relative layout for that symlink to resolve inside the image.
FROM node:24-slim
RUN corepack enable

WORKDIR /repo/sdk/typescript
COPY . .
RUN pnpm install --frozen-lockfile && pnpm run build

WORKDIR /repo/sdk/typescript/examples/slack-bot
RUN pnpm install --frozen-lockfile

CMD ["pnpm", "run", "start"]
