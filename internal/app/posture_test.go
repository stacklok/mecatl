package app

import (
	"strings"
	"testing"
)

// TestPostureIotaOrderingIsContract pins the load-bearing integer ordering
// strict < trusted < auto < yolo. The MAX-fold (ResolveAliasPosture), the ceiling
// clamp, narratePosture's `>=` flags, and the root-refusal's `>= PostureAuto` all
// depend on it; reordering the const block (or inserting a tier out of order) breaks
// those silently. This is the cheap guard the ORDER-IS-CONTRACT comment promises.
func TestPostureIotaOrderingIsContract(t *testing.T) {
	ordered := PostureStrict < PostureTrusted && PostureTrusted < PostureAuto && PostureAuto < PostureYolo
	if !ordered {
		t.Fatalf("posture ordering contract violated: strict=%d trusted=%d auto=%d yolo=%d",
			PostureStrict, PostureTrusted, PostureAuto, PostureYolo)
	}
}

// TestResolveAliasPostureTable pins the EXPORTED pure alias-fold helper (the ONE
// grammar both cmd roots and resolvePosture share). The refusal/print surface drives
// off it, so drift here is security-relevant.
func TestResolveAliasPostureTable(t *testing.T) {
	cases := []struct {
		name         string
		flag         Posture
		yolo         bool
		trustProject bool
		want         Posture
	}{
		{"empty + --yolo => yolo", PostureStrict, true, false, PostureYolo},
		{"empty + --trust-project => trusted", PostureStrict, false, true, PostureTrusted},
		{"explicit strict + --yolo => yolo (alias raises)", PostureStrict, true, false, PostureYolo},
		{"explicit auto + --trust-project does not lower", PostureAuto, false, true, PostureAuto},
		{"explicit yolo, no alias", PostureYolo, false, false, PostureYolo},
		{"both aliases => yolo (max)", PostureStrict, true, true, PostureYolo},
		{"no alias, no flag => strict", PostureStrict, false, false, PostureStrict},
		// A garbage --posture token parses to strict (ParsePosture), then aliases apply.
		{"garbage flag parses strict then --trust-project", ParsePosture("garbage"), false, true, PostureTrusted},
	}
	for _, tc := range cases {
		if got := ResolveAliasPosture(tc.flag, tc.yolo, tc.trustProject); got != tc.want {
			t.Errorf("%s: ResolveAliasPosture(%v,%v,%v) = %v, want %v", tc.name, tc.flag, tc.yolo, tc.trustProject, got, tc.want)
		}
	}
}

// TestPostureConflictWarnFires proves the resolvePosture WARN actually emits when an
// alias raised the effective tier ABOVE an explicit lower --posture, and stays silent
// when there is no conflict. The WARN is the operator's only signal that an alias they
// also passed superseded their explicit choice.
func TestPostureConflictWarnFires(t *testing.T) {
	// Conflict: explicit --posture strict + --yolo.
	rec := slogdiagBuffer(t)
	cfg := Config{Posture: PostureStrict, PostureFlagSet: true, AllowAllTools: true, Diagnostics: rec.diag}
	if got := resolvePosture(cfg, postureNoCeiling); got != PostureYolo {
		t.Fatalf("conflict case resolved %v, want yolo", got)
	}
	if !strings.Contains(rec.String(), "raised the effective posture") {
		t.Fatalf("expected the alias-raised-posture WARN; log:\n%s", rec.String())
	}

	// No conflict (alias only, no explicit flag): no WARN.
	rec2 := slogdiagBuffer(t)
	cfg2 := Config{AllowAllTools: true, Diagnostics: rec2.diag} // PostureFlagSet false
	_ = resolvePosture(cfg2, postureNoCeiling)
	if strings.Contains(rec2.String(), "raised the effective posture") {
		t.Fatalf("no-conflict (no explicit flag) must NOT WARN; log:\n%s", rec2.String())
	}

	// No conflict (explicit flag already >= alias): no WARN.
	rec3 := slogdiagBuffer(t)
	cfg3 := Config{Posture: PostureYolo, PostureFlagSet: true, AllowAllTools: true, Diagnostics: rec3.diag}
	_ = resolvePosture(cfg3, postureNoCeiling)
	if strings.Contains(rec3.String(), "raised the effective posture") {
		t.Fatalf("explicit-flag-already-at-tier must NOT WARN; log:\n%s", rec3.String())
	}
}

