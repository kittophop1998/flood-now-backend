// Package place contains the location search / reverse-geocoding use cases.
package place

import (
	"context"

	"floodnow-api/internal/domain/apperr"
	domainplace "floodnow-api/internal/domain/place"
	"floodnow-api/internal/ports"
)

const searchLimit = 6

type Service struct {
	geocoder ports.Geocoder
}

func NewService(geocoder ports.Geocoder) *Service {
	return &Service{geocoder: geocoder}
}

// Search finds places matching q, preferring results inside near (the
// current map viewport) when given.
func (s *Service) Search(ctx context.Context, q, lang string, near *ports.BBox) ([]domainplace.Place, error) {
	q, err := domainplace.NormalizeQuery(q)
	if err != nil {
		return nil, err
	}
	return s.geocoder.Search(ctx, q, domainplace.NormalizeLang(lang), near, searchLimit)
}

// Reverse returns an approximate address for a point, or nil if unknown.
func (s *Service) Reverse(ctx context.Context, lat, lng float64, lang string) (*domainplace.Place, error) {
	if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return nil, apperr.Validation("coordinates are invalid", map[string]string{"lat": "must be between -90 and 90", "lng": "must be between -180 and 180"})
	}
	return s.geocoder.Reverse(ctx, lat, lng, domainplace.NormalizeLang(lang))
}
