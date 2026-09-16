// Package executionenv defines the private, versioned host-side execution-provider protocol.
// It is intentionally not part of the public Harness API.
package executionenv

import "time"

// Private protocol bounds.
const (
	ProtocolVersion  = "execution-grpc/1"
	MaxMessageBytes  = 8 << 20
	MaxJSONBody      = 8 << 20 // credential-free provider-to-workload stdin framing only
	MaxFileBytes     = 5 << 20
	MaxCommandBytes  = 1 << 20
	MaxPathBytes     = 4096
	MaxIdentityBytes = 1024
	MaxBindingBytes  = 253
	MaxGrantBytes    = 16 << 10
	MaxListEntries   = 10_000
)

// Operation identifies one provider or workload-helper operation.
type Operation string

// Supported private-protocol operations.
const (
	OpAttach               Operation = "attach"
	OpReferenceRelease     Operation = "reference.release"
	OpRetire               Operation = "retire"
	OpFileRead             Operation = "file.read"
	OpFileResolveAuthority Operation = "file.resolve_authority"
	OpFileStat             Operation = "file.stat"
	OpFileCreate           Operation = "file.create"
	OpFileReplace          Operation = "file.replace"
	OpFileList             Operation = "file.list"
	OpFileRemove           Operation = "file.remove"
	OpFileRename           Operation = "file.rename"
	OpFileCopy             Operation = "file.copy"
	OpFileGlob             Operation = "file.glob"
	OpFileGrep             Operation = "file.grep"
	OpCommandStart         Operation = "command.start"
	OpCommandStatus        Operation = "command.status"
	OpCommandCancel        Operation = "command.cancel"
	OpCommandStream        Operation = "command.stream"
)

// Valid reports whether the operation belongs to the closed protocol vocabulary.
func (o Operation) Valid() bool {
	switch o {
	case OpAttach, OpReferenceRelease, OpRetire, OpFileRead, OpFileResolveAuthority, OpFileStat, OpFileCreate, OpFileReplace, OpFileList, OpFileRemove, OpFileRename, OpFileCopy, OpFileGlob, OpFileGrep, OpCommandStart, OpCommandStatus, OpCommandCancel, OpCommandStream:
		return true
	}
	return false
}

// EnvironmentRef is the provider-private durable environment identity.
type EnvironmentRef struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

// Owner is an authenticated principal attestation.
type Owner struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

// RequestContext carries the exact authorization binding for an operation.
type RequestContext struct {
	Environment EnvironmentRef `json:"environment"`
	Owner       Owner          `json:"owner"`
	BindingID   string         `json:"binding_id"`
	Epoch       uint64         `json:"epoch"`
	Grant       string         `json:"grant"`
}

// ValidateProfileRequest selects an operator-defined profile for validation.
type ValidateProfileRequest struct {
	Profile string `json:"profile"`
}

// ValidateProfileResponse reports immutable profile capabilities and bounds.
type ValidateProfileResponse struct {
	Profile                  string   `json:"profile"`
	Digest                   string   `json:"digest"`
	Capabilities             []string `json:"capabilities"`
	MaxFileBytes             int64    `json:"max_file_bytes"`
	MaxCommandBytes          int64    `json:"max_command_bytes"`
	MaxCommandDurationMillis int64    `json:"max_command_duration_ms"`
}

// EnsureEnvironmentRequest requests an idempotent environment allocation.
type EnsureEnvironmentRequest struct {
	BindingID string `json:"binding_id"`
	Profile   string `json:"profile"`
	Owner     Owner  `json:"owner"`
}

// EnsureEnvironmentResponse returns allocation identity, readiness, and a short-lived grant.
type EnsureEnvironmentResponse struct {
	Environment    EnvironmentRef `json:"environment"`
	Epoch          uint64         `json:"epoch"`
	Ready          bool           `json:"ready"`
	Grant          string         `json:"grant"`
	GrantExpiresAt time.Time      `json:"grant_expires_at"`
}

// AttachEnvironmentRequest requests exact reattachment to an existing environment.
type AttachEnvironmentRequest struct {
	Context RequestContext `json:"context"`
	Purpose string         `json:"purpose"`
}

// PurposeSession is the supported attachment purpose.
const PurposeSession = "session"

// AttachEnvironmentResponse returns exact attachment state and a refreshed grant.
type AttachEnvironmentResponse struct {
	Environment    EnvironmentRef `json:"environment"`
	Epoch          uint64         `json:"epoch"`
	Ready          bool           `json:"ready"`
	Grant          string         `json:"grant"`
	GrantExpiresAt time.Time      `json:"grant_expires_at"`
}

