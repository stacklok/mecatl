package scrollback

func cloneStrings(in []string) []string { return append([]string(nil), in...) }

func cloneCall(in ToolCall) ToolCall {
	in.Artifacts = cloneArtifacts(in.Artifacts)
	return in
}

func cloneResult(in ToolResult) ToolResult {
	in.Artifacts = cloneArtifacts(in.Artifacts)
	return in
}

func cloneArtifacts(in []Artifact) []Artifact {
	out := append([]Artifact(nil), in...)
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
	return append([]TraceEntry(nil), in...)
}

func cloneSubagentStart(in SubagentStart) SubagentStart {
	in.Routing = cloneRouting(in.Routing)
	return in
}

func cloneSubagentUpdate(in SubagentUpdate) SubagentUpdate {
	in.Trace = cloneTrace(in.Trace)
	in.Artifacts = cloneArtifacts(in.Artifacts)
	return in
}

func cloneTeamLane(in TeamLane) TeamLane {
	in.Routing = cloneRouting(in.Routing)
	in.Trace = cloneTrace(in.Trace)
	return in
}

func cloneTeamUpdate(in TeamUpdate) TeamUpdate {
	in.Lanes = append([]TeamLane(nil), in.Lanes...)
	for i := range in.Lanes {
		in.Lanes[i] = cloneTeamLane(in.Lanes[i])
	}
	in.Tasks = append([]Task(nil), in.Tasks...)
	for i := range in.Tasks {
		in.Tasks[i].Dependencies = cloneStrings(in.Tasks[i].Dependencies)
	}
	in.Findings = append([]Finding(nil), in.Findings...)
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
