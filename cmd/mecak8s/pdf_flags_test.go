package main

import (
	"testing"

	"github.com/stacklok/mecatl/engine/port"
)

func TestArtifactStorage_Flags(t *testing.T) {
	for _, argv := range [][]string{
		{"--redis-url=redis:6379", "--artifact-s3-bucket=private-pdf"},
		{"--redis-url=redis:6379", "--artifact-s3-region=eu-west-1"},
		{"--redis-url=redis:6379", "--artifact-s3-endpoint=https://minio.example:9000"},
		{"--redis-url=redis:6379", "--artifact-s3-bucket=private-pdf", "--artifact-s3-region=eu-west-1", "--artifact-s3-endpoint=http://minio.example:9000"},
	} {
		if _, err := parseFlags(argv); err == nil {
			t.Fatalf("parseFlags(%q) accepted incomplete or insecure artifact configuration", argv)
		}
	}
	cfg, err := parseFlags([]string{"--redis-url=redis:6379", "--artifact-s3-bucket=private-pdf", "--artifact-s3-region=eu-west-1", "--artifact-s3-endpoint=https://minio.example:9000"})
	if err != nil {
		t.Fatalf("parseFlags(valid S3): %v", err)
	}
	if cfg.artifactS3Bucket != "private-pdf" || cfg.artifactS3Region != "eu-west-1" || cfg.artifactS3Endpoint != "https://minio.example:9000" {
		t.Fatalf("parsed S3 flags = %+v", cfg)
	}
	projected := appConfig(cfg, port.NopDiagnostics{}, observability{}).ArtifactS3
	if projected.Bucket != cfg.artifactS3Bucket || projected.Region != cfg.artifactS3Region || projected.Endpoint != cfg.artifactS3Endpoint {
		t.Fatalf("S3 flags lost by appConfig: %+v", projected)
	}
}
