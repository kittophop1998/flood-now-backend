// Package imagecleanup contains the scheduled use case that reclaims R2
// storage: find images no longer needed by any entity, revalidate each one
// immediately before deleting it, and only touch Postgres after the R2
// delete succeeds. See internal/domain/imagecleanup for the rules.
package imagecleanup

import (
	"context"
	"time"

	"floodnow-api/internal/domain/imagecleanup"
	"floodnow-api/internal/domain/upload"
	"floodnow-api/internal/ports"
)

// orphanListPageSize is the R2 ListObjectsV2 page size (S3's max is 1000).
const orphanListPageSize = 1000

// maxOrphanPagesPerPrefix bounds R2 listing calls per prefix per run, so one
// run can't turn into an unbounded bucket scan. Objects past the first
// maxOrphanPagesPerPrefix*orphanListPageSize under a prefix are picked up by
// a later run — safe because nothing is ever deleted for being merely old
// (see domain/imagecleanup), only incompleteness in how fast the backlog
// drains.
const maxOrphanPagesPerPrefix = 20

// Service runs one cleanup pass across all three rules: expired report
// images, expired announcement images, and unreferenced ("orphan") uploads
// under either prefix.
type Service struct {
	reports       ports.ReportRepository
	announcements ports.AnnouncementRepository
	store         ports.ImageStore
	clock         ports.Clock
	policy        imagecleanup.Policy
	logf          func(format string, args ...any)
}

func NewService(
	reports ports.ReportRepository,
	announcements ports.AnnouncementRepository,
	store ports.ImageStore,
	clock ports.Clock,
	policy imagecleanup.Policy,
	logf func(format string, args ...any),
) *Service {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Service{reports: reports, announcements: announcements, store: store, clock: clock, policy: policy, logf: logf}
}

// Run executes one pass and returns a summary for observability. It never
// panics or returns an error: every failure is per-candidate, logged, and
// left for the next run.
func (s *Service) Run(ctx context.Context) imagecleanup.Summary {
	start := s.clock.Now()
	s.logf("image cleanup started: dry_run=%v orphan_retention=%s resolved_retention=%s batch_size=%d",
		s.policy.DryRun, s.policy.OrphanRetention, s.policy.ResolvedRetention, s.policy.BatchSize)

	var sum imagecleanup.Summary
	sum.DryRun = s.policy.DryRun

	s.cleanReportImages(ctx, start, &sum)
	s.cleanAnnouncementImages(ctx, start, &sum)
	s.cleanOrphans(ctx, start, &sum)

	sum.Duration = s.clock.Now().Sub(start)
	s.logf("image cleanup finished: dry_run=%v candidates=%d deleted=%d skipped=%d failed=%d bytes_freed=%d duration=%s",
		sum.DryRun, sum.Candidates, sum.Deleted, sum.Skipped, sum.Failed, sum.BytesFreed, sum.Duration)
	return sum
}

// cleanReportImages applies rule B to reports: image_key cleared only after
// the R2 object is gone, and only for a report that is still resolved/
// expired past retention at the moment of deletion (not merely when the
// candidate query ran).
func (s *Service) cleanReportImages(ctx context.Context, now time.Time, sum *imagecleanup.Summary) {
	cutoff := now.Add(-s.policy.ResolvedRetention)
	candidates, err := s.reports.ImageCleanupCandidates(ctx, cutoff, s.policy.BatchSize)
	if err != nil {
		s.logf("image cleanup: query report candidates: %v", err)
		return
	}
	sum.Candidates += len(candidates)

	for _, c := range candidates {
		eligible, err := s.revalidateReport(ctx, c, cutoff)
		if err != nil {
			s.logf("image cleanup: revalidate report %s: %v", c.ReportID, err)
			sum.Failed++
			continue
		}
		if !eligible {
			sum.Skipped++
			continue
		}
		if s.policy.DryRun {
			s.logf("image cleanup (dry-run): would delete report image report_id=%s key=%q", c.ReportID, c.ImageKey)
			sum.Deleted++
			continue
		}
		failed, err := s.store.DeleteObjects(ctx, []string{c.ImageKey})
		if err != nil {
			s.logf("image cleanup: delete report image report_id=%s key=%q: %v", c.ReportID, c.ImageKey, err)
			sum.Failed++
			continue
		}
		if reason, bad := failed[c.ImageKey]; bad {
			s.logf("image cleanup: provider refused delete report_id=%s key=%q: %s", c.ReportID, c.ImageKey, reason)
			sum.Failed++
			continue
		}
		if _, err := s.reports.ClearImageIfUnchanged(ctx, c.ReportID, c.ImageKey); err != nil {
			// The R2 object is already gone; only the DB metadata failed to
			// update. Log it — a rerun will find image_key still set but the
			// object missing, treat it as not-found, and clear it then.
			s.logf("image cleanup: clear report %s image_key after delete: %v", c.ReportID, err)
			sum.Failed++
			continue
		}
		sum.Deleted++
	}
}

func (s *Service) revalidateReport(ctx context.Context, c ports.ImageCleanupCandidate, cutoff time.Time) (bool, error) {
	r, err := s.reports.GetByID(ctx, c.ReportID)
	if err != nil {
		return false, err
	}
	if r == nil || r.ImageKey == nil || *r.ImageKey != c.ImageKey {
		return false, nil // deleted, or the image moved on since the candidate was read
	}
	if r.ResolvedAt != nil {
		return !r.ResolvedAt.After(cutoff), nil
	}
	return !r.ExpiresAt.After(cutoff), nil
}

