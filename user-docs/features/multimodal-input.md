---
sidebar_position: 120
title: Multimodal input
description:
  Send supported images and content blocks to models through Mecatl clients.
---

# Multimodal input

Mecatl accepts provider-neutral prompts containing text and typed media parts.
Before admitting a prompt, the server checks whether the selected model and
provider support each modality. The server rejects unsupported media instead of
dropping it or sending an empty text prompt.

## Availability

Image input is available when the selected model and its provider adapter both
advertise image support. Authoritative live modality metadata wins, including an
explicit text-only declaration. If a gateway omits modality metadata, Mecatl uses
an exact catalog match when available and otherwise falls back to the adapter's
capability; it does not guess from similarly named models on another provider.

That same resolved result is intersected with the adapter capability and used for
model listings, session capability echoes, and prompt validation, so a shared
multimodal adapter cannot make a known text-only model appear image-capable.

Audio is represented in the neutral wire contract and validation path, but the
OpenAI Responses path currently has no audio input support. Treat audio as
unavailable unless the selected deployment explicitly advertises it.

## Send multimodal content

The gRPC `Converse` prompt carries optional text plus repeated `Content` parts.
A prompt must contain text, at least one part, or both. The same shape is
supported by mid-run `Steer` frames and their committed echoes, so images/audio
staged while a run is active reach the next turn boundary without becoming
literal attachment markers. Each media part must name its kind and MIME type and
must use exactly one source: inline bytes or a URL.

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

Use the HTTP/SSE prompt endpoint with the same provider-neutral JSON shape, or
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

Before sending media, inspect the model inventory or the session's advertised
capabilities. Model metadata is live-first, so a catalog refresh can change the
reported modality for a model without changing the shared provider adapter. For
a session created with a provider/model selector, use the capability echo
returned at creation rather than assuming that every model on that endpoint has
the same input support. `GetSession` returns the same per-session media capability,
so resumed sessions and mecatui's clear/fork flows keep attachment and paste gates
aligned with the selected model. Older servers that omit this additive snapshot
field fall back to their server-wide capability echo.

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
- **mecatui:** the client relays supported content to the connected server; the
  remote server's capability and validation rules remain authoritative.
- **Engine embeddings:** construct the provider capability result and use the
  domain content validation path; the host owns transport and media acquisition.

## Limitations

- Capability support is per model and session, not a universal provider switch.
  Changing models may require a new session or a selector-specific engine.
- The OpenAI Responses adapter currently supports image input but audio input is
  dormant in the current wire path.
- Media validation does not make untrusted image/audio content trustworthy. It
  checks structure, capability, and transport safety; model prompt-injection
  defenses still apply to content returned by tools or remote sources.
- Inline media and fetched content are bounded by the server and provider
  limits. Large media can consume context and provider budget even when the
  request is structurally valid.
- URL media requires network access from the server. An isolated or no-network
  deployment should use inline content or reject URL-sourced parts.
- A no-filesystem session can still use provider-supported media; no local
  workspace is needed for the prompt itself.

For the wire definitions and exact event flow, see
[Drive via gRPC / HTTP](/building/deployment/grpc-http.md) and
[the HTTP/SSE API guide](/reference/http-sse-api.md). For provider adapter
capability requirements, see
[LLM provider extension points](/building/extension-points/llm-provider.md).

## Next steps

- [Choose models and providers](./choose-models.md)
- [Context windows](./context-windows.md)
- [Drive via gRPC / HTTP](/building/deployment/grpc-http.md)
- [Capability and deployment matrix](./capability-matrix.md)
