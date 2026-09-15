package executioncontroller

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProfilesStrictAndDigestPinned(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.yaml")
	good := validProfileYAML()
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	v, ok := p.get("go")
	if !ok || !strings.HasPrefix(v.Digest, "sha256:") {
		t.Fatalf("profile=%+v", v)
	}
	if err := os.WriteFile(path, []byte(good+"    unknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProfiles(path); err == nil {
		t.Fatal("unknown field accepted")
	}
	unpinned := strings.Replace(good, "@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", 1)
	_ = os.WriteFile(path, []byte(unpinned), 0o600)
	if _, err := LoadProfiles(path); err == nil {
		t.Fatal("unpinned image accepted")
	}
}

func TestLoadProfilesRejectsInvalidQuantities(t *testing.T) {
	cases := map[string]string{
		"malformed":                 strings.Replace(validProfileYAML(), "cpuRequest: 100m", "cpuRequest: invalid", 1),
		"zero":                      strings.Replace(validProfileYAML(), "memoryRequest: 128Mi", `memoryRequest: "0"`, 1),
		"negative":                  strings.Replace(validProfileYAML(), `cpuLimit: "1"`, `cpuLimit: "-1"`, 1),
		"cpu request over limit":    strings.Replace(validProfileYAML(), "cpuRequest: 100m", "cpuRequest: 2", 1),
		"memory request over limit": strings.Replace(validProfileYAML(), "memoryLimit: 1Gi", "memoryLimit: 64Mi", 1),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "profiles.yaml")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadProfiles(path); err == nil {
				t.Fatal("invalid quantity accepted")
			}
		})
	}
}

func TestLoadProfilesAcceptsQuantityUnitVariants(t *testing.T) {
	content := validProfileYAML()
	content = strings.Replace(content, "storageSize: 2Gi", "storageSize: 500M", 1)
	content = strings.Replace(content, "cpuRequest: 100m", "cpuRequest: 0.1", 1)
	content = strings.Replace(content, `cpuLimit: "1"`, "cpuLimit: 250m", 1)
	content = strings.Replace(content, "memoryRequest: 128Mi", "memoryRequest: 1M", 1)
	content = strings.Replace(content, "memoryLimit: 1Gi", "memoryLimit: 2M", 1)
	path := filepath.Join(t.TempDir(), "profiles.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := LoadProfiles(path)
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := profiles.get("go")
	if !ok || profile.CPURequest.String() != "100m" || profile.MemoryRequest.String() != "1M" || profile.StorageSize.String() != "500M" {
		t.Fatalf("parsed quantities=%+v", profile)
	}
}

func validProfileYAML() string {
	return `profiles:
  go:
    image: ghcr.io/example/workload@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    storageClass: standard
    storageSize: 2Gi
    cpuRequest: 100m
    memoryRequest: 128Mi
    cpuLimit: "1"
    memoryLimit: 1Gi
    maxFileBytes: 1048576
    maxCommandBytes: 65536
    maxCommandDuration: 1m
`
}
