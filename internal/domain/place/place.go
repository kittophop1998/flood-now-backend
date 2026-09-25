// Package place is location search / reverse geocoding: turning a typed
// query into coordinates and coordinates into an approximate address.
package place

import (
	"strings"
	"unicode/utf8"

	"floodnow-api/internal/domain/apperr"
)

type Place struct {
	Name        string
	DisplayName string
	Latitude    float64
	Longitude   float64
}

// NormalizeQuery trims a search query and validates its length.
func NormalizeQuery(q string) (string, error) {
	q = strings.TrimSpace(q)
	if n := utf8.RuneCountInString(q); n < 2 || n > 200 {
		return "", apperr.Validation("search query is invalid", map[string]string{"q": "must be 2-200 characters"})
	}
	return q, nil
}

// NormalizeLang limits the response language to the locales the app ships.
func NormalizeLang(lang string) string {
	if lang == "th" {
		return "th"
	}
	return "en"
}
