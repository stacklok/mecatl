import { spawn } from "@stacklok/mecatl-sdk/node";

await using client = await spawn({ args: ["--mock"] });
const session = await client.sessions.create({});
const result = await (await session.run("List the main packages in this repository")).result();

console.log(result.text);
