package tool

import (
	"context"

	"github.com/stacklok/mecatl/engine/session"
)

// AuthorizationRequester is an optional Tool capability. The dispatcher asks
// it only after ordinary permission and PreToolUse gates have allowed the
// effective call. required=false leaves execution unchanged.
type AuthorizationRequester interface {
	Tool
	RequestAuthorization(context.Context, session.ToolCall) (authorization session.ExternalAuthorization, required bool, err error)
	// AbortAuthorization settles the exact pending transaction identified by its
	// ID and private binding. If settlement cannot be confirmed, it must make the
	// requester unusable before returning an error, so cleanup failure cannot leave
	// the transaction executable.
	AbortAuthorization(context.Context, session.ExternalAuthorization) error
}
