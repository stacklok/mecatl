// Package credentialstore defines an internal, credential-format-agnostic store
// for opaque binary records and provides namespace-bound backend handles.
//
// Every mutation is conditional: a nil expected version creates only, while a
// supplied opaque version replaces or deletes only the matching record. Values
// and keys have no OAuth, MCP, provider, or configuration semantics. The package
// performs no environment lookup, logging, or key acquisition.
//
// The encrypted-file backend protects copied local record files from offline
// disclosure and provides cooperating-process CAS on supported local Unix
// filesystems. It does not defend against root or the same UID, authenticated
// rollback, process-memory inspection, crash-left encrypted temporary files, or
// unreliable advisory locks and rename semantics on non-local filesystems.
package credentialstore
