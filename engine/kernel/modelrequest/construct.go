package modelrequest

import (
	"errors"
	"iter"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
	"github.com/stacklok/mecatl/engine/kernel/views"
)

// Source is the constructor's finite, immutable, internally consistent record
// source. All records and the active run belong to SessionID. Records is repeatable
// and agrees with RecordCount/RecordAt; indexing is O(1). Borrowed backing must stay
// stable throughout Construct. Implementations are trusted in-process readers, not
// hostile executable inputs. Session Snapshot and CommitView implement Source;
// neither an implementation nor construction grants acceptance or execution authority.
type Source interface {
	SessionID() sessionid.SessionID
	ActiveRun() session.RunID
	RecordCount() int
	RecordAt(int) session.RecordView
	Records() iter.Seq[session.RecordView]
}

// Construct builds an owned chronological-v1 logical request and detached recipe
// from exactly the captured source, including preparation preceding a proposed
// request. Adjacent text within each content sequence is joined byte-for-byte;
// media, message, instruction-block and assistant-item boundaries are preserved.
// It derives the run from source, never mutable Session state. Idle,
// pending-model, unsettled-tool and unscoped sources fail. Limits govern parameters
// and projected output, not unrelated history. Output limits are checked during
// final encoding, after projection; they do not bound peak construction memory.
// Parameters must remain stable for the call. No retrieval or invocation occurs.
// Failure returns zero, never a usable partial request or recipe.
func Construct(source Source, parameters format.ParametersV1, limits codec.Limits) (ConstructionResult, error) {
	if err := codec.Check(parameters, limits); err != nil {
		return ConstructionResult{}, err
	}
	if source == nil || source.SessionID() == (sessionid.SessionID{}) || source.ActiveRun() == (session.RunID{}) || source.ActiveRun().SessionID() != source.SessionID() {
		return ConstructionResult{}, errors.New("model request requires a scoped active source")
	}
	selection, err := selectChronological(source, parameters)
	if err != nil {
		return ConstructionResult{}, err
	}
	request, err := selection.project()
	if err != nil {
		return ConstructionResult{}, err
	}
	return finalizeConstruction(request, parameters, limits)
}

// chronologicalSelection indexes associations only, retaining immutable views,
// not payload copies or a second lifecycle aggregate. Its space is source-bounded.
type chronologicalSelection struct {
	source       Source
	parameters   format.ParametersV1
	root         session.RootInstructionsView
	user         session.UserInstructionsView
	binding      session.ModelBindingView
	catalog      session.ToolCatalogView
	inputs       map[session.UserInputID]session.UserInputView
	origins      map[session.ModelRequestID]session.ModelBindingView
	dispositions map[session.RequestedCallID]session.ToolDispositionView
	invoked      map[session.RequestedCallID]bool
	bulk         map[session.RunID]bool
}

func selectChronological(source Source, parameters format.ParametersV1) (chronologicalSelection, error) {
	s := chronologicalSelection{source: source, parameters: parameters,
		inputs:       make(map[session.UserInputID]session.UserInputView),
		origins:      make(map[session.ModelRequestID]session.ModelBindingView),
		dispositions: make(map[session.RequestedCallID]session.ToolDispositionView),
		invoked:      make(map[session.RequestedCallID]bool), bulk: make(map[session.RunID]bool)}
	pending := make(map[session.ModelRequestID]bool)
	for r := range source.Records() {
		switch r.Kind() {
		case sessionformat.KindRootInstructions:
			s.root, _ = r.RootInstructions()
		case sessionformat.KindUserInstructions:
			s.user, _ = r.UserInstructions()
		case sessionformat.KindModelBinding:
			s.binding, _ = r.ModelBinding()
		case sessionformat.KindToolCatalog:
			s.catalog, _ = r.ToolCatalog()
		case sessionformat.KindUserInput:
			v, _ := r.UserInput()
			s.inputs[v.ID()] = v
		case sessionformat.KindModelRequest:
			v, _ := r.ModelRequest()
			s.origins[v.ID()] = s.binding
			pending[v.ID()] = true
		case sessionformat.KindModelResponse:
			v, _ := r.ModelResponse()
			delete(pending, v.RequestID())
		case sessionformat.KindInvocationFailure:
			v, _ := r.InvocationFailure()
			delete(pending, v.RequestID())
		case sessionformat.KindToolInvocation:
			v, _ := r.ToolInvocation()
			s.invoked[v.CallID()] = true
		case sessionformat.KindToolDisposition:
			v, _ := r.ToolDisposition()
			s.dispositions[v.CallID()] = v
		case sessionformat.KindRunEnded:
			v, _ := r.RunEnded()
			s.bulk[v.RunID()] = v.BulkToolCloseout()
			for id := range pending {
				if id.RunID() == v.RunID() {
					delete(pending, id)
				}
			}
		}
	}
	if len(pending) != 0 || s.binding.API() == "" || s.binding.Model() == "" {
		return chronologicalSelection{}, errors.New("model request source is pending or has no binding")
	}
	return s, nil
}

// inputParts resolves a selected whole submission without copying siblings.
func (s *chronologicalSelection) inputParts(v session.InputItemView) (views.SliceView[sessionformat.ContentPart, session.ContentPartView], string, error) {
	if id, ok := v.UserInputID(); ok {
		input, found := s.inputs[id]
		if !found {
			return views.SliceView[sessionformat.ContentPart, session.ContentPartView]{}, "", errors.New("selected input is missing")
		}
		return input.Parts(), "", nil
	}
	inline, ok := v.Inline()
	if !ok {
		return views.SliceView[sessionformat.ContentPart, session.ContentPartView]{}, "", errors.New("selected input has no content branch")
	}
	if inline.Purpose() == sessionformat.PromptInstructions {
		return inline.Parts(), "supplemental-instructions", nil
	}
	return inline.Parts(), "supplemental-context", nil
}

func (s *chronologicalSelection) private(v session.ModelResponseView) (session.OpaqueProviderDataView, bool) {
	p, ok := v.Private()
	if !ok {
		return session.OpaqueProviderDataView{}, false
	}
	origin := s.origins[v.RequestID()]
	for _, selector := range s.parameters.PrivateReplay {
		if selector.SourceAPI == origin.API() && selector.SourceModel == origin.Model() && selector.Producer == p.Producer() && selector.Format == p.Format() && selector.Version == p.Version() {
			return p, true
		}
	}
	return session.OpaqueProviderDataView{}, false
}
