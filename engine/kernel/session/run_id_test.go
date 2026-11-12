package session_test

import (
	"reflect"
	"testing"

	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestRunID(t *testing.T) {
	h := testHistoryID(t)
	first, second := allocatedRun(t, h, 1), allocatedRun(t, h, 2)
	copied := first
	if first.Validate() != nil || first.HistoryID() != h || !first.Equal(copied) || first == second {
		t.Fatal("allocated run identity or scope")
	}
	if (session.RunID{}).Validate() == nil {
		t.Fatal("zero run identity validated")
	}
}

func TestOccurrenceIDDecompositionAndReconstruction(t *testing.T) {
	h := newSessionHistory(t)
	s := boundSession(t, h, sessionPolicy())
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	allocatedRun, err := b.StartRun()
	mustBatch(t, err)
	allocatedInput, err := b.AddUserInput(allocatedRun, session.UserInput{})
	mustBatch(t, err)
	mustBatch(t, b.ReplaceModelBinding(allocatedRun.HistoryID(), session.ModelBinding{Route: "r", API: "a", Model: "m", Settings: []byte("{}")}))
	submitBatch(t, s, b)
	b = beginBatch(t, s)
	head := s.Snapshot().Head()
	requestID, err := b.AddModelRequest(allocatedRun, session.ModelRequest{Source: session.SourceHead{Sequence: head.Sequence, CID: head.CID}, Recipe: session.ConstructionRecipe{Algorithm: "fixture", Version: 1, Check: []byte{1}}})
	mustBatch(t, err)
	submitBatch(t, s, b)
	b = beginBatch(t, s)
	mustBatch(t, b.AddModelResponse(requestID, session.ModelResponse{Terminal: session.ResponseTerminalNormal, Items: []session.ResponseItem{{Call: &session.RequestedCall{Name: "call", Arguments: []byte("{}")}}}}))
	submitBatch(t, s, b)
	allocatedRequest := requestID
	response, ok := s.Snapshot().RecordAt(s.Snapshot().RecordCount() - 1).ModelResponse()
	if !ok {
		t.Fatal("submitted response missing")
	}
	allocatedCall := response.CallAt(0).ID()
	if got := mustRunID(t, allocatedRun.HistoryID(), allocatedRun.HistoryRelative()); got != allocatedRun {
		t.Fatal("allocated run round trip")
	}
	if got := mustUserInputID(t, allocatedInput.RunID(), allocatedInput.RunRelative()); got != allocatedInput {
		t.Fatal("allocated input round trip")
	}
	if got := mustModelRequestID(t, allocatedRequest.RunID(), allocatedRequest.RunRelative()); got != allocatedRequest {
		t.Fatal("allocated request round trip")
	}
	if got := mustRequestedCallID(t, allocatedCall.RequestID(), allocatedCall.RequestRelative()); got != allocatedCall {
		t.Fatal("allocated call round trip")
	}
	s, err = session.Load(t.Context(), h, allocatedRun.HistoryID(), sessionPolicy())
	mustBatch(t, err)

	loadedRun := s.Snapshot().ActiveRun()
	loadedInput, ok := s.Snapshot().UserInputByID(mustUserInputID(t, loadedRun, 1))
	if !ok {
		t.Fatal("loaded input missing")
	}
	inputID := loadedInput.ID()
	var callID session.RequestedCallID
	for record := range s.Snapshot().Records() {
		if response, ok := record.ModelResponse(); ok {
			callID = response.CallAt(0).ID()
		}
	}
	if callID == (session.RequestedCallID{}) {
		t.Fatal("loaded call missing")
	}
	requestID = callID.RequestID()
	if loadedRun != allocatedRun || inputID != allocatedInput || requestID != allocatedRequest || callID != allocatedCall {
		t.Fatal("loaded occurrence identities differ from allocated values")
	}

	for name, tc := range map[string]struct {
		got  any
		want any
	}{
		"loaded run":     {mustRunID(t, loadedRun.HistoryID(), loadedRun.HistoryRelative()), loadedRun},
		"loaded input":   {mustUserInputID(t, inputID.RunID(), inputID.RunRelative()), inputID},
		"loaded request": {mustModelRequestID(t, requestID.RunID(), requestID.RunRelative()), requestID},
		"loaded call":    {mustRequestedCallID(t, callID.RequestID(), callID.RequestRelative()), callID},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("reconstructed ID = %v; want %v", tc.got, tc.want)
			}
		})
	}

	otherRun := mustRunID(t, historyid.NewHistoryID(), loadedRun.HistoryRelative())
	otherInput := mustUserInputID(t, otherRun, inputID.RunRelative())
	otherRequest := mustModelRequestID(t, otherRun, requestID.RunRelative())
	otherCall := mustRequestedCallID(t, otherRequest, callID.RequestRelative())
	if loadedRun == otherRun || inputID == otherInput || requestID == otherRequest || callID == otherCall {
		t.Fatal("equal components under different parents lost scope")
	}

	missing := mustUserInputID(t, loadedRun, inputID.RunRelative()+1)
	if missing.Validate() != nil {
		t.Fatal("positive reconstructed component is not structurally valid")
	}
	if got, ok := s.Snapshot().UserInputByID(missing); ok || got != (session.UserInputView{}) {
		t.Fatal("structurally valid but unallocated input was admitted")
	}

	for name, tc := range map[string]func() bool{
		"run parent": func() bool {
			id, err := session.NewRunID(historyid.HistoryID{}, 1)
			return err != nil && id == (session.RunID{})
		},
		"run component": func() bool {
			id, err := session.NewRunID(loadedRun.HistoryID(), 0)
			return err != nil && id == (session.RunID{})
		},
		"input parent": func() bool {
			id, err := session.NewUserInputID(session.RunID{}, 1)
			return err != nil && id == (session.UserInputID{})
		},
		"input component": func() bool {
			id, err := session.NewUserInputID(loadedRun, 0)
			return err != nil && id == (session.UserInputID{})
		},
		"request parent": func() bool {
			id, err := session.NewModelRequestID(session.RunID{}, 1)
			return err != nil && id == (session.ModelRequestID{})
		},
		"request component": func() bool {
			id, err := session.NewModelRequestID(loadedRun, 0)
			return err != nil && id == (session.ModelRequestID{})
		},
		"call parent": func() bool {
			id, err := session.NewRequestedCallID(session.ModelRequestID{}, 1)
			return err != nil && id == (session.RequestedCallID{})
		},
		"call component": func() bool {
			id, err := session.NewRequestedCallID(requestID, 0)
			return err != nil && id == (session.RequestedCallID{})
		},
	} {
		t.Run(name, func(t *testing.T) {
			if !tc() {
				t.Fatal("invalid reconstruction did not return its zero identity and an error")
			}
		})
	}

	if (session.RunID{}).HistoryRelative() != 0 || (session.UserInputID{}).RunRelative() != 0 || (session.ModelRequestID{}).RunRelative() != 0 || (session.RequestedCallID{}).RequestRelative() != 0 {
		t.Fatal("zero identity relative component")
	}
}