// TestPostureResolutionTable is the structural anti-drift guard for the
// root-aware ladder. TrustProject is both the ingestion decision (through the
// named seam) and the read-only child-shell gate. Allow-all and loose-child
// substitution are now mode-driven (Policy.Evaluate, not applyPosture), so only
// the TrustProject ladder is tested here.
func TestPostureResolutionTable(t *testing.T) {
	for _, root := range []struct {
		name     string
		headless bool
		cases    []struct {
			name      string
			posture   Posture
			trustFlag bool
			trusted   bool
		}
	}{
		{"headless", true, []struct {
			name               string
			posture            Posture
			trustFlag, trusted bool
		}{
			{"strict+no-trust", PostureStrict, false, false},
			{"strict+trust", PostureStrict, true, true},
			{"trusted+no-trust", PostureTrusted, false, false},
			{"trusted+trust", PostureTrusted, true, true},
			{"auto+no-trust", PostureAuto, false, false},
			{"auto+trust", PostureAuto, true, true},
			{"yolo+no-trust", PostureYolo, false, false},
			{"yolo+trust", PostureYolo, true, true},
		}},
		{"interactive", false, []struct {
			name               string
			posture            Posture
			trustFlag, trusted bool
		}{
			{"strict+no-trust", PostureStrict, false, false},
			{"strict+trust", PostureStrict, true, true},
			{"trusted+no-trust", PostureTrusted, false, true},
			{"trusted+trust", PostureTrusted, true, true},
			{"auto+no-trust", PostureAuto, false, true},
			{"auto+trust", PostureAuto, true, true},
			{"yolo+no-trust", PostureYolo, false, true},
			{"yolo+trust", PostureYolo, true, true},
		}},
	} {
		t.Run(root.name, func(t *testing.T) {
			for _, tc := range root.cases {
				got := applyPosture(Config{Headless: root.headless, Posture: tc.posture, TrustProject: tc.trustFlag})
				if got.TrustProject != tc.trusted {
					t.Errorf("%s: got trust=%v; want %v", tc.name, got.TrustProject, tc.trusted)
				}
				if projectIngestionAdmitted(got) != tc.trusted {
					t.Errorf("%s: ingestion=%v, want %v", tc.name, projectIngestionAdmitted(got), tc.trusted)
				}
			}
		})
	}
}

// TestPostureApplyNeverLowersTrust pins that applyPosture only RAISES TrustProject: an
// operator who passed --trust-project (TrustProject=true) under PostureStrict keeps it
// (strict derives nothing, so the existing true survives). It would fail if applyPosture
// hard-set TrustProject=false for strict.
func TestPostureApplyNeverLowersTrust(t *testing.T) {
	got := applyPosture(Config{Posture: PostureStrict, TrustProject: true})
	if !got.TrustProject {
		t.Fatalf("strict must not LOWER an operator's pre-set TrustProject; got false")
	}
}

