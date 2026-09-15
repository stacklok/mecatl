import { proxyControl } from "@/lib/server-proxy";

type Context = { params: Promise<{ path: string[] }> };

const handle = async (request: Request, context: Context) =>
  proxyControl(request, (await context.params).path);

export {
  handle as DELETE,
  handle as GET,
  handle as PATCH,
  handle as POST,
  handle as PUT,
};
