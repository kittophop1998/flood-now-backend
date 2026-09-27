package imagecleanup_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	appimagecleanup "floodnow-api/internal/application/imagecleanup"
	domainannouncement "floodnow-api/internal/domain/announcement"
	domainimagecleanup "floodnow-api/internal/domain/imagecleanup"
	domainreport "floodnow-api/internal/domain/report"
	"floodnow-api/internal/domain/upload"
	"floodnow-api/internal/ports"
)

type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

// --- fake ReportRepository -------------------------------------------------

type clearCall struct {
	ID     uuid.UUID
	Key    string
	Result bool
}

// fakeReportRepo lets a test set the candidate list and the "current truth"
// (reports map) independently, so revalidation can be exercised without a
// real query planner: a candidate can describe a stale view of a report that
// has since changed in the map.
type fakeReportRepo struct {
	ports.ReportRepository
	reports        map[uuid.UUID]*domainreport.ReportWithStats
	candidates     []ports.ImageCleanupCandidate
	candidatesErr  error
	getErr         error
	referencedKeys map[string]bool
	clearErr       error
	clearCalls     []clearCall
}

func newFakeReportRepo() *fakeReportRepo {
	return &fakeReportRepo{reports: map[uuid.UUID]*domainreport.ReportWithStats{}, referencedKeys: map[string]bool{}}
}

func (f *fakeReportRepo) put(r domainreport.ReportWithStats) { f.reports[r.ID] = &r }

func (f *fakeReportRepo) ImageCleanupCandidates(context.Context, time.Time, int) ([]ports.ImageCleanupCandidate, error) {
	return f.candidates, f.candidatesErr
}

func (f *fakeReportRepo) GetByID(_ context.Context, id uuid.UUID) (*domainreport.ReportWithStats, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	r, ok := f.reports[id]
	if !ok {
		return nil, nil
	}
	cp := *r
	return &cp, nil
}

func (f *fakeReportRepo) ImageKeyReferenced(_ context.Context, key string) (bool, error) {
	return f.referencedKeys[key], nil
}

func (f *fakeReportRepo) ClearImageIfUnchanged(_ context.Context, id uuid.UUID, key string) (bool, error) {
	if f.clearErr != nil {
		return false, f.clearErr
	}
	r, ok := f.reports[id]
	ok = ok && r.ImageKey != nil && *r.ImageKey == key
	if ok {
		r.ImageKey = nil
	}
	f.clearCalls = append(f.clearCalls, clearCall{ID: id, Key: key, Result: ok})
	return ok, nil
}

// --- fake AnnouncementRepository --------------------------------------------

type removeCall struct {
	ID     uuid.UUID
	Key    string
	Result bool
}

type fakeAnnouncementRepo struct {
	ports.AnnouncementRepository
	anns           map[uuid.UUID]*domainannouncement.Announcement
	candidates     []ports.AnnouncementImageCandidate
	referencedKeys map[string]bool
	removeCalls    []removeCall
}

func newFakeAnnouncementRepo() *fakeAnnouncementRepo {
	return &fakeAnnouncementRepo{anns: map[uuid.UUID]*domainannouncement.Announcement{}, referencedKeys: map[string]bool{}}
}

func (f *fakeAnnouncementRepo) put(a domainannouncement.Announcement) { f.anns[a.ID] = &a }

func (f *fakeAnnouncementRepo) ExpiredImageCandidates(context.Context, time.Time, int) ([]ports.AnnouncementImageCandidate, error) {
	return f.candidates, nil
}

func (f *fakeAnnouncementRepo) Get(_ context.Context, id uuid.UUID) (*domainannouncement.Announcement, error) {
	a, ok := f.anns[id]
	if !ok {
		return nil, nil
	}
	cp := *a
	cp.Images = append([]domainannouncement.Image{}, a.Images...)
	return &cp, nil
}

func (f *fakeAnnouncementRepo) ImageKeyReferenced(_ context.Context, key string) (bool, error) {
	return f.referencedKeys[key], nil
}

func (f *fakeAnnouncementRepo) RemoveImageIfPresent(_ context.Context, id uuid.UUID, key string) (bool, error) {
	a, ok := f.anns[id]
	if ok {
		for i, img := range a.Images {
			if img.Key == key {
				a.Images = append(a.Images[:i:i], a.Images[i+1:]...)
				f.removeCalls = append(f.removeCalls, removeCall{ID: id, Key: key, Result: true})
				return true, nil
			}
		}
	}
	f.removeCalls = append(f.removeCalls, removeCall{ID: id, Key: key, Result: false})
	return false, nil
}

