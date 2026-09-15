package microvm

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/control"
)

func TestLifecycleResolverRejectsInvalidForeignStaleDestroyedAndIncompatibleRefs(t *testing.T) {
	t.Parallel()
	base := EnvironmentRecord{
		State: EnvironmentReady, Owner: "caller:alice", EnvironmentID: "env-1",
		Ref: EnvironmentRef{Kind: Kind, ID: "env-1@7"}, Generation: 7,
		Agreement: control.Agreement{Version: control.ProtocolVersion, Capabilities: control.RequiredCapabilities(), MaxMessageBytes: control.DefaultMaxMessageBytes},
	}
	tests := []struct {
		name   string
		ref    EnvironmentRef
		owner  string
		mutate func(*EnvironmentRecord)
		want   error
	}{
		{name: "invalid", ref: EnvironmentRef{Kind: Kind, ID: "not-generation-fenced"}, owner: base.Owner, want: ErrInvalidEnvironmentRef},
		{name: "unknown", ref: base.Ref, owner: base.Owner, mutate: func(record *EnvironmentRecord) { *record = EnvironmentRecord{} }, want: ErrEnvironmentUnknown},
		{name: "foreign", ref: base.Ref, owner: "caller:bob", want: ErrEnvironmentForeign},
		{name: "generation mismatch", ref: base.Ref, owner: base.Owner, mutate: func(record *EnvironmentRecord) { record.Generation++ }, want: ErrEnvironmentStale},
		{name: "destroyed", ref: base.Ref, owner: base.Owner, mutate: func(record *EnvironmentRecord) { record.State = EnvironmentDestroyed }, want: ErrEnvironmentDestroyed},
		{name: "incompatible", ref: base.Ref, owner: base.Owner, mutate: func(record *EnvironmentRecord) { record.Agreement.Version++ }, want: ErrEnvironmentIncompatible},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			record := cloneEnvironmentRecord(base)
			if tc.mutate != nil {
				tc.mutate(&record)
			}
			resolver := NewResolver(fakeRegistryReader{record: record})
			if _, err := resolver.Resolve(context.Background(), tc.ref, tc.owner); !errors.Is(err, tc.want) {
				t.Fatalf("Resolve() error = %v, want %v", err, tc.want)
			}
		})
	}
}

type fakeRegistryReader struct{ record EnvironmentRecord }

func (f fakeRegistryReader) Lookup(context.Context, string) (EnvironmentRecord, error) {
	if f.record.EnvironmentID == "" {
		return EnvironmentRecord{}, ErrEnvironmentUnknown
	}
	return f.record, nil
}
