package mecak8s_test

import (
	"strings"
	"testing"
)

func TestArtifactStorage_HelmValues(t *testing.T) {
	baseline, err := helm(t, "template", "pdf", ".", "--set", "redis.endpoint=redis.example:6379", "--set", "redis.caKey=", "--set", "mockProvider=true")
	if err != nil {
		t.Fatal(err, baseline)
	}
	if strings.Contains(baseline, "--artifact-s3-") {
		t.Fatal("default render enables PDF artifact storage")
	}
	configured, err := helm(t, "template", "pdf", ".", "--set", "redis.endpoint=redis.example:6379", "--set", "redis.caKey=", "--set", "mockProvider=true", "--set", "artifacts.s3.bucket=private-pdf", "--set", "artifacts.s3.region=eu-west-1", "--set", "artifacts.s3.endpoint=https://minio.example:9000")
	if err != nil {
		t.Fatal(err, configured)
	}
	for _, flag := range []string{"--artifact-s3-bucket=private-pdf", "--artifact-s3-region=eu-west-1", "--artifact-s3-endpoint=https://minio.example:9000"} {
		if !strings.Contains(configured, flag) {
			t.Fatalf("configured render missing %s", flag)
		}
	}
	for _, values := range [][]string{
		{"artifacts.s3.bucket=private-pdf"},
		{"artifacts.s3.region=eu-west-1"},
		{"artifacts.s3.bucket=private-pdf", "artifacts.s3.region=eu-west-1", "artifacts.s3.endpoint=http://minio.example:9000"},
	} {
		args := []string{"template", "pdf", ".", "--set", "redis.endpoint=redis.example:6379", "--set", "redis.caKey=", "--set", "mockProvider=true"}
		for _, value := range values {
			args = append(args, "--set", value)
		}
		if output, err := helm(t, args...); err == nil {
			t.Fatalf("invalid artifact values rendered: %q", output)
		}
	}
}
