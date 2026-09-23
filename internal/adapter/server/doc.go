// Package server is the API adapter for mecatl: it exposes the WP8 agent
// loop over two network surfaces that share one domain Event taxonomy.
//
//   - gRPC (primary): HarnessServer implements the generated
//     mecatlv1.HarnessServiceServer. The bidi Converse stream carries a whole run:
//     a mandatory first Prompt frame, then zero or more ResumeApproval / Cancel
//     control frames, while the server streams Event envelopes until the
//     terminal result.
//   - HTTP/SSE (pragmatic): HTTPHandler serves POST /v1/sessions,
//     GET /v1/sessions/{id}, POST /v1/sessions/{id}/prompt (text/event-stream),
//     POST /v1/sessions/{id}/controls/resolve-ask and
//     POST /v1/sessions/{id}/controls/cancel over the same *agent.Engine and the
//     same session.Event; every control names its exact run.
//
// Both surfaces translate session.Event into the proto Event with the pure
// toProto mapper; neither surface ever sees an OpenAI type. As an adapter this
// package MAY import engine/agent, engine/session, engine/tool,
// engine/port and the generated contracts/gen/go.
//
// Required-field validation is enforced here in Go (the proto carries
// buf.validate annotations for documentation and future runtime enforcement;
// wiring the protovalidate runtime is a deferred follow-up).
package server
