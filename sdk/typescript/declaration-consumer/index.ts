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

const wireMajor: 2 = mecatl.SUPPORTED_API_MAJOR;
void wireMajor;
function displayHistorical(event: mecatl.Event, archive: mecatl.ArchivedConversationMessage): void {
  if (event.kind === "historical") {
    const record: mecatl.HistoricalRecord = event.payload;
    const nodeRecord: mecatlNode.HistoricalRecord = record;
    const denoRecord: mecatlDeno.HistoricalRecord = nodeRecord;
    void denoRecord.parts;
    // @ts-expect-error Source-local history identity is not a tool-call attachment.
    void record.callId;
  }
  void archive.historical;
  void archive.providerPhase;
  // @ts-expect-error Opaque provider replay is not part of the public archive.
  void archive.reasoning;
  // @ts-expect-error Provider item identity is not part of the public archive.
  void archive.reasoningItemId;
}
void displayHistorical;
