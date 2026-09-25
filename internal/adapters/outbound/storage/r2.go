// Package storage implements ports.Presigner against Cloudflare R2 (S3
// compatible) and derives ImageKit delivery URLs from stored object keys.
package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const presignTTL = 5 * time.Minute

type R2Presigner struct {
	client *s3.PresignClient
	bucket string
}

func NewR2Presigner(accountID, accessKeyID, secretAccessKey, endpoint, bucket string) *R2Presigner {
	client := s3.New(s3.Options{
		Region:       "auto",
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
		UsePathStyle: true, // Cloudflare R2's documented S3-compatible addressing mode
	})
	return &R2Presigner{client: s3.NewPresignClient(client, s3.WithPresignExpires(presignTTL)), bucket: bucket}
}

func (p *R2Presigner) PresignUpload(ctx context.Context, objectKey, contentType string, contentLength int64) (string, time.Duration, error) {
	req, err := p.client.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(p.bucket),
		Key:           aws.String(objectKey),
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(contentLength),
	})
	if err != nil {
		return "", 0, fmt.Errorf("presign upload: %w", err)
	}
	return req.URL, presignTTL, nil
}
