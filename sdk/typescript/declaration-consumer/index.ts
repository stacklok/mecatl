import * as mecatl from "@stacklok-oss/mecatl-sdk";
import * as mecatlDeno from "@stacklok-oss/mecatl-sdk/deno";
import * as mecatlNode from "@stacklok-oss/mecatl-sdk/node";

void mecatl;
void mecatlNode;

const denoClient = mecatlDeno.connect({
  baseUrl: "https://localhost:50051",
  nodeOptions: { ca: "test CA" },
});
const commonClient: mecatl.Client = denoClient;
void commonClient;
const nodeClient: mecatlNode.NodeClient = mecatlNode.connect({
  baseUrl: "https://localhost:50051",
  nodeOptions: { ca: "test CA" },
});
void nodeClient.tool;
// @ts-expect-error Deno's ordinary Client does not grant callback-tool registration.
void denoClient.tool;
const denoSocket: mecatlDeno.DenoConnectOptions = { socketPath: "/private/mecated.sock" };
const denoInjected: mecatlDeno.DenoConnectOptions = {
  transport: mecatl.createHttpTransport({ baseUrl: "http://localhost:8081" }),
  transportKind: "http",
};
void denoSocket;
void denoInjected;
