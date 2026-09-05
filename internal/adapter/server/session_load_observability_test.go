package server_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/telemetry"
)

func TestADR_0212_SessionLoadObservability_Scenario1_NotFoundConcealsMissingForeignAndLoadFailure(t *testing.T) {
	const target = session.SessionID("private-target-session")
	owner := &session.Principal{Issuer: "https://issuer.example", Subject: "owner", GrantType: session.GrantTypeUser}
	foreign := &session.Principal{Issuer: "https://other.example", Subject: "foreign", GrantType: session.GrantTypeUser}

	stored := session.New(target, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	if err := stored.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	present := memstore.New()
	if err := present.Save(t.Context(), stored); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		store port.SessionStore
		ctx   context.Context
	}{
		{name: "missing", store: memstore.New(), ctx: session.WithPrincipal(t.Context(), foreign)},
		{name: "foreign", store: present, ctx: session.WithPrincipal(t.Context(), foreign)},
		{name: "classified store failure", store: loadFailingStore{err: port.NewSessionLoadFailure(port.SessionLoadFailureStore, fmt.Errorf("redis key mecatl:session:%s", target))}, ctx: session.WithPrincipal(t.Context(), foreign)},
		{name: "classified snapshot failure", store: loadFailingStore{err: port.NewSessionLoadFailure(port.SessionLoadFailureSnapshot, fmt.Errorf("blob for %s", target))}, ctx: session.WithPrincipal(t.Context(), foreign)},
		{name: "unknown failure", store: loadFailingStore{err: fmt.Errorf("raw failure for %s", target)}, ctx: session.WithPrincipal(t.Context(), foreign)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newLoadObservabilityService(t, tc.store, port.NopDiagnostics{}, nil)
			got, err := svc.GetSession(tc.ctx, target)
			if got != nil || !errors.Is(err, server.ErrNotFound) {
				t.Fatalf("GetSession = (%v, %v), want nil NotFound", got, err)
			}
			if got := err.Error(); got != server.ErrNotFound.Error() {
				t.Fatalf("caller error = %q, want target-free %q", got, server.ErrNotFound)
			}
		})
	}
}

