package localservice

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

var t0 = time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

func ptrTime(t time.Time) *time.Time { return &t }

func TestEffectiveStatus(t *testing.T) {
	offer := uuid.New()
	base := Request{Status: StatusOpen, ExpiresAt: t0.Add(2 * time.Hour)}

	if got := base.Effective(t0); got != StatusOpen {
		t.Fatalf("open request = %s", got)
	}
	if got := base.Effective(t0.Add(2 * time.Hour)); got != StatusExpired {
		t.Fatalf("open request past expiry = %s, want expired", got)
	}

	pending := base
	pending.Status, pending.SelectedOfferID, pending.SelectionExpiresAt = StatusPendingConfirmation, &offer, ptrTime(t0.Add(10*time.Minute))
	if got := pending.Effective(t0.Add(5 * time.Minute)); got != StatusPendingConfirmation {
		t.Fatalf("pending within deadline = %s", got)
	}
	if got := pending.Effective(t0.Add(10 * time.Minute)); got != StatusOpen {
		t.Fatalf("pending past confirmation deadline = %s, want open (back to the customer)", got)
	}
	if got := pending.Effective(t0.Add(3 * time.Hour)); got != StatusExpired {
		t.Fatalf("pending past request expiry = %s, want expired", got)
	}

	matched := base
	matched.Status = StatusMatched
	if got := matched.Effective(t0.Add(48 * time.Hour)); got != StatusMatched {
		t.Fatalf("a matched job never expires on its own, got %s", got)
	}
}

func TestEffectiveOffer(t *testing.T) {
	id := uuid.New()
	r := Request{Status: StatusPendingConfirmation, SelectedOfferID: &id, SelectionExpiresAt: ptrTime(t0.Add(10 * time.Minute)), ExpiresAt: t0.Add(time.Hour)}
	selected := Offer{ID: id, Status: OfferSelected}
	if got := EffectiveOffer(selected, r, t0); got != OfferSelected {
		t.Fatalf("selected within deadline = %s", got)
	}
	if got := EffectiveOffer(selected, r, t0.Add(11*time.Minute)); got != OfferExpired {
		t.Fatalf("selection past deadline = %s, want expired", got)
	}
	pending := Offer{ID: uuid.New(), Status: OfferPending}
	if got := EffectiveOffer(pending, r, t0.Add(2*time.Hour)); got != OfferExpired {
		t.Fatalf("pending offer on an expired request = %s, want expired", got)
	}
	if got := EffectiveOffer(pending, r, t0); got != OfferPending {
		t.Fatalf("pending offer while another is selected = %s, want pending (can still be chosen later)", got)
	}
}

func TestJobTransitions(t *testing.T) {
	cases := []struct {
		from, to Status
		role     Role
		want     bool
	}{
		{StatusMatched, StatusOnTheWay, RoleProvider, true},
		{StatusMatched, StatusOnTheWay, RoleCustomer, false},
		{StatusOnTheWay, StatusArrived, RoleProvider, true},
		{StatusArrived, StatusCompleted, RoleProvider, true},
		{StatusArrived, StatusCompleted, RoleCustomer, true},
		{StatusMatched, StatusCompleted, RoleProvider, false},
		{StatusOpen, StatusMatched, RoleProvider, false}, // matching is accept, never a status change
		{StatusPendingConfirmation, StatusMatched, RoleProvider, false},
	}
	for _, c := range cases {
		if got := CanTransition(c.from, c.to, c.role); got != c.want {
			t.Errorf("CanTransition(%s→%s, %s) = %v, want %v", c.from, c.to, c.role, got, c.want)
		}
	}
}

func TestCanCancel(t *testing.T) {
	for _, s := range []Status{StatusOpen, StatusPendingConfirmation, StatusMatched, StatusOnTheWay, StatusArrived} {
		if !CanCancel(s, RoleCustomer) {
			t.Errorf("customer should be able to cancel from %s", s)
		}
	}
	for _, s := range []Status{StatusCompleted, StatusCancelled, StatusExpired} {
		if CanCancel(s, RoleCustomer) {
			t.Errorf("customer must not cancel a closed request (%s)", s)
		}
	}
	if !CanCancel(StatusMatched, RoleProvider) || !CanCancel(StatusOnTheWay, RoleProvider) {
		t.Error("provider may back out before arriving")
	}
	if CanCancel(StatusArrived, RoleProvider) || CanCancel(StatusOpen, RoleProvider) {
		t.Error("provider may only back out of a matched job before arriving")
	}
}