// --- fake ImageStore (R2) ----------------------------------------------------

type fakeStore struct {
	pages            map[string][][]ports.ObjectSummary // prefix -> pages, consumed in order
	pageIndex        map[string]int
	lastOlderThan    map[string]time.Time
	deleteErr        error
	deleteFailedKeys map[string]string
	deletedKeys      []string
	deleteCalls      [][]string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		pages:            map[string][][]ports.ObjectSummary{},
		pageIndex:        map[string]int{},
		lastOlderThan:    map[string]time.Time{},
		deleteFailedKeys: map[string]string{},
	}
}

func (s *fakeStore) ListObjects(_ context.Context, prefix string, olderThan time.Time, _ string, _ int32) ([]ports.ObjectSummary, string, error) {
	s.lastOlderThan[prefix] = olderThan
	idx := s.pageIndex[prefix]
	pages := s.pages[prefix]
	if idx >= len(pages) {
		return nil, "", nil
	}
	s.pageIndex[prefix] = idx + 1
	next := ""
	if idx+1 < len(pages) {
		next = "more"
	}
	return pages[idx], next, nil
}

func (s *fakeStore) DeleteObjects(_ context.Context, keys []string) (map[string]string, error) {
	if s.deleteErr != nil {
		return nil, s.deleteErr
	}
	s.deleteCalls = append(s.deleteCalls, append([]string{}, keys...))
	failed := map[string]string{}
	for _, k := range keys {
		if reason, bad := s.deleteFailedKeys[k]; bad {
			failed[k] = reason
			continue
		}
		s.deletedKeys = append(s.deletedKeys, k)
	}
	return failed, nil
}

// --- helpers -----------------------------------------------------------------

func newService(reports *fakeReportRepo, anns *fakeAnnouncementRepo, store *fakeStore, now time.Time, mutate func(*domainimagecleanup.Policy)) *appimagecleanup.Service {
	policy := domainimagecleanup.DefaultPolicy()
	policy.BatchSize = 50
	if mutate != nil {
		mutate(&policy)
	}
	return appimagecleanup.NewService(reports, anns, store, fakeClock{now: now}, policy, nil)
}

var now = time.Date(2026, 9, 27, 3, 0, 0, 0, time.UTC)

// --- report image (rule B) ---------------------------------------------------

func TestReportImage_ResolvedPastRetention_Deleted(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()
	id := uuid.New()
	resolvedAt := now.Add(-8 * 24 * time.Hour)
	reports.put(domainreport.ReportWithStats{Report: domainreport.Report{
		ID: id, ImageKey: strPtr("reports/2026/09/19/a.jpg"), ResolvedAt: &resolvedAt, ExpiresAt: now.Add(24 * time.Hour),
	}})
	reports.candidates = []ports.ImageCleanupCandidate{{ReportID: id, ImageKey: "reports/2026/09/19/a.jpg", ResolvedAt: &resolvedAt}}

	svc := newService(reports, anns, store, now, nil)
	sum := svc.Run(context.Background())

	if sum.Deleted != 1 || sum.Skipped != 0 || sum.Failed != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	if len(store.deletedKeys) != 1 || store.deletedKeys[0] != "reports/2026/09/19/a.jpg" {
		t.Fatalf("deletedKeys = %v", store.deletedKeys)
	}
	if reports.reports[id].ImageKey != nil {
		t.Fatal("image_key should be cleared after a successful delete")
	}
}

func TestReportImage_ResolvedWithinRetention_Kept(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()
	id := uuid.New()
	resolvedAt := now.Add(-3 * 24 * time.Hour) // within the 7d default retention
	reports.put(domainreport.ReportWithStats{Report: domainreport.Report{
		ID: id, ImageKey: strPtr("reports/x.jpg"), ResolvedAt: &resolvedAt, ExpiresAt: now.Add(24 * time.Hour),
	}})
	// Even if a query bug surfaced this as a candidate, revalidation must
	// still refuse to delete it.
	reports.candidates = []ports.ImageCleanupCandidate{{ReportID: id, ImageKey: "reports/x.jpg", ResolvedAt: &resolvedAt}}

	svc := newService(reports, anns, store, now, nil)
	sum := svc.Run(context.Background())

	if sum.Deleted != 0 || sum.Skipped != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if len(store.deletedKeys) != 0 {
		t.Fatalf("nothing should be deleted, got %v", store.deletedKeys)
	}
	if reports.reports[id].ImageKey == nil {
		t.Fatal("image_key must not be cleared")
	}
}

