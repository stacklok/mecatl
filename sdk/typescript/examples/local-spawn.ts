import { spawn } from "@stacklok/mecatl-sdk/node";

await using client = await spawn(
  process.env.MECATED_BIN === undefined ? {} : { binaryPath: process.env.MECATED_BIN },
);
const session = await client.sessions.create({});
const result = await (await session.run("List the main packages in this repository")).result();

console.log(result.text);
