package sessnap

import (
	"encoding/json"

	"github.com/stacklok/mecatl/engine/session"
)

// DecodeLegacyArchive migrates an archive as an independent complete source. Its
// namespace must identify the original archive record, never a live/snapshot ID
// crosswalk. Private provider replay stays internal to the returned messages.
func DecodeLegacyArchive(data []byte, inc session.IncarnationID, namespace string) ([]session.Message, error) {
	var archive struct {
		Replaced []struct {
			Role                                      session.Role
			Text                                      string
			ToolCalls                                 []legacyCallDTO
			ToolResult                                *legacyResultDTO
			Reasoning, ReasoningItemID, ProviderPhase string
			Parts                                     []session.Content
			UserPromptProvenance                      session.UserPromptProvenance
		}
	}
	if err := json.Unmarshal(data, &archive); err != nil {
		return nil, err
	}
	old := make([]legacyMessageDTO, len(archive.Replaced))
	for i, m := range archive.Replaced {
		old[i] = legacyMessageDTO{Role: m.Role, Text: m.Text, ToolCalls: m.ToolCalls, ToolResult: m.ToolResult, Reasoning: m.Reasoning, ReasoningItemID: m.ReasoningItemID, Phase: m.ProviderPhase, Parts: contentToDTO(m.Parts), UserPromptProvenance: m.UserPromptProvenance}
	}
	dtos, err := migrateLegacyMessages(old, inc, namespace)
	if err != nil {
		return nil, err
	}
	messages := make([]session.Message, len(dtos))
	for i, dto := range dtos {
		messages[i] = fromDTO(dto)
	}
	// An archive is closed history, not a parked continuation. Incomplete calls
	// cannot become executable calls or public normal tool cards.
	if err := session.ValidateToolPairing(messages); err != nil {
		return nil, err
	}
	return session.CloneMessages(messages), nil
}
