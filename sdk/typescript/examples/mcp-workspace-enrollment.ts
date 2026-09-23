import {
  connect,
  McpConnectorAvailability,
  WorkspaceEnrollmentStatus,
} from "@stacklok-oss/mecatl-sdk";

const client = connect({ baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8080" });

try {
  const compatibility = await client.server.compatibility();
  if (!compatibility.capabilities.workspaceEnrollment) {
    throw new Error("This server does not offer MCP workspace enrollment");
  }

  const session = await client.sessions.create({});
  const inventory = await session.listMcpConnectors();
  if (inventory.availability !== McpConnectorAvailability.Available) {
    throw new Error("The MCP connector inventory is unavailable");
  }
  console.log("Connectors:", inventory.connectors.map((connector) => connector.name).join(", "));

  const started = await session.connectWorkspaceServices();
  console.log("Enrollment:", started.status);
  if (started.status === WorkspaceEnrollmentStatus.Pending) {
    if (started.presentationUrl !== undefined) {
      console.log("Complete enrollment at:", started.presentationUrl);
    }
    console.log("Press Enter after your application completes authorization");
    await new Promise<void>((resolve) => process.stdin.once("data", () => resolve()));
    const observed = await session.connectWorkspaceServices();
    console.log("Enrollment after recheck:", observed.status);
  }
} finally {
  await client.close();
}
