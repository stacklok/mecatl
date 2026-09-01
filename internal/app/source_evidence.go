package app

import (
	"context"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
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
}

type canonicalEvidenceReflector interface {
	ReflectProjection(context.Context, learning.Projection) (learning.Outcome, error)
}

func reflectAttemptEvidence(ctx context.Context, loader *learningEvidenceLoader, reflector canonicalEvidenceReflector, partition learning.AttemptPartition, attempt learning.AttemptRecord) (learning.Outcome, learning.AttemptFailureCode, error) {
	if reflector == nil {
		return learning.Outcome{}, learning.FailureEvidenceUnavailable, nil
	}
	projection, failure := loader.Load(ctx, partition, attempt)
	if failure != learning.FailureNone {
		return learning.Outcome{}, failure, nil
	}
	outcome, err := reflector.ReflectProjection(ctx, projection)
	return outcome, learning.FailureNone, err
}

func newLearningEvidenceLoader(sessions port.SessionStore, events port.EventLog) *learningEvidenceLoader {
	return &learningEvidenceLoader{sessions: sessions, events: events}
}

// Load reconstructs evidence using the attempt's repository partition as the
// private owner authority. Caller identity in ctx is deliberately irrelevant:
// a worker cannot substitute either a user or system principal for that binding.
func (l *learningEvidenceLoader) Load(ctx context.Context, partition learning.AttemptPartition, attempt learning.AttemptRecord) (learning.Projection, learning.AttemptFailureCode) {
	_, projection, failure := l.loadInput(ctx, partition, attempt)
	return projection, failure
}

