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
