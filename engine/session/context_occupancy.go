package session

// ContextOccupancy is the latest display-only context-meter numerator from a
// completed agent-loop turn. It is neither durable TokenUsage nor a run budget
// baseline.
type ContextOccupancy struct {
	InputTokens int
	Estimated   bool
}

// LatestContextOccupancy returns the latest non-zero display meter recorded for
// this session. Its absence means no completed turn has established one yet.
func (s *Session) LatestContextOccupancy() (ContextOccupancy, bool) {
	if s.latestContextOccupancy == nil {
		return ContextOccupancy{}, false
	}
	return *s.latestContextOccupancy, true
}

// RecordLatestContextOccupancy records the display meter from a completed
// agent-loop turn. A zero input count is unknown display state and retains the
// prior value rather than creating or clearing one.
func (s *Session) RecordLatestContextOccupancy(occupancy ContextOccupancy) {
	if occupancy.InputTokens == 0 {
		return
	}
	s.latestContextOccupancy = &occupancy
}
