package ruleengine

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestReasonCodeCatalogue(t *testing.T) {
	nonFatal := map[ReasonCode]bool{
		ReasonOK: true, ReasonDefaultApplied: true, ReasonPrecedenceConflict: true,
		ReasonCrossTenantRuleDropped: true, ReasonFieldDeprecated: true,
		ReasonFactShadowedAttribute: true, ReasonDatasetDeprecated: true,
		ReasonPIIColumnInFilter: true, ReasonCacheStale: true,
	}
	all := []ReasonCode{
		ReasonOK, ReasonSchemaInvalid, ReasonHierarchyUnavailable,
		ReasonHierarchyNotConfigured, ReasonNoRuleResolved, ReasonDefaultApplied,
		ReasonPrecedenceConflict, ReasonCrossTenantRuleDropped,
		ReasonPinnedVersionUnavailable, ReasonFieldNotRegistered,
		ReasonFieldDeprecated, ReasonFieldTypeMismatch, ReasonFactUnresolvable,
		ReasonFactBudgetExceeded, ReasonFactTimeout, ReasonFactRawQueryRejected,
		ReasonFactColumnNotAllowed, ReasonFactAggregateUnsupported,
		ReasonFactWindowTooWide, ReasonFactDerivedCycle,
		ReasonFactShadowedAttribute, ReasonDatasetNotPublished, ReasonDatasetRecipeInvalid,
		ReasonDatasetDeprecated, ReasonPIIColumnInFilter, ReasonEngineError,
		ReasonOutputContractViolation, ReasonCacheStale,
	}
	if len(all) != 28 {
		t.Fatalf("catalogue contains %d codes, want 28", len(all))
	}
	for _, code := range all {
		if !code.Valid() || code.String() == "" {
			t.Fatalf("catalogue code %q is invalid", code)
		}
		if code.Fatal() == nonFatal[code] {
			t.Fatalf("Fatal(%q) = %v", code, code.Fatal())
		}
		parsed, err := ParseReasonCode(code.String())
		if err != nil || parsed != code {
			t.Fatalf("ParseReasonCode(%q) = %q, %v", code, parsed, err)
		}
		data, err := json.Marshal(code)
		if err != nil {
			t.Fatal(err)
		}
		var decoded ReasonCode
		if err := json.Unmarshal(data, &decoded); err != nil || decoded != code {
			t.Fatalf("reason code round trip = %q, %v", decoded, err)
		}
	}
	unknown := ReasonCode("UNKNOWN")
	if unknown.Valid() || !unknown.Fatal() {
		t.Fatalf("unknown code validity=%v fatal=%v", unknown.Valid(), unknown.Fatal())
	}
	if _, err := ParseReasonCode("UNKNOWN"); err == nil {
		t.Fatal("unknown code was accepted")
	}
	if err := json.Unmarshal([]byte(`"UNKNOWN"`), &unknown); err == nil {
		t.Fatal("unknown JSON code was accepted")
	}
}

func TestStructuredError(t *testing.T) {
	secret := "employee-E9001"
	err := &Error{Code: ReasonFactTimeout, Field: "availableBalance", Dataset: "leave.balance", Err: errors.New(secret)}
	if !errors.Is(err, ErrFactTimeout) || !errors.Is(err, &Error{Code: ReasonFactTimeout}) {
		t.Fatalf("structured error does not support errors.Is: %v", err)
	}
	if errors.Is(err, ErrEngine) || err.Is(nil) {
		t.Fatalf("structured error matched an unrelated target")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked wrapped value: %q", err.Error())
	}
	if !errors.Is(&Error{Code: ReasonEngineError, Err: ErrEngine}, ErrEngine) {
		t.Fatal("wrapped sentinel was not matched")
	}
	for _, tt := range []struct {
		code ReasonCode
		want error
	}{
		{ReasonHierarchyUnavailable, ErrHierarchyUnavailable},
		{ReasonPinnedVersionUnavailable, ErrPinnedVersionUnavailable},
		{ReasonEngineError, ErrEngine},
		{ReasonFactBudgetExceeded, ErrFactBudget},
		{ReasonFactTimeout, ErrFactTimeout},
	} {
		if !errors.Is(&Error{Code: tt.code}, tt.want) {
			t.Fatalf("code %q did not match %v", tt.code, tt.want)
		}
	}
	if errors.Is(&Error{Code: ReasonSchemaInvalid}, ErrConfig) {
		t.Fatal("unmapped reason unexpectedly matched ErrConfig")
	}
	var nilError *Error
	if nilError.Error() != "<nil>" || nilError.Unwrap() != nil || !nilError.Is(nil) {
		t.Fatal("nil structured error methods are not safe")
	}
}

// TestReasonCodesMatchValid pins the catalogue slice against the Valid switch,
// so the two cannot drift: a code in one and not the other would make either
// the operations table or the boundary check wrong.
func TestReasonCodesMatchValid(t *testing.T) {
	codes := ReasonCodes()
	if len(codes) != 28 {
		t.Fatalf("catalogue holds %d codes, want 28", len(codes))
	}
	seen := make(map[ReasonCode]struct{}, len(codes))
	for _, code := range codes {
		if !code.Valid() {
			t.Errorf("%s is in the catalogue but Valid rejects it", code)
		}
		if _, duplicate := seen[code]; duplicate {
			t.Errorf("%s appears twice in the catalogue", code)
		}
		seen[code] = struct{}{}
	}
	// The returned slice is a copy, so a caller cannot edit the catalogue.
	codes[0] = "MUTATED"
	if ReasonCodes()[0] != ReasonOK {
		t.Fatal("ReasonCodes handed out the catalogue itself")
	}
}
