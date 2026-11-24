package sessionruntime

import (
	"reflect"
	"testing"
	"time"

	engineid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/resourcegroup"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

func validFactoryConfig() FactoryConfig {
	limits := codec.Limits{
		MaxBytes: 1, MaxArrayElements: 1, MaxStringBytes: 1,
		MaxByteStringBytes: 1, MaxMapPairs: 1, MaxDepth: 1,
		MaxNodes: 1, MaxCollectionElements: 1,
	}
	return FactoryConfig{
		Session: session.Config{
			Policy: session.Policy{
				CommitLimits: limits, PayloadLimits: limits, MaxRecordsPerCommit: 1,
				SoftMaxRetainedRecords: 1, SoftMaxPayloadBytes: 1, SoftMaxDecodedWork: 1,
				MaxRetainedRecords: 1, MaxPayloadBytes: 1, MaxDecodedWork: 1,
			},
			InstanceID: 1,
			Now:        func() time.Time { panic("unexpected Session.Now") },
		},
		Policy: resourcegroup.FactoryPolicy{
			History: history.Policy{
				CommitLimits: limits, RetryWindow: 1, MaxPageBytes: 1, MaxPageCommits: 1,
			},
			MaxInstances: 1,
		},
	}
}

func TestFactoryConfigValidateAndConstructor(t *testing.T) {
	for name, mutate := range map[string]func(*FactoryConfig){
		"valid poison clock":     func(*FactoryConfig) {},
		"valid nil clock":        func(c *FactoryConfig) { c.Session.Now = nil },
		"zero":                   func(c *FactoryConfig) { *c = FactoryConfig{} },
		"missing writer":         func(c *FactoryConfig) { c.Session.InstanceID = 0 },
		"invalid session policy": func(c *FactoryConfig) { c.Session.Policy.PayloadLimits.MaxBytes = 0 },
		"invalid soft bound":     func(c *FactoryConfig) { c.Session.Policy.SoftMaxDecodedWork = 2 },
		"invalid history":        func(c *FactoryConfig) { c.Policy.History.RetryWindow = 0 },
		"zero instances":         func(c *FactoryConfig) { c.Policy.MaxInstances = 0 },
		"negative instances":     func(c *FactoryConfig) { c.Policy.MaxInstances = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			config := validFactoryConfig()
			mutate(&config)
			want := ErrInvalidConfig
			if name == "valid poison clock" || name == "valid nil clock" {
				want = nil
			}
			if err := config.Validate(); err != want {
				t.Fatalf("Validate() = %v, want %v", err, want)
			}
			if got, err := NewFactory(config); got != nil || err != ErrNotImplemented {
				t.Fatalf("NewFactory() = %v, %v", got, err)
			}
		})
	}
}

func TestFactoryMetadataAndLoad(t *testing.T) {
	key, err := engineid.Parse("SES1111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	wrongKind, err := engineid.Parse("ENV1111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	configured := &Factory{config: validFactoryConfig()}
	for _, factory := range []*Factory{nil, {}, configured} {
		if factory.ResourceType() != reflect.TypeFor[*SessionRuntime]() || factory.Kind() != engineid.Kind("SES") {
			t.Fatal("incorrect exact type or kind")
		}
		wantPolicy := resourcegroup.FactoryPolicy{}
		if factory == configured {
			wantPolicy = configured.config.Policy
		}
		if got := factory.Policy(); got != wantPolicy {
			t.Fatalf("Policy() = %v, want %v", got, wantPolicy)
		}
		policyCopy := factory.Policy()
		policyCopy.MaxInstances++
		if factory.Policy() != wantPolicy {
			t.Fatal("Policy returned mutable storage")
		}
		for _, input := range []engineid.ID{{}, wrongKind, key} {
			got, err := factory.DecodeID(input)
			if input == key {
				if err != nil || got.ResourceID() != key || got.String() != key.String() {
					t.Fatalf("DecodeID() = %v, %v", got, err)
				}
			} else if err == nil || got != (sessionid.SessionID{}) {
				t.Fatalf("invalid DecodeID() = %v, %v", got, err)
			}
			for _, scope := range []*resourcegroup.Scope{nil, {}} {
				loaded, err := factory.Load(scope, got)
				if loaded != (resourcegroup.Loaded[*SessionRuntime]{}) || err != ErrNotImplemented {
					t.Fatalf("Factory.Load() = %v, %v", loaded, err)
				}
			}
		}
	}
}

func TestRuntimeIDForwardsSessionID(t *testing.T) {
	sid, err := sessionid.ParseSessionID("SES1111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &SessionRuntime{sessionID: sid}
	if runtime.ID() != sid || runtime.ID() != runtime.SessionID() {
		t.Fatal("ID changed the Session identity")
	}
}
