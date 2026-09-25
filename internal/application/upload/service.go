// Package upload contains the presign-upload use case: validate the request
// against domain rules, mint an object key, delegate signing to the storage
// port.
package upload

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/upload"
	"floodnow-api/internal/ports"
)

type Service struct {
	presigner ports.Presigner
	clock     ports.Clock
}

func NewService(presigner ports.Presigner, clock ports.Clock) *Service {
	return &Service{presigner: presigner, clock: clock}
}

type Result struct {
	ObjectKey string
	UploadURL string
	ExpiresIn time.Duration
}

func (s *Service) Presign(ctx context.Context, req upload.Request) (*Result, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	now := s.clock.Now()
	objectKey := fmt.Sprintf("reports/%s/%s.%s", now.Format("2006/01/02"), uuid.New().String(), req.Extension())

	url, expiresIn, err := s.presigner.PresignUpload(ctx, objectKey, req.ContentType, req.ContentLength)
	if err != nil {
		return nil, err
	}

	return &Result{ObjectKey: objectKey, UploadURL: url, ExpiresIn: expiresIn}, nil
}
