import { createRouterTransport } from "@connectrpc/connect";
import { afterEach, expect, test, vi } from "vitest";
import { connect } from "../src/deno.js";
import { createNodeTransport } from "../src/node-transport.js";

vi.mock("../src/node-transport.js", () => ({ createNodeTransport: vi.fn() }));
afterEach(() => vi.clearAllMocks());

function transportFixture() {
  const transport = createRouterTransport(() => {}) as ReturnType<typeof createRouterTransport> &
    AsyncDisposable;
  const dispose = vi.fn(async () => {});
  transport[Symbol.asyncDispose] = dispose;
  return { transport, dispose };
}

test("Deno gRPC connect owns its built-in transport without callback-tool authority", async () => {
  const { transport, dispose } = transportFixture();
  vi.mocked(createNodeTransport).mockReturnValue(transport);
  const options = { socketPath: "/private/daemon.sock", headers: { authorization: "Bearer test" } };
  const client = connect(options);
  expect(createNodeTransport).toHaveBeenCalledExactlyOnceWith(options);
  expect(client).not.toHaveProperty("tool");
  await client.close();
  await client[Symbol.asyncDispose]();
  expect(dispose).toHaveBeenCalledOnce();
});

test("Deno gRPC connect forwards literal TLS session options", async () => {
  const { transport } = transportFixture();
  vi.mocked(createNodeTransport).mockReturnValue(transport);
  const client = connect({ baseUrl: "https://127.0.0.1:50051", nodeOptions: { ca: "test CA" } });
  expect(createNodeTransport).toHaveBeenCalledExactlyOnceWith({
    baseUrl: "https://127.0.0.1:50051",
    nodeOptions: { ca: "test CA" },
  });
  await client.close();
});

test("Deno gRPC connect leaves an injected transport caller-owned", async () => {
  const { transport, dispose } = transportFixture();
  const client = connect({ transport });
  await client.close();
  expect(createNodeTransport).not.toHaveBeenCalled();
  expect(dispose).not.toHaveBeenCalled();
  await transport[Symbol.asyncDispose]();
  expect(dispose).toHaveBeenCalledOnce();
});
