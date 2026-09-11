import { connect, imagePartFromPath, textPart } from "@stacklok/mecatl-sdk/node";

await using client = connect({
  baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8080",
});
const session = await client.sessions.create({});
const image = await imagePartFromPath(new URL("./diagram.png", import.meta.url), "image/png");
const run = await session.run([textPart("Explain this diagram"), image]);

console.log((await run.result()).text);