//nolint:gocyclo // the fail-closed source, owner, run, archive, and digest checks form one boundary
func (l *learningEvidenceLoader) loadInput(ctx context.Context, partition learning.AttemptPartition, attempt learning.AttemptRecord) (learning.Input, learning.Projection, learning.AttemptFailureCode) {
	unavailable := func() (learning.Input, learning.Projection, learning.AttemptFailureCode) {
		return learning.Input{}, learning.Projection{}, learning.FailureEvidenceUnavailable
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
		!sess.Owner.IdentityWellFramed() {
		return unavailable()
	}
	ownerPartition, err := learning.DeriveAttemptPartition(reflectionPrincipal(sess.Owner))
	if err != nil || ownerPartition != partition {
		return unavailable()
	}

	runEvents, terminal, _, ok := l.readExactRun(ctx, provenance.Source)
	if !ok {
		return unavailable()
	}
	reconstructed, err := eventsource.Fold(eventsource.SessionMeta{
		ID:              sess.ID,
		Mode:            sess.Mode,
		Limits:          sess.Limits,
		Workspace:       sess.Workspace,
		Profile:         sess.Profile,
		ProviderID:      sess.ProviderID,
		ModelID:         sess.ModelID,
		ReasoningEffort: sess.ReasoningEffort,
		Kind:            sess.Kind,
		Relationship:    sess.Relationship,
		CreatedAt:       sess.CreatedAt,
	}, func(yield func(session.Event, error) bool) {
		for _, event := range runEvents {
			if !yield(event, nil) {
				return
			}
		}
	})
	if err != nil || reconstructed == nil || len(reconstructed.Conversation.Messages) == 0 ||
		len(reconstructed.Conversation.Messages) > learning.MaxInputMessages {
		return unavailable()
	}
	persistedStop, stopRecorded := reconstructed.RecordedStopReason()
	if !stopRecorded || persistedStop != terminal.Stop {
		return unavailable()
	}

	trajectory := learning.NewTrajectory(sess.ID, sess.Workspace, terminal.Stop, terminal.Usage, reconstructed.Conversation.Messages)
	trajectory.RunID = string(provenance.Source.RunID)
	trajectory.Kind = sess.Kind
	trajectory.Counters = reconstructed.Counters
	input := learning.NewInput(trajectory, runEvents, nil, nil)
	prompt := provenance.CurrentPrompt
	promptOrdinal := -1
	for i, message := range trajectory.Messages {
		if !session.IsGenuineUserPrompt(message) {
			continue
		}
		ref, refErr := learning.MessageEvidenceRef(input, i, "")
		if refErr == nil && learning.CanonicalDigest(ref.Digest) == prompt.Digest {
			if promptOrdinal >= 0 {
				return unavailable()
			}
			promptOrdinal = i
		}
	}
	if promptOrdinal < 0 {
		return unavailable()
	}
	trajectory.Current = learning.MessageSpan{Start: promptOrdinal, End: len(trajectory.Messages)}
	input = learning.NewInput(trajectory, runEvents, nil, nil)
	digest, err := automaticTrajectoryDigest(input)
	if err != nil || learning.CanonicalDigest(digest) != provenance.Source.CanonicalDigest {
		return unavailable()
	}
	signals := learning.DetectSignals(input)
	if provenance.Class == learning.AdmissionHostRequested {
		signals = append(signals, learning.Signal{Kind: learning.SignalHostRequested})
	}
	input = learning.NewInput(trajectory, runEvents, signals, nil)
	projection, err := learning.ProjectInput(input)
	if err != nil {
		return unavailable()
	}
	return input, projection, learning.FailureNone
}

//nolint:gocyclo // exact-run ordering and compaction form a deliberately closed event state machine
func (l *learningEvidenceLoader) readExactRun(ctx context.Context, source learning.AttemptSource) ([]session.Event, session.ResultPayload, bool, bool) {
	events := make([]session.Event, 0, 16)
	var terminal session.ResultPayload
	var previous int64
	seenRun, leftRun, seenTerminal := false, false, false
	compactionPending, archiveOK := false, false
	read := 0

	consume := func(event session.Event) (done, valid bool) {
		if event.RunID != string(source.RunID) {
			if seenRun && event.RunID != "" {
				leftRun = true
			}
			return false, true
		}
		read++
		if read > learning.MaxInputEvents {
			return false, false
		}
		if leftRun || event.Seq <= 0 || !seenRun && event.Seq != 1 || seenRun && event.Seq != previous+1 || seenTerminal {
			return false, false
		}
		seenRun = true
		previous = event.Seq
		switch event.Type {
		case session.EvCompaction:
			if compactionPending {
				return false, false
			}
			compactionPending = true
		case session.EvCompactionArchive:
			if !compactionPending || event.CompactionArchive == nil || len(event.CompactionArchive.Replaced) == 0 ||
				len(event.CompactionArchive.Replaced) > learning.MaxInputMessages || session.ValidateToolPairing(event.CompactionArchive.Replaced) != nil {
				return false, false
			}
			compactionPending = false
			archiveOK = true
		case session.EvResult:
			if compactionPending || event.Result == nil || event.Result.Stop == session.StopNone {
				return false, false
			}
			terminal = *event.Result
			seenTerminal = true
		}
		events = append(events, event)
		return seenTerminal, true
	}

	if cursorLog, ok := l.events.(port.CursorEventLog); ok {
		for record, err := range cursorLog.ReadAfter(ctx, source.SessionID, "", port.ReadOptions{}) {
			if err != nil {
				return nil, session.ResultPayload{}, false, false
			}
			switch record.Kind {
			case port.LogRecordGap:
				if seenRun {
					return nil, session.ResultPayload{}, false, false
				}
				continue
			case port.LogRecordEvent:
			default:
				return nil, session.ResultPayload{}, false, false
			}
			done, valid := consume(record.Event)
			if !valid {
				return nil, session.ResultPayload{}, false, false
			}
			if done {
				return events, terminal, archiveOK, true
			}
		}
		return nil, session.ResultPayload{}, false, false
	}

	for event, err := range l.events.Read(ctx, source.SessionID) {
		if err != nil {
			return nil, session.ResultPayload{}, false, false
		}
		done, valid := consume(event)
		if !valid {
			return nil, session.ResultPayload{}, false, false
		}
		if done {
			return events, terminal, archiveOK, true
		}
	}
	return nil, session.ResultPayload{}, false, false
}
