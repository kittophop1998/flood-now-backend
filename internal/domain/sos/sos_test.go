package sos

import "testing"

func TestStateMachine(t *testing.T) {
	allowed := []struct {
		from, to Status
		role     Role
	}{
		{StatusWaiting, StatusCancelled, RoleRequester},
		{StatusMatched, StatusOnTheWay, RoleHelper},
		{StatusMatched, StatusWaiting, RoleHelper}, // helper withdraws
		{StatusOnTheWay, StatusArrived, RoleHelper},
		{StatusArrived, StatusCompleted, RoleHelper},
		{StatusArrived, StatusCompleted, RoleRequester},
		{StatusOnTheWay, StatusCancelled, RoleRequester},
	}
	for _, c := range allowed {
		if !CanTransition(c.from, c.to, c.role) {
			t.Errorf("%s: %s -> %s should be allowed", c.role, c.from, c.to)
		}
	}
	denied := []struct {
		from, to Status
		role     Role
	}{
		{StatusWaiting, StatusMatched, RoleHelper},      // only via atomic Accept
		{StatusWaiting, StatusCompleted, RoleRequester}, // can't skip ahead
		{StatusMatched, StatusCancelled, RoleHelper},    // helpers withdraw, they don't cancel
		{StatusOnTheWay, StatusArrived, RoleRequester},  // only the helper reports arrival
		{StatusMatched, StatusArrived, RoleHelper},      // no skipping on_the_way
		{StatusCompleted, StatusWaiting, RoleHelper},    // terminal
		{StatusCancelled, StatusWaiting, RoleRequester}, // terminal
	}
	for _, c := range denied {
		if CanTransition(c.from, c.to, c.role) {
			t.Errorf("%s: %s -> %s should be denied", c.role, c.from, c.to)
		}
	}
}

func TestCapabilityMatching(t *testing.T) {
	if !CanAnswer(TypeTrapped, []Capability{CapBoat}) || !CanAnswer(TypeTrapped, []Capability{CapHighVehicle}) {
		t.Error("boats and high vehicles can reach trapped people")
	}
	if CanAnswer(TypeNeedBoat, []Capability{CapHighVehicle, CapFirstAid}) {
		t.Error("a boat request needs a boat")
	}
	if !CanAnswer(TypeVehicleStalled, []Capability{CapTowing}) || CanAnswer(TypeVehicleStalled, []Capability{CapFoodWater}) {
		t.Error("stalled vehicles need repair or towing")
	}
	if !CanAnswer(TypeOther, []Capability{CapShelter}) {
		t.Error("any helper can answer an 'other' request")
	}
	if CanAnswer(TypeNeedShelter, nil) {
		t.Error("a helper without capabilities answers nothing")
	}
}

func TestRoleOf(t *testing.T) {
	helper := "helper-device-1"
	r := Request{DeviceID: "requester-device", HelperDeviceID: &helper}
	if r.RoleOf("requester-device") != RoleRequester || r.RoleOf(helper) != RoleHelper || r.RoleOf("stranger-device") != "" || r.RoleOf("") != "" {
		t.Error("role resolution is wrong")
	}
}

func TestValidation(t *testing.T) {
	people := 0
	bad := NewRequestInput{DeviceID: "short", Type: "alien", Latitude: 91, PeopleCount: &people}
	if bad.Validate() == nil {
		t.Error("invalid request accepted")
	}
	ok := NewRequestInput{DeviceID: "12345678-device", Type: TypeNeedBoat, Latitude: 13.7, Longitude: 100.5}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid request rejected: %v", err)
	}

	h := HelperInput{DeviceID: "12345678-device", Active: true, RadiusM: 3000}
	if h.Validate() == nil {
		t.Error("an active helper needs at least one capability")
	}
	h.Capabilities = []Capability{CapBoat, CapBoat}
	if h.Validate() == nil {
		t.Error("duplicate capabilities accepted")
	}
	h.Capabilities = []Capability{CapBoat}
	h.RadiusM = 2000
	if h.Validate() == nil {
		t.Error("unsupported radius accepted")
	}
	h.RadiusM = 10000
	if err := h.Validate(); err != nil {
		t.Errorf("valid helper rejected: %v", err)
	}
}

func TestApproximateCoordinate(t *testing.T) {
	if got := ApproximateCoordinate(13.756349); got != 13.756 {
		t.Errorf("got %v", got)
	}
	if got := ApproximateCoordinate(-0.0126); got != -0.013 {
		t.Errorf("got %v", got)
	}
}
