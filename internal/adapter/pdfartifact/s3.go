package pdfartifact

import (
	"context"
	"io"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Config names a dedicated private bucket. Credentials come from the AWS
// default chain (including workload identity or projected environment), never
// chart values or this configuration.
type S3Config struct {
	Bucket   string
	Region   string
	Endpoint string
}

// Validate rejects incomplete S3 settings and non-HTTPS custom endpoints.
func (cfg S3Config) Validate() error {
	if cfg.Bucket == "" || cfg.Region == "" || strings.TrimSpace(cfg.Bucket) != cfg.Bucket || strings.TrimSpace(cfg.Region) != cfg.Region {
		return ErrStorage
	}
	if cfg.Endpoint != "" {
		u, err := url.Parse(cfg.Endpoint)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return ErrStorage
		}
	}
	return nil
}

// S3Objects is the production S3-compatible private object adapter.
type S3Objects struct {
	bucket   string
	client   *s3.Client
	uploader *manager.Uploader //nolint:staticcheck // Multipart upload streams bounded, unknown-length readers.
}

var _ ObjectStore = (*S3Objects)(nil)

// NewS3 constructs a private S3 object adapter using the AWS credential chain.
func NewS3(ctx context.Context, cfg S3Config) (*S3Objects, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, ErrStorage
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		if cfg.Endpoint != "" {
			options.BaseEndpoint = aws.String(cfg.Endpoint)
			options.UsePathStyle = true
		}
	})
	uploader := manager.NewUploader(client, func(u *manager.Uploader) { //nolint:staticcheck // Keep unknown-length streaming with bounded multipart buffers.
		u.PartSize = 5 << 20
		u.Concurrency = 1
		u.LeavePartsOnError = false
		// The validator is the byte limit. This additionally bounds the SDK's
		// multipart bookkeeping for a 20 MiB object.
		u.MaxUploadParts = 5
	})
	return &S3Objects{bucket: cfg.Bucket, client: client, uploader: uploader}, nil
}

// Put streams a PDF into the private bucket.
func (o *S3Objects) Put(ctx context.Context, key string, source io.Reader) error {
	_, err := o.uploader.Upload(ctx, &s3.PutObjectInput{Bucket: aws.String(o.bucket), Key: aws.String(key), Body: source, ContentType: aws.String("application/pdf")}) //nolint:staticcheck // The uploader preserves streaming and cancellation for unknown-length input.
	if err != nil {
		return ErrStorage
	}
	return nil
}

// Open returns a streaming reader for a private object.
func (o *S3Objects) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	result, err := o.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(o.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, ErrStorage
	}
	return result.Body, nil
}

// Delete removes one private object.
func (o *S3Objects) Delete(ctx context.Context, key string) error {
	_, err := o.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(o.bucket), Key: aws.String(key)})
	if err != nil {
		return ErrStorage
	}
	return nil
}

// ListPrefix lists private objects under a session prefix.
func (o *S3Objects) ListPrefix(ctx context.Context, prefix string) ([]string, error) {
	pager := s3.NewListObjectsV2Paginator(o.client, &s3.ListObjectsV2Input{Bucket: aws.String(o.bucket), Prefix: aws.String(prefix)})
	var keys []string
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, ErrStorage
		}
		for _, object := range page.Contents {
			if object.Key != nil {
				keys = append(keys, *object.Key)
			}
		}
		if len(keys) > 4096 {
			return nil, ErrStorage
		}
	}
	return keys, nil
}