func TestReportImage_ActiveNeverDeletedRegardlessOfAge(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()
	id := uuid.New()
	// A long-lived facility report, created long ago, still active: no
	// resolved_at, expires_at far in the future.
	reports.put(domainreport.ReportWithStats{Report: domainreport.Report{
		ID: id, ImageKey: strPtr("reports/old-but-active.jpg"), ResolvedAt: nil, ExpiresAt: now.Add(30 * 24 * time.Hour),
	}})
	reports.candidates = []ports.ImageCleanupCandidate{{ReportID: id, ImageKey: "reports/old-but-active.jpg", ExpiresAt: now.Add(30 * 24 * time.Hour)}}

	svc := newService(reports, anns, store, now, nil)
	sum := svc.Run(context.Background())

	if sum.Deleted != 0 || sum.Skipped != 1 {
		t.Fatalf("an active report's image must never be deleted for age alone: summary = %+v", sum)
	}
}

func TestReportImage_ReopenedBeforeDelete_Skipped(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()
	id := uuid.New()
	staleResolvedAt := now.Add(-10 * 24 * time.Hour)
	// Candidate query saw it resolved 10 days ago; by the time we revalidate,
	// a new vote reopened it (resolved_at cleared, expires_at pushed out).
	reports.put(domainreport.ReportWithStats{Report: domainreport.Report{
		ID: id, ImageKey: strPtr("reports/reopened.jpg"), ResolvedAt: nil, ExpiresAt: now.Add(6 * time.Hour),
	}})
	reports.candidates = []ports.ImageCleanupCandidate{{ReportID: id, ImageKey: "reports/reopened.jpg", ResolvedAt: &staleResolvedAt}}

	svc := newService(reports, anns, store, now, nil)
	sum := svc.Run(context.Background())

	if sum.Deleted != 0 || sum.Skipped != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if len(store.deleteCalls) != 0 {
		t.Fatalf("R2 delete must not be called once the candidate is no longer eligible, got %v", store.deleteCalls)
	}
}

func TestReportImage_ImageReplacedBeforeDelete_Skipped(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()
	id := uuid.New()
	resolvedAt := now.Add(-8 * 24 * time.Hour)
	// A newer condition update gave the report a different image between the
	// candidate query and now.
	reports.put(domainreport.ReportWithStats{Report: domainreport.Report{
		ID: id, ImageKey: strPtr("reports/new.jpg"), ResolvedAt: &resolvedAt, ExpiresAt: now.Add(24 * time.Hour),
	}})
	reports.candidates = []ports.ImageCleanupCandidate{{ReportID: id, ImageKey: "reports/old.jpg", ResolvedAt: &resolvedAt}}

	svc := newService(reports, anns, store, now, nil)
	sum := svc.Run(context.Background())

	if sum.Deleted != 0 || sum.Skipped != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if len(store.deleteCalls) != 0 {
		t.Fatalf("must not touch the old key, got %v", store.deleteCalls)
	}
}

func TestReportImage_R2DeleteError_DBNotUpdated(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()
	id := uuid.New()
	resolvedAt := now.Add(-8 * 24 * time.Hour)
	reports.put(domainreport.ReportWithStats{Report: domainreport.Report{
		ID: id, ImageKey: strPtr("reports/a.jpg"), ResolvedAt: &resolvedAt, ExpiresAt: now.Add(24 * time.Hour),
	}})
	reports.candidates = []ports.ImageCleanupCandidate{{ReportID: id, ImageKey: "reports/a.jpg", ResolvedAt: &resolvedAt}}
	store.deleteErr = errors.New("r2 unavailable")

	svc := newService(reports, anns, store, now, nil)
	sum := svc.Run(context.Background())

	if sum.Failed != 1 || sum.Deleted != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	if len(reports.clearCalls) != 0 {
		t.Fatal("image_key must not be cleared when the R2 delete failed")
	}
	if reports.reports[id].ImageKey == nil {
		t.Fatal("image_key must still be set")
	}
}

