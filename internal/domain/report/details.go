package report

import "strings"

// Details holds a report's category-specific fields as key → enum value
// (e.g. an accident's "lanes_blocked": "one"). Which keys a category has and
// their allowed values live in detailRules, so no category carries another
// category's fields — a flood never has lanes, an accident never has depth.
// Water depth and passability predate this and stay first-class columns.
type Details map[string]string

// Detail keys read by other rules (route risk, the web app's summaries).
const (
	DetailLanesBlocked    = "lanes_blocked"
	DetailTrafficImpact   = "traffic_impact"
	DetailClosure         = "closure"
	DetailDirection       = "direction"
	DetailObstructionType = "obstruction_type"
	DetailDamageType      = "damage_type"
	DetailSignalIssue     = "signal_issue"
)

// Values used by the route rules.
const (
	LanesAll          = "all"
	ClosureFull       = "full"
	ClosurePartial    = "partial"
	TrafficStandstill = "standstill"
)

var laneValues = []string{"none", "one", "multiple", LanesAll}

// detailRules lists, per category, the optional detail fields it accepts and
// their allowed values. Everything is optional: a reporter who isn't sure
// leaves it out. Categories without an entry have no details.
var detailRules = map[Type]map[string][]string{
	TypeAccident: {
		DetailLanesBlocked:  laneValues,
		DetailTrafficImpact: {"light", "slow", TrafficStandstill},
	},
	TypeRoadClosed: {
		DetailClosure:   {ClosureFull, ClosurePartial},
		DetailDirection: {"both", "one_way"},
	},
	TypeObstruction: {
		DetailObstructionType: {"fallen_tree", "debris", "landslide", "fallen_object", "other"},
	},
	TypeRoadDamage: {
		DetailDamageType: {"pothole", "subsidence", "surface_damage", "other"},
	},
	TypeConstruction: {
		DetailLanesBlocked: laneValues,
	},
	TypeTrafficSignal: {
		DetailSignalIssue: {"not_working", "flashing", "malfunction"},
	},
}

// validateDetails adds a field error for each detail of t with a value it
// doesn't allow. Keys t doesn't have are not errors — they're dropped by
// Details.For, like water depth sent for a non-flood report.
func validateDetails(t Type, d Details, fields map[string]string) {
	rules := detailRules[t]
	for key, value := range d {
		allowed, ok := rules[key]
		if !ok {
			continue
		}
		if !contains(allowed, value) {
			fields["details."+key] = "must be one of " + strings.Join(allowed, ", ")
		}
	}
}

// For keeps only the details category t has (nil when none remain).
func (d Details) For(t Type) Details {
	rules := detailRules[t]
	var out Details
	for key, value := range d {
		if _, ok := rules[key]; !ok {
			continue
		}
		if out == nil {
			out = Details{}
		}
		out[key] = value
	}
	return out
}

// Get returns the value of key, or "" when absent.
func (d Details) Get(key string) string { return d[key] }

func contains(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}
