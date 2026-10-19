package ruleengine

import (
	"encoding/json"
	"fmt"
)

// ReasonCode is a stable, machine-readable decision explanation.
type ReasonCode string

const (
	// ReasonOK means evaluation completed normally.
	ReasonOK ReasonCode = "OK"
	// ReasonSchemaInvalid means boundary schema validation failed.
	ReasonSchemaInvalid ReasonCode = "SCHEMA_INVALID"
	// ReasonHierarchyUnavailable means no hierarchy snapshot was available.
	ReasonHierarchyUnavailable ReasonCode = "HIERARCHY_UNAVAILABLE"
	// ReasonHierarchyNotConfigured means the tenant or subject has no
	// resolvable hierarchy path.
	ReasonHierarchyNotConfigured ReasonCode = "HIERARCHY_NOT_CONFIGURED"
	// ReasonSubjectNodeUnresolvable is retained for source compatibility with
	// v1. V2 emits HIERARCHY_NOT_CONFIGURED for this condition.
	//
	// Deprecated: use ReasonHierarchyNotConfigured.
	ReasonSubjectNodeUnresolvable ReasonCode = "SUBJECT_NODE_UNRESOLVABLE"
	// ReasonNoRuleResolved means no candidate survived resolution.
	ReasonNoRuleResolved ReasonCode = "NO_RULE_RESOLVED"
	// ReasonDefaultApplied means an explicit tenant-authored low-risk fallback
	// was used because no rule resolved.
	ReasonDefaultApplied ReasonCode = "DEFAULT_APPLIED"
	// ReasonPrecedenceTieUnresolved is retained for source compatibility with
	// v1. V2 applies a deterministic fallback and emits PRECEDENCE_CONFLICT.
	//
	// Deprecated: use ReasonPrecedenceConflict.
	ReasonPrecedenceTieUnresolved ReasonCode = "PRECEDENCE_TIE_UNRESOLVED"
	// ReasonPrecedenceConflict records a same-rank conflict that escaped
	// publication lint and was resolved by the deterministic runtime fallback.
	ReasonPrecedenceConflict ReasonCode = "PRECEDENCE_CONFLICT"
	// ReasonCrossTenantRuleDropped records a discarded cross-tenant candidate.
	ReasonCrossTenantRuleDropped ReasonCode = "CROSS_TENANT_RULE_DROPPED"
	// ReasonPinnedVersionUnavailable means a required pin could not be obtained.
	ReasonPinnedVersionUnavailable ReasonCode = "PINNED_VERSION_UNAVAILABLE"
	// ReasonFieldNotRegistered means a referenced field is absent from the registry.
	ReasonFieldNotRegistered ReasonCode = "FIELD_NOT_REGISTERED"
	// ReasonFieldDeprecated records use of a deprecated field.
	ReasonFieldDeprecated ReasonCode = "FIELD_DEPRECATED"
	// ReasonFieldTypeMismatch means a value violates its declared data type.
	ReasonFieldTypeMismatch ReasonCode = "FIELD_TYPE_MISMATCH"
	// ReasonFactUnresolvable means a declared fact cannot be obtained.
	ReasonFactUnresolvable ReasonCode = "FACT_UNRESOLVABLE"
	// ReasonFactBudgetExceeded means a fact-fetch limit was exceeded.
	ReasonFactBudgetExceeded ReasonCode = "FACT_BUDGET_EXCEEDED"
	// ReasonFactTimeout means a fact fetch exceeded its deadline.
	ReasonFactTimeout ReasonCode = "FACT_TIMEOUT"
	// ReasonFactRawQueryRejected means author input resembled query or script syntax.
	ReasonFactRawQueryRejected ReasonCode = "FACT_RAW_QUERY_REJECTED"
	// ReasonFactColumnNotAllowed means a dataset column use was not permitted.
	ReasonFactColumnNotAllowed ReasonCode = "FACT_COLUMN_NOT_ALLOWED"
	// ReasonFactAggregateUnsupported means a dataset does not offer an aggregate.
	ReasonFactAggregateUnsupported ReasonCode = "FACT_AGGREGATE_UNSUPPORTED"
	// ReasonFactWindowTooWide means a requested dataset window exceeded its maximum.
	ReasonFactWindowTooWide ReasonCode = "FACT_WINDOW_TOO_WIDE"
	// ReasonFactDerivedCycle means derived fact dependencies form a cycle.
	ReasonFactDerivedCycle ReasonCode = "FACT_DERIVED_CYCLE"
	// ReasonFactShadowedAttribute records a fact overriding a subject attribute.
	ReasonFactShadowedAttribute ReasonCode = "FACT_SHADOWED_ATTRIBUTE"
	// ReasonDatasetNotPublished means a dataset was unknown or unpublished.
	ReasonDatasetNotPublished ReasonCode = "DATASET_NOT_PUBLISHED"
	// ReasonDatasetRecipeInvalid means an authored dataset recipe violated a
	// compile guardrail and should have been rejected at publication.
	ReasonDatasetRecipeInvalid ReasonCode = "DATASET_RECIPE_INVALID"
	// ReasonDatasetDeprecated records use of a deprecated dataset.
	ReasonDatasetDeprecated ReasonCode = "DATASET_DEPRECATED"
	// ReasonPIIColumnInFilter records a non-subject PII filter.
	ReasonPIIColumnInFilter ReasonCode = "PII_COLUMN_IN_FILTER"
	// ReasonEngineError means decision logic evaluation failed.
	ReasonEngineError ReasonCode = "ENGINE_ERROR"
	// ReasonOutputContractViolation means a declared output was missing or mistyped.
	ReasonOutputContractViolation ReasonCode = "OUTPUT_CONTRACT_VIOLATION"
	// ReasonCacheStale records evaluation against a stale cache snapshot.
	ReasonCacheStale ReasonCode = "CACHE_STALE"
)