func TestRefundEligible(t *testing.T) {
	grace := 10 * time.Minute
	m := Match{ID: uuid.New(), FeeCredits: 20, Status: MatchActive, CreatedAt: t0}

	if !RefundEligible(m, StatusMatched, RoleCustomer, t0.Add(5*time.Minute), grace) {
		t.Fatal("customer cancelling within grace before travel should be refunded")
	}
	if RefundEligible(m, StatusMatched, RoleCustomer, t0.Add(11*time.Minute), grace) {
		t.Fatal("after the grace period there is no refund")
	}
	if RefundEligible(m, StatusOnTheWay, RoleCustomer, t0.Add(time.Minute), grace) {
		t.Fatal("once the provider set off there is no refund")
	}
	travelled := m
	travelled.StartedTravelAt = ptrTime(t0.Add(time.Minute))
	if RefundEligible(travelled, StatusMatched, RoleCustomer, t0.Add(2*time.Minute), grace) {
		t.Fatal("a match that started travel is never refunded")
	}
	if RefundEligible(m, StatusMatched, RoleProvider, t0.Add(time.Minute), grace) {
		t.Fatal("a provider backing out is never refunded")
	}
	waived := m
	waived.FeeWaived = true
	if RefundEligible(waived, StatusMatched, RoleCustomer, t0.Add(time.Minute), grace) {
		t.Fatal("nothing to refund when the fee was waived")
	}
}

func TestBillingPolicy(t *testing.T) {
	p := BillingPolicy{CreditEnabled: true, MatchFee: 20, ConfirmTimeout: 10 * time.Minute, RequestTTL: 2 * time.Hour, RefundGrace: 10 * time.Minute}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if !p.ChargesMatch() || !p.LowCredit(19) || p.LowCredit(20) {
		t.Fatal("charges 20 per match; 19 is low, 20 is enough")
	}
	p.CreditEnabled = false
	if p.ChargesMatch() || p.LowCredit(0) {
		t.Fatal("billing waived: nothing is charged and no balance is low")
	}
	bad := p
	bad.ConfirmTimeout = time.Second
	if bad.Validate() == nil {
		t.Fatal("a 1s confirmation timeout must be rejected")
	}
}

func TestParsePackages(t *testing.T) {
	pkgs, err := ParsePackages("a:100:100, b:300:330")
	if err != nil || len(pkgs) != 2 || pkgs[1] != (Package{ID: "b", THB: 300, Credits: 330}) {
		t.Fatalf("got %+v, %v", pkgs, err)
	}
	if pkgs[0].AmountMinor() != 10000 {
		t.Fatalf("100 THB = 10000 satang, got %d", pkgs[0].AmountMinor())
	}
	for _, bad := range []string{"", "a:5:10", "a:100", "a:100:0", "a:100:10,a:200:20", "a:x:1"} {
		if _, err := ParsePackages(bad); err == nil {
			t.Errorf("ParsePackages(%q) should fail", bad)
		}
	}
	if _, ok := FindPackage(pkgs, "zzz"); ok {
		t.Fatal("unknown package id must not resolve")
	}
}

func TestProviderInputValidate(t *testing.T) {
	ok := ProviderInput{DisplayName: "อู่ช่างเอ", Categories: []Category{CatAutoRepair, CatTowing}, Phone: "0812345678",
		Latitude: 13.75, Longitude: 100.5, ServiceRadiusM: 10000}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*ProviderInput){
		"no category":    func(p *ProviderInput) { p.Categories = nil },
		"bad category":   func(p *ProviderInput) { p.Categories = []Category{"sos"} },
		"repeated":       func(p *ProviderInput) { p.Categories = []Category{CatTyre, CatTyre} },
		"radius":         func(p *ProviderInput) { p.ServiceRadiusM = 4000 },
		"phone":          func(p *ProviderInput) { p.Phone = "" },
		"foreign image":  func(p *ProviderInput) { k := "announcements/x.jpg"; p.LogoKey = &k },
		"negative price": func(p *ProviderInput) { n := -1; p.StartingPriceTHB = &n },
	} {
		in := ok
		mut(&in)
		if in.Validate() == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
}

func TestVehicleInfoOnlyForVehicleCategories(t *testing.T) {
	if !CatTowing.TakesVehicleInfo() || CatPlumber.TakesVehicleInfo() {
		t.Fatal("vehicle details belong to vehicle services only")
	}
}

func TestApproximateCoordinate(t *testing.T) {
	if got := ApproximateCoordinate(13.756349); got != 13.756 {
		t.Fatalf("got %v", got)
	}
	if got := ApproximateCoordinate(-33.8688); got != -33.869 {
		t.Fatalf("got %v", got)
	}
}