func TestReportImage_ProviderPerKeyFailure_DBNotUpdated(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()
	id := uuid.New()
	resolvedAt := now.Add(-8 * 24 * time.Hour)
	reports.put(domainreport.ReportWithStats{Report: domainreport.Report{
		ID: id, ImageKey: strPtr("reports/a.jpg"), ResolvedAt: &resolvedAt, ExpiresAt: now.Add(24 * time.Hour),
	}})
	reports.candidates = []ports.ImageCleanupCandidate{{ReportID: id, ImageKey: "reports/a.jpg", ResolvedAt: &resolvedAt}}
	store.deleteFailedKeys["reports/a.jpg"] = "AccessDenied: nope"

	svc := newService(reports, anns, store, now, nil)
	sum := svc.Run(context.Background())

	if sum.Failed != 1 || sum.Deleted != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	if len(reports.clearCalls) != 0 {
		t.Fatal("image_key must not be cleared when the provider refused the delete")
	}
}

func TestReportImage_QueryError_NoPanic(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()
	reports.candidatesErr = errors.New("db down")

	svc := newService(reports, anns, store, now, nil)
	sum := svc.Run(context.Background())
	if sum.Failed != 0 && sum.Deleted != 0 {
		// a query failure just yields nothing to do this pass, not a crash
	}
}

// --- announcement image (rule B) ---------------------------------------------

func TestAnnouncementImage_ExpiredPastRetention_Deleted(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()
	id := uuid.New()
	endsAt := now.Add(-8 * 24 * time.Hour)
	anns.put(domainannouncement.Announcement{ID: id, EndsAt: &endsAt, Images: []domainannouncement.Image{{Key: "announcements/a.jpg"}, {Key: "announcements/b.jpg"}}})
	anns.candidates = []ports.AnnouncementImageCandidate{
		{AnnouncementID: id, ImageKey: "announcements/a.jpg", EndsAt: endsAt},
		{AnnouncementID: id, ImageKey: "announcements/b.jpg", EndsAt: endsAt},
	}

	svc := newService(reports, anns, store, now, nil)
	sum := svc.Run(context.Background())

	if sum.Deleted != 2 {
		t.Fatalf("summary = %+v", sum)
	}
	if len(anns.anns[id].Images) != 0 {
		t.Fatalf("both images should be removed, got %v", anns.anns[id].Images)
	}
}

func TestAnnouncementImage_ExtendedBeforeDelete_Skipped(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()
	id := uuid.New()
	staleEndsAt := now.Add(-8 * 24 * time.Hour)
	extendedEndsAt := now.Add(30 * 24 * time.Hour) // an admin extended the window since
	anns.put(domainannouncement.Announcement{ID: id, EndsAt: &extendedEndsAt, Images: []domainannouncement.Image{{Key: "announcements/a.jpg"}}})
	anns.candidates = []ports.AnnouncementImageCandidate{{AnnouncementID: id, ImageKey: "announcements/a.jpg", EndsAt: staleEndsAt}}

	svc := newService(reports, anns, store, now, nil)
	sum := svc.Run(context.Background())

	if sum.Deleted != 0 || sum.Skipped != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if len(anns.anns[id].Images) != 1 {
		t.Fatal("image must be kept")
	}
}

func TestAnnouncementImage_AlreadyRemovedBeforeDelete_Skipped(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()
	id := uuid.New()
	endsAt := now.Add(-8 * 24 * time.Hour)
	anns.put(domainannouncement.Announcement{ID: id, EndsAt: &endsAt, Images: nil}) // admin already dropped it
	anns.candidates = []ports.AnnouncementImageCandidate{{AnnouncementID: id, ImageKey: "announcements/gone.jpg", EndsAt: endsAt}}

	svc := newService(reports, anns, store, now, nil)
	sum := svc.Run(context.Background())

	if sum.Deleted != 0 || sum.Skipped != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if len(store.deleteCalls) != 0 {
		t.Fatal("must not delete an object the candidate no longer names")
	}
}

// --- orphan sweep (rules A/C) --------------------------------------------------

func TestOrphan_UnreferencedPastGrace_Deleted(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()
	old := now.Add(-48 * time.Hour)
	store.pages[upload.ReportKeyPrefix] = [][]ports.ObjectSummary{
		{{Key: "reports/orphan.jpg", Size: 1024, LastModified: old}},
	}

	svc := newService(reports, anns, store, now, nil)
	sum := svc.Run(context.Background())

	if sum.Deleted != 1 || sum.BytesFreed != 1024 {
		t.Fatalf("summary = %+v", sum)
	}
	if len(store.deletedKeys) != 1 || store.deletedKeys[0] != "reports/orphan.jpg" {
		t.Fatalf("deletedKeys = %v", store.deletedKeys)
	}
	// The orphan cutoff handed to the store must be now - OrphanRetention.
	want := now.Add(-domainimagecleanup.DefaultPolicy().OrphanRetention)
	if got := store.lastOlderThan[upload.ReportKeyPrefix]; !got.Equal(want) {
		t.Fatalf("cutoff = %v, want %v", got, want)
	}
}

