import { connect } from "@stacklok/mecatl-sdk";

// Production guidance: point the browser at a same-origin BFF that injects the
// daemon credential and enforces Origin and CSRF policy. The SDK does not ship
// that BFF server or service; this file shows only the browser-facing shape.
await using client = connect({
  baseUrl: "/mecatl",
  credentials: "include",
});

const session = await client.sessions.create({});
const result = await (await session.run("Explain the selected file")).result();
console.log(result.text);
