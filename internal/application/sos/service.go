// Package sos contains the SOS request and helper-mode use cases. Every read
// and write is authorized server-side by the caller's device: requesters see
// and cancel their own requests, only the assigned helper sees a request's
// exact location and contact, everyone else gets 404.
package sos

import (
	"context"
	"log"

	"github.com/google/uuid"

	"floodnow-api/internal/domain/apperr"
	domainsos "floodnow-api/internal/domain/sos"
	"floodnow-api/internal/ports"
)

const (
	listLimit       = 20
	nearbyLimit     = 50
	minNearbyRadius = 100
)

type Service struct {
	repo  ports.SOSRepository
	clock ports.Clock
}

func NewService(repo ports.SOSRepository, clock ports.Clock) *Service {
	return &Service{repo: repo, clock: clock}
}

// View is a request as seen by one of its parties.
type View struct {
	domainsos.Request
	Role   domainsos.Role
	Events []domainsos.Event
	// Helper is the assigned helper's public profile, shown to the requester
	// once matched.
	Helper *domainsos.Helper
	// NearbyHelpers counts active helpers able to answer, for a waiting
	// request (requester view only).
	NearbyHelpers *int
}

// Create files a new SOS. A device can have only one open request; with a
// ClientID a retried submission returns the original request.
func (s *Service) Create(ctx context.Context, in domainsos.NewRequestInput) (*View, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if in.ClientID != nil {
		existing, err := s.repo.FindByClientID(ctx, in.DeviceID, *in.ClientID)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return s.view(ctx, existing, domainsos.RoleRequester)
		}
	}
	now := s.clock.Now()
	r := &domainsos.Request{
		ID:           uuid.New(),
		DeviceID:     in.DeviceID,
		ClientID:     in.ClientID,
		Type:         in.Type,
		Description:  domainsos.Trimmed(in.Description),
		Latitude:     in.Latitude,
		Longitude:    in.Longitude,
		PeopleCount:  in.PeopleCount,
		ContactPhone: domainsos.Trimmed(in.ContactPhone),
		Status:       domainsos.StatusWaiting,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := s.repo.Create(ctx, r); err != nil {
		return nil, err
	}
	log.Printf("sos %s created type=%s", r.ID, r.Type)
	return s.view(ctx, r, domainsos.RoleRequester)
}

// Get returns a request to its requester or assigned helper; anyone else
// gets NOT_FOUND so request ids reveal nothing.
func (s *Service) Get(ctx context.Context, deviceID string, id uuid.UUID) (*View, error) {
	if err := domainsos.ValidateDeviceID(deviceID); err != nil {
		return nil, err
	}
	r, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	role := roleOf(r, deviceID)
	if role == "" {
		return nil, apperr.NotFound("sos request not found")
	}
	return s.view(ctx, r, role)
}

func roleOf(r *domainsos.Request, deviceID string) domainsos.Role {
	if r == nil {
		return ""
	}
	return r.RoleOf(deviceID)
}

// Mine lists requests the device made or is helping with.
func (s *Service) Mine(ctx context.Context, deviceID string) ([]View, error) {
	if err := domainsos.ValidateDeviceID(deviceID); err != nil {
		return nil, err
	}
	rs, err := s.repo.ListForDevice(ctx, deviceID, listLimit)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(rs))
	for i := range rs {
		// Timelines and helper profiles are only loaded for open requests;
		// the list stays a handful of rows so this is bounded.
		if rs[i].Status.IsClosed() {
			out = append(out, View{Request: rs[i], Role: rs[i].RoleOf(deviceID)})
			continue
		}
		v, err := s.view(ctx, &rs[i], rs[i].RoleOf(deviceID))
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, nil
}

// UpdateStatus applies a transition requested by one of the parties. The
// allowed moves per role live in the domain state machine; the repository
// applies it only if nobody changed the request in between.
func (s *Service) UpdateStatus(ctx context.Context, deviceID string, id uuid.UUID, to domainsos.Status) (*View, error) {
	if err := domainsos.ValidateDeviceID(deviceID); err != nil {
		return nil, err
	}
	if !to.Valid() {
		return nil, apperr.Validation("status is invalid", map[string]string{"status": "must be one of waiting, on_the_way, arrived, completed, cancelled"})
	}
	r, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	role := roleOf(r, deviceID)
	if role == "" {
		return nil, apperr.NotFound("sos request not found")
	}
	if !domainsos.CanTransition(r.Status, to, role) {
		return nil, apperr.Conflict("this status change isn't allowed now (current status: " + string(r.Status) + ")")
	}
	t := ports.SOSTransition{ID: id, From: r.Status, To: to, Actor: role, Now: s.clock.Now()}
	if role == domainsos.RoleHelper {
		t.HelperDeviceID = &deviceID
	}
	ok, err := s.repo.Transition(ctx, t)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.Conflict("the request changed in the meantime; reload and try again")
	}
	log.Printf("sos %s %s -> %s by %s", id, r.Status, to, role)
	return s.Get(ctx, deviceID, id)
}