func TestOrphan_Referenced_Kept(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()
	old := now.Add(-48 * time.Hour)
	store.pages[upload.ReportKeyPrefix] = [][]ports.ObjectSummary{
		{{Key: "reports/still-used.jpg", Size: 500, LastModified: old}},
	}
	reports.referencedKeys["reports/still-used.jpg"] = true

	svc := newService(reports, anns, store, now, nil)
	sum := svc.Run(context.Background())

	if sum.Deleted != 0 || sum.Skipped != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if len(store.deletedKeys) != 0 {
		t.Fatal("a referenced image must never be deleted")
	}
}

func TestOrphan_AnnouncementPrefix_UsesAnnouncementReferenceCheck(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()
	old := now.Add(-48 * time.Hour)
	store.pages[upload.AnnouncementKeyPrefix] = [][]ports.ObjectSummary{
		{{Key: "announcements/used.jpg", Size: 10, LastModified: old}, {Key: "announcements/unused.jpg", Size: 20, LastModified: old}},
	}
	anns.referencedKeys["announcements/used.jpg"] = true

	svc := newService(reports, anns, store, now, nil)
	sum := svc.Run(context.Background())

	if sum.Deleted != 1 || sum.Skipped != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	if len(store.deletedKeys) != 1 || store.deletedKeys[0] != "announcements/unused.jpg" {
		t.Fatalf("deletedKeys = %v", store.deletedKeys)
	}
}

func TestOrphan_MultiplePages_AllProcessed(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()
	old := now.Add(-48 * time.Hour)
	store.pages[upload.ReportKeyPrefix] = [][]ports.ObjectSummary{
		{{Key: "reports/p1.jpg", Size: 1, LastModified: old}},
		{{Key: "reports/p2.jpg", Size: 2, LastModified: old}},
	}

	svc := newService(reports, anns, store, now, nil)
	sum := svc.Run(context.Background())

	if sum.Deleted != 2 {
		t.Fatalf("summary = %+v", sum)
	}
	if store.pageIndex[upload.ReportKeyPrefix] != 2 {
		t.Fatalf("expected both pages to be fetched, got %d calls", store.pageIndex[upload.ReportKeyPrefix])
	}
}

// --- dry run -------------------------------------------------------------------

func TestDryRun_NothingPhysicallyDeleted(t *testing.T) {
	reports, anns, store := newFakeReportRepo(), newFakeAnnouncementRepo(), newFakeStore()

	id := uuid.New()
	resolvedAt := now.Add(-8 * 24 * time.Hour)
	reports.put(domainreport.ReportWithStats{Report: domainreport.Report{
		ID: id, ImageKey: strPtr("reports/a.jpg"), ResolvedAt: &resolvedAt, ExpiresAt: now.Add(24 * time.Hour),
	}})
	reports.candidates = []ports.ImageCleanupCandidate{{ReportID: id, ImageKey: "reports/a.jpg", ResolvedAt: &resolvedAt}}

	annID := uuid.New()
	endsAt := now.Add(-8 * 24 * time.Hour)
	anns.put(domainannouncement.Announcement{ID: annID, EndsAt: &endsAt, Images: []domainannouncement.Image{{Key: "announcements/a.jpg"}}})
	anns.candidates = []ports.AnnouncementImageCandidate{{AnnouncementID: annID, ImageKey: "announcements/a.jpg", EndsAt: endsAt}}

	old := now.Add(-48 * time.Hour)
	store.pages[upload.ReportKeyPrefix] = [][]ports.ObjectSummary{{{Key: "reports/orphan.jpg", Size: 9, LastModified: old}}}

	svc := newService(reports, anns, store, now, func(p *domainimagecleanup.Policy) { p.DryRun = true })
	sum := svc.Run(context.Background())

	if !sum.DryRun {
		t.Fatal("summary should report dry_run")
	}
	if sum.Deleted == 0 {
		t.Fatal("dry-run should still report what it would have deleted")
	}
	if len(store.deleteCalls) != 0 {
		t.Fatalf("dry-run must never call DeleteObjects, got %v", store.deleteCalls)
	}
	if reports.reports[id].ImageKey == nil {
		t.Fatal("dry-run must not clear image_key")
	}
	if len(anns.anns[annID].Images) != 1 {
		t.Fatal("dry-run must not remove the announcement image")
	}
}

func strPtr(s string) *string { return &s }
