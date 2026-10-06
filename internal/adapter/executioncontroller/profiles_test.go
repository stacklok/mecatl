package executioncontroller

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
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
		"malformed":                    strings.Replace(validProfileYAML(), "cpuRequest: 100m", "cpuRequest: invalid", 1),
		"zero":                         strings.Replace(validProfileYAML(), "memoryRequest: 128Mi", `memoryRequest: "0"`, 1),
		"negative":                     strings.Replace(validProfileYAML(), `cpuLimit: "1"`, `cpuLimit: "-1"`, 1),
		"cpu request over limit":       strings.Replace(validProfileYAML(), "cpuRequest: 100m", "cpuRequest: 2", 1),
		"memory request over limit":    strings.Replace(validProfileYAML(), "memoryLimit: 1Gi", "memoryLimit: 64Mi", 1),
		"ephemeral request over limit": strings.Replace(validProfileYAML(), "ephemeralStorageLimit: 1Gi", "ephemeralStorageLimit: 32Mi", 1),
		"tmp over ephemeral limit":     strings.Replace(validProfileYAML(), "tmpSizeLimit: 256Mi", "tmpSizeLimit: 2Gi", 1),
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

func TestProfilePullSecretDigestAndValidation(t *testing.T) {
	load := func(extra string) (resolvedProfile, error) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "profiles.yaml")
		if err := os.WriteFile(path, []byte(validProfileYAML()+extra), 0o600); err != nil {
			t.Fatal(err)
		}
		profiles, err := LoadProfiles(path)
		if err != nil {
			return resolvedProfile{}, err
		}
		profile, _ := profiles.get("go")
		return profile, nil
	}
	legacy, err := load("")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := load("    imagePullSecrets: []\n")
	if err != nil || legacy.Digest != empty.Digest || legacy.Digest != "sha256:7f64bdddd86008c73dd9601fad9dfdbbfa696c91e75dd3c7005e98934679a994" {
		t.Fatalf("legacy digest changed: %s, empty: %s, err: %v", legacy.Digest, empty.Digest, err)
	}
	configured, err := load("    imagePullSecrets: [registry-one, registry.two]\n")
	if err != nil || configured.Digest == legacy.Digest {
		t.Fatalf("configured pull identity: %+v: %v", configured, err)
	}
	if _, err := load("    imagePullSecrets: [a,b,c,d,e,f,g,h]\n"); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"empty": `[""]`, "duplicate": `[registry, registry]`, "uppercase": `[Registry]`,
		"slash": `[ns/registry]`, "empty label": `[a..b]`, "long label": "[" + strings.Repeat("a", 64) + "]",
		"oversize": `[a,b,c,d,e,f,g,h,i]`, "scalar": `"registry"`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := load("    imagePullSecrets: " + value + "\n"); err == nil {
				t.Fatal("invalid pull Secret list accepted")
			}
		})
	}
	if _, err := load("    imagePullSecret: [registry]\n"); err == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestProfileSchedulingDigestAndValidation(t *testing.T) {
	load := func(extra string) (resolvedProfile, error) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "profiles.yaml")
		if err := os.WriteFile(path, []byte(validProfileYAML()+extra), 0o600); err != nil {
			t.Fatal(err)
		}
		profiles, err := LoadProfiles(path)
		if err != nil {
			return resolvedProfile{}, err
		}
		profile, _ := profiles.get("go")
		return profile, nil
	}
	legacy, err := load("")
	if err != nil {
		t.Fatal(err)
	}
	empty, err := load("    nodeSelector: {}\n    tolerations: []\n")
	if err != nil || legacy.Digest != empty.Digest {
		t.Fatalf("empty scheduling changed digest: legacy=%s empty=%s err=%v", legacy.Digest, empty.Digest, err)
	}
	configured, err := load("    nodeSelector: {node.kubernetes.io/instance-type: worker}\n    tolerations:\n      - key: dedicated\n        operator: Equal\n        value: build\n        effect: NoSchedule\n      - key: node.kubernetes.io/not-ready\n        operator: Exists\n        effect: NoExecute\n        tolerationSeconds: 300\n")
	if err != nil || configured.Digest == legacy.Digest || configured.Spec.NodeSelector["node.kubernetes.io/instance-type"] != "worker" || len(configured.Spec.Tolerations) != 2 || configured.Spec.Tolerations[0].Operator != corev1.TolerationOpEqual {
		t.Fatalf("configured scheduling=%+v: %v", configured, err)
	}
	for name, value := range map[string]string{
		"unknown nested field":   "    tolerations:\n      - operator: Exists\n        unexpected: true\n",
		"invalid selector key":   "    nodeSelector: {'bad/key/again': worker}\n",
		"invalid selector value": "    nodeSelector: {pool: 'bad value'}\n",
		"exists value":           "    tolerations: [{operator: Exists, value: build}]\n",
		"equal no key":           "    tolerations: [{operator: Equal, value: build}]\n",
		"empty key equal":        "    tolerations: [{key: '', operator: Equal, value: build}]\n",
		"invalid effect":         "    tolerations: [{key: dedicated, operator: Exists, effect: Invalid}]\n",
		"seconds wrong effect":   "    tolerations: [{key: dedicated, operator: Exists, effect: NoSchedule, tolerationSeconds: 1}]\n",
		"negative seconds":       "    tolerations: [{key: dedicated, operator: Exists, effect: NoExecute, tolerationSeconds: -1}]\n",
		"excessive seconds":      "    tolerations: [{key: dedicated, operator: Exists, effect: NoExecute, tolerationSeconds: 86401}]\n",
		"duplicate":              "    tolerations: [{key: dedicated, operator: Exists}, {key: dedicated, operator: Exists}]\n",
		"unsupported operator":   "    tolerations: [{key: dedicated, operator: Lt, value: '1'}]\n",
		"too many tolerations":   "    tolerations:\n" + strings.Repeat("      - {key: dedicated, operator: Exists}\n", 17),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := load(value); err == nil {
				t.Fatal("invalid scheduling accepted")
			}
		})
	}
	selectors := "    nodeSelector:\n"
	for i := range 33 {
		selectors += fmt.Sprintf("      key%d: value\n", i)
	}
	if _, err := load(selectors); err == nil {
		t.Fatal("too many selectors accepted")
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
    ephemeralStorageRequest: 64Mi
    ephemeralStorageLimit: 1Gi
    tmpSizeLimit: 256Mi
    runtimeClassName: sandboxed
    maxFileBytes: 1048576
    maxCommandBytes: 65536
    maxCommandDuration: 1m
    maxEnvironments: 100
`
}
