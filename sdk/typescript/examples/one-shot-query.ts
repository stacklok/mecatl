import { query } from "@stacklok/mecatl-sdk/node";

const oneShot = await query("Summarize the current working tree");
for await (const event of oneShot) {
  if (event.kind === "result") console.log(event.payload.text);
}
