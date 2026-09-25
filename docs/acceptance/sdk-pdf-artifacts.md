# SDK session artifacts (PDF first) — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — PDF input and output change the public SDK and wire contracts, durable content ownership, provider translation, and mecak8s storage topology.
**Decision record:** [ADR 0367](../adr/0367-session-owned-pdf-artifacts.md)
**Phase:** Generic artifact transport and storage with PDF input and tool-result download
**Status:** proposed, 2026-09-25. The directing human selected both PDF directions, tool-result output, S3-compatible object storage outside Redis, and generic artifact API names with PDF as the only supported file type in this release.
**Delivery:** Split. Public wire, engine, persistence, security, and deployment interfaces need separate contract review.
**Expected tasks:** deferred to orchestration

A TypeScript SDK client uploads a PDF as a session artifact, sends its
session-bound reference in a chat prompt or steer, and downloads a PDF returned
in a top-level tool result as an async byte stream. Upload, download, storage,
and result-reference names are artifact-generic; this release accepts only
`application/pdf`. The server stores PDF bytes in a private S3-compatible
object store. mecak8s stores only bounded artifact metadata and IDs in its
Redis snapshot and event log. [ADR 0367](../adr/0367-session-owned-pdf-artifacts.md)
owns this boundary; [the API surface](../architecture/api-surface.md) and
[persistence architecture](../architecture/observability.md) describe the
current server seams.

## Human decisions

- [x] Should the first SDK release support PDF input, output, or both? — Decision: both directions.
- [x] Where should mecak8s keep PDF bytes across pod restarts? — Decision: external object storage; Redis keeps references.
- [x] Which returned PDFs should become downloadable in the first release? — Decision: PDF bytes in agent/tool results.
- [x] Which external object-store API should the first mecak8s adapter target? — Decision: S3-compatible, including MinIO.
- [x] Should artifact APIs be PDF-named or reusable for later file types? — Decision: use generic artifact names for upload, download, storage, capabilities, and result references. This release accepts only `application/pdf`; PDF prompt parts, validation, provider translation, and model capability remain specific.

## Interface contract

