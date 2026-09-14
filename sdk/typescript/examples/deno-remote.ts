import { connect } from "@stacklok-oss/mecatl-sdk/deno";

await using client = connect({
  baseUrl: "http://127.0.0.1:8080",
});

const session = await client.sessions.create({});
const result = await (await session.run("Summarize this repository")).result();
console.log(result.text);