// TestPostureFailsClosed proves the fail-closed core: an empty, whitespace, or unknown
// --posture value resolves to strict, and the YOLO derivations are OFF there. (The
// project-tier-ignored half is proven in the permconfig operator-tier test.)
func TestPostureFailsClosed(t *testing.T) {
	for _, v := range []string{"", "   ", "YOLO!", "supersafe", "auto2"} {
		if got := parsePosture(v); got != PostureStrict {
			t.Errorf("parsePosture(%q) = %v, want PostureStrict (fail closed)", v, got)
		}
		// ParsePosture is the exported cmd-facing alias; same answer.
		if got := ParsePosture(v); got != PostureStrict {
			t.Errorf("ParsePosture(%q) = %v, want PostureStrict", v, got)
		}
	}
	// And the derived knobs are all off at strict.
	got := applyPosture(Config{Posture: parsePosture("nonsense")})
	if got.AllowAllTools || got.LooseChildSubstitution || got.TrustProject {
		t.Fatalf("unknown posture must derive nothing (fail closed); got %+v", got)
	}
	// IsKnownPostureToken classifies the same set for the cmd WARN.
	for _, v := range []string{"", "strict", "trusted", "auto", "yolo"} {
		if !IsKnownPostureToken(v) {
			t.Errorf("IsKnownPostureToken(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"YOLO!", "supersafe"} {
		if IsKnownPostureToken(v) {
			t.Errorf("IsKnownPostureToken(%q) = true, want false (WARN-worthy)", v)
		}
	}
}

// TestPostureAliasesMapAndConflict proves the alias fold (resolvePosture): --yolo maps
// to yolo, --trust-project to >=trusted, and an explicit lower --posture with a higher
// alias resolves to the MAX (the alias wins, never silently downgraded). A regression
// that let the explicit flag win over a higher alias (silently weakening the requested
// posture) flips a row.
func TestPostureAliasesMapAndConflict(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want Posture
	}{
		{"yolo alias", Config{AllowAllTools: true}, PostureYolo},
		{"trust-project alias", Config{TrustProject: true}, PostureTrusted},
		{"explicit auto, no alias", Config{Posture: PostureAuto, PostureFlagSet: true}, PostureAuto},
		{"explicit strict + --yolo => max(yolo)", Config{Posture: PostureStrict, PostureFlagSet: true, AllowAllTools: true}, PostureYolo},
		{"explicit strict + --trust-project => max(trusted)", Config{Posture: PostureStrict, PostureFlagSet: true, TrustProject: true}, PostureTrusted},
		{"explicit yolo, --trust-project does not lower", Config{Posture: PostureYolo, PostureFlagSet: true, TrustProject: true}, PostureYolo},
		{"explicit auto + --yolo => yolo", Config{Posture: PostureAuto, PostureFlagSet: true, AllowAllTools: true}, PostureYolo},
	}
	for _, tc := range cases {
		if got := resolvePosture(tc.cfg, postureNoCeiling); got != tc.want {
			t.Errorf("%s: resolvePosture = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestPostureCeilingClamps proves the (v1-unused) ceiling parameter clamps the resolved
// tier — the one-line seam a future managed-scope ceiling will use. postureNoCeiling is
// a no-op; a real ceiling lowers the result.
func TestPostureCeilingClamps(t *testing.T) {
	cfg := Config{AllowAllTools: true} // would resolve to yolo unbounded
	if got := resolvePosture(cfg, postureNoCeiling); got != PostureYolo {
		t.Fatalf("unbounded: got %v, want yolo", got)
	}
	if got := resolvePosture(cfg, PostureAuto); got != PostureAuto {
		t.Fatalf("ceiling=auto must clamp yolo down to auto; got %v", got)
	}
}

// TestPostureStringRoundTrips pins the wire/CLI token strings (the chrome badge and the
// settings.yaml/CLI grammar depend on these exact tokens).
func TestPostureStringRoundTrips(t *testing.T) {
	for _, p := range []Posture{PostureStrict, PostureTrusted, PostureAuto, PostureYolo} {
		if got := parsePosture(p.String()); got != p {
			t.Errorf("round-trip %v: parsePosture(%q) = %v", p, p.String(), got)
		}
	}
	if PostureStrict.String() != "strict" || PostureYolo.String() != "yolo" {
		t.Fatalf("unexpected token strings")
	}
}