// ReferenceReleaseRequest releases one durable binding reference.
type ReferenceReleaseRequest struct {
	Context RequestContext `json:"context"`
}

// RetireEnvironmentRequest requests controlled retirement without PVC deletion.
type RetireEnvironmentRequest struct {
	Context RequestContext `json:"context"`
}

// EmptyResponse is the successful response for operations without a payload.
type EmptyResponse struct{}

// FileRequest carries one bounded, authorized filesystem operation.
type FileRequest struct {
	Context     RequestContext `json:"context"`
	Operation   Operation      `json:"operation"`
	Path        string         `json:"path"`
	Destination string         `json:"destination,omitempty"`
	Pattern     string         `json:"pattern,omitempty"`
	Data        []byte         `json:"data,omitempty"`
	Version     string         `json:"version,omitempty"`
	Limit       int            `json:"limit,omitempty"`
}

// FileInfo is bounded filesystem metadata returned by the helper.
type FileInfo struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Mode    uint32    `json:"mode"`
	ModTime time.Time `json:"mod_time"`
	IsDir   bool      `json:"is_dir"`
}

// GrepMatch is one bounded textual grep result.
type GrepMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// FileResponse carries the result of one filesystem operation.
type FileResponse struct {
	Data               []byte      `json:"data,omitempty"`
	Version            string      `json:"version,omitempty"`
	Info               *FileInfo   `json:"info,omitempty"`
	Entries            []FileInfo  `json:"entries,omitempty"`
	Paths              []string    `json:"paths,omitempty"`
	Matches            []GrepMatch `json:"matches,omitempty"`
	AuthorityTarget    string      `json:"authority_target,omitempty"`
	AuthorityWorkspace string      `json:"authority_workspace,omitempty"`
}

// CommandStartRequest starts one bounded foreground shell command.
type CommandStartRequest struct {
	Context       RequestContext `json:"context"`
	Command       string         `json:"command"`
	TimeoutMillis int64          `json:"timeout_ms,omitempty"`
}

// CommandStartResponse returns the command identity and terminal result.
type CommandStartResponse struct {
	CommandID string                `json:"command_id"`
	State     CommandState          `json:"state"`
	Result    CommandStatusResponse `json:"result"`
}

// CommandState classifies command lifecycle and fencing outcomes.
type CommandState string

// Command states.
const (
	CommandRunning      CommandState = "running"
	CommandSucceeded    CommandState = "succeeded"
	CommandFailed       CommandState = "failed"
	CommandCancelled    CommandState = "cancelled"
	CommandFenceUnknown CommandState = "fence_unknown"
)

// CommandQueryRequest identifies one command for status or cancellation.
type CommandQueryRequest struct {
	Context   RequestContext `json:"context"`
	CommandID string         `json:"command_id"`
	Offset    int64          `json:"offset,omitempty"`
}

// CommandStatusResponse carries bounded output and terminal proof.
type CommandStatusResponse struct {
	CommandID       string       `json:"command_id"`
	State           CommandState `json:"state"`
	ExitCode        int          `json:"exit_code,omitempty"`
	Stdout          []byte       `json:"stdout,omitempty"`
	Stderr          []byte       `json:"stderr,omitempty"`
	NextOffset      int64        `json:"next_offset,omitempty"`
	Truncated       bool         `json:"truncated,omitempty"`
	TerminalReceipt string       `json:"terminal_receipt,omitempty"`
}

// ExecutorRequest is the credential-free provider-to-workload stdin protocol.
// It deliberately cannot select an environment, root, Pod, image, argv, or process environment.
type ExecutorRequest struct {
	Operation     Operation `json:"operation"`
	Path          string    `json:"path,omitempty"`
	Destination   string    `json:"destination,omitempty"`
	Pattern       string    `json:"pattern,omitempty"`
	Data          []byte    `json:"data,omitempty"`
	Version       string    `json:"version,omitempty"`
	Limit         int       `json:"limit,omitempty"`
	Command       string    `json:"command,omitempty"`
	CommandID     string    `json:"command_id,omitempty"`
	TimeoutMillis int64     `json:"timeout_ms,omitempty"`
}

// ExecutorResponse carries one credential-free helper result.
type ExecutorResponse struct {
	FileResponse
	Command *CommandStatusResponse `json:"command,omitempty"`
}

// ExecutorEnvelope provides a terminal, credential-free result over pods/exec.
type ExecutorEnvelope struct {
	Response *ExecutorResponse `json:"response,omitempty"`
	Error    *Error            `json:"error,omitempty"`
}
