package donation

import "testing"

func TestNormalizePromptPayID(t *testing.T) {
	cases := []struct {
		raw    string
		id     string
		idType IDType
		ok     bool
	}{
		{"081-234-5678", "0812345678", IDPhone, true},
		{"+66 81 234 5678", "0812345678", IDPhone, true},
		{"66812345678", "0812345678", IDPhone, true},
		{"1101700207030", "1101700207030", IDNationalID, true}, // valid check digit
		{"1101700207031", "", "", false},                       // wrong check digit
		{"123456789012345", "123456789012345", IDEWallet, true},
		{"", "", "", false},
		{"08123", "", "", false},
		{"0012345678", "", "", false}, // not a mobile number
		{"081234567x", "", "", false},
	}
	for _, c := range cases {
		id, typ, ok := NormalizePromptPayID(c.raw)
		if ok != c.ok || id != c.id || typ != c.idType {
			t.Errorf("%q: got (%q, %q, %v), want (%q, %q, %v)", c.raw, id, typ, ok, c.id, c.idType, c.ok)
		}
	}
}

func TestLoadIsOffUnlessExplicitlyEnabledAndValid(t *testing.T) {
	if Load("", "0812345678", "FloodNow") != nil {
		t.Error("unset DONATION_ENABLED must disable donations")
	}
	if Load("false", "0812345678", "FloodNow") != nil {
		t.Error("DONATION_ENABLED=false must disable donations")
	}
	if Load("true", "", "FloodNow") != nil {
		t.Error("missing PROMPTPAY_ID must disable donations")
	}
	if Load("true", "not-an-id", "FloodNow") != nil {
		t.Error("invalid PROMPTPAY_ID must disable donations")
	}
	cfg := Load("TRUE", "081 234 5678", "  มูลนิธิ FloodNow \n")
	if cfg == nil || cfg.PromptPayID != "0812345678" || cfg.IDType != IDPhone || cfg.RecipientName == nil || *cfg.RecipientName != "มูลนิธิ FloodNow" {
		t.Fatalf("valid config not loaded: %+v", cfg)
	}
	if noName := Load("1", "0812345678", " "); noName == nil || noName.RecipientName != nil {
		t.Error("a blank name is optional")
	}
}
