// Package credentialstore defines a host-internal, credential-format-agnostic
// port for opaque binary records and provides namespace-bound backend handles.
// Reader is the read-only boundary; ConditionalWriter adds CAS mutation, and
// Store combines both for mutable backends. This port remains under internal
// because the engine is not its consumer.
//
// Every mutation is conditional: a nil expected version creates only, while a
// supplied opaque version replaces or deletes only the matching record. Values
// and keys have no OAuth, MCP, provider, or configuration semantics. The package
// performs no environment lookup, logging, or key acquisition. Future environment
// or Kubernetes Secret-backed sources may implement Reader only; durable refresh
// rotation requires a mutable Store.
//
// The encrypted-file backend is one local Store adapter. It protects copied local
// disclosure and provides cooperating-process CAS on supported local Unix
// filesystems. It does not defend against root or the same UID, authenticated
// rollback, process-memory inspection, crash-left encrypted temporary files, or
// unreliable advisory locks and rename semantics on non-local filesystems.
package credentialstore