- **gRPC / protobuf:** Add `HarnessService.UploadArtifact(stream UploadArtifactRequest) returns (UploadArtifactResponse)` and `HarnessService.DownloadArtifact(DownloadArtifactRequest) returns (stream DownloadArtifactResponse)`. `UploadArtifactRequest` has a oneof of `UploadArtifactMetadata metadata = 1` and `bytes chunk = 2`; the first and only metadata frame carries `session_id = 1`, `name = 2`, and `mime_type = 3`, followed by nonempty chunks. `UploadArtifactResponse` carries `artifact_id = 1`, `name = 2`, `size = 3`, `sha256 = 4`, and `mime_type = 5`. `DownloadArtifactRequest` carries `session_id = 1` and `artifact_id = 2`; `DownloadArtifactResponse` carries `bytes chunk = 1`. Each chunk is at most 256 KiB and each PDF at most 20 MiB. Add `Content.Kind.KIND_PDF = 3` and `Content.artifact_id = 5`; a PDF prompt part requires an artifact ID and `application/pdf`, with empty `data`/`url` and no client-supplied metadata, while image/audio keep their current XOR rule. Add `Content.name = 6`, `Content.size = 7`, and `Content.sha256 = 8` for server-filled PDF metadata in transcript and event projections. Add `ContentBlock.Kind.KIND_ARTIFACT = 7`, `ContentBlock.artifact_id = 13`, and `ContentBlock.sha256 = 14`; artifact blocks require server-filled `name`, `size`, `mime_type`, and SHA-256 and carry no inline `data` or remote `url`; only `application/pdf` is produced in this release. Add `SessionCapabilities.pdf = 3` and `ServerCapabilities.artifacts = 31`. The HTTP mirror provides `POST /v1/sessions/{id}/artifacts?name=(percent-encoded filename)` with an `application/pdf` request stream and JSON `{artifact_id,name,size,sha256,mime_type}` response, plus `GET /v1/sessions/{id}/artifacts/{artifact_id}` with a streamed `application/pdf` response. The download response sets `Cache-Control: private, no-store` and a safe attachment filename.
- **Exported Go APIs / interfaces:** Add `session.MediaPDF`, `session.BlockArtifact`, `Content.ArtifactID`, `Content.SHA256`, `session.NewPDFContent(id, name string, size int64, sha256 string) (Content, error)`, and `session.NewArtifactBlock(id, name, mimeType string, size int64, sha256 string) (Content, error)`; `NewPDFContent` sets `application/pdf`, and `NewArtifactBlock` validates bounded metadata and accepts only `application/pdf` in this release. Add `port.ProviderCapabilities.PDF bool` and a `port.ToolResultProcessor` with `ProcessToolResult(context.Context, session.SessionID, session.ToolResult) (session.ToolResult, error)`, injected through `agent.Dependencies`. In `internal/adapter/server`, add `Artifact{ID, Name, MIMEType string; Size int64; SHA256 string}` and `ArtifactLifecycle` with `Stage(context.Context, session.SessionID, string, string, io.Reader) (Artifact, error)`, `Resolve(context.Context, session.SessionID, string) (Artifact, error)`, `Open(context.Context, session.SessionID, string) (Artifact, io.ReadCloser, error)`, `CommitPrompt(context.Context, session.SessionID, []string) error`, `CopyFork(context.Context, session.SessionID, session.SessionID, []session.Message) ([]session.Message, error)`, `DiscardUnpublished(context.Context, session.SessionID) error`, and `Reconcile(context.Context) error`. The `Stage` strings are the safe filename and declared MIME type; it accepts only `application/pdf` in this release. The server checks caller ownership before using this lifecycle. When artifact storage is enabled, composition must install it in the service and its result processor in every engine. The engine calls the processor after `PostToolUse` and UTF-8 repair but before audit, event emission, and recording; a processing error becomes a bounded tool error with the original call ID. Extend `session.ValidateToolResultParts` and the MCP producer so `application/pdf` embedded blobs up to 20 MiB reach the processor and an oversized PDF becomes a tool error, while other block caps and clamp behavior stay as they are. `session.ToolBlockText` renders `BlockArtifact` as a concise filename/size summary, and `port.RouteToolResultParts` retains that text-bearing block in mixed results and replay without sending PDF bytes to the model. `port.LLMRequest` remains unchanged. Composition binds a PDF artifact reader to the selected session's provider wrapper and resolves references into a temporary request copy for native provider translation. The real session factory includes a stable model-visible instruction explaining PDF attachments only when its resolved model has PDF input capability. Update engine API snapshots and the classified changelog entry.
- **Tool schemas:** None — existing tool results may return an `application/pdf` embedded-resource blob. No model-callable artifact tool is introduced.
- **CLI / config:** Add optional chart keys `artifacts.s3.bucket`, `artifacts.s3.region`, and `artifacts.s3.endpoint`, defaulting to empty, and matching `--artifact-s3-bucket`, `--artifact-s3-region`, and `--artifact-s3-endpoint` flags. A nonempty bucket and region enable the feature; partial settings fail startup. The chart passes values as flags and obtains credentials through workload identity or explicitly projected AWS-compatible environment, never chart values. An optional custom endpoint must use HTTPS and path-style S3 requests so TLS-enabled MinIO is supported. Require a private bucket dedicated to this mecak8s installation; object keys use server-owned session/artifact prefixes and do not depend on the rotatable telemetry installation ID. With no artifact store, `ServerCapabilities.artifacts` and per-session PDF input are false. Local `mecated` artifact storage is outside this first deployment slice.
- **Events / persistence:** With artifact storage enabled, `EvUserPrompt`, `EvToolResult`, snapshots, transcript projections, and tool-call audit carry artifact ID, safe name, byte size, SHA-256, and `application/pdf` only for recognized PDFs. They carry neither the recognized PDF's bytes/base64 copy nor a public object URL. The result processor constructs a fresh bounded `ToolResult.Content` summary, replaces each PDF blob block, and rejects a result whose surviving text or structured block repeats the PDF bytes or their base64 encoding. The S3-compatible store keeps immutable PDF objects under server-owned session/artifact keys. Redis holds bounded artifact metadata, including the validated MIME type, staging state, and a durable deletion outbox, never PDF bytes. Redis keys and S3 object prefixes use artifact-generic namespaces. A staging marker is written before an object write; a staged upload is unusable after 24 hours even if cleanup has not run. Composition decorates each successful session snapshot `Save`, including a recorded steer, to call `CommitPrompt` for newly referenced PDFs. If that marker update fails, snapshot references protect the object and reconciliation repairs its state. The result processor publishes a PDF object before emitting its reference. Reconciliation waits for an age grace, excludes an active session or fork lease, rereads the authoritative snapshot before deleting an unreferenced object, and retries when Redis is unavailable. Fork copies referenced objects once into the successor's namespace and rewrites refs before committing the fork; before successor publication, a failure calls `DiscardUnpublished`, while a post-publication failure preserves its objects for the published snapshot. Clear copies none. The Redis adapter's atomic `Delete` and `DeleteSessionIfUnchanged` operations enqueue prefix cleanup in the same transaction as snapshot deletion, revoking new download access immediately; a restart-safe worker drains the outbox and reconciles abandoned writes or failed forks. Add the owner, cleanup, and restart behavior to ADR 0027 Lists 1 and 2 during implementation.
- **Security / authority:** Every upload, prompt reference, and download authenticates and authorizes the exact session owner. An artifact ID grants no authority. Cross-session and deleted references return not-found without revealing existence. A download checks ownership before its first byte; deletion revokes new downloads, while bytes already authorized in an open stream may finish unless its caller cancels. The server checks `application/pdf`, `%PDF-` at byte zero, `%%EOF` within the final 1024 bytes, a safe 1–255-character basename without separators or control characters, the 20 MiB total, 256 KiB chunks, and the server-computed SHA-256. This is a format-signature check; the provider may still reject a structurally malformed PDF. For tool results it assigns a server-generated safe filename such as `artifact.pdf`, independent of the embedded resource URI. It rejects every non-PDF MIME type and unsupported PDF input before starting a model call. With artifact storage enabled, a storage or validation failure never falls back to inline bytes in durable state; a missing processor is a composition error. S3 credentials, keys, raw bytes, and provider payloads remain out of diagnostics and client errors. Cancellation closes upload/download streams and aborts unfinished object writes.
- **Compatibility / migration:** The protobuf enum/field and SDK/engine API additions are additive. The SDK exports `UploadedArtifact`, `PdfPromptPart`, `pdfPart`, `Session.uploadArtifact`, and `Session.downloadArtifact` from its core entry point with the signatures below. `uploadArtifact` returns bounded metadata; callers use `pdfPart(uploaded.artifactId)` for a PDF prompt. `PromptInput` and `RunControls.steer` accept PDF parts; `Run.steer` remains the text-only stream control. The SDK checks the echoed artifact deployment capability for transfers and the selected session PDF capability for prompt use; the server remains authoritative. Extend public SDK `EventContent`, `EventContentBlock`, `SessionCapabilities`, and `ServerCapabilities` with their PDF prompt kind, generic artifact result kind, artifact ID, name, MIME type, size, SHA-256, and capability fields where applicable, and decode them in event, snapshot, and transcript projections; typed users can read the ID from `tool.result`. HTTP and gRPC stream without buffering a complete download, and early iterator return closes the response body. The HTTP transport uses its configured fetch, credentials, and headers for artifact routes; gRPC uses the new streaming RPCs, including when a transport is injected. New SDKs fail with a typed `artifacts_unavailable` unsupported-feature error against older servers. Existing text/image/audio requests, historical snapshots, and tool-result handling when artifact storage is disabled keep their current behavior, including the existing inline embedded-resource limit and the possibility of a legacy embedded PDF blob in Redis. Enabling artifact storage externalizes PDF tool-result blobs before recording; this is the operator-selected change to the tool-result contract. Generate protobuf and TypeScript artifacts from sources. Native OpenAI Responses and Anthropic Messages accept PDF only when the exact model advertises `pdf`; OpenRouter and other adapters remain false until qualified. ACP PDF blocks and scheduled PDF prompts are rejected explicitly in this release.

