package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// ToolCaptureState is the closed output-capture vocabulary, not tool outcome.
type ToolCaptureState = sessionformat.ToolCaptureState

const (
	// ToolCaptureComplete retains complete terminal output, possibly empty.
	ToolCaptureComplete = sessionformat.ToolCaptureComplete
	// ToolCaptureTruncated retains incomplete whole parts in canonical order.
	ToolCaptureTruncated = sessionformat.ToolCaptureTruncated
	// ToolCaptureOmitted retains no parts, not necessarily no original output.
	ToolCaptureOmitted = sessionformat.ToolCaptureOmitted
)

// ToolOutput retains ordered whole output parts; Clone detaches shared storage.
type ToolOutput = sessionformat.ToolOutput

// ToolOutputPart retains exactly one content, exact JSON, or resource branch.
// Assignment shares nested storage; Clone detaches it.
type ToolOutputPart = sessionformat.ToolOutputPart

// ToolEmbeddedResource retains reference metadata and exactly one inline body.
// Assignment shares body storage; Clone detaches it.
type ToolEmbeddedResource = sessionformat.ToolEmbeddedResource

// checkToolOutputSize bounds output count before traversal and body sizes before
// validation or materialization. The full codec subsequently checks shared work.
func checkToolOutputSize(v ToolOutput, b *valueBudget) {
	b.text(string(v.Capture))
	b.collection(len(v.Parts))
	for _, part := range v.Parts {
		if b.err != nil {
			return
		}
		if part.Content != nil {
			b.pointer()
			checkContentPartSize(*part.Content, b)
		}
		if part.JSON != nil {
			b.pointer()
			b.bytes(len(*part.JSON))
		}
		if part.ResourceLink != nil {
			b.pointer()
			checkToolResourceSize(*part.ResourceLink, b)
		}
		if part.Resource != nil {
			b.pointer()
			checkToolResourceSize(part.Resource.Resource, b)
			if part.Resource.Text != nil {
				b.pointer()
				b.text(*part.Resource.Text)
			}
			if part.Resource.Blob != nil {
				b.pointer()
				b.bytes(len(*part.Resource.Blob))
			}
		}
	}
}

// ToolOutputView borrows immutable capture and ordered parts for its lifetime.
// Zero has empty capture and parts. Construction and navigation take O(1);
// iteration adapts children lazily without copying siblings.
//
//dtogen:include
type ToolOutputView struct{ value *sessionformat.ToolOutput }

// ToolOutputPartView borrows an immutable output union for its lifetime. Zero
// has no branch. Navigation is O(1); byte Copy detaches only the selected leaf.
//
//dtogen:include
type ToolOutputPartView struct{ value *sessionformat.ToolOutputPart }

// ToolEmbeddedResourceView borrows immutable metadata and inline body backing
// for its lifetime. Zero has neither body; present-empty bodies remain present.
// Navigation is O(1); Blob borrows bytes and Copy detaches only that leaf.
//
//dtogen:include
type ToolEmbeddedResourceView struct {
	value *sessionformat.ToolEmbeddedResource
}
