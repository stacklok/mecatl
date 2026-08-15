package learning

import (
	"strings"

	"github.com/stacklok/mecatl/engine/session"
)

const substantialSuccessfulTools = 3

// DetectSignals returns only signals structurally defensible within this input.
// It never invents cross-session repetition or contradiction; hosts supply those
// explicitly through Input.Signals.
func DetectSignals(in Input) []Signal {
	if err := ValidateInput(in); err != nil {
		return nil
	}
	var signals []Signal
	if refs := substantialSuccess(in); len(refs) > 0 {
		signals = append(signals, Signal{Kind: SignalSubstantialSuccess, Evidence: refs})
	}
	if refs := failureRecovery(in); len(refs) > 0 {
		signals = append(signals, Signal{Kind: SignalFailureRecovery, Evidence: refs})
	}
	if refs := repeatedSequence(in); len(refs) > 0 {
		signals = append(signals, Signal{Kind: SignalRepeatedToolSequence, Evidence: refs})
	}
	if refs := explicitRemember(in); len(refs) > 0 {
		signals = append(signals, Signal{Kind: SignalExplicitRemember, Evidence: refs})
	}
	if refs := explicitLearnProcedure(in); len(refs) > 0 {
		signals = append(signals, Signal{Kind: SignalExplicitLearnProcedure, Evidence: refs})
	}
	if refs := repeatedCorrections(in); len(refs) > 0 {
		signals = append(signals, Signal{Kind: SignalRepeatedCorrection, Evidence: refs})
	}
	return signals
}

// DetectSignalsScoped returns deterministic standard signals whose complete
// pattern evidence belongs to the genuine current run. Trusted host signals are
// deliberately limited to contradiction and host-requested provenance; callers
// cannot forge built-in evidence classes.
func DetectSignalsScoped(in Input, scope DetectionScope) []Signal {
	if err := ValidateInput(in); err != nil || !scope.Current.Valid(len(in.Trajectory.Messages)) {
		return nil
	}
	current := in.Trajectory.Messages[scope.Current.Start:scope.Current.End]
	trajectory := NewTrajectory(in.Trajectory.SessionID, in.Trajectory.Workspace, in.Trajectory.Stop, in.Trajectory.Usage, current)
	trajectory.Principal = in.Trajectory.Principal.Clone()
	trajectory.Kind = in.Trajectory.Kind
	trajectory.Counters = in.Trajectory.Counters
	trajectory.Current = MessageSpan{Start: 0, End: len(current)}
	currentInput := NewInput(trajectory, nil, nil, in.Existing)
	detected := DetectSignals(currentInput)
	for i := range detected {
		for j := range detected[i].Evidence {
			detected[i].Evidence[j].Ordinal += scope.Current.Start
		}
	}
	all := make([]Signal, 0, len(in.Signals)+len(detected))
	for _, signal := range in.Signals {
		if (signal.Kind == SignalContradiction || signal.Kind == SignalHostRequested) && signalHasOnlyCurrentEvidence(signal, scope.Current) {
			all = append(all, signal)
		}
	}
	all = append(all, detected...)
	order := []SignalKind{
		SignalExplicitRemember, SignalExplicitLearnProcedure,
		SignalRepeatedCorrection, SignalContradiction, SignalFailureRecovery,
		SignalRepeatedToolSequence, SignalSubstantialSuccess, SignalHostRequested,
	}
	result := make([]Signal, 0, len(all))
	for _, kind := range order {
		for _, signal := range all {
			if signal.Kind == kind {
				result = append(result, signal)
				break
			}
		}
	}
	return result
}

func signalHasOnlyCurrentEvidence(signal Signal, span MessageSpan) bool {
	if len(signal.Evidence) == 0 {
		return signal.Kind == SignalContradiction || signal.Kind == SignalHostRequested
	}
	for _, ref := range signal.Evidence {
		if ref.Locator != EvidenceMessage || !span.Contains(ref.Ordinal) {
			return false
		}
	}
	return true
}

func substantialSuccess(in Input) []EvidenceRef {
	if in.Trajectory.Stop != session.StopEndTurn {
		return nil
	}
	refs := make([]EvidenceRef, 0, substantialSuccessfulTools+1)
	finalAssistant := -1
	for i, message := range in.Trajectory.Messages {
		if message.Role == session.RoleAssistant && strings.TrimSpace(message.Text) != "" {
			finalAssistant = i
		}
		if message.Role != session.RoleTool || message.ToolResult == nil {
			continue
		}
		if message.ToolResult.IsError {
			return nil
		}
		ref, err := MessageEvidenceRef(in, i, message.ToolResult.CallID)
		if err == nil {
			refs = append(refs, ref)
		}
	}
	if len(refs) < substantialSuccessfulTools || finalAssistant < 0 {
		return nil
	}
	finalRef, err := MessageEvidenceRef(in, finalAssistant, "")
	if err != nil {
		return nil
	}
	refs = append(refs, finalRef)
	return refs[:min(len(refs), MaxCandidateEvidence)]
}

