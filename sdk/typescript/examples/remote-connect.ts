import { connect } from "@stacklok/mecatl-sdk/node";

const client = connect({
  baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8080",
});

try {
  const session = await client.sessions.create({});
  const result = await (await session.run("Summarize this repository")).result();
  console.log(result.text);
} finally {
  await client.close();
}
