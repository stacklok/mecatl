package scrollback

import "slices"

func cloneStrings(in []string) []string { return slices.Clone(in) }

func cloneCall(in ToolCall) ToolCall {
	in.Artifacts = cloneArtifacts(in.Artifacts)
	return in
}

func cloneResult(in ToolResult) ToolResult {
	in.Artifacts = cloneArtifacts(in.Artifacts)
	return in
}

func cloneArtifacts(in []Artifact) []Artifact {
	out := slices.Clone(in)
	for i := range out {
		out[i].Data = append([]byte(nil), out[i].Data...)
	}
	return out
}

func cloneRouting(in RoutingDecision) RoutingDecision {
	out := in
	if in.Confidence != nil {
		out.Confidence = Float64(*in.Confidence)
	}
	if in.MinimumConfidence != nil {
		out.MinimumConfidence = Float64(*in.MinimumConfidence)
	}
	return out
}

func cloneTrace(in []TraceEntry) []TraceEntry {
	if len(in) > MaxTraceEntries {
		in = in[len(in)-MaxTraceEntries:]
	}
	return slices.Clone(in)
}

func cloneSubagentTrace(in []TraceEntry) []TraceEntry {
	// A parent card can contain several resumed child sessions. Bound each
	// session independently without changing the interleaved event order.
	type counts struct{ tools, messages int }
	perLane := make(map[string]counts)
	out := make([]TraceEntry, 0, min(len(in), MaxTraceEntries))
	for i := len(in) - 1; i >= 0; i-- {
		entry := in[i]
		count := perLane[entry.Lane]
		if entry.Kind == "message" {
			if count.messages >= 12 {
				continue
			}
			count.messages++
		} else {
			if count.tools >= 128 {
				continue
			}
			count.tools++
		}
		perLane[entry.Lane] = count
		out = append(out, entry)
	}
	slices.Reverse(out)
	return out
}

func cloneSubagentStart(in SubagentStart) SubagentStart {
	in.Routing = cloneRouting(in.Routing)
	return in
}

func cloneSubagentUpdate(in SubagentUpdate) SubagentUpdate {
	in.Trace = cloneSubagentTrace(in.Trace)
	in.Artifacts = cloneArtifacts(in.Artifacts)
	return in
}

func cloneTeamLane(in TeamLane) TeamLane {
	in.Routing = cloneRouting(in.Routing)
	in.Trace = cloneTrace(in.Trace)
	return in
}

func cloneTeamLanes(in []TeamLane) []TeamLane {
	out := slices.Clone(in)
	for i := range out {
		out[i] = cloneTeamLane(out[i])
	}
	return out
}

func cloneTeamUpdate(in TeamUpdate) TeamUpdate {
	in.Lanes = cloneTeamLanes(in.Lanes)
	in.Tasks = slices.Clone(in.Tasks)
	for i := range in.Tasks {
		in.Tasks[i].Dependencies = cloneStrings(in.Tasks[i].Dependencies)
	}
	in.Findings = slices.Clone(in.Findings)
	return in
}

func clonePayload(in PayloadSnapshot) PayloadSnapshot {
	switch payload := in.(type) {
	case UserCardSnapshot:
		payload.Media = cloneStrings(payload.Media)
		return payload
	case AssistantCardSnapshot:
		return payload
	case ToolCardSnapshot:
		payload.Call = cloneCall(payload.Call)
		payload.Result = cloneResult(payload.Result)
		return payload
	case SubagentCardSnapshot:
		payload.Call = cloneCall(payload.Call)
		payload.Result = cloneResult(payload.Result)
		payload.Start = cloneSubagentStart(payload.Start)
		payload.Update = cloneSubagentUpdate(payload.Update)
		return payload
	case TeamCardSnapshot:
		payload.Call = cloneCall(payload.Call)
		payload.Result = cloneResult(payload.Result)
		payload.Update = cloneTeamUpdate(payload.Update)
		return payload
	case NoticeCardSnapshot:
		return payload
	case TurnStatCardSnapshot:
		return payload
	case ErrorCardSnapshot:
		return payload
	case HookCardSnapshot:
		return payload
	case DeliveryCardSnapshot:
		return payload
	default:
		panic("scrollback: unknown payload")
	}
}
