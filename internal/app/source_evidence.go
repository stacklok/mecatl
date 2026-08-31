package app

import (
	"context"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// learningEvidenceLoader is the composition-owned exact-source boundary. It
// returns only the canonical learning projection; source snapshots and raw log
// values never leave this type.
type learningEvidenceLoader struct {
	sessions port.SessionStore
	events   port.EventLog
	gaps     learningEvidenceGapOracle
}

type learningEvidenceGapOracle interface {
	LearningEvidenceGap(context.Context, session.SessionID, learning.DurableRunID) (bool, error)
}

func newLearningEvidenceLoader(sessions port.SessionStore, events port.EventLog) *learningEvidenceLoader {
	loader := &learningEvidenceLoader{sessions: sessions, events: events}
	loader.gaps, _ = events.(learningEvidenceGapOracle)
	return loader
}

// Load reconstructs evidence using the attempt's repository partition as the
// private owner authority. Caller identity in ctx is deliberately irrelevant:
// a worker cannot substitute either a user or system principal for that binding.
//
//nolint:gocyclo // the fail-closed source, owner, run, archive, and digest checks form one boundary
func (l *learningEvidenceLoader) Load(ctx context.Context, partition learning.AttemptPartition, attempt learning.AttemptRecord) (learning.Projection, learning.AttemptFailureCode) {
	unavailable := func() (learning.Projection, learning.AttemptFailureCode) {
		return learning.Projection{}, learning.FailureEvidenceUnavailable
	}
	if l == nil || l.sessions == nil || l.events == nil || partition == "" {
		return unavailable()
	}
	provenance := attempt.Provenance
	if err := provenance.Validate(provenance.Source, provenance.CurrentPrompt); err != nil {
		return unavailable()
	}

	sess, err := l.sessions.Load(ctx, provenance.Source.SessionID)
	if err != nil || sess == nil || sess.ID != provenance.Source.SessionID || sess.Owner == nil ||
		!sess.Owner.IdentityWellFramed() || sess.RunID() != string(provenance.Source.RunID) {
		return unavailable()
	}
	if l.gaps != nil {
		gapped, gapErr := l.gaps.LearningEvidenceGap(ctx, provenance.Source.SessionID, provenance.Source.RunID)
		if gapErr != nil || gapped {
			return unavailable()
		}
	}
	ownerPartition, err := learning.DeriveAttemptPartition(reflectionPrincipal(sess.Owner))
	if err != nil || ownerPartition != partition {
		return unavailable()
	}
	if len(sess.Conversation.Messages) == 0 || len(sess.Conversation.Messages) > learning.MaxInputMessages {
		return unavailable()
	}

	runEvents, terminal, archiveOK, ok := l.readExactRun(ctx, provenance.Source)
	persistedStop, stopRecorded := sess.RecordedStopReason()
	if !ok || !stopRecorded || persistedStop != terminal.Stop {
		return unavailable()
	}
	compacted := false
	for _, message := range sess.Conversation.Messages {
		if session.IsSynthesisedSummary(message.Text) {
			compacted = true
			break
		}
	}
	if compacted && !archiveOK {
		return unavailable()
	}

	prompt := provenance.CurrentPrompt
	if prompt.Ordinal < 0 || prompt.Ordinal >= len(sess.Conversation.Messages) ||
		!session.IsGenuineUserPrompt(sess.Conversation.Messages[prompt.Ordinal]) {
		return unavailable()
	}
	trajectory := learning.NewTrajectory(sess.ID, sess.Workspace, terminal.Stop, terminal.Usage, sess.Conversation.Messages)
	trajectory.RunID = sess.RunID()
	trajectory.Kind = sess.Kind
	trajectory.Counters = sess.Counters
	trajectory.Current = learning.MessageSpan{Start: prompt.Ordinal, End: len(trajectory.Messages)}
	input := learning.NewInput(trajectory, runEvents, nil, nil)
	promptRef, err := learning.MessageEvidenceRef(input, prompt.Ordinal, "")
	if err != nil || learning.CanonicalDigest(promptRef.Digest) != prompt.Digest {
		return unavailable()
	}
	digest, err := automaticTrajectoryDigest("", input)
	if err != nil || learning.CanonicalDigest(digest) != provenance.Source.CanonicalDigest {
		return unavailable()
	}
	projection, err := learning.ProjectInput(input)
	if err != nil {
		return unavailable()
	}
	return projection, learning.FailureNone
}

//nolint:gocyclo // exact-run ordering and compaction form a deliberately closed event state machine
func (l *learningEvidenceLoader) readExactRun(ctx context.Context, source learning.AttemptSource) ([]session.Event, session.ResultPayload, bool, bool) {
	events := make([]session.Event, 0, 16)
	var terminal session.ResultPayload
	var previous int64
	seenRun, leftRun, seenTerminal := false, false, false
	compactionPending, archiveOK := false, false
	read := 0
	for event, err := range l.events.Read(ctx, source.SessionID) {
		if err != nil {
			return nil, session.ResultPayload{}, false, false
		}
		read++
		if read > learning.MaxInputEvents {
			return nil, session.ResultPayload{}, false, false
		}
		if event.RunID != string(source.RunID) {
			if seenRun && event.RunID != "" {
				leftRun = true
			}
			continue
		}
		if leftRun || event.Seq <= 0 || seenRun && event.Seq <= previous || seenTerminal {
			return nil, session.ResultPayload{}, false, false
		}
		seenRun = true
		previous = event.Seq
		switch event.Type {
		case session.EvCompaction:
			if compactionPending {
				return nil, session.ResultPayload{}, false, false
			}
			compactionPending = true
		case session.EvCompactionArchive:
			if !compactionPending || event.CompactionArchive == nil || len(event.CompactionArchive.Replaced) == 0 ||
				len(event.CompactionArchive.Replaced) > learning.MaxInputMessages || session.ValidateToolPairing(event.CompactionArchive.Replaced) != nil {
				return nil, session.ResultPayload{}, false, false
			}
			compactionPending = false
			archiveOK = true
		case session.EvResult:
			if compactionPending || event.Result == nil || event.Result.Stop == session.StopNone {
				return nil, session.ResultPayload{}, false, false
			}
			terminal = *event.Result
			seenTerminal = true
		}
		events = append(events, event)
	}
	if !seenRun || !seenTerminal || compactionPending {
		return nil, session.ResultPayload{}, false, false
	}
	return events, terminal, archiveOK, true
}
