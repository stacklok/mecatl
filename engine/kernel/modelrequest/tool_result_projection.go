package modelrequest

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// toolResult correlates through kernel occurrence identity, never through the
// provider's reusable ID. Explicit facts take priority over bulk closeout.
func (s *chronologicalSelection) toolResult(p *requestProjection, call session.RequestedCallView) *format.ToolResult {
	outcome, status, output, hasOutput := s.toolOutcome(p, call.ID())
	if p.err != nil {
		return nil
	}
	count := 0
	if hasOutput {
		var err error
		count, err = outputPartCount(output, p.limits.MaxArrayElements)
		if err != nil {
			p.err = err
			return nil
		}
	}
	capture := ""
	if hasOutput {
		switch output.Capture() {
		case sessionformat.ToolCaptureComplete:
			if count == 0 {
				capture = "Tool returned no output."
			}
		case sessionformat.ToolCaptureOmitted:
			capture = "Tool output was not retained."
		case sessionformat.ToolCaptureTruncated:
			capture = "Tool output is incomplete."
		}
	}
	for _, notice := range []string{status, capture} {
		if notice != "" {
			if count == p.limits.MaxArrayElements {
				p.err = codec.ErrLimitExceeded
				return nil
			}
			count++
		}
	}
	p.fields(2, "t")
	p.fields(3, "i", "o", "p")
	p.text(call.ProviderCallID(), 4)
	p.text(string(outcome), 4)
	if !p.array(count, 4) {
		return nil
	}
	out := &format.ToolResult{ProviderCallID: call.ProviderCallID(), Outcome: outcome}
	if p.materialize {
		out.Parts = make([]sessionformat.ContentPart, 0, count)
	}
	if status != "" {
		out.Parts = p.appendPart(out.Parts, p.textPart(status, 5))
	}
	if capture != "" {
		out.Parts = p.appendPart(out.Parts, p.textPart(capture, 5))
	}
	if hasOutput {
		for i := 0; i < output.Parts().Len() && p.err == nil; i++ {
			out.Parts = p.outputPart(out.Parts, output.Parts().At(i), 5)
		}
	}
	return out
}

// toolOutcome resolves explicit or bulk-derived settlement and its broad status
// notice. The bool distinguishes retained output from a closeout with no capture.
func (s *chronologicalSelection) toolOutcome(p *requestProjection, call session.RequestedCallID) (format.ToolOutcome, string, session.ToolOutputView, bool) {
	outcome := format.ToolOutcomeNotInvoked
	status := "Tool was not invoked"
	var output session.ToolOutputView
	hasOutput := false
	if disposition, ok := s.dispositions[call]; ok {
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
	} else if s.bulk[call.RequestID().RunID()] {
		if s.invoked[call] {
			outcome, status = format.ToolOutcomeIndeterminate, "Tool outcome is unknown; side effects may have occurred."
		}
	} else {
		p.err = errors.New("model request source has unsettled tool work")
	}
	return outcome, status, output, hasOutput
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

func outputPartCount(output session.ToolOutputView, maximum int) (int, error) {
	n := 0
	for i := 0; i < output.Parts().Len(); i++ {
		part := output.Parts().At(i)
		count := 1
		if content, ok := part.Content(); ok {
			if text, ok := content.Text(); ok && text == "" {
				count = 0
			}
		}
		if resource, ok := part.Resource(); ok {
			count = 2
			if text, ok := resource.Text(); ok && text == "" {
				count = 1
			}
		}
		if count > maximum-n {
			return 0, codec.ErrLimitExceeded
		}
		n += count
	}
	return n, nil
}

func (p *requestProjection) outputPart(out []sessionformat.ContentPart, v session.ToolOutputPartView, depth int) []sessionformat.ContentPart {
	if content, ok := v.Content(); ok {
		if text, ok := content.Text(); !ok || text != "" {
			out = p.appendPart(out, p.content(content, depth))
		}
	} else if json, ok := v.JSON(); ok {
		p.fields(depth, "t")
		if !p.materialize {
			p.unformattedText(json.Len(), depth+1)
			return out
		}
		if p.textAllocation(json.Len()) {
			var text strings.Builder
			text.Grow(json.Len())
			for _, b := range json.All() {
				text.WriteByte(b)
			}
			s := text.String()
			p.text(s, depth+1)
			if p.err == nil {
				out = append(out, sessionformat.ContentPart{Text: &s})
			}
		}
	} else if link, ok := v.ResourceLink(); ok {
		out = p.appendPart(out, p.resourceMetadata(link, depth))
	} else if resource, ok := v.Resource(); ok {
		out = p.appendPart(out, p.resourceMetadata(resource.Resource(), depth))
		if p.err != nil {
			return out
		}
		if text, ok := resource.Text(); ok {
			if text != "" {
				out = p.appendPart(out, p.textPart(text, depth))
			}
		} else {
			out = p.appendPart(out, p.textPart("Binary body not included in this request.", depth))
		}
	}
	return out
}

// quotedLength measures strconv.Quote's exact UTF-8 rendering without formatting
// or allocating. Subtraction bounds every addition, including escaped expansion.
func quotedLength(v string, maximum int) (int, bool) {
	if maximum < 2 {
		return 0, false
	}
	n := 2
	for _, r := range v {
		k := utf8.RuneLen(r)
		switch r {
		case '"', '\\', '\a', '\b', '\f', '\n', '\r', '\t', '\v':
			k = 2
		default:
			if r < ' ' || r == 0x7f {
				k = 4
			} else if !strconv.IsPrint(r) {
				if r < 0x10000 {
					k = 6
				} else {
					k = 10
				}
			}
		}
		if k > maximum-n {
			return 0, false
		}
		n += k
	}
	return n, true
}

// resourceMetadata renders only retained supplied labels. It never infers media
// from MIME, includes a blob, dereferences a URI, or promises later retrieval.
func (p *requestProjection) resourceMetadata(v session.ToolResourceView, depth int) sessionformat.ContentPart {
	p.fields(depth, "t")
	labels := []string{"Resource URI: ", "\nName: ", "\nTitle: ", "\nMIME type: "}
	name, hasName := v.Name()
	title, hasTitle := v.Title()
	mime, hasMIME := v.MIMEType()
	values := []string{v.URI(), name, title, mime}
	present := []bool{true, hasName, hasTitle, hasMIME}
	n := 0
	for i, value := range values {
		if !present[i] {
			continue
		}
		if len(labels[i]) > p.limits.MaxStringBytes-n {
			p.err = codec.ErrLimitExceeded
			return sessionformat.ContentPart{}
		}
		n += len(labels[i])
		quoted, ok := quotedLength(value, p.limits.MaxStringBytes-n)
		if !ok {
			p.err = codec.ErrLimitExceeded
			return sessionformat.ContentPart{}
		}
		n += quoted
	}
	if !p.materialize {
		p.unformattedText(n, depth+1)
		return sessionformat.ContentPart{}
	}
	if !p.textAllocation(n) {
		return sessionformat.ContentPart{}
	}
	var text strings.Builder
	text.Grow(n)
	for i, value := range values {
		if present[i] {
			text.WriteString(labels[i])
			text.WriteString(strconv.Quote(value))
		}
	}
	result := text.String()
	p.text(result, depth+1)
	if p.err != nil {
		return sessionformat.ContentPart{}
	}
	return sessionformat.ContentPart{Text: &result}
}

// Compile-time checks keep the source seam independent of session implementation.
var (
	_ Source = session.Snapshot{}
	_ Source = session.CommitView{}
)
