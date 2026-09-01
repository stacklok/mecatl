package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
)

const learningDedupeTTL = 24 * time.Hour

func automaticTrajectoryDigest(principal string, in learning.Input) (string, error) {
	tr := in.Trajectory
	messages := tr.Messages
	if tr.Current.Valid(len(messages)) {
		messages = messages[tr.Current.Start:tr.Current.End]
	}
	canonical := learning.NewTrajectory(tr.SessionID, "", tr.Stop, tr.Usage, messages)
	canonical.Kind, canonical.Counters = tr.Kind, tr.Counters
	canonical.Current = learning.MessageSpan{Start: 0, End: len(messages)}
	projected, err := learning.ProjectInput(learning.NewInput(canonical, nil, nil, nil))
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(struct {
		Principal string `json:"principal"`
		Kind      string `json:"kind"`
		Counters  any    `json:"counters"`
		Usage     any    `json:"usage"`
		Evidence  any    `json:"evidence"`
	}{principal, string(tr.Kind), tr.Counters, tr.Usage, projected})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
