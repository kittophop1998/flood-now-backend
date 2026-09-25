package moderation

import "testing"

func TestAutoHideThreshold(t *testing.T) {
	p := Policy{AutoHideThreshold: 3, MaxPerDevicePerHour: 10}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.ShouldAutoHide(2) || !p.ShouldAutoHide(3) {
		t.Error("hide exactly at the threshold of distinct devices")
	}
	if (Policy{AutoHideThreshold: 1, MaxPerDevicePerHour: 1}).Validate() == nil {
		t.Error("a single complaint must never hide a report")
	}
}

func TestActionOutcomes(t *testing.T) {
	cases := map[Action]struct {
		hide   bool
		status Status
	}{
		ActionDismiss: {false, StatusDismissed},
		ActionHide:    {true, StatusResolved},
		ActionUnhide:  {false, StatusResolved},
	}
	for a, want := range cases {
		hide, status := a.Outcome()
		if hide != want.hide || status != want.status {
			t.Errorf("%s: got (%v, %s)", a, hide, status)
		}
	}
	if Action("merge").Valid() {
		t.Error("merging duplicates is not supported")
	}
}

func TestComplaintValidation(t *testing.T) {
	if (NewComplaintInput{DeviceID: "12345678-device", Reason: ReasonSpam}).Validate() != nil {
		t.Error("valid complaint rejected")
	}
	if (NewComplaintInput{DeviceID: "12345678-device", Reason: "rude"}).Validate() == nil {
		t.Error("unknown reason accepted")
	}
}
