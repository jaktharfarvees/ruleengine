package contextbuilder_test

import (
	"context"
	"testing"

	"github.com/arrowhr/ruleengine/contextbuilder"
)

func TestStaticReturnsTheRequestedSubset(t *testing.T) {
	builder := contextbuilder.Static{"serviceMonths": 12, "joinDate": "2025-06-15", "unused": true}

	values, err := builder.Build(context.Background(), []string{"serviceMonths", "joinDate"}, "E1001", "2026-06")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || values["serviceMonths"] != 12 || values["joinDate"] != "2025-06-15" {
		t.Fatalf("Build = %#v, want only the two requested fields", values)
	}

	// A field the builder does not hold is omitted rather than returned nil: a
	// missing field becomes a fail-closed decision naming it, while a guessed
	// nil is a wrong answer that looks right.
	values, err = builder.Build(context.Background(), []string{"serviceMonths", "notHeld"}, "E1001", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 {
		t.Fatalf("Build = %#v, want the missing field omitted", values)
	}
	if _, present := values["notHeld"]; present {
		t.Fatal("Build invented a value for a field it does not hold")
	}

	// No fields requested is an empty answer, not an error.
	values, err = builder.Build(context.Background(), nil, "E1001", "")
	if err != nil || len(values) != 0 {
		t.Fatalf("Build(nil) = %#v, %v", values, err)
	}
}

func TestStaticHonoursCancellation(t *testing.T) {
	// A builder runs inside a bounded fetch, so it has to stop when the
	// decision's deadline has passed.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (contextbuilder.Static{"a": 1}).Build(cancelled, []string{"a"}, "E1001", ""); err == nil {
		t.Fatal("a cancelled build was served")
	}
}

func TestStaticSatisfiesTheInterface(t *testing.T) {
	var builder contextbuilder.ContextBuilder = contextbuilder.Static{}
	if _, err := builder.Build(context.Background(), nil, "", ""); err != nil {
		t.Fatal(err)
	}
}
