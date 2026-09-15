package learning

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/tool"
)

var testProjectionTextLimit = maxProjectionTextBytes

func legacySafeProjectionText(key, value string) string {
	value = boundedCanonicalText(value, testProjectionTextLimit)
	value = projectionCredential.ReplaceAllString(value, "[redacted-secret]")
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		if tool.SecretShapedMemoryValue(key, line) {
			lines[i] = "[redacted-secret]"
		}
	}
	return governance.NeutraliseFraming(strings.Join(lines, "\n"))
}

func projectionTextFixtures() []struct {
	name  string
	key   string
	value string
} {
	credentials := []string{
		"sk-" + strings.Repeat("a", 20),
		"ghp_" + strings.Repeat("B", 20),
		"github_pat_" + strings.Repeat("c", 20),
		"xoxb-" + strings.Repeat("D", 20),
		"xoxp-" + strings.Repeat("e", 20),
		"AKIA" + strings.Repeat("F", 16),
		"ASIA" + strings.Repeat("0", 16),
		strings.Repeat("g", 16) + "." + strings.Repeat("H", 16) + "." + strings.Repeat("1", 16),
	}
	return []struct {
		name  string
		key   string
		value string
	}{
		{name: "empty"},
		{name: "plain no gate characters", value: strings.Repeat("x", 1024)},
		{name: "realistic prose", value: "The operator reviewed this ordinary paragraph and found no credential in it."},
		{name: "all credential families", value: strings.Join(credentials, " ")},
		{name: "mixed case", value: "GHP_" + strings.Repeat("aB", 10)},
		{name: "unicode simple fold", value: "ſK-" + strings.Repeat("z", 20)},
		{name: "controls create match", value: "ghp_\u200b" + strings.Repeat("q", 10) + "\x00" + strings.Repeat("q", 10)},
		{name: "line controls and separators", value: "before\u0085after\v\f\rnext\u2028line\u2029end"},
		{name: "adjacent embedded overlap", value: "prefix" + credentials[0] + credentials[1] + "suffix " + strings.Repeat("j", 16) + "." + strings.Repeat("k", 16) + "." + strings.Repeat("l", 20)},
		{name: "truncation before token", value: strings.Repeat("x", maxProjectionTextBytes-8) + credentials[0]},
		{name: "truncation retains token", value: strings.Repeat("x", maxProjectionTextBytes-len(credentials[0])) + credentials[0] + "tail"},
		{name: "secret shaped line", key: "password", value: "ordinary-value"},
		{name: "json and fence", value: "```json\n{\"token\":\"" + credentials[2] + "\"}\n```"},
	}
}

func TestSafeProjectionTextMatchesUnconditionalCredentialScan(t *testing.T) {
	for _, tc := range projectionTextFixtures() {
		t.Run(tc.name, func(t *testing.T) {
			got := safeProjectionText(tc.key, tc.value)
			want := legacySafeProjectionText(tc.key, tc.value)
			if got != want {
				t.Fatalf("safeProjectionText() = %q, want legacy result %q", got, want)
			}
		})
	}
}

func TestProjectionCredentialMatchImpliesGateCharacter(t *testing.T) {
	for _, tc := range projectionTextFixtures() {
		canonical := boundedCanonicalText(tc.value, testProjectionTextLimit)
		for _, match := range projectionCredential.FindAllString(canonical, -1) {
			if !strings.ContainsAny(match, "._-aA") {
				t.Fatalf("credential regexp matched %q without a gate character", match)
			}
		}
	}
}

func FuzzSafeProjectionTextMatchesUnconditionalCredentialScan(f *testing.F) {
	for _, tc := range projectionTextFixtures() {
		f.Add(tc.key, tc.value)
	}
	f.Fuzz(func(t *testing.T, key, value string) {
		got := safeProjectionText(key, value)
		want := legacySafeProjectionText(key, value)
		if got != want {
			t.Fatalf("safeProjectionText() = %q, want legacy result %q", got, want)
		}
		canonical := boundedCanonicalText(value, testProjectionTextLimit)
		for _, match := range projectionCredential.FindAllString(canonical, -1) {
			if !strings.ContainsAny(match, "._-aA") {
				t.Fatalf("credential regexp matched %q without a gate character", match)
			}
		}
	})
}

var projectionTextBenchmarkSink string

func BenchmarkSafeProjectionTextCredentialGate(b *testing.B) {
	fixtures := []struct {
		name  string
		value string
	}{
		{name: "realistic-prose", value: strings.Repeat("This is realistic prose about tools and identifiers without credentials. ", 128)},
		{name: "16KiB-no-match", value: strings.Repeat("x", maxProjectionTextBytes)},
		{name: "identifiers-and-tools", value: strings.Repeat("Read ListDir tool_call_id user-model/v1 ", 256)},
		{name: "secret-with-controls", value: strings.Repeat("x", 8<<10) + "ghp_\u200b" + strings.Repeat("q", 20)},
	}
	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			b.SetBytes(int64(len(fixture.value)))
			b.Run("unconditional", func(b *testing.B) {
				for b.Loop() {
					projectionTextBenchmarkSink = legacySafeProjectionText("", fixture.value)
				}
			})
			b.Run("gated", func(b *testing.B) {
				for b.Loop() {
					projectionTextBenchmarkSink = safeProjectionText("", fixture.value)
				}
			})
		})
	}
}
