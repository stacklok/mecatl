import { spawn } from "@stacklok-oss/mecatl-sdk/deno";

await using client = await spawn();
const session = await client.sessions.create({});
const result = await (await session.run("List the main packages in this repository")).result();

console.log(result.text);
