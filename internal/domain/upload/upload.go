// Package upload holds the business rules for what an image upload to
// FloodNow is allowed to look like, independent of R2/S3 or HTTP.
package upload

import (
	"fmt"
	"strings"

	"floodnow-api/internal/domain/apperr"
)

const MaxImageBytes int64 = 8 * 1024 * 1024 // 8 MiB

// Object key prefixes. Report photos come from the public presign endpoint;
// announcement images only from the admin one, so a key under
// AnnouncementKeyPrefix can only have been minted by an operator.
const (
	ReportKeyPrefix       = "reports/"
	AnnouncementKeyPrefix = "announcements/"
)

// allowedContentTypes maps an accepted image content type to the file
// extension used when building the stored object key.
var allowedContentTypes = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/webp": "webp",
}

type Request struct {
	ContentType   string
	ContentLength int64
}

func (r Request) Validate() error {
	fields := map[string]string{}

	if _, ok := allowedContentTypes[strings.ToLower(r.ContentType)]; !ok {
		fields["content_type"] = "must be one of image/jpeg, image/png, image/webp"
	}
	if r.ContentLength <= 0 {
		fields["content_length"] = "must be greater than 0"
	}

	if len(fields) > 0 {
		return apperr.Validation("upload request is invalid", fields)
	}

	if r.ContentLength > MaxImageBytes {
		return apperr.PayloadTooLarge(fmt.Sprintf("image exceeds max size of %d bytes", MaxImageBytes))
	}

	return nil
}

func (r Request) Extension() string {
	return allowedContentTypes[strings.ToLower(r.ContentType)]
}
