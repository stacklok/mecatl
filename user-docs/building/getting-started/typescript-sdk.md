---
sidebar_position: 4
title: Use the TypeScript SDK
description: Run your first Mecatl prompt from TypeScript against a private offline daemon.
---

# Use the TypeScript SDK

In this tutorial, you will use `@stacklok/mecatl-sdk` to start a private
`mecated` process, create a session, and run one prompt without model-provider
credentials.

## Prerequisites

You need:

- Node.js 22 or later;
- an executable `mecated` on `PATH`, installed through the
  [Mecatl installation guide](/install.md);
- a GitHub personal access token with `read:packages`; and
- access to the internal `stacklok/mecatl` repository and its package.

## Create a project

Create an empty project directory, then initialize an ESM package:

```sh
mkdir mecatl-sdk-quickstart
cd mecatl-sdk-quickstart
npm init -y
npm pkg set type=module
```

Expose your GitHub token to the package manager:

```sh
export GITHUB_PACKAGES_TOKEN=<GITHUB_TOKEN>
```

Create `.npmrc` in the project directory. The file refers to the environment
variable and does not contain the token value:

```ini title=".npmrc"
@stacklok:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${GITHUB_PACKAGES_TOKEN}
```

Install the SDK preview and TypeScript:

```sh
npm install @stacklok/mecatl-sdk@0.0.1
npm install --save-dev --save-exact typescript@6.0.3
```

## Configure TypeScript

Create `tsconfig.json`:

```json title="tsconfig.json"
{
  "compilerOptions": {
    "module": "NodeNext",
    "moduleResolution": "NodeNext",
    "outDir": "dist",
    "strict": true,
    "target": "ES2022"
  },
  "include": ["quickstart.ts"]
}
```

## Run a prompt

Create `quickstart.ts`:

```ts title="quickstart.ts"
import { spawn } from "@stacklok/mecatl-sdk/node";

const client = await spawn({ args: ["--mock"] });

try {
  const session = await client.sessions.create({});
  const run = await session.run("Say hello from Mecatl");
  const result = await run.result();

  console.log(result.text);
} finally {
  await client.close();
}
```

Compile and run the program:

```sh
npx tsc
node dist/quickstart.js
```

The offline provider returns:

```plain
Mock provider: no real model is configured. Set OPENAI_API_KEY for live use.
```

Closing the client stops the private daemon and removes its runtime directory.
You have created and consumed a complete Mecatl run from TypeScript.

## Next steps

- [Connect an application](/building/typescript-sdk/connect.md) to use an
  operator-owned deployment.
- [Run a private local daemon](/building/typescript-sdk/local-daemon.md) to
  configure `spawn()` or use `query()`.
- [Work with sessions and runs](/building/typescript-sdk/sessions-and-runs.md)
  to stream events, send controls, and include media.

## Troubleshooting

<details>
<summary>Package installation returns 401 or 404</summary>

Confirm that `GITHUB_PACKAGES_TOKEN` contains a GitHub token with
`read:packages` and that your GitHub account can access the internal
`stacklok/mecatl` repository and package.

</details>

<details>
<summary>The SDK cannot find mecated</summary>

Run `mecated --version` in the same terminal. If the command is unavailable,
install Mecatl or pass an absolute `binaryPath` to `spawn()`.

</details>
