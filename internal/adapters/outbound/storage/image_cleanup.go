package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"floodnow-api/internal/ports"
)

// ListObjects implements ports.ImageStore. It pages through prefix using the
// raw (non-presigning) S3 client and returns only objects whose LastModified
// is at or before olderThan — the rest of the page is dropped, so a returned
// batch can be smaller than maxKeys even when more pages remain (check
// nextToken).
func (p *R2Presigner) ListObjects(ctx context.Context, prefix string, olderThan time.Time, pageToken string, maxKeys int32) ([]ports.ObjectSummary, string, error) {
	in := &s3.ListObjectsV2Input{
		Bucket:  aws.String(p.bucket),
		Prefix:  aws.String(prefix),
		MaxKeys: aws.Int32(maxKeys),
	}
	if pageToken != "" {
		in.ContinuationToken = aws.String(pageToken)
	}
	out, err := p.raw.ListObjectsV2(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("list objects under %q: %w", prefix, err)
	}

	objects := make([]ports.ObjectSummary, 0, len(out.Contents))
	for _, obj := range out.Contents {
		if obj.Key == nil || obj.LastModified == nil || obj.LastModified.After(olderThan) {
			continue
		}
		objects = append(objects, ports.ObjectSummary{
			Key:          *obj.Key,
			Size:         aws.ToInt64(obj.Size),
			LastModified: *obj.LastModified,
		})
	}

	nextToken := ""
	if aws.ToBool(out.IsTruncated) && out.NextContinuationToken != nil {
		nextToken = *out.NextContinuationToken
	}
	return objects, nextToken, nil
}

// DeleteObjects implements ports.ImageStore, deleting up to 1000 keys per
// call (S3/R2's limit) via one batch request. A key that doesn't exist is
// not reported as failed: R2/S3 batch delete is idempotent by design.
func (p *R2Presigner) DeleteObjects(ctx context.Context, keys []string) (map[string]string, error) {
	failed := map[string]string{}
	const maxPerCall = 1000
	for start := 0; start < len(keys); start += maxPerCall {
		end := min(start+maxPerCall, len(keys))
		chunk := keys[start:end]

		objs := make([]types.ObjectIdentifier, len(chunk))
		for i, k := range chunk {
			objs[i] = types.ObjectIdentifier{Key: aws.String(k)}
		}

		out, err := p.raw.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(p.bucket),
			Delete: &types.Delete{Objects: objs, Quiet: aws.Bool(true)},
		})
		if err != nil {
			return failed, fmt.Errorf("batch delete %d objects: %w", len(chunk), err)
		}
		for _, e := range out.Errors {
			failed[aws.ToString(e.Key)] = fmt.Sprintf("%s: %s", aws.ToString(e.Code), aws.ToString(e.Message))
		}
	}
	return failed, nil
}

var _ ports.ImageStore = (*R2Presigner)(nil)
