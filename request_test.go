package ruleengine

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/schema"
)

func TestEvalRequestRoundTrip(t *testing.T) {
	asOf := time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)
	want := EvalRequest{
		SchemaVersion: 2, Tenant: Tenant{ID: "RWB"},
		Routing: Routing{Domain: "leave", RuleSet: "eligibility", Name: "min-service"},
		Subject: Subject{Type: "employee", Identifier: Identifier{System: "hr", Value: "E1", IDType: "employeeId"}, NodeID: "BR001"},
		Context: map[string]any{"serviceMonths": 12}, AsOf: asOf,
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got EvalRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != want.SchemaVersion || got.Tenant != want.Tenant || got.Routing != want.Routing || !got.AsOf.Equal(want.AsOf) {
		t.Fatalf("request round trip got %#v, want %#v", got, want)
	}
}

func TestEvalRequestV2AllowsRuleKeyToSupplyOptionalRoutingParts(t *testing.T) {
	request := EvalRequest{
		SchemaVersion: 2,
		Tenant:        Tenant{ID: "RWB"},
		Routing: Routing{
			Domain:  "leave",
			RuleKey: rule.RuleKey("urn:arrowhr:rule:leave:eligibility:min-service:v1"),
		},
		Subject: Subject{
			Type:       "employee",
			Identifier: Identifier{System: "urn:arrowhr:ns:employee-id", Value: "E1", IDType: "employeeId"},
			NodeID:     "BR001",
		},
		Context: map[string]any{},
		AsOf:    time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC),
	}
	if err := schema.Validate(schema.EvaluationRequest, request); err != nil {
		t.Fatalf("minimal v2 request failed schema validation: %v", err)
	}
}
