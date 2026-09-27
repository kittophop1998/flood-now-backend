// Package imagecleanup holds the business rules for reclaiming R2 storage:
// what counts as safe to delete and what never is. No file ever leaves R2
// because of age alone — only because the domain judged it unreferenced or
// belonging to an entity whose retention window has passed.
package imagecleanup

import (
	"fmt"
	"time"
)

// Policy is the single, configurable source of cleanup timing and batching,
// mirroring domain/report.FreshnessPolicy's role for report lifecycle.
type Policy struct {
	// Enabled gates whether the cron job is registered at all.
	Enabled bool
	// OrphanRetention: an uploaded object under a managed prefix that is not
	// referenced by any entity is safe to delete once it is this old. Covers
	// both a presigned upload that was never submitted and an image that was
	// replaced by a newer one on the same entity.
	OrphanRetention time.Duration
	// ResolvedRetention: an entity's image is safe to delete once the entity
	// has been resolved/expired for this long.
	ResolvedRetention time.Duration
	// BatchSize caps how many candidates one query/pass fetches, so a run
	// never loads an unbounded result set into memory.
	BatchSize int
	// DryRun: find and log candidates, delete nothing.
	DryRun bool
}

// DefaultPolicy matches the documented defaults (see docs/database.md).
func DefaultPolicy() Policy {
	return Policy{
		Enabled:           true,
		OrphanRetention:   24 * time.Hour,
		ResolvedRetention: 7 * 24 * time.Hour,
		BatchSize:         100,
		DryRun:            false,
	}
}

func (p Policy) Validate() error {
	if p.OrphanRetention <= 0 {
		return fmt.Errorf("orphan retention must be positive")
	}
	if p.ResolvedRetention <= 0 {
		return fmt.Errorf("resolved image retention must be positive")
	}
	if p.BatchSize <= 0 {
		return fmt.Errorf("cleanup batch size must be positive")
	}
	return nil
}

// Summary is what one cleanup run reports for observability.
type Summary struct {
	Candidates int
	Deleted    int
	Skipped    int
	Failed     int
	BytesFreed int64
	DryRun     bool
	Duration   time.Duration
}
