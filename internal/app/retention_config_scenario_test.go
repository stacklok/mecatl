package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/sessionretention"
)

func TestSessionStorageContinuity_Scenario6_RetentionConfigPrecedence(t *testing.T) {
	t.Parallel()
	settings := writeOperatorSettingsFile(t, `retention:
  version: 1
  main: {max_age: 720h, max_count: 200}
  child: {max_age: 168h, max_count: 500}
  scheduled: {max_age: 24h, max_count: 1000}
  sweep_cadence: 2h
  acknowledge_main_deletion: true
`)
	base := Config{PermissionConfigs: []string{settings}}
	base.permResolver = buildPermResolver(base)
	base.MainRetention = 48 * time.Hour
	base.RetentionCLISet = RetentionCLISet{MainMaxAge: true}
	cfg, err := foldOperatorRetention(base)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MainRetention != 48*time.Hour || cfg.MainRetentionMaxTotal != 200 || cfg.ChildRetention != 168*time.Hour || cfg.ScheduleFireRetentionMaxTotal != 1000 || cfg.ChildGCInterval != 2*time.Hour {
		t.Fatalf("effective retention precedence = %+v", cfg)
	}
	if !cfg.AcknowledgeMainRetention {
		t.Fatal("operator acknowledgement was not folded")
	}
	for name, body := range map[string]string{
		"negative": "retention:\n  version: 1\n  main: {max_age: -1h}\n",
		"unknown":  "retention:\n  version: 1\n  typo: true\n",
		"version":  "retention:\n  version: 2\n",
	} {
		t.Run(name, func(t *testing.T) {
			bad := Config{PermissionConfigs: []string{writeOperatorSettingsFile(t, body)}}
			bad.permResolver = buildPermResolver(bad)
			if _, err := foldOperatorRetention(bad); err == nil {
				t.Fatal("invalid retention config accepted")
			}
		})
	}
}

func TestSessionStorageContinuity_Scenario6_DestructivePolicyAcknowledgement(t *testing.T) {
	t.Parallel()
	var logs []string
	d := recordingRetentionDiagnostics{logs: &logs}
	cfg := Config{MainRetention: 24 * time.Hour, Diagnostics: d}
	if err := validateDestructiveMainRetention(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "acknowledge") {
		t.Fatalf("unacknowledged destructive policy error = %v", err)
	}
	cfg.AcknowledgeMainRetention = true
	if err := validateDestructiveMainRetention(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 || !strings.Contains(logs[len(logs)-1], "main_max_age=24h0m0s") {
		t.Fatalf("planner summary logs = %v", logs)
	}
	now := time.Unix(1000, 0)
	unknown := port.SessionDiscoveryMeta{ID: "legacy", Kind: session.SessionKindUnknown, State: session.StateCompleted, ModifiedAt: now.Add(-48 * time.Hour)}
	plan := sessionretention.Plan([]port.SessionDiscoveryMeta{unknown}, sessionretention.Policy{MainMaxAge: time.Hour}, sessionretention.Scope{}, sessionretention.RuntimeProtection{}, now)
	if len(plan.Eligible) != 0 || plan.Protected.ByReason[sessionretention.ProtectedUnknown] != 1 {
		t.Fatalf("unknown protection = %+v", plan)
	}
}

type recordingRetentionDiagnostics struct{ logs *[]string }

func (d recordingRetentionDiagnostics) Log(_ context.Context, _ port.Level, msg string, attrs ...any) {
	*d.logs = append(*d.logs, msg+" "+attrsString(attrs))
}
func (d recordingRetentionDiagnostics) With(...any) port.Diagnostics { return d }
func attrsString(attrs []any) string {
	var b strings.Builder
	for i := 0; i+1 < len(attrs); i += 2 {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(attrs[i].(string))
		b.WriteByte('=')
		b.WriteString(strings.TrimSpace(toString(attrs[i+1])))
	}
	return b.String()
}
func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if d, ok := v.(time.Duration); ok {
		return d.String()
	}
	return ""
}
