import { connect, ServerFeature } from "@stacklok-oss/mecatl-sdk";

const client = connect({ baseUrl: process.env.MECATL_URL ?? "http://127.0.0.1:8080" });

try {
  const compatibility = await client.server.compatibility();
  console.log("API major:", compatibility.apiMajor);
  console.log("Scheduling available:", compatibility.capabilities.scheduling);
  console.log("Workspace enrollment available:", compatibility.capabilities.workspaceEnrollment);

  if (compatibility.features.has(ServerFeature.ServerInfo)) {
    const identity = await client.server.info();
    console.log("Server implementation:", identity.serverImplementation);
  }
} finally {
  await client.close();
}
