package ruleengine

import (
	"time"

	"github.com/arrowhr/ruleengine/rule"
)

// EvalRequest is the versioned transport envelope accepted by evaluation.
type EvalRequest struct {
	SchemaVersion     int               `json:"schemaVersion"`
	Tenant            Tenant            `json:"tenant"`
	Routing           Routing           `json:"routing"`
	Subject           Subject           `json:"subject"`
	Resource          map[string]any    `json:"resource,omitempty"`
	Context           map[string]any    `json:"context"`
	EmploymentCountry string            `json:"employmentCountry,omitempty"`
	Jurisdiction      string            `json:"jurisdiction,omitempty"`
	Locale            string            `json:"locale,omitempty"`
	Currency          string            `json:"currency,omitempty"`
	Timezone          string            `json:"timezone,omitempty"`
	AsOf              time.Time         `json:"asOf"`
	ValidAt           *time.Time        `json:"validAt,omitempty"`
	RunID             string            `json:"runId,omitempty"`
	RuleVersionPin    string            `json:"ruleVersionPin,omitempty"`
	FactSourcePins    map[string]string `json:"factSourcePins,omitempty"`
	IdempotencyKey    string            `json:"idempotencyKey,omitempty"`
	CorrelationID     string            `json:"correlationId,omitempty"`
	Traceparent       string            `json:"traceparent,omitempty"`
}

// Tenant identifies the tenant boundary of a request.
type Tenant struct {
	ID string `json:"id"`
}

// Routing identifies the requested rule family or exact key.
type Routing struct {
	Domain  string       `json:"domain"`
	RuleSet string       `json:"ruleSet"`
	Name    string       `json:"name"`
	RuleKey rule.RuleKey `json:"ruleKey,omitempty"`
	Action  string       `json:"action,omitempty"`
}

// Subject identifies the entity to which a decision applies.
type Subject struct {
	Type                 string         `json:"type"`
	Identifier           Identifier     `json:"identifier"`
	AlternateIdentifiers []Identifier   `json:"alternateIdentifiers,omitempty"`
	NodeID               string         `json:"nodeId,omitempty"`
	Attributes           map[string]any `json:"attributes,omitempty"`
}

// Identifier is a typed identifier in an owning system.
type Identifier struct {
	System string `json:"system"`
	Value  string `json:"value"`
	IDType string `json:"idType"`
}