func failureRecovery(in Input) []EvidenceRef {
	callNames := make(map[session.ToolCallID]string)
	failedByName := make(map[string]EvidenceRef)
	for i, message := range in.Trajectory.Messages {
		for _, call := range message.ToolCalls {
			callNames[call.ID] = call.Name
		}
		if message.Role != session.RoleTool || message.ToolResult == nil {
			continue
		}
		name := callNames[message.ToolResult.CallID]
		if name == "" {
			continue
		}
		ref, err := MessageEvidenceRef(in, i, message.ToolResult.CallID)
		if err != nil {
			continue
		}
		if message.ToolResult.IsError {
			failedByName[name] = ref
			continue
		}
		if failed, ok := failedByName[name]; ok {
			return []EvidenceRef{failed, ref}
		}
	}
	return nil
}

func repeatedSequence(in Input) []EvidenceRef {
	type occurrence struct {
		key string
		ref EvidenceRef
	}
	var occurrences []occurrence
	for i, message := range in.Trajectory.Messages {
		if message.Role != session.RoleAssistant || len(message.ToolCalls) < 2 {
			continue
		}
		names := make([]string, len(message.ToolCalls))
		for j, call := range message.ToolCalls {
			names[j] = call.Name
		}
		ref, err := MessageEvidenceRef(in, i, "")
		if err == nil {
			occurrences = append(occurrences, occurrence{key: strings.Join(names, "\x00"), ref: ref})
		}
	}
	first := make(map[string]EvidenceRef)
	for _, occurrence := range occurrences {
		if ref, ok := first[occurrence.key]; ok {
			return []EvidenceRef{ref, occurrence.ref}
		}
		first[occurrence.key] = occurrence.ref
	}

	// The ordinary loop often emits one tool call per assistant message. Detect a
	// repeated adjacent pair across those turn boundaries as the smallest stable
	// sequence, requiring two non-overlapping occurrences.
	type callOccurrence struct {
		name string
		ref  EvidenceRef
	}
	var calls []callOccurrence
	for i, message := range in.Trajectory.Messages {
		if message.Role != session.RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			ref, err := MessageEvidenceRef(in, i, call.ID)
			if err == nil {
				calls = append(calls, callOccurrence{name: call.Name, ref: ref})
			}
		}
	}
	pairs := make(map[string]int)
	for i := 0; i+1 < len(calls); i++ {
		key := calls[i].name + "\x00" + calls[i+1].name
		if firstIndex, ok := pairs[key]; ok && firstIndex+1 < i {
			return []EvidenceRef{calls[firstIndex].ref, calls[firstIndex+1].ref, calls[i].ref, calls[i+1].ref}
		}
		if _, ok := pairs[key]; !ok {
			pairs[key] = i
		}
	}
	return nil
}

func explicitRemember(in Input) []EvidenceRef {
	for i, message := range in.Trajectory.Messages {
		if message.Role != session.RoleUser {
			continue
		}
		line := strings.ToLower(strings.TrimSpace(message.Text))
		for _, prefix := range []string{"remember that ", "please remember that ", "please remember ", "learn that "} {
			if strings.HasPrefix(line, prefix) && len(strings.TrimSpace(line[len(prefix):])) >= 4 {
				ref, err := MessageEvidenceRef(in, i, "")
				if err == nil {
					return []EvidenceRef{ref}
				}
			}
		}
	}
	return nil
}

func explicitLearnProcedure(in Input) []EvidenceRef {
	for i, message := range in.Trajectory.Messages {
		if message.Role != session.RoleUser {
			continue
		}
		line := strings.ToLower(strings.TrimSpace(message.Text))
		for _, prefix := range []string{"learn this procedure", "learn this workflow", "save this as a skill", "remember this procedure"} {
			if strings.HasPrefix(line, prefix) {
				ref, err := MessageEvidenceRef(in, i, "")
				if err == nil {
					return []EvidenceRef{ref}
				}
			}
		}
	}
	return nil
}

func repeatedCorrections(in Input) []EvidenceRef {
	var refs []EvidenceRef
	for i, message := range in.Trajectory.Messages {
		if message.Role != session.RoleUser || !correctionTurn(message.Text) {
			continue
		}
		ref, err := MessageEvidenceRef(in, i, "")
		if err == nil {
			refs = append(refs, ref)
		}
	}
	if len(refs) < 2 {
		return nil
	}
	return refs[:min(len(refs), MaxCandidateEvidence)]
}

func correctionTurn(text string) bool {
	line := strings.ToLower(strings.TrimSpace(text))
	for _, prefix := range []string{"no, ", "no — ", "no: ", "actually, ", "that's incorrect", "that is incorrect", "you missed ", "i already said "} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}
