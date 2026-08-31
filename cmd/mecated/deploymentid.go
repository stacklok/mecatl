package main

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxDeploymentIDLen bounds --deployment-id (ADR 0248).
//
// The label rides every GetServerInfo response, which the SDK polls on its
// connection-status heartbeat, so an unbounded string is a per-heartbeat cost on
// every connected client. 128 bytes is generous for "eu-west-1 staging" and far
// too small to smuggle a payload through.
const maxDeploymentIDLen = 128

// validateDeploymentID checks the operator-set deployment label.
//
// It is deliberately strict, because this value is echoed verbatim to every
// authenticated caller and rendered by clients we do not control:
//
//   - VALID UTF-8, so it cannot break a protobuf string field at marshal time
//     (the invalid-UTF-8-kills-the-stream class AGENTS.md documents).
//   - SINGLE LINE and PRINTABLE, so it cannot forge a log line, inject terminal
//     escapes into a TUI that renders it, or smuggle framing into a client's
//     output. Rejecting at startup is the right moment: the operator is present,
//     the message is actionable, and nothing downstream has to sanitise it.
//   - BOUNDED, per maxDeploymentIDLen.
//
// Empty is valid and is the default: the overwhelming majority of deployments
// have no label, and absence is never fabricated.
func validateDeploymentID(id string) error {
	if id == "" {
		return nil
	}
	if len(id) > maxDeploymentIDLen {
		return fmt.Errorf("--deployment-id is %d bytes; the maximum is %d", len(id), maxDeploymentIDLen)
	}
	if !utf8.ValidString(id) {
		return fmt.Errorf("--deployment-id is not valid UTF-8")
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("--deployment-id is whitespace only; omit the flag instead")
	}
	for _, r := range id {
		// unicode.IsPrint is false for control characters, line separators, and
		// the C1 range — every mechanism a label could use to forge structure in
		// something that renders it. Space is printable and allowed.
		if !unicode.IsPrint(r) {
			return fmt.Errorf("--deployment-id contains a non-printable character (%U); use printable single-line text", r)
		}
	}
	return nil
}
