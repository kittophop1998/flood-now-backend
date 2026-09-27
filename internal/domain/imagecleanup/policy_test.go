package imagecleanup

import "testing"

func TestPolicyValidate(t *testing.T) {
	valid := DefaultPolicy()
	if err := valid.Validate(); err != nil {
		t.Fatalf("default policy should be valid: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Policy)
	}{
		{"zero orphan retention", func(p *Policy) { p.OrphanRetention = 0 }},
		{"negative orphan retention", func(p *Policy) { p.OrphanRetention = -1 }},
		{"zero resolved retention", func(p *Policy) { p.ResolvedRetention = 0 }},
		{"zero batch size", func(p *Policy) { p.BatchSize = 0 }},
		{"negative batch size", func(p *Policy) { p.BatchSize = -5 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := DefaultPolicy()
			tc.mutate(&p)
			if err := p.Validate(); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
