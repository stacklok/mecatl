package modelrequest

import (
	"errors"
	"strconv"
	"strings"

	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// toolResult correlates through kernel occurrence identity, never through the
// provider's reusable ID. Explicit facts take priority over bulk closeout.
func (s *chronologicalSelection) toolResult(call session.RequestedCallView) (*format.ToolResult, error) {
	outcome := format.ToolOutcomeNotInvoked
	status := "Tool was not invoked"
	var output session.ToolOutputView
	hasOutput := false
	if disposition, ok := s.dispositions[call.ID()]; ok {
		if returned, ok := disposition.Returned(); ok {
			outcome, status = format.ToolOutcomeReturnedSuccess, ""
			if returned.Status() == sessionformat.ToolReturnError {
				outcome, status = format.ToolOutcomeReturnedError, "Tool returned an error."
			}
			output, hasOutput = returned.Output(), true
		} else if notInvoked, ok := disposition.NotInvoked(); ok {
			status = notInvokedNotice(notInvoked.Reason())
		} else if indeterminate, ok := disposition.Indeterminate(); ok {
			outcome, status = format.ToolOutcomeIndeterminate, "Tool outcome is unknown; side effects may have occurred."
			output, hasOutput = indeterminate.Output(), true
		}
	} else if s.bulk[call.ID().RequestID().RunID()] {
		if s.invoked[call.ID()] {
			outcome, status = format.ToolOutcomeIndeterminate, "Tool outcome is unknown; side effects may have occurred."
		}
	} else {
		return nil, errors.New("model request source has unsettled tool work")
	}

	var parts []sessionformat.ContentPart
	if hasOutput {
		for i := 0; i < output.Parts().Len(); i++ {
			parts = appendOutputPart(parts, output.Parts().At(i))
		}
	}
	capture := ""
	if hasOutput {
		switch output.Capture() {
		case sessionformat.ToolCaptureComplete:
			if len(parts) == 0 {
				capture = "Tool returned no output."
			}
		case sessionformat.ToolCaptureOmitted:
			capture = "Tool output was not retained."
		case sessionformat.ToolCaptureTruncated:
			capture = "Tool output is incomplete."
		}
	}
	out := &format.ToolResult{ProviderCallID: call.ProviderCallID(), Outcome: outcome}
	for _, notice := range []string{status, capture} {
		if notice != "" {
			out.Parts = append(out.Parts, textPart(notice))
		}
	}
	out.Parts = append(out.Parts, parts...)
	return out, nil
}

func notInvokedNotice(reason sessionformat.ToolNotInvokedReason) string {
	switch reason {
	case sessionformat.ToolNotInvokedUserDenied:
		return "User denied tool execution"
	case sessionformat.ToolNotInvokedPolicyRejected:
		return "Tool execution was denied by policy"
	case sessionformat.ToolNotInvokedUnavailable:
		return "Requested tool was unavailable"
	case sessionformat.ToolNotInvokedPreparationFailed:
		return "Tool invocation could not be prepared"
	case sessionformat.ToolNotInvokedInfrastructureFailed:
		return "Tool was not invoked because of an infrastructure failure"
	case sessionformat.ToolNotInvokedCancelled:
		return "Tool invocation was cancelled before execution"
	default:
		return "Tool was not invoked"
	}
}

func appendOutputPart(out []sessionformat.ContentPart, v session.ToolOutputPartView) []sessionformat.ContentPart {
	if content, ok := v.Content(); ok {
		if text, ok := content.Text(); !ok || text != "" {
			out = append(out, content.MutableClone())
		}
	} else if json, ok := v.JSON(); ok {
		out = append(out, textPart(string(json.Copy())))
	} else if link, ok := v.ResourceLink(); ok {
		out = append(out, resourceMetadata(link))
	} else if resource, ok := v.Resource(); ok {
		out = append(out, resourceMetadata(resource.Resource()))
		if text, ok := resource.Text(); ok {
			if text != "" {
				out = append(out, textPart(text))
			}
		} else {
			out = append(out, textPart("Binary body not included in this request."))
		}
	}
	return out
}

// resourceMetadata renders only retained supplied labels. It never infers media
// from MIME, includes a blob, dereferences a URI, or promises later retrieval.
func resourceMetadata(v session.ToolResourceView) sessionformat.ContentPart {
	var text strings.Builder
	text.WriteString("Resource URI: ")
	text.WriteString(strconv.Quote(v.URI()))
	if name, ok := v.Name(); ok {
		text.WriteString("\nName: ")
		text.WriteString(strconv.Quote(name))
	}
	if title, ok := v.Title(); ok {
		text.WriteString("\nTitle: ")
		text.WriteString(strconv.Quote(title))
	}
	if mime, ok := v.MIMEType(); ok {
		text.WriteString("\nMIME type: ")
		text.WriteString(strconv.Quote(mime))
	}
	return textPart(text.String())
}

// Compile-time checks keep the source seam independent of session implementation.
var (
	_ Source = session.Snapshot{}
	_ Source = session.CommitView{}
)
