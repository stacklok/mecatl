import { connect } from "@stacklok-oss/mecatl-sdk";

const client = connect({ baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8080" });

try {
  const session = await client.sessions.create({});
  const before = await session.snapshot();
  const renamed = await session.rename("SDK lifecycle example");
  const fork = await client.sessions.fork(session.id, { title: "A second approach" });
  const fresh = await session.clear();

  console.log("Source session:", before.sessionId);
  console.log("Renamed title:", renamed.title?.value);
  console.log("Forked session:", fork.id);
  console.log("Empty-history successor:", fresh.id);

  // Each handle has its own lifetime. Keep the source if you need its history.
  await fork.delete();
  await fresh.delete();
  await session.delete();
} finally {
  await client.close();
}
