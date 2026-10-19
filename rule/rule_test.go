package rule

import (
	"errors"
	"testing"
	"time"
)

func validRule() RuleDefinition {
	return RuleDefinition{
		RuleKey: "urn:arrowhr:rule:leave:eligibility:min-service:v1",
		Domain:  "leave", RuleSet: "eligibility", Name: "min-service",
		Version: "1.0.0", ChangeType: ChangeMajor, Status: StatusActive, Tenant: "RWB",
		Scope:          Scope{Anchor: Anchor{Kind: AnchorNode, NodeID: "G001"}},
		Effective:      Effective{ValidFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		DeclaredInputs: []string{"serviceMonths"}, DeclaredOutputs: []string{"eligible"},
		DecisionType: "leave-eligibility", EngineRef: EngineRef{Engine: "decisiontable"},
		Logic: RuleLogic{HitPolicy: HitFirst, Rows: []DecisionRow{{ID: "default", When: map[string]string{}, Then: map[string]any{"eligible": true}}}},
	}
}

func TestRuleKey(t *testing.T) {
	key := RuleKey("urn:arrowhr:rule:leave:eligibility:min-service:v1")
	parts, err := key.Parse()
	if err != nil {
		t.Fatal(err)
	}
	want := Parts{Domain: "leave", RuleSet: "eligibility", Name: "min-service", Major: 1}
	if parts != want || key.Family() != (Family{Domain: "leave", RuleSet: "eligibility", Name: "min-service"}) || key.String() != string(key) {
		t.Fatalf("unexpected key projection: parts=%#v family=%#v string=%q", parts, key.Family(), key.String())
	}
	if got := RuleKey("bad").Family(); got != (Family{}) {
		t.Fatalf("invalid key family = %#v, want zero", got)
	}
}

func TestEffectiveContains(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	effective := Effective{ValidFrom: from, ValidTo: &to}
	for _, tt := range []struct {
		name string
		at   time.Time
		want bool
	}{{"before", from.Add(-time.Second), false}, {"start", from, true}, {"inside", from.Add(time.Hour), true}, {"end excluded", to, false}} {
		t.Run(tt.name, func(t *testing.T) {
			if got := effective.Contains(tt.at); got != tt.want {
				t.Fatalf("Contains(%v) = %v, want %v", tt.at, got, tt.want)
			}
		})
	}
}

func TestStatusEvaluable(t *testing.T) {
	for _, tt := range []struct {
		status Status
		want   bool
	}{{StatusActive, true}, {StatusScheduled, true}, {StatusDraft, false}, {StatusPendingApproval, false}, {StatusDeprecated, false}, {StatusRetired, false}} {
		if got := tt.status.Evaluable(); got != tt.want {
			t.Fatalf("Status(%q).Evaluable() = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RuleDefinition)
	}{
		{"valid", func(*RuleDefinition) {}},
		{"invalid key", func(r *RuleDefinition) { r.RuleKey = "bad" }},
		{"identity mismatch", func(r *RuleDefinition) { r.Name = "other" }},
		{"missing identity", func(r *RuleDefinition) { r.Tenant = "" }},
		{"invalid change", func(r *RuleDefinition) { r.ChangeType = "PATCH" }},
		{"invalid status", func(r *RuleDefinition) { r.Status = "unknown" }},
		{"invalid effective", func(r *RuleDefinition) { r.Effective.ValidFrom = time.Time{} }},
		{"node anchor missing node", func(r *RuleDefinition) { r.Scope.Anchor.NodeID = "" }},
		{"retired employee anchor", func(r *RuleDefinition) { r.Scope.Anchor = Anchor{Kind: AnchorKind("employee")} }},
		{"invalid anchor", func(r *RuleDefinition) { r.Scope.Anchor.Kind = "other" }},
		{"empty filter attribute", func(r *RuleDefinition) { r.Scope.Filters = []FilterCondition{{Op: FilterEquals, Value: "x"}} }},
		{"empty in", func(r *RuleDefinition) {
			r.Scope.Filters = []FilterCondition{{Attribute: "group", Op: FilterIn, Value: []string{}}}
		}},
		{"invalid range", func(r *RuleDefinition) {
			r.Scope.Filters = []FilterCondition{{Attribute: "level", Op: FilterRange, Value: []int{1}}}
		}},
		{"exists has value", func(r *RuleDefinition) {
			r.Scope.Filters = []FilterCondition{{Attribute: "group", Op: FilterExists, Value: true}}
		}},
		{"invalid filter op", func(r *RuleDefinition) { r.Scope.Filters = []FilterCondition{{Attribute: "group", Op: "like"}} }},
		{"invalid logic", func(r *RuleDefinition) { r.Logic.Rows = nil }},
		{"missing outputs", func(r *RuleDefinition) { r.DeclaredOutputs = nil }},
		{"duplicate output", func(r *RuleDefinition) { r.DeclaredOutputs = []string{"eligible", "eligible"} }},
		{"duplicate input", func(r *RuleDefinition) { r.DeclaredInputs = []string{"a", "a"} }},
		{"missing row id", func(r *RuleDefinition) { r.Logic.Rows[0].ID = "" }},
		{"duplicate row id", func(r *RuleDefinition) { r.Logic.Rows = append(r.Logic.Rows, r.Logic.Rows[0]) }},
		{"undeclared output", func(r *RuleDefinition) { r.Logic.Rows[0].Then["reason"] = "x" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := validRule()
			tt.mutate(&rule)
			err := rule.Validate()
			if tt.name == "valid" && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if tt.name != "valid" {
				var validation *ValidationError
				if !errors.As(err, &validation) {
					t.Fatalf("Validate() error = %v, want *ValidationError", err)
				}
			}
		})
	}
}

func TestValidationErrorAndValidFilterShapes(t *testing.T) {
	cause := errors.New("shape failure")
	err := &ValidationError{Field: "scope", Err: cause}
	if !errors.Is(err, cause) || err.Error() == "" {
		t.Fatalf("validation error does not wrap its cause: %v", err)
	}

	tests := []FilterCondition{
		{Attribute: "group", Op: FilterEquals, Value: "Development"},
		{Attribute: "group", Op: FilterIn, Value: []string{"Development"}},
		{Attribute: "group", Op: FilterNotIn, Value: []string{"Finance"}},
		{Attribute: "level", Op: FilterRange, Value: []int{1, 5}},
		{Attribute: "category", Op: FilterExists},
		{Attribute: "category", Op: FilterNotExists},
	}
	for _, filter := range tests {
		rule := validRule()
		rule.Scope.Filters = []FilterCondition{filter}
		if err := rule.Validate(); err != nil {
			t.Fatalf("Validate() rejected valid filter %#v: %v", filter, err)
		}
	}
}
