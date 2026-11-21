package modelrequest

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestHistoryMetadataExcludedFromLogicalRequest(t *testing.T) {
	var original Request
	var originalBytes []byte
	var originalHead history.CommitRef
	for _, variant := range []struct {
		name     string
		stamp    int64
		instance uint64
	}{{"epoch", 0, 1}, {"timestamp", -100, 1}, {"writer", 0, 99}, {"both", 123, 7}} {
		t.Run(variant.name, func(t *testing.T) {
			// Author identical logical facts and scope in independent histories.
			forbidden := false
			config := session.Config{InstanceID: variant.instance, Now: func() time.Time {
				if forbidden {
					t.Fatal("logical construction, reconstruction or Load sampled clock")
				}
				return time.Unix(0, variant.stamp)
			}}
			f := newConstructionFixtureWithConfig(t, config)
			h, s, b, run := f.h, f.s, f.b, f.run
			scope := run.SessionID()
			root := session.RootInstructions{Blocks: []session.InstructionBlock{instruction("same root")}}
			requireConstruction(t, b.ReplaceRootInstructions(scope, root))
			input, err := b.AddUserInput(run, session.UserInput{Parts: textParts("selected", "nonempty sibling")})
			requireConstruction(t, err)
			requireConstruction(t, b.AddEffectiveInput(run, session.EffectiveInput{Inputs: []session.InputItem{{UserInputID: new(input.RunRelative())}}}))
			proposed, err := b.View()
			requireConstruction(t, err)
			forbidden = true
			built, err := Construct(proposed, format.ParametersV1{}, testLimits())
			requireConstruction(t, err)
			wire, err := codec.Encode(built.Request().MutableClone(), testLimits())
			requireConstruction(t, err)
			if original.CID() == (contentid.CID{}) {
				original, originalBytes = built.Request(), wire
			} else if built.Request().CID() != original.CID() || !bytes.Equal(wire, originalBytes) {
				t.Fatal("diagnostic history metadata changed logical canonical bytes or CID")
			}
			forbidden = false
			head, err := s.SubmitCommit(t.Context(), b)
			requireConstruction(t, err)
			if originalHead == (history.CommitRef{}) {
				originalHead = head
			} else if head.CID == originalHead.CID {
				t.Fatal("changed history metadata did not change history CID")
			}
			accepted := s.Snapshot()
			forbidden = true
			after, err := Construct(accepted, format.ParametersV1{}, testLimits())
			requireConstruction(t, err)
			if after.Request().CID() != original.CID() {
				t.Fatal("accepted association changed logical construction")
			}
			if _, ok := proposed.RecordAt(0).Commit(); ok {
				t.Fatal("construction source proposal acquired accepted metadata")
			}
			if metadata, ok := accepted.RecordAt(0).Commit(); !ok || metadata.Ref() != head || metadata.Timestamp() != time.Unix(0, variant.stamp).UTC() || metadata.InstanceID() != variant.instance {
				t.Fatal("accepted history metadata not inspectable")
			}

			b, err = s.BeginCommit()
			requireConstruction(t, err)
			stale, err := b.View()
			requireConstruction(t, err)
			forbidden = false
			// Identical logical content is still a new occurrence. Metadata must
			// not weaken same-batch capture freshness or mutate older captures.
			requireConstruction(t, b.ReplaceRootInstructions(scope, root))
			forbidden = true
			if _, err := b.AddModelRequest(run, stale, session.ModelRequest{Recipe: after.Recipe()}); !errors.Is(err, session.ErrIdentity) {
				t.Fatalf("stale logically-equivalent capture accepted: %v", err)
			}
			fresh, err := b.View()
			requireConstruction(t, err)
			current, err := Construct(fresh, format.ParametersV1{}, testLimits())
			requireConstruction(t, err)
			if current.Request().CID() != original.CID() {
				t.Fatal("fresh capture changed identical logical content")
			}
			forbidden = false
			request, err := b.AddModelRequest(run, fresh, session.ModelRequest{Recipe: current.Recipe()})
			requireConstruction(t, err)
			// Reconstruction must use the captured prefix, not later same-batch facts.
			requireConstruction(t, b.ReplaceModelBinding(scope, session.ModelBinding{Route: "later", API: "later", Model: "later"}))
			_, err = s.SubmitCommit(t.Context(), b)
			requireConstruction(t, err)
			forbidden = true
			loadConfig := session.Config{Policy: f.policy, InstanceID: 999, Now: config.Now}
			loaded, err := session.Load(t.Context(), h, scope, loadConfig)
			requireConstruction(t, err)
			for _, snapshot := range []session.Snapshot{s.Snapshot(), loaded.Snapshot()} {
				reconstructed, err := Reconstruct(snapshot, request, testLimits())
				requireConstruction(t, err)
				wire, err := codec.Encode(reconstructed.MutableClone(), testLimits())
				requireConstruction(t, err)
				if reconstructed.CID() != original.CID() || !bytes.Equal(wire, originalBytes) {
					t.Fatal("metadata or later context changed reconstructed logical bytes/CID")
				}
			}
		})
	}
}
