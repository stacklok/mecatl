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
