---
id: 08-media
title: Multimodal prompt helpers
blocked_by: [07-permissions]
status: pending
branch: ""
worktree: ""
issue: "916"
retries: 0
last_error: ""
accumulator: sdk/10-architecture-adr
---

# Task brief

Isomorphic prompt parts plus runtime helpers. Scenario 8.

Text / image / audio from `Uint8Array` **or** HTTPS URL (XOR — both or
neither fails locally before any request). Browser helpers accept
`Blob`/`File`. Node path helpers live under `./node` only — the isomorphic
`.` export must never import Node filesystem modules.

Local MIME/size/capability checks are a UX courtesy. Capability truth is
the composition-computed intersection echoed on the session. A server-side
rejection of a client-accepted part still surfaces as the server's typed
error.

Branch `sdk/18-media` off the stack tip. Do not push.

## Acceptance criteria

- AC8.1: Text, image, and audio parts construct from `Uint8Array` or HTTPS
  URL; supplying both or neither source fails locally with a typed
  validation error before any request is sent.
  - verify: `sdk/typescript/test/media.test.ts :: "part sources are XOR-validated locally"`
- AC8.2: The browser helpers accept `Blob`/`File` and the Node helpers read
  paths — and the path helpers live under `./node` only, so the isomorphic
  `.` export never imports Node filesystem modules.
  - verify: `sdk/typescript/test/media.test.ts :: "runtime helpers stay in their subpath"`
- AC8.3: A part whose MIME type or size violates the local bounds, or whose
  media kind the session's echoed capability rejects, fails locally with a
  typed error naming the reason; a server-side rejection of a
  client-accepted part still surfaces as the server's typed error — the
  server stays authoritative.
  - verify: `sdk/typescript/test/media.test.ts :: "local validation is a courtesy, the server is authoritative"`
