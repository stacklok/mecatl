package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// submitResultTool is the synthetic deliverable tool a structured-output Subagent child is
// given (run-scoped — injected via RunRequest.ExtraTools, NEVER registered into the
// shared catalog). Its parameters ARE the model-authored output schema; when the child
// calls it, Execute validates the submitted payload against that schema (the single
// session.ValidateJSON choke point) and RECORDS the payload + validity onto this
// per-run struct, which the Subagent tool reads to decide success vs a correction re-drive.
//
// It is ReadOnly (it performs no workspace mutation — it only records into this struct),
// so it dispatches on the read-parallel path exactly like Read/Grep, and a child that
// calls it concurrently with other read-only tools is safe. It is per-call: a fresh one
// is built for each structured-output Subagent call, so its mutable state is never shared.
type submitResultTool struct {
	// schema is the model-authored output schema; it doubles as the tool's parameter
	// schema (so the model authors the result shape) and the validation target.
	schema json.RawMessage

	mu sync.Mutex
	// gotValid is set once a VALID payload has been submitted.
	gotValid bool
	// raw is the last submitted payload bytes (the validated JSON on success).
	raw string
	// lastValidationError is the most recent schema-validation failure message,
	// surfaced to the model in the correction prompt and, on retry-exhaustion, in the
	// Subagent result tool error.
	lastValidationError string
}

// newSubmitResultTool builds the per-run deliverable tool over the model-authored
// output schema. The schema is used both as the tool's advertised parameter schema and
// as the validation target.
func newSubmitResultTool(schema json.RawMessage) *submitResultTool {
	return &submitResultTool{schema: schema}
}

// Spec advertises SubmitResult with the model-authored schema as its parameters, so the
// model authors a payload of exactly the requested shape.
func (s *submitResultTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: submitResultToolName,
		Description: "Submit your FINAL structured result, with arguments matching the requested " +
			"schema. If validation fails, fix the reported mismatch and call SubmitResult again " +
			"until it is accepted. Do not also write a free-text answer.",
		Schema: s.schema,
	}
}

// ReadOnly reports true: SubmitResult performs no workspace mutation (it records into a
// per-run struct), so it dispatches on the read-parallel path like the explorer tools.
func (*submitResultTool) ReadOnly() bool { return true }

// Execute validates the submitted payload against the schema (the single
// session.ValidateJSON choke point) and records it. On a VALID payload it records the
// payload as the deliverable and returns a brief success ack. On an INVALID payload it
// records the validation error (for the correction prompt) and returns a model-visible
// error result naming the mismatch, so even within a single child turn the model sees
// what was wrong. The args are the model's payload (the call's raw Args).
func (s *submitResultTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	payload := call.Args
	if err := session.ValidateJSON(s.schema, payload); err != nil {
		s.mu.Lock()
		s.lastValidationError = err.Error()
		s.gotValid = false
		s.mu.Unlock()
		return session.NewToolError(call.ID,
			fmt.Sprintf("SubmitResult: your payload did not match the schema (%v); call SubmitResult again with a corrected payload", err)), nil
	}
	s.mu.Lock()
	s.raw = string(payload)
	s.gotValid = true
	s.lastValidationError = ""
	s.mu.Unlock()
	return session.NewToolResult(call.ID, "SubmitResult: accepted."), nil
}

// valid reports whether a schema-valid payload has been submitted.
func (s *submitResultTool) valid() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gotValid
}

// payload returns the validated payload bytes (empty until a valid submission).
func (s *submitResultTool) payload() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.gotValid {
		return ""
	}
	return s.raw
}

// lastError returns the most recent validation-failure message (empty when none).
func (s *submitResultTool) lastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastValidationError
}

// structuredOutputPrompt augments the child's task prompt with the structured-output
// contract: deliver by calling SubmitResult with JSON matching the schema. The schema
// is rendered inline so the model authors a conforming payload. No tool_choice forcing
// (incompatible with the reasoning paths) — the instruction is a plain prompt directive.
func structuredOutputPrompt(taskPrompt string, schema json.RawMessage) string {
	return fmt.Sprintf("%s\n\nWhen you have your answer, deliver it by calling the %s tool "+
		"with arguments that match this JSON schema EXACTLY:\n\n%s\n\nDo not write a free-text "+
		"answer; call %s to finish.",
		taskPrompt, submitResultToolName, indentSchema(schema), submitResultToolName)
}

// structuredCorrectionPrompt is the model-visible re-injection on a structured-output
// validation miss (or a child that never called SubmitResult): it names the failure and
// asks for a corrected SubmitResult call. lastErr may be empty when SubmitResult was
// never called, which the prompt handles distinctly.
func structuredCorrectionPrompt(schema json.RawMessage, lastErr string) string {
	if lastErr == "" {
		return fmt.Sprintf("You did not call the %s tool. Deliver your result now by calling %s "+
			"with arguments matching this JSON schema EXACTLY:\n\n%s",
			submitResultToolName, submitResultToolName, indentSchema(schema))
	}
	return fmt.Sprintf("Your %s payload did not match the requested schema: %s. "+
		"Call %s again with a corrected payload matching this JSON schema EXACTLY:\n\n%s",
		submitResultToolName, lastErr, submitResultToolName, indentSchema(schema))
}

// indentSchema pretty-prints the schema for prompt rendering; on a non-object/malformed
// schema it returns the raw bytes (the validator fails-open on those anyway).
func indentSchema(schema json.RawMessage) string {
	pretty, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return string(schema)
	}
	return string(pretty)
}
