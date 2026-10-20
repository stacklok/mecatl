package openai

import (
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
)

const responsesReplayFormat = "openai.responses"
const responsesReplayVersion uint32 = 1

// responsesReplay preserves today's flattened text/last-phase replay, not an
// ordered sequence of message items. Calls and their item IDs travel in LLM DTOs.
// Legacy packed Reasoning items map directly to Reasoning; ProviderPhase maps to
// Phase. Legacy ReasoningItemID must not reactivate bare ciphertext.
type responsesReplay struct {
	Reasoning []reasoningItem `json:"reasoning,omitempty"`
	Phase     string          `json:"phase,omitempty"`
}

// reasoningItem binds encrypted content to its exact provider item ID. After
// counts preceding function calls, retaining reasoning/call interleaving.
type reasoningItem struct {
	ID    string `json:"i"`
	Blob  string `json:"e"`
	After int    `json:"n,omitempty"`
}

func packReplay(env responsesReplay) *session.ProviderReplay {
	if len(env.Reasoning) == 0 && env.Phase == "" {
		return nil
	}
	// Only scalar strings and integers are encoded, so Marshal cannot fail.
	data, _ := json.Marshal(env)
	return &session.ProviderReplay{Format: responsesReplayFormat, Version: responsesReplayVersion, Data: data}
}

// Unknown namespaces/versions are incompatible, not malformed. Never inspect
// another adapter's bytes. Errors deliberately contain no private payload.
func unpackReplay(replay *session.ProviderReplay) (responsesReplay, error) {
	if replay != nil && replay.Format == "mecatl.legacy-message" && replay.Version == 1 {
		return unpackLegacyReplay(replay.Data), nil
	}
	if replay == nil || replay.Format != responsesReplayFormat || replay.Version != responsesReplayVersion {
		return responsesReplay{}, nil
	}
	return unpackCurrentReplay(replay.Data)
}

func unpackLegacyReplay(data []byte) responsesReplay {
	var legacy struct {
		Reasoning string `json:"reasoning"`
		Phase     string `json:"phase"`
	}
	if !utf8.Valid(data) || json.Unmarshal(data, &legacy) != nil {
		return responsesReplay{}
	}
	env := responsesReplay{Phase: legacy.Phase}
	var old struct {
		V     int             `json:"v"`
		Items []reasoningItem `json:"items"`
	}
	if !utf8.ValidString(legacy.Reasoning) || json.Unmarshal([]byte(legacy.Reasoning), &old) != nil || old.V != 1 {
		return env
	}
	for _, item := range old.Items {
		if item.ID == "" || item.Blob == "" || item.After < 0 {
			return env
		}
	}
	env.Reasoning = old.Items
	return env
}

func unpackCurrentReplay(data []byte) (responsesReplay, error) {
	if !utf8.Valid(data) {
		return responsesReplay{}, errors.New("openai: malformed Responses replay envelope")
	}
	var env *responsesReplay
	if err := json.Unmarshal(data, &env); err != nil || env == nil {
		return responsesReplay{}, errors.New("openai: malformed Responses replay envelope")
	}
	previous := 0
	for _, item := range env.Reasoning {
		if item.ID == "" || item.Blob == "" || item.After < previous {
			return responsesReplay{}, errors.New("openai: invalid Responses reasoning replay item")
		}
		previous = item.After
	}
	return *env, nil
}

func compatibleReplay(replay *session.ProviderReplay) bool {
	return replay != nil && replay.Version == 1 && (replay.Format == responsesReplayFormat || replay.Format == "mecatl.legacy-message")
}
