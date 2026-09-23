import { connect, TransportError } from "@stacklok-oss/mecatl-sdk/node";

async function main(): Promise<void> {
  await using client = connect({
    baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8080",
  });
  const session = await client.sessions.create({});
  const run = await session.run("Use the configured MCP server to inspect the repository");
  const initial = await run.outcome();

  if (initial.outcome === "completed") {
    console.log(initial.result.text);
    return;
  }

  let authorization = session.mcpAuthorization(initial.authorization.payload.authorizationId);
  let recoveryAttempts = 0;

  for (;;) {
    const presentationUrl = await authorization.presentation({ timeoutMs: 10_000 });
    console.log("Complete authorization at:", presentationUrl);
    await waitForApplicationRecheck();

    const flow = authorization.recheck(
      {
        onPermissionAsk: (ask, signal) => {
          if (signal.aborted) return undefined;
          return ask.tool === "Read" ? "allow_once" : "deny";
        },
        permissionRequestOptions: { timeoutMs: 10_000 },
      },
      { timeoutMs: 30_000 },
    );

    try {
      const result = await flow.result();
      recoveryAttempts = 0;
      switch (result.outcome) {
        case "pending":
          console.log("Authorization is still pending");
          break;
        case "settled":
          console.log("Authorization ended with status", result.status);
          return;
        case "completed":
          console.log(result.continuation.text);
          return;
        case "authorization_required":
          console.log("Continuation parked on another authorization");
          console.log(result.nextAuthorization.payload.authorizationId);
          authorization = session.mcpAuthorization(
            result.nextAuthorization.payload.authorizationId,
          );
          break;
      }
    } catch (error) {
      if (!(error instanceof TransportError) || recoveryAttempts >= 1) throw error;
      // A lost control response is ambiguous. This bounded retry is a new recheck
      // and can succeed only if this authorization remains pending on the server.
      recoveryAttempts += 1;
    }
  }
}

await main();

async function waitForApplicationRecheck(): Promise<void> {
  console.log("Press Enter to check authorization status");
  await new Promise<void>((resolve) => process.stdin.once("data", () => resolve()));
}