func TestADR_0212_SessionLoadObservability_Scenario1_BoundedWarningAndMetric(t *testing.T) {
	const sensitive = "session=private principal=alice redis=mecatl:session:private path=/secret/store blob=TOPSECRET size=98765"
	classes := []struct {
		name string
		err  error
		want port.SessionLoadFailureClass
	}{
		{name: "store", err: fmt.Errorf("outer: %w", port.NewSessionLoadFailure(port.SessionLoadFailureStore, errors.New(sensitive))), want: port.SessionLoadFailureStore},
		{name: "snapshot", err: fmt.Errorf("outer: %w", port.NewSessionLoadFailure(port.SessionLoadFailureSnapshot, errors.New(sensitive))), want: port.SessionLoadFailureSnapshot},
		{name: "unknown", err: errors.New(sensitive), want: port.SessionLoadFailureUnknown},
	}

	for _, tc := range classes {
		t.Run(tc.name, func(t *testing.T) {
			diag := &loadRecordingDiagnostics{}
			var metrics []port.SessionLoadFailureClass
			svc := newLoadObservabilityService(t, loadFailingStore{err: tc.err}, diag, func(class port.SessionLoadFailureClass) { metrics = append(metrics, class) })
			_, _ = svc.GetSession(session.WithPrincipal(t.Context(), &session.Principal{Issuer: "i", Subject: "s"}), "private")

			if len(diag.records) != 1 {
				t.Fatalf("warnings = %d, want 1", len(diag.records))
			}
			record := diag.records[0]
			if record.level != port.LevelWarn || record.message != "session load failed" {
				t.Fatalf("record = %#v", record)
			}
			if got := fmt.Sprint(record.fields["class"]); got != tc.want.String() {
				t.Fatalf("class field = %q, want %q", got, tc.want)
			}
			if len(record.fields) > 2 || (len(record.fields) == 2 && record.fields["ownership"] != "enforced") {
				t.Fatalf("unbounded diagnostic fields: %#v", record.fields)
			}
			if strings.Contains(record.render(), sensitive) || strings.Contains(record.render(), "private") || strings.Contains(record.render(), "alice") || strings.Contains(record.render(), "98765") {
				t.Fatalf("diagnostic leaked sensitive data: %s", record.render())
			}
			if len(metrics) != 1 || metrics[0] != tc.want {
				t.Fatalf("metrics = %v, want [%s]", metrics, tc.want)
			}

			diagOnly := &loadRecordingDiagnostics{}
			svc = newLoadObservabilityService(t, loadFailingStore{err: tc.err}, diagOnly, nil)
			_, _ = svc.GetSession(session.WithPrincipal(t.Context(), &session.Principal{Issuer: "i", Subject: "s"}), "private")
			if len(diagOnly.records) != 1 {
				t.Fatalf("nil telemetry warnings = %d, want 1", len(diagOnly.records))
			}
		})
	}

	t.Run("real metric has exact bounded series", func(t *testing.T) {
		registry := prometheus.NewRegistry()
		exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
		if err != nil {
			t.Fatal(err)
		}
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
		metrics, err := telemetry.NewMetrics(provider)
		if err != nil {
			t.Fatal(err)
		}
		svc := newLoadObservabilityService(t, loadFailingStore{err: port.NewSessionLoadFailure(port.SessionLoadFailureStore, errors.New(sensitive))}, port.NopDiagnostics{}, metrics.EmitSessionLoadFailure)
		_, _ = svc.GetSession(session.WithPrincipal(t.Context(), &session.Principal{Issuer: "i", Subject: "s"}), "private")
		families, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, family := range families {
			if family.GetName() != "mecatl_session_load_failures_total" {
				continue
			}
			if len(family.Metric) != 1 {
				t.Fatalf("metric points = %d, want 1", len(family.Metric))
			}
			metric := family.Metric[0]
			classLabels := 0
			for _, label := range metric.Label {
				if label.GetName() == "class" && label.GetValue() == "store" {
					classLabels++
					continue
				}
				if !strings.HasPrefix(label.GetName(), "otel_scope_") {
					t.Fatalf("unsafe metric label: %s=%q", label.GetName(), label.GetValue())
				}
			}
			if len(family.Metric) != 1 || classLabels != 1 || metric.Counter.GetValue() != 1 {
				t.Fatalf("unsafe metric family: %v", family)
			}
			return
		}
		t.Fatal("mecatl_session_load_failures_total not emitted")
	})

	t.Run("not found stays silent", func(t *testing.T) {
		diag := &loadRecordingDiagnostics{}
		metrics := 0
		svc := newLoadObservabilityService(t, memstore.New(), diag, func(port.SessionLoadFailureClass) { metrics++ })
		_, _ = svc.GetSession(session.WithPrincipal(t.Context(), &session.Principal{Issuer: "i", Subject: "s"}), "missing")
		if len(diag.records) != 0 || metrics != 0 {
			t.Fatalf("not-found observability = (%d warnings, %d metrics), want silent", len(diag.records), metrics)
		}
	})
}

type loadFailingStore struct{ err error }

func (loadFailingStore) Save(context.Context, *session.Session) error { return nil }
func (s loadFailingStore) Load(context.Context, session.SessionID) (*session.Session, error) {
	return nil, s.err
}

func newLoadObservabilityService(t *testing.T, store port.SessionStore, diag port.Diagnostics, metric func(port.SessionLoadFailureClass)) *server.Service {
	t.Helper()
	eng := agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil), Store: store})
	svc, err := newPlacementTestService(server.Config{Engine: eng, Store: store, OwnershipEnforced: true, Diagnostics: diag, SessionLoadFailureMetric: metric})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

type loadDiagnosticRecord struct {
	level   port.Level
	message string
	fields  map[string]any
}

func (r loadDiagnosticRecord) render() string { return fmt.Sprintf("%s %#v", r.message, r.fields) }

type loadRecordingDiagnostics struct {
	mu      sync.Mutex
	attrs   []any
	records []loadDiagnosticRecord
}

func (d *loadRecordingDiagnostics) Log(_ context.Context, level port.Level, message string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	all := append(append([]any(nil), d.attrs...), args...)
	fields := make(map[string]any, len(all)/2)
	for i := 0; i+1 < len(all); i += 2 {
		fields[fmt.Sprint(all[i])] = all[i+1]
	}
	d.records = append(d.records, loadDiagnosticRecord{level: level, message: message, fields: fields})
}

func (d *loadRecordingDiagnostics) With(args ...any) port.Diagnostics {
	return &loadRecordingDiagnostics{attrs: append(append([]any(nil), d.attrs...), args...), records: d.records}
}
