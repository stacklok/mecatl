---
sidebar_position: 120
title: Multimodal input
description:
  Send supported images and content blocks to models through Mecatl clients.
---

# Multimodal input

Send text and typed media in the same prompt. Mecatl checks the selected model
and provider before sending the request and rejects unsupported media.

## Availability

Image input is available when the selected model and its provider adapter both
advertise image support. Authoritative live modality metadata wins, including an
explicit text-only declaration. If a gateway omits modality metadata, Mecatl
uses an exact catalog match when available and otherwise falls back to the
adapter's capability; it does not guess from similarly named models on another
provider.

Mecatl uses the same resolved support in model listings, session capabilities,
and prompt validation.

Audio is represented in the neutral wire contract and validation path, but the
OpenAI Responses path currently has no audio input support. Treat audio as
unavailable unless the selected deployment explicitly advertises it.

## Send multimodal content

The gRPC `Converse` prompt carries optional text and repeated `Content` parts. A
prompt must contain at least one of them. Mid-run `Steer` frames use the same
format and apply staged media at the next turn boundary. Each media part must
include its kind, MIME type, and exactly one source: inline bytes or a URL.

Conceptually:

```json
{
  "session_id": "session-id",
  "text": "Describe this image.",
  "parts": [
    {
      "kind": "KIND_IMAGE",
      "mime_type": "image/png",
      "data": "<base64-encoded-image-bytes>"
    }
  ]
}
```

A URL-sourced part uses `url` instead of `data`:

```json
{
  "kind": "KIND_IMAGE",
  "mime_type": "image/jpeg",
  "url": "https://cdn.example/image.jpg"
}
```

Use the HTTP/SSE prompt endpoint with the same provider-neutral JSON format, or
set `parts` on the first gRPC `Prompt` frame. The rest of the run is unchanged:
content is validated before the model call, then the ordinary event stream,
permissions, and terminal result apply.

## Validation behavior

The server rejects a malformed part when:

- the kind is unspecified or not supported by the selected capability set;
- both `data` and `url` are set, or neither is set;
- the MIME type is missing or inconsistent with the media kind;
- the URL is invalid or violates the media URL safety policy; or
- the prompt has neither text nor a media part.

ACP and the wire adapters use the same content validation. A client receives an
invalid-parameters error when an image or audio block is invalid. Text repair
rules do not rewrite inline binary media.

URL-sourced media is fetched under the server's bounded public-URL policy. Do
not use it as a way to reach private, loopback, link-local, metadata, or other
reserved destinations. Prefer inline bytes when the client already controls the
payload and the deployment's size limits permit them.

## Capability discovery

Before sending media, inspect the session's advertised capabilities. Use the
capability returned at creation because models on one endpoint can support
different input types. `GetSession` returns the same session-specific media
capability for resumed sessions and `mecatui` clear or fork flows.

If a session is restored with a persisted provider/model selector, the service
rehydrates the matching per-session engine before accepting a prompt. A missing
or unresolved capability is fail-closed.

## Client support

- **gRPC:** send `Prompt{session_id, text, parts}` on the mandatory first
  `Converse` frame.
- **HTTP/SSE:** send text and `parts` in the prompt request body; the response
  remains the normal SSE event stream.
- **ACP:** content blocks are converted to text or validated `session.Content`
  parts. Image/audio support is gated on the same provider capability result.
- **`mecatui`:** the client relays supported content to the connected server;
  the remote server's capability and validation rules remain authoritative.
- **Engine embeddings:** construct the provider capability result and use the
  domain content validation path; the host owns transport and media acquisition.

## Limitations

- Capability support is per model and session, not a universal provider switch.
  Changing models may require a new session or a selector-specific engine.
- The OpenAI Responses adapter currently supports image input but audio input is
  dormant in the current wire path.
- Media validation checks structure, capability, and transport safety. Treat
  content from tools and remote sources as untrusted.
- Inline media and fetched content are bounded by the server and provider
  limits. Large media can consume context and provider budget even when the
  request is structurally valid.
- URL media requires network access from the server. An isolated or no-network
  deployment should use inline content or reject URL-sourced parts.
- A no-filesystem session can still use provider-supported media; no local
  workspace is needed for the prompt itself.

For the wire definitions and exact event flow, see
[Drive via gRPC / HTTP](/building/grpc-http.md) and
[the HTTP/SSE API guide](/reference/http-sse-api.md). For provider adapter
capability requirements, see
[LLM provider extension points](/building/extension-points/llm-provider.md).

## Next steps

- [Choose models and providers](./choose-models.md)
- [Context windows](./context-windows.md)
- [Drive via gRPC / HTTP](/building/grpc-http.md)