func mustRunID(t testing.TB, historyID historyid.HistoryID, component uint64) session.RunID {
	t.Helper()
	id, err := session.NewRunID(historyID, component)
	mustBatch(t, err)
	return id
}

func mustUserInputID(t testing.TB, runID session.RunID, component uint64) session.UserInputID {
	t.Helper()
	id, err := session.NewUserInputID(runID, component)
	mustBatch(t, err)
	return id
}

func mustModelRequestID(t testing.TB, runID session.RunID, component uint64) session.ModelRequestID {
	t.Helper()
	id, err := session.NewModelRequestID(runID, component)
	mustBatch(t, err)
	return id
}

func mustRequestedCallID(t testing.TB, requestID session.ModelRequestID, component uint64) session.RequestedCallID {
	t.Helper()
	id, err := session.NewRequestedCallID(requestID, component)
	mustBatch(t, err)
	return id
}
func TestOccurrenceIDsExposeOnlyIdentityOperations(t *testing.T) {
	types := []reflect.Type{reflect.TypeFor[session.RunID](), reflect.TypeFor[session.UserInputID](), reflect.TypeFor[session.ModelRequestID](), reflect.TypeFor[session.RequestedCallID]()}
	for _, typ := range types {
		t.Run(typ.Name(), func(t *testing.T) {
			if !typ.Comparable() {
				t.Fatal("occurrence identity is not comparable")
			}
			for i := range typ.NumField() {
				field := typ.Field(i)
				if field.IsExported() {
					t.Errorf("public occurrence component: %s", field.Name)
				}
			}
			for _, receiver := range []reflect.Type{typ, reflect.PointerTo(typ)} {
				for i := range receiver.NumMethod() {
					switch name := receiver.Method(i).Name; name {
					case "Equal", "Validate", "HistoryID", "HistoryRelative", "RunID", "RunRelative", "RequestID", "RequestRelative", "String":
					default:
						t.Errorf("non-identity public operation: %s", name)
					}
				}
			}
			for _, other := range types {
				if typ != other && typ.ConvertibleTo(other) {
					t.Errorf("convertible identity kinds: %s and %s", typ, other)
				}
				if typ != other && typ.AssignableTo(other) {
					t.Errorf("interchangeable identity kinds: %s and %s", typ, other)
				}
			}
		})
	}
}
