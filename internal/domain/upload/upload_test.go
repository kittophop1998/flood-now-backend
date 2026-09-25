package upload

import "testing"

func TestRequestValidate(t *testing.T) {
	t.Run("valid jpeg accepted", func(t *testing.T) {
		req := Request{ContentType: "image/jpeg", ContentLength: 1024}
		if err := req.Validate(); err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
	})

	t.Run("unsupported content type rejected", func(t *testing.T) {
		req := Request{ContentType: "application/pdf", ContentLength: 1024}
		if err := req.Validate(); err == nil {
			t.Fatal("expected error for unsupported content type")
		}
	})

	t.Run("oversized upload rejected", func(t *testing.T) {
		req := Request{ContentType: "image/png", ContentLength: MaxImageBytes + 1}
		if err := req.Validate(); err == nil {
			t.Fatal("expected error for oversized upload")
		}
	})

	t.Run("extension derived from content type", func(t *testing.T) {
		req := Request{ContentType: "image/webp", ContentLength: 1024}
		if got := req.Extension(); got != "webp" {
			t.Errorf("extension = %q, want webp", got)
		}
	})
}
