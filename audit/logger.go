package audit

import (
	"context"
	"time"
)

// Record is one decision, flattened for storage.
//
// It carries identifiers, unlike anything the library logs, because the audit
// store is access-controlled and a decision cannot be explained without them.
// It does not carry fact or result *values* for fields the registry marks as
// personal: Facts names the fields that were used and where each came from,
// which is what explains a decision without copying an employee's data into a
// second store.
type Record struct {
	// DecisionID is the identity of this decision.
	DecisionID string `json:"decisionId"`
	// Tenant, Domain, RuleSet and Name locate the decision in the catalog.
	Tenant  string `json:"tenant"`
	Domain  string `json:"domain"`
	RuleSet string `json:"ruleSet,omitempty"`
	Name    string `json:"name,omitempty"`
	// RuleKey and RuleVersion identify exactly which rule decided.
	RuleKey     string `json:"ruleKey,omitempty"`
	RuleVersion string `json:"ruleVersion,omitempty"`
	// DecisionType is the output contract the result satisfies.
	DecisionType string `json:"decisionType,omitempty"`
	// SubjectType and SubjectID identify who the decision is about.
	SubjectType string `json:"subjectType,omitempty"`
	SubjectID   string `json:"subjectId,omitempty"`
	// NodeID is where the subject sat in the hierarchy, and ResolvedNodeID is
	// the anchor that won. Together they explain the precedence outcome.
	NodeID         string `json:"nodeId,omitempty"`
	ResolvedNodeID string `json:"resolvedNodeId,omitempty"`
	// SubjectPath is the full node path, root last, for an auditor who needs
	// to see why a deeper anchor was or was not on it.
	SubjectPath []string `json:"subjectPath,omitempty"`
	// Candidates records every rule considered and why each was in or out.
	Candidates []Candidate `json:"candidates,omitempty"`
	// Facts records which fields were used and how each was obtained, by name
	// only.
	Facts []Fact `json:"facts,omitempty"`
	// Result is the decision's output. It is stored because a payroll figure
	// has to be reproducible, and the audit store is access-controlled.
	Result map[string]any `json:"result,omitempty"`
	// ReasonCodes explains the outcome, and Failed says whether it stands.
	ReasonCodes []string `json:"reasonCodes,omitempty"`
	Failed      bool     `json:"failed"`
	// AsOf and ValidAt are the decision time and the business time.
	AsOf    time.Time `json:"asOf"`
	ValidAt time.Time `json:"validAt"`
	// RunID ties a batch decision to the run that pinned its versions.
	RunID string `json:"runId,omitempty"`
	// IdempotencyKey is what a caller used to make a money-moving decision
	// safe to retry.
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// CorrelationID and Traceparent tie the decision to the request that
	// caused it, across services.
	CorrelationID string `json:"correlationId,omitempty"`
	Traceparent   string `json:"traceparent,omitempty"`
	// LibVersion names the library that produced the decision, so a replay can
	// tell whether the engine itself has changed.
	LibVersion string `json:"libVersion,omitempty"`
	// RecordedAt is when the record was created, which is not the decision
	// time: a dropped and re-sent record keeps its decision time.
	RecordedAt time.Time `json:"recordedAt"`
}

// Candidate is one rule that resolution considered.
type Candidate struct {
	RuleKey      string `json:"ruleKey"`
	RuleVersion  string `json:"ruleVersion"`
	AnchorNodeID string `json:"anchorNodeId,omitempty"`
	AnchorKind   string `json:"anchorKind,omitempty"`
	Order        int    `json:"order"`
	Depth        int    `json:"depth"`
	Winner       bool   `json:"winner"`
	EliminatedBy string `json:"eliminatedBy,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// Fact is how one field was obtained, named without its value.
type Fact struct {
	Field             string `json:"field"`
	Kind              string `json:"kind"`
	FactSourceVersion string `json:"factSourceVersion,omitempty"`
	Dataset           string `json:"dataset,omitempty"`
	RowsScanned       int    `json:"rowsScanned,omitempty"`
	ElapsedMS         int    `json:"elapsedMs,omitempty"`
	Cached            bool   `json:"cached"`
	FromRequest       bool   `json:"fromRequest"`
}

// DecisionLogger receives one record per decision.
//
// Log must not block and must not fail: a decision has already been made by
// the time it is called, and losing the record is strictly better than losing
// the decision.
type DecisionLogger interface {
	Log(record Record)
}

// Sink persists a batch of records. It is the only part of auditing that may
// block or fail, and it is always called away from the decision path.
type Sink interface {
	Write(ctx context.Context, batch []Record) error
}

// Nop returns a DecisionLogger that discards every record. It is the default,
// so a consumer that supplies none still gets decisions.
func Nop() DecisionLogger { return nopLogger{} }

type nopLogger struct{}

func (nopLogger) Log(Record) {}