The TypeScript SDK signatures are:

```ts
export interface PdfPromptPart { readonly kind: "pdf"; readonly artifactId: string }
export function pdfPart(artifactId: string): PdfPromptPart;
export interface UploadedArtifact {
  readonly artifactId: string;
  readonly name: string;
  readonly mimeType: string;
  readonly size: bigint;
  readonly sha256: string;
}
export interface Session {
  uploadArtifact(source: Blob | AsyncIterable<Uint8Array>, options: { name: string; mimeType: "application/pdf" }, requestOptions?: RequestOptions): Promise<UploadedArtifact>;
  downloadArtifact(artifactId: string, requestOptions?: RequestOptions): AsyncIterable<Uint8Array>;
}
```

## In scope — 4 scenarios, in implementation order

### Scenario 1 — upload and send a PDF to a PDF-capable model

The SDK streams a browser `Blob` or Node async byte source into a session-owned
artifact, then sends its reference in a structured prompt or steer. The server
uses the exact model's PDF modality and provider capability, following the
[current per-session media gate](../architecture/api-surface.md).

**Acceptance:**

- AC1.1: `session.uploadArtifact(source, { name, mimeType: "application/pdf" })` yields an opaque artifact ID and bounded metadata; `session.run([textPart(...), pdfPart(uploaded.artifactId)])` records a reference and the native provider receives PDF input through the selected session factory.
  - verify: `TestSDKPDFArtifacts_Scenario1_UploadPromptProvider`
