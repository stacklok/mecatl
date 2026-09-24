package pdfartifact

import (
	"testing"
)

func TestPDFArtifactStorage_S3Config(t *testing.T) {
	for _, cfg := range []S3Config{
		{Bucket: "private-pdf"},
		{Region: "eu-west-1"},
		{Bucket: "private-pdf", Region: "eu-west-1", Endpoint: "http://minio:9000"},
		{Bucket: "private-pdf", Region: "eu-west-1", Endpoint: "https://user:pass@minio:9000"},
	} {
		if _, err := NewS3(t.Context(), cfg); err == nil {
			t.Fatalf("NewS3(%+v) accepted invalid configuration", cfg)
		}
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/credentials")
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/config")
	objects, err := NewS3(t.Context(), S3Config{Bucket: "private-pdf", Region: "eu-west-1", Endpoint: "https://minio.example:9000"})
	if err != nil {
		t.Fatalf("NewS3(TLS endpoint): %v", err)
	}
	if !objects.client.Options().UsePathStyle {
		t.Fatal("custom MinIO endpoint must use path-style requests")
	}
}
