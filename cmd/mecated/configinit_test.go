package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "github.com/goccy/go-yaml"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/configgen"
)

// TestConfigInitPrintWritesNoFile: `config init --print` emits the skeleton to the
// writer and writes NOTHING to disk. Run with HOME/XDG pointed at a temp dir so an
// accidental write would be visible (and fail the no-file assertion).
func TestConfigInitPrintWritesNoFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	var out bytes.Buffer
	if err := runConfigInit([]string{"--print"}, &out); err != nil {
		t.Fatalf("config init --print: %v", err)
	}
	if !strings.Contains(out.String(), "GENERATED commented skeleton") {
		t.Errorf("--print output does not look like the skeleton:\n%s", out.String())
	}
	if out.String() != configgen.Skeleton() {
		t.Error("--print output is not byte-identical to the embedded skeleton")
	}
	// Nothing should have been written under the config dir.
	target := filepath.Join(home, ".config", configgen.SettingsRelPath)
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Errorf("--print wrote a file at %s (stat err: %v); it must write nothing", target, err)
	}
}

// TestConfigInitWritesThenRefusesThenForces: the default write creates the file at the
// resolved operator-tier path; a second default invocation REFUSES (error names the
// path); --force overwrites.
func TestConfigInitWritesThenRefusesThenForces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	target := filepath.Join(home, ".config", configgen.SettingsRelPath)

	// First write succeeds and creates the parent dir + file.
	var out bytes.Buffer
	if err := runConfigInit(nil, &out); err != nil {
		t.Fatalf("first config init: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected a written settings file at %s: %v", target, err)
	}
	if string(data) != configgen.Skeleton() {
		t.Error("written file is not the embedded skeleton")
	}

	// Second default invocation refuses, and the error names the existing path.
	err = runConfigInit(nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("second config init should refuse to overwrite, got nil error")
	}
	if !strings.Contains(err.Error(), target) {
		t.Errorf("refusal error must name the path %q, got: %v", target, err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal error should mention --force, got: %v", err)
	}

	// --force overwrites (write a sentinel first to prove it is replaced).
	if err := os.WriteFile(target, []byte("# stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runConfigInit([]string{"--force"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("config init --force: %v", err)
	}
	data, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != configgen.Skeleton() {
		t.Error("--force did not overwrite with the embedded skeleton")
	}
}

// TestConfigInitUnresolvableConfigDir: when neither XDG_CONFIG_HOME nor HOME resolves a
// config dir, the write path fails with an actionable error pointing at --print (rather
// than writing to a bogus location or panicking).
func TestConfigInitUnresolvableConfigDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")        // makes os.UserHomeDir fail on Linux
	t.Setenv("USERPROFILE", "") // and on Windows, for portability
	err := runConfigInit(nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected an error when the config dir is unresolvable, got nil")
	}
	if !strings.Contains(err.Error(), "--print") {
		t.Errorf("error should point the operator at --print, got: %v", err)
	}
}

// TestConfigInitWritePathEqualsResolverReadPath proves the path config init writes to
// is the SAME path the permconfig resolver reads from — they share
// permconfig.UserSettingsRelPath (re-exported as configgen.SettingsRelPath), so a
// drift between write and read is impossible by construction. Asserting the const
// identity here makes the shared-source contract explicit and load-bearing.
func TestConfigInitWritePathEqualsResolverReadPath(t *testing.T) {
	if configgen.SettingsRelPath != permconfig.UserSettingsRelPath {
		t.Fatalf("write path %q != resolver read path %q — the shared const drifted",
			configgen.SettingsRelPath, permconfig.UserSettingsRelPath)
	}
}

// TestSkeletonRoundTripsThroughLiveSchema is the strong agreement guard: uncommenting
// the skeleton's YAML body and parsing it through the LIVE permconfig schema must
// produce NO strict-unknown-key error. Because permconfig parses with strict
// UnmarshalYAML maps (an unknown key is a parse error), this fails if the skeleton
// carries a key the schema does not accept (a typo) OR — combined with the
// configgen single-source test — proves the skeleton's keys are exactly the schema's.
func TestSkeletonRoundTripsThroughLiveSchema(t *testing.T) {
	body := uncommentSkeleton(configgen.Skeleton())
	var cfg permconfig.Config
	if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatalf("uncommented skeleton failed the live permconfig strict parse: %v\n\n--- body ---\n%s", err, body)
	}
	// Sanity: the uncomment actually produced parseable structure (not an empty doc),
	// so the round-trip is non-vacuous.
	if cfg.Models == nil || cfg.Guardrails == nil {
		t.Fatalf("uncommented skeleton parsed but carried no models/guardrails subtree (vacuous round-trip):\n%s", body)
	}
	if len(cfg.Models.Router.Categories) == 0 {
		t.Error("round-trip lost the models.router.categories taxonomy")
	}
}

// uncommentSkeleton turns the fully-commented skeleton into a parseable YAML document
// for the round-trip. It relies on the skeleton's deliberate comment-marker scheme:
//
//   - "#| ..."  → PURE DOCUMENTATION (subtree banners, the worked Example block) — drop.
//   - "#   # x" → a body DOC line (double-commented by commentBlock) — drop.
//   - "#   key:" / "#   - x" → a commented YAML STRUCTURE line — KEEP (strip one "# ").
//   - top-of-file "# ..." header lines (before the first subtree) — not structure, drop.
//
// So the rule is: take only lines starting "# " whose remainder, after the one strip,
// is NOT itself a comment ("#"-prefixed) and IS indented or a "key:" — i.e. real YAML
// body. Indentation is preserved so nesting survives.
//
// TestSkeletonRoundTripsThroughLiveSchema (above) is what PROTECTS the "#|" (doc) vs
// "# " (commented-structure) marker convention this relies on: if a future renderer
// change blurs the two markers, this uncommenter recovers garbage and the round-trip
// parse fails. A maintainer debugging such a parse failure should look at the renderer's
// marker scheme in internal/configgen/render.go, not at the schema.
func uncommentSkeleton(skeleton string) string {
	var b strings.Builder
	for _, line := range strings.Split(skeleton, "\n") {
		if !strings.HasPrefix(line, "# ") {
			continue // "#|" docs, bare "#", "#|", or blank — all non-structure
		}
		dec := line[2:]
		if strings.TrimSpace(dec) == "" {
			continue
		}
		// A body DOC line is still a comment after one strip ("  # ...") — drop it.
		if strings.HasPrefix(strings.TrimSpace(dec), "#") {
			continue
		}
		b.WriteString(dec)
		b.WriteString("\n")
	}
	return b.String()
}