- AC1.2: The SDK and server reject unsupported models, another session's ID, a non-PDF MIME type, wrong PDF declaration/signature or unsafe name, oversized input, and a malformed or cancelled stream before a model call. No rejected path records the PDF blob in Redis.
  - verify: `TestSDKPDFArtifacts_Scenario1_RejectInvalidOrUnauthorized`
- AC1.3: HTTP and gRPC uploads honor backpressure and cancellation; the SDK does not call `Blob.arrayBuffer()` or accumulate an async source into one whole-file buffer.
  - verify: `TestSDKPDFArtifacts_Scenario1_StreamTransports`; Vitest PDF transport tests exercise both SDK transports.
- AC1.4: The real session factory places a PDF-attachment instruction in the selected PDF-capable model's system-prompt layer, without adding it for a model that lacks PDF input capability.
  - verify: `TestSDKPDFArtifacts_Scenario1_SystemPromptAffordance` through the composed session factory.

### Scenario 2 — externalize a PDF returned by a tool

An existing top-level tool may return an embedded PDF resource. The effective
result is chosen under [the agent loop's tool-result ordering](../architecture/agent-loop.md)
before a durable record is made.

**Acceptance:**

- AC2.1: After the post-tool hook, a valid PDF embedded-resource blob of up to 20 MiB becomes an artifact block with `application/pdf` before audit, event delivery, and conversation recording; all three views carry the same ID and metadata and no PDF bytes. An MCP producer preserves the eligible blob through its size gate, and mixed tool blocks retain a model-visible PDF summary through replay.
  - verify: `TestSDKPDFArtifacts_Scenario2_ExternalizeEffectiveResult` and `TestSDKPDFArtifacts_Scenario2_MCPAndReplay`
- AC2.2: A hook that removes the blob creates no artifact. A bad PDF signature, oversized PDF, PDF bytes duplicated in another result field, missing processor wiring, or object-store failure produces a bounded tool error with the original call ID; the raw bytes do not reach the event log, snapshot, audit, or model. An empty or hostile embedded-resource URI never becomes a filename.
  - verify: `TestSDKPDFArtifacts_Scenario2_FailClosed` and `TestSDKPDFArtifacts_Scenario2_SafeName`

### Scenario 3 — stream the returned PDF to the SDK client

The SDK reads a PDF artifact ID from the `tool.result` block and requests the
binary from the server. The server owns the lookup and authorization under
[the API boundary](../architecture/api-surface.md).

**Acceptance:**

- AC3.1: The typed SDK `tool.result` block exposes a generic artifact kind, the PDF MIME type, artifact ID, and bounded metadata. `session.downloadArtifact(id)` yields ordered chunks over gRPC and HTTP without buffering the whole object; completion reproduces the stored size and SHA-256.
  - verify: `TestSDKPDFArtifacts_Scenario3_StreamDownload`; Vitest PDF download tests exercise both SDK transports.
- AC3.2: A different principal or session, a deleted session, and an unknown ID receive no PDF bytes on a new download. Cancelling an open download stops further chunks and releases its object-store reader; deletion during an already authorized stream does not weaken new-request revocation.
  - verify: `TestSDKPDFArtifacts_Scenario3_OwnershipAndCancellation`

### Scenario 4 — preserve references across mecak8s failover and cleanup

mecak8s restarts from Redis and the shared object store. Its pods remain
storage-free under [ADR 0048](../adr/0048-mecak8s.md).

**Acceptance:**

- AC4.1: After an upload, prompt, and tool PDF result, another replica can resume the session, replay the prompt to a PDF-capable model, and download the tool PDF; Redis snapshots and event entries contain metadata but no PDF bytes or base64 copy.
  - verify: `TestADR_0367_PDFBytesStayOutsideSessionState` and `TestSDKPDFArtifacts_Scenario4_FailoverReferenceOnly`
- AC4.2: Fork keeps usable private copies of referenced PDFs, Clear starts without them, and session deletion or retention pruning revokes new download access and eventually removes objects even when a cleanup attempt or pod stops mid-operation. An in-flight upload, tool result, or fork cannot be mistaken for an orphan. A fork that inherits a PDF prompt rejects a target provider/model without PDF input capability before publishing its successor.
  - verify: `TestSDKPDFArtifacts_Scenario4_ForkAndCleanup`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Local `mecated` filesystem artifact adapter | Later deployment slice | The first adapter targets mecak8s with S3-compatible storage. |
| Non-PDF file types | Later acceptance contract | Generic artifact names reserve a reusable transport and storage seam; this release rejects every MIME type except `application/pdf`. |
| Arbitrary PDF URLs and presigned links in prompts or results | Later security review | Server-owned IDs keep authorization and retention under the session boundary. |
| ACP PDF input and scheduled PDF prompts | Later contracts | These lifetimes and client capabilities are separate from SDK chat runs. |
| PDFs generated only as workspace files without a tool-result blob | Later publishing workflow | The first output path consumes a typed PDF tool result. |

## Definition of done

1. Focused offline Go and Vitest proofs pass, including the real session factory and both SDK transports.
2. `task generate`, `task api:update`, `task lint`, `task test:race`, `task docs`, `task site:build`, `task ac-trace-strict`, and the offline demo pass on the implementation candidate.
3. The SDK sessions-and-runs guide and mecak8s deployment guide document the respective client and operator tasks when behavior ships; generated references are updated from their sources.
4. ADR 0027 Lists 1 and 2 account for artifact objects, staged uploads, and cleanup workers with owner, cleanup, and restart decisions.
5. `/panel-review` reports no ship blockers; the implementation PR links the approved plan baseline and reports interface conformance.

## Deferred decisions and known risks

- The selected provider may impose a PDF limit below Mecatl's 20 MiB upload cap. The provider error is surfaced for that model call; the server never rewrites the durable reference into bytes.
