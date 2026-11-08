package session

import "testing"

func lookupInternalInput(t testing.TB, v Snapshot, run RunID, ordinal uint64) UserInputView {
	t.Helper()
	id := UserInputID{runID: run, ordinal: ordinal}
	input, ok := v.UserInputByID(id)
	if !ok || input.ID() != id {
		t.Fatalf("missing input %v", id)
	}
	return input
}

func internalUserInputsMatch(v Snapshot, run RunID, count uint64) bool {
	var seen uint64
	for i := 0; i < v.RecordCount(); i++ {
		input, ok := v.RecordAt(i).UserInput()
		if !ok || input.ID().runID != run {
			continue
		}
		seen++
		id := UserInputID{runID: run, ordinal: seen}
		got, present := v.UserInputByID(id)
		if !present || input.ID() != id || got.ID() != id || got != input {
			return false
		}
	}
	_, extra := v.UserInputByID(UserInputID{runID: run, ordinal: count + 1})
	return seen == count && !extra
}

// internalByteParts keeps arbitrary fixture bytes in media, not UTF-8 text.
func internalByteParts(data []byte) []ContentPart {
	if len(data) == 0 {
		return nil
	}
	return []ContentPart{{Media: &Media{Kind: MediaKindDocument, MIMEType: "application/octet-stream", Source: MediaSourceInline, InlineData: data}}}
}
func internalInputBytes(v UserInputView) []byte {
	if v.Parts().Len() == 0 {
		return nil
	}
	media, _ := v.Parts().At(0).Media()
	data, _ := media.InlineData()
	return data.Copy()
}
