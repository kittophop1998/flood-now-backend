// Package route contains the safe-route use case: ask the routing provider
// for candidate routes, load the open reports along them in one corridor
// query, and score each route with the deterministic domain rules.
package route

import (
	"context"
	"time"

	domainreport "floodnow-api/internal/domain/report"
	domainroute "floodnow-api/internal/domain/route"
	"floodnow-api/internal/ports"
)

const (
	// maxCorridorBoxes bounds the OR'ed bounding boxes of the corridor query.
	maxCorridorBoxes = 24
	// corridorReportLimit bounds the reports loaded for one evaluation; if it
	// is hit the result is flagged incomplete instead of silently truncated.
	corridorReportLimit = 1000
)

type Service struct {
	provider ports.RouteProvider
	reports  ports.ReportRepository
	clock    ports.Clock
}

func NewService(provider ports.RouteProvider, reports ports.ReportRepository, clock ports.Clock) *Service {
	return &Service{provider: provider, reports: reports, clock: clock}
}

type Result struct {
	Vehicle     domainroute.Vehicle
	EvaluatedAt time.Time
	Routes      []domainroute.Evaluation
	// DataComplete is false when more reports matched the corridor than one
	// evaluation loads; the client must not present the verdict as complete.
	DataComplete bool
}

// Evaluate returns candidate routes, safest first. An empty Routes means the
// provider found no route between the points.
func (s *Service) Evaluate(ctx context.Context, origin, destination domainroute.Point, vehicle domainroute.Vehicle) (*Result, error) {
	if err := domainroute.ValidateTrip(origin, destination, vehicle); err != nil {
		return nil, err
	}
	candidates, err := s.provider.Routes(ctx, origin, destination, vehicle.Profile())
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	res := &Result{Vehicle: vehicle, EvaluatedAt: now, DataComplete: true}
	if len(candidates) == 0 {
		return res, nil
	}

	var boxes []ports.BBox
	for _, c := range candidates {
		for _, b := range domainroute.CorridorBoxes(c.Path, domainroute.MaxAffectRadiusM, maxCorridorBoxes/len(candidates)) {
			boxes = append(boxes, ports.BBox{MinLat: b.MinLat, MaxLat: b.MaxLat, MinLng: b.MinLng, MaxLng: b.MaxLng})
		}
	}
	reports, err := s.reports.List(ctx, ports.ReportFilter{
		BBoxes:   boxes,
		Statuses: domainreport.DefaultVisibleStatuses,
		Limit:    corridorReportLimit + 1,
		Now:      now,
	})
	if err != nil {
		return nil, err
	}
	if len(reports) > corridorReportLimit {
		reports = reports[:corridorReportLimit]
		res.DataComplete = false
	}
	res.Routes = domainroute.Evaluate(candidates, reports, vehicle, now)
	return res, nil
}
