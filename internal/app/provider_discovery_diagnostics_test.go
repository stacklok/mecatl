package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
)

type heldDiscoveryDiagnostics struct {
	port.NopDiagnostics
	entered   chan struct{}
	release   chan struct{}
	delivered chan diagFact
}

func (d *heldDiscoveryDiagnostics) Log(_ context.Context, level port.Level, msg string, args ...any) {
	close(d.entered)
	<-d.release
	d.delivered <- diagFact{level: level, msg: msg, args: args}
}

func TestProviderDiscoveryDiagnosticsDoNotBlockSameProviderRefresh(t *testing.T) {
	const providerA = "a"
	const modelA = "a-model"

	diag := &heldDiscoveryDiagnostics{entered: make(chan struct{}), release: make(chan struct{}), delivered: make(chan diagFact, 1)}
	lister := &fakeLister{err: errors.New("offline fixture")}
	reg := &providerRegistry{defaultID: "b", entries: map[string]providerEntry{
		providerA: {id: providerA, available: true, lister: lister},
		"b":       {id: "b", available: true},
	}}
	now := time.Now()
	d := newProviderDiscovery(reg, Config{Diagnostics: diag, modelDiscoveryNow: func() time.Time { return now }})
	reg.discovery = d
	t.Cleanup(func() {
		close(diag.release)
		d.Close()
		select {
		case <-diag.delivered:
		default:
			t.Error("Close did not join diagnostic delivery")
		}
	})

	firstDone := make(chan error, 1)
	go func() { _, err := d.request(context.Background(), providerA, discoveryPicker); firstDone <- err }()
	select {
	case <-diag.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("A did not reach diagnostic sink")
	}
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("A waiter was not notified before diagnostic delivery")
	}

	lister.err = nil
	lister.models = []modelEntry{{ID: modelA, ContextLimit: 222222}}
	now = now.Add(discoveryCooldown + time.Nanosecond)
	if err := awaitContextWindowWithin(context.Background(), reg, providerA, modelA, time.Second); err != nil {
		t.Fatalf("same-provider refresh blocked behind its prior diagnostic: %v", err)
	}
	if got := lister.calls.Load(); got != 2 {
		t.Fatalf("same-provider listing calls=%d, want 2", got)
	}
	if got := d.snapshot().providers[providerA]; got.outcome.State != statusOK || got.inFlight {
		t.Fatalf("same-provider refresh not published: %+v", got)
	}
	if got := d.CurrentModelSnapshot().Models; len(got) != 1 || got[0].GetProviderId() != providerA || got[0].GetId() != modelA || got[0].GetContextLimit() != 222222 {
		t.Fatalf("same-provider public projection before diagnostic release = %v, want %s/%s with context limit 222222", got, providerA, modelA)
	}
}

func TestProviderDiscoveryDiagnosticsDoNotBlockPublicationOrAdmission(t *testing.T) {
	for _, heal := range []bool{false, true} {
		name := "listing failure"
		if heal {
			name = "default healing"
		}
		t.Run(name, func(t *testing.T) {
			diag := &heldDiscoveryDiagnostics{entered: make(chan struct{}), release: make(chan struct{}), delivered: make(chan diagFact, 1)}
			lister := &fakeLister{err: errors.New("offline fixture")}
			want := diagFact{level: port.LevelDebug, msg: "live model listing failed; retaining last-good metadata or catalog floor", args: []any{"provider", providerToolhive, "state", statusUnreachable}}
			if heal {
				lister = &fakeLister{models: []modelEntry{{ID: "a-model", ContextLimit: 222222}}}
				want = diagFact{level: port.LevelInfo, msg: "provider default model (auto-selected) after live refresh", args: []any{"provider", providerToolhive, "model", "a-model", "base_url", "https://fixture.invalid/v1", "gateway_url", "https://fixture.invalid"}}
			}
			reg := &providerRegistry{defaultID: providerToolhive, entries: map[string]providerEntry{
				providerToolhive: {id: providerToolhive, available: true, intentDriven: true, lister: lister, baseURL: "https://fixture.invalid/v1", intentGatewayURL: "https://fixture.invalid"},
				"b":              {id: "b", available: true, lister: &fakeLister{models: []modelEntry{{ID: "b-model", ContextLimit: 333333}}}},
			}}
			d := newProviderDiscovery(reg, Config{Diagnostics: diag})
			reg.discovery = d
			t.Cleanup(func() {
				close(diag.release)
				d.Close()
				select {
				case got := <-diag.delivered:
					if !reflect.DeepEqual(got, want) {
						t.Errorf("delivered diagnostic = %+v, want %+v", got, want)
					}
				default:
					t.Error("Close did not join diagnostic delivery")
				}
			})
			aDone := make(chan error, 1)
			go func() { _, err := d.request(context.Background(), providerToolhive, discoveryPicker); aDone <- err }()
			select {
			case <-diag.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("A did not reach diagnostic sink")
			}
			// B must finish while A's actual sink is still blocked, not after cleanup.
			if err := awaitContextWindowWithin(context.Background(), reg, "b", "b-model", time.Second); err != nil {
				t.Fatalf("B admission blocked behind A diagnostic: %v", err)
			}
			if got := d.snapshot().providers["b"]; got.outcome.State != statusOK || got.inFlight {
				t.Fatalf("B timely listing not published: %+v", got)
			}
			select {
			case err := <-aDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("A waiter was not notified before diagnostic delivery")
			}
		})
	}
}