// Accept assigns a waiting request to the calling helper. The helper must be
// active, able to answer the request type and within their radius; the
// assignment itself is atomic so two helpers can never both win.
func (s *Service) Accept(ctx context.Context, deviceID string, id uuid.UUID) (*View, error) {
	if err := domainsos.ValidateDeviceID(deviceID); err != nil {
		return nil, err
	}
	h, err := s.repo.GetHelper(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if h == nil || !h.Active {
		return nil, apperr.Conflict("turn on helper mode first")
	}
	r, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if r == nil || r.DeviceID == deviceID {
		return nil, apperr.NotFound("sos request not found")
	}
	if r.Status != domainsos.StatusWaiting {
		if r.RoleOf(deviceID) == domainsos.RoleHelper {
			return s.view(ctx, r, domainsos.RoleHelper) // already ours: idempotent
		}
		return nil, apperr.Conflict("another helper already accepted this request")
	}
	if !domainsos.CanAnswer(r.Type, h.Capabilities) {
		return nil, apperr.Conflict("your helper capabilities don't match this request")
	}
	ok, err := s.repo.Accept(ctx, id, deviceID, domainsos.MaxActiveAssignments, s.clock.Now())
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.Conflict("another helper already accepted this request")
	}
	log.Printf("sos %s accepted", id)
	return s.Get(ctx, deviceID, id)
}

// Nearby lists waiting requests the calling helper can answer from where
// they are now, and records that position for requester-side counts.
func (s *Service) Nearby(ctx context.Context, deviceID string, lat, lng float64) ([]ports.SOSWithDistance, error) {
	if err := domainsos.ValidateDeviceID(deviceID); err != nil {
		return nil, err
	}
	if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
		return nil, apperr.Validation("location is invalid", map[string]string{"lat": "must be a valid latitude/longitude"})
	}
	h, err := s.repo.GetHelper(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if h == nil || !h.Active {
		return nil, apperr.Conflict("helper mode is off")
	}
	now := s.clock.Now()
	if err := s.repo.UpdateHelperLocation(ctx, deviceID, lat, lng, now); err != nil {
		return nil, err
	}
	var types []domainsos.Type
	for _, t := range allTypes {
		if domainsos.CanAnswer(t, h.Capabilities) {
			types = append(types, t)
		}
	}
	radius := float64(h.RadiusM)
	if radius < minNearbyRadius {
		radius = minNearbyRadius
	}
	items, err := s.repo.NearbyWaiting(ctx, ports.NearbySOSQuery{
		Latitude: lat, Longitude: lng, RadiusM: radius, Types: types,
		ExcludeDeviceID: deviceID, CreatedSince: now.Add(-domainsos.MaxOpenAge), Limit: nearbyLimit,
	})
	return items, err
}

var allTypes = []domainsos.Type{
	domainsos.TypeTrapped, domainsos.TypeElderlyOrPatient, domainsos.TypeVehicleStalled, domainsos.TypeNeedBoat,
	domainsos.TypeNeedHighVehicle, domainsos.TypeNeedFoodWater, domainsos.TypeNeedShelter, domainsos.TypeOther,
}

// Helper returns the device's helper profile (nil if it never opted in).
func (s *Service) Helper(ctx context.Context, deviceID string) (*domainsos.Helper, error) {
	if err := domainsos.ValidateDeviceID(deviceID); err != nil {
		return nil, err
	}
	return s.repo.GetHelper(ctx, deviceID)
}

// SaveHelper creates or replaces the device's helper profile.
func (s *Service) SaveHelper(ctx context.Context, in domainsos.HelperInput) (*domainsos.Helper, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	now := s.clock.Now()
	existing, err := s.repo.GetHelper(ctx, in.DeviceID)
	if err != nil {
		return nil, err
	}
	h := &domainsos.Helper{DeviceID: in.DeviceID, CreatedAt: now}
	if existing != nil {
		*h = *existing
	}
	h.Active = in.Active
	h.Capabilities = in.Capabilities
	h.RadiusM = in.RadiusM
	h.DisplayName = domainsos.Trimmed(in.DisplayName)
	h.ContactPhone = domainsos.Trimmed(in.ContactPhone)
	h.UpdatedAt = now
	if in.Latitude != nil {
		h.Latitude, h.Longitude, h.LocationAt = in.Latitude, in.Longitude, &now
	}
	if err := s.repo.UpsertHelper(ctx, h); err != nil {
		return nil, err
	}
	return h, nil
}

// view loads what the given party may see.
func (s *Service) view(ctx context.Context, r *domainsos.Request, role domainsos.Role) (*View, error) {
	v := &View{Request: *r, Role: role}
	events, err := s.repo.Events(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	v.Events = events
	if role == domainsos.RoleRequester && r.HelperDeviceID != nil && !r.Status.IsClosed() {
		h, err := s.repo.GetHelper(ctx, *r.HelperDeviceID)
		if err != nil {
			return nil, err
		}
		v.Helper = h
	}
	if role == domainsos.RoleRequester && r.Status == domainsos.StatusWaiting {
		n, err := s.repo.CountHelpersNear(ctx, ports.HelperCountQuery{
			Latitude: r.Latitude, Longitude: r.Longitude,
			Capabilities:    r.Type.RequiredCapabilities(),
			ExcludeDeviceID: r.DeviceID,
			LocationSince:   s.clock.Now().Add(-domainsos.HelperLocationMaxAge),
		})
		if err != nil {
			return nil, err
		}
		v.NearbyHelpers = &n
	}
	return v, nil
}
