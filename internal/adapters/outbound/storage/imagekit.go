package storage

import "strings"

// ImageURL derives an ImageKit delivery URL from a stored R2 object key.
// Kept as a pure function so both the report list/detail mapping and tests
// can use it without constructing an adapter.
func ImageURL(baseURL, objectKey string) string {
	if objectKey == "" {
		return ""
	}
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(objectKey, "/")
}