// Fatal reports whether the reason requires a fail-closed decision. Unknown
// codes are fatal so malformed external data cannot accidentally fail open.
func (r ReasonCode) Fatal() bool {
	switch r {
	case ReasonOK, ReasonDefaultApplied, ReasonPrecedenceConflict, ReasonCrossTenantRuleDropped, ReasonFieldDeprecated,
		ReasonFactShadowedAttribute, ReasonDatasetDeprecated,
		ReasonPIIColumnInFilter, ReasonCacheStale:
		return false
	default:
		return true
	}
}

// String returns the wire representation.
func (r ReasonCode) String() string { return string(r) }

// reasonCatalogue is every code the library can produce, in the order the
// pipeline reaches them: envelope, resolution, version, facts, evaluation,
// then the operational notes.
var reasonCatalogue = []ReasonCode{
	ReasonOK, ReasonSchemaInvalid, ReasonHierarchyUnavailable, ReasonHierarchyNotConfigured,
	ReasonNoRuleResolved, ReasonDefaultApplied,
	ReasonPrecedenceConflict, ReasonCrossTenantRuleDropped,
	ReasonPinnedVersionUnavailable, ReasonFieldNotRegistered,
	ReasonFieldDeprecated, ReasonFieldTypeMismatch,
	ReasonFactUnresolvable, ReasonFactBudgetExceeded, ReasonFactTimeout,
	ReasonFactRawQueryRejected, ReasonFactColumnNotAllowed,
	ReasonFactAggregateUnsupported, ReasonFactWindowTooWide,
	ReasonFactDerivedCycle, ReasonFactShadowedAttribute,
	ReasonDatasetNotPublished, ReasonDatasetRecipeInvalid, ReasonDatasetDeprecated,
	ReasonPIIColumnInFilter, ReasonEngineError,
	ReasonOutputContractViolation, ReasonCacheStale,
}

// ReasonCodes returns the whole catalogue.
//
// It is exported so a consumer can build an operations table from it, and so
// the library's own tests can assert that nothing in the catalogue is a code
// no condition produces — a code that cannot happen tells an operator a
// condition is handled when it is not.
func ReasonCodes() []ReasonCode {
	return append([]ReasonCode(nil), reasonCatalogue...)
}

// Valid reports whether r belongs to the reason-code catalogue.
func (r ReasonCode) Valid() bool {
	switch r {
	case ReasonOK, ReasonSchemaInvalid, ReasonHierarchyUnavailable, ReasonHierarchyNotConfigured,
		ReasonNoRuleResolved, ReasonDefaultApplied,
		ReasonPrecedenceConflict, ReasonCrossTenantRuleDropped,
		ReasonPinnedVersionUnavailable, ReasonFieldNotRegistered,
		ReasonFieldDeprecated, ReasonFieldTypeMismatch,
		ReasonFactUnresolvable, ReasonFactBudgetExceeded, ReasonFactTimeout,
		ReasonFactRawQueryRejected, ReasonFactColumnNotAllowed,
		ReasonFactAggregateUnsupported, ReasonFactWindowTooWide,
		ReasonFactDerivedCycle, ReasonFactShadowedAttribute,
		ReasonDatasetNotPublished, ReasonDatasetRecipeInvalid, ReasonDatasetDeprecated,
		ReasonPIIColumnInFilter, ReasonEngineError,
		ReasonOutputContractViolation, ReasonCacheStale:
		return true
	default:
		return false
	}
}

// ParseReasonCode parses a catalogue value.
func ParseReasonCode(value string) (ReasonCode, error) {
	code := ReasonCode(value)
	if !code.Valid() {
		return "", fmt.Errorf("unknown reason code %q", value)
	}
	return code, nil
}

// UnmarshalJSON rejects reason codes outside the catalogue.
func (r *ReasonCode) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("reason code: %w", err)
	}
	code, err := ParseReasonCode(value)
	if err != nil {
		return err
	}
	*r = code
	return nil
}
