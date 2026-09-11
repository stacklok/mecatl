---
sidebar_position: 4
title: Use the TypeScript SDK
description: Run your first Mecatl prompt from TypeScript against a private offline daemon.
---

# Use the TypeScript SDK

In this tutorial, you will use `@stacklok-oss/mecatl-sdk` to start a private
`mecated` process, create a session, and run one prompt without model-provider
credentials.

## Prerequisites

You need:

- macOS or Linux with [Homebrew](https://brew.sh/); and
- Node.js 22 or later.

## Install `mecated`

Install the released `mecated` executable from Stacklok's Homebrew tap, then
verify that it is available on `PATH`:

```sh
brew install stacklok/tap/mecatl
mecated --version
```

The version command prints the installed release tag.

## Create a project

Create an empty project directory, then initialize an ESM package:

```sh
mkdir mecatl-sdk-quickstart
cd mecatl-sdk-quickstart
npm init -y
npm pkg set type=module
```

Install the SDK and TypeScript:

```sh
npm install @stacklok-oss/mecatl-sdk
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
import { spawn } from "@stacklok-oss/mecatl-sdk/node";

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
<summary>The SDK cannot find mecated</summary>

Run `mecated --version` in the same terminal. If the command is unavailable,
install Mecatl or pass an absolute `binaryPath` to `spawn()`.

</details>
