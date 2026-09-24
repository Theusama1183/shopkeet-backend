package media

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// ObjectStore is the storage abstraction over Cloudflare R2. It's an interface
// so the module's tests can substitute a fake; the production implementation
// is the S3-compatible signing client below. Bytes never flow through the API:
// the client PUTs straight to R2 on a presigned URL, the Go side only ever
// signs and records metadata.
type ObjectStore interface {
	// PresignPutURL returns a short-lived presigned PUT URL for a fresh object
	// key. The key is decided by the caller (always "<tenantID>/<uuid>.<ext>").
	PresignPutURL(ctx context.Context, key, contentType string) (string, error)
	// Delete removes an object from the bucket.
	Delete(ctx context.Context, key string) error
}

// R2 is the production ObjectStore. Cloudflare R2 speaks S3, so presigning
// uses the AWS SDK with the R2 endpoint + static (S3 API) credentials.
type R2 struct {
	client  *s3.Client
	bucket  string
	timeout time.Duration
}

// NewR2 builds the R2 signer. presignTTL controls the PUT URL lifetime.
func NewR2(accountID, accessKeyID, secretKey, bucket string, presignTTL time.Duration) (*R2, error) {
	if accountID == "" || accessKeyID == "" || secretKey == "" || bucket == "" {
		return nil, fmt.Errorf("R2 configuration incomplete")
	}

	cfg := aws.Config{
		Region:      "auto", // R2 requires region "auto"
		Credentials: credentials.NewStaticCredentialsProvider(accessKeyID, secretKey, ""),
		AppID:       "",
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID))
	})
	return &R2{client: client, bucket: bucket, timeout: presignTTL}, nil
}

// PresignPutURL signs a PUT for r2_key, restricted to the given content type.
func (r *R2) PresignPutURL(ctx context.Context, key, contentType string) (string, error) {
	p := s3.NewPresignClient(r.client)
	req, err := p.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(r.bucket),
		Key:         aws.String(key),
		ContentType: aws.String(contentType),
	}, s3.WithPresignExpires(r.timeout))
	if err != nil {
		return "", fmt.Errorf("presign put: %w", err)
	}
	return req.URL, nil
}

// Delete removes the object for key.
func (r *R2) Delete(ctx context.Context, key string) error {
	_, err := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(r.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("r2 delete %q: %w", key, err)
	}
	return nil
}