// cleanAnnouncementImages applies rule B to announcements: an image is
// removed from the images array only after the R2 object is gone, and only
// if the announcement is still ended past retention at delete time.
func (s *Service) cleanAnnouncementImages(ctx context.Context, now time.Time, sum *imagecleanup.Summary) {
	cutoff := now.Add(-s.policy.ResolvedRetention)
	candidates, err := s.announcements.ExpiredImageCandidates(ctx, cutoff, s.policy.BatchSize)
	if err != nil {
		s.logf("image cleanup: query announcement candidates: %v", err)
		return
	}
	sum.Candidates += len(candidates)

	for _, c := range candidates {
		eligible, err := s.revalidateAnnouncement(ctx, c, cutoff)
		if err != nil {
			s.logf("image cleanup: revalidate announcement %s: %v", c.AnnouncementID, err)
			sum.Failed++
			continue
		}
		if !eligible {
			sum.Skipped++
			continue
		}
		if s.policy.DryRun {
			s.logf("image cleanup (dry-run): would delete announcement image announcement_id=%s key=%q", c.AnnouncementID, c.ImageKey)
			sum.Deleted++
			continue
		}
		failed, err := s.store.DeleteObjects(ctx, []string{c.ImageKey})
		if err != nil {
			s.logf("image cleanup: delete announcement image announcement_id=%s key=%q: %v", c.AnnouncementID, c.ImageKey, err)
			sum.Failed++
			continue
		}
		if reason, bad := failed[c.ImageKey]; bad {
			s.logf("image cleanup: provider refused delete announcement_id=%s key=%q: %s", c.AnnouncementID, c.ImageKey, reason)
			sum.Failed++
			continue
		}
		if _, err := s.announcements.RemoveImageIfPresent(ctx, c.AnnouncementID, c.ImageKey); err != nil {
			s.logf("image cleanup: remove announcement %s image after delete: %v", c.AnnouncementID, err)
			sum.Failed++
			continue
		}
		sum.Deleted++
	}
}

func (s *Service) revalidateAnnouncement(ctx context.Context, c ports.AnnouncementImageCandidate, cutoff time.Time) (bool, error) {
	a, err := s.announcements.Get(ctx, c.AnnouncementID)
	if err != nil {
		return false, err
	}
	if a == nil || a.EndsAt == nil || a.EndsAt.After(cutoff) {
		return false, nil // deleted, or extended since the candidate was read
	}
	for _, img := range a.Images {
		if img.Key == c.ImageKey {
			return true, nil
		}
	}
	return false, nil // already removed (e.g. an edit dropped it)
}

// cleanOrphans applies rules A/C: any object under a managed prefix, older
// than the orphan grace period, that no report or announcement currently
// references. This is the only rule that lists R2 rather than driving off a
// DB query — this codebase has no separate upload-tracking table, and an
// image that was replaced by a newer one on the same entity leaves no DB
// trace of the old key, so listing is the only way to find it.
func (s *Service) cleanOrphans(ctx context.Context, now time.Time, sum *imagecleanup.Summary) {
	cutoff := now.Add(-s.policy.OrphanRetention)
	s.sweepOrphanPrefix(ctx, upload.ReportKeyPrefix, cutoff, s.reports.ImageKeyReferenced, sum)
	s.sweepOrphanPrefix(ctx, upload.AnnouncementKeyPrefix, cutoff, s.announcements.ImageKeyReferenced, sum)
}

func (s *Service) sweepOrphanPrefix(ctx context.Context, prefix string, cutoff time.Time, referenced func(context.Context, string) (bool, error), sum *imagecleanup.Summary) {
	token := ""
	for page := 0; page < maxOrphanPagesPerPrefix; page++ {
		objects, next, err := s.store.ListObjects(ctx, prefix, cutoff, token, orphanListPageSize)
		if err != nil {
			s.logf("image cleanup: list %s: %v", prefix, err)
			return
		}

		var toDelete []ports.ObjectSummary
		for _, o := range objects {
			ref, err := referenced(ctx, o.Key)
			if err != nil {
				s.logf("image cleanup: check reference for %q: %v", o.Key, err)
				sum.Failed++
				continue
			}
			sum.Candidates++
			if ref {
				sum.Skipped++
				continue
			}
			toDelete = append(toDelete, o)
		}
		if len(toDelete) > 0 {
			s.deleteOrphans(ctx, toDelete, sum)
		}

		if next == "" {
			return
		}
		token = next
	}
}

func (s *Service) deleteOrphans(ctx context.Context, objects []ports.ObjectSummary, sum *imagecleanup.Summary) {
	if s.policy.DryRun {
		for _, o := range objects {
			s.logf("image cleanup (dry-run): would delete orphan key=%q", o.Key)
		}
		sum.Deleted += len(objects)
		for _, o := range objects {
			sum.BytesFreed += o.Size
		}
		return
	}

	keys := make([]string, len(objects))
	sizeOf := make(map[string]int64, len(objects))
	for i, o := range objects {
		keys[i] = o.Key
		sizeOf[o.Key] = o.Size
	}

	failed, err := s.store.DeleteObjects(ctx, keys)
	if err != nil {
		s.logf("image cleanup: batch delete %d orphans: %v", len(keys), err)
		sum.Failed += len(keys)
		return
	}
	for _, k := range keys {
		if reason, bad := failed[k]; bad {
			s.logf("image cleanup: provider refused delete orphan key=%q: %s", k, reason)
			sum.Failed++
			continue
		}
		sum.Deleted++
		sum.BytesFreed += sizeOf[k]
	}
}
