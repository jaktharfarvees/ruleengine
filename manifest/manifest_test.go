package manifest

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStoreKeepsPinsImmutableAndClosesRun(t *testing.T) {
	opened := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	manifest := RunManifest{
		RunID: "payroll-aug", Tenant: "T1", ValidAt: opened, Status: StatusOpen, OpenedAt: opened,
		RulePins:            map[string]string{"urn:arrowhr:rule:payroll:statutory:tax:v1": "1.2.0"},
		FactPins:            map[string]string{"taxableIncome": "3"},
		HierarchySnapshotID: "default",
	}
	store := NewInMemoryStore()
	if err := store.Put(manifest); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(manifest); err != nil {
		t.Fatalf("idempotent Put failed: %v", err)
	}
	if store.Len() != 1 {
		t.Fatalf("Len = %d, want 1", store.Len())
	}
	if pin, found := manifest.RulePin("urn:arrowhr:rule:payroll:statutory:tax:v1"); !found || pin != "1.2.0" {
		t.Fatalf("RulePin = %q, %v", pin, found)
	}
	if pin, found := manifest.FactPin("taxableIncome"); !found || pin != "3" {
		t.Fatalf("FactPin = %q, %v", pin, found)
	}
	manifest.RulePins["urn:arrowhr:rule:payroll:statutory:tax:v1"] = "9.9.9"
	loaded, found, err := store.Get(context.Background(), "payroll-aug")
	if err != nil || !found || loaded.RulePins["urn:arrowhr:rule:payroll:statutory:tax:v1"] != "1.2.0" {
		t.Fatalf("stored manifest mutated: %#v", loaded)
	}
	loaded.FactPins["taxableIncome"] = "99"
	again, _, _ := store.Get(context.Background(), "payroll-aug")
	if again.FactPins["taxableIncome"] != "3" {
		t.Fatal("Get returned a mutable internal fact-pin map")
	}
	closedAt := opened.Add(31 * 24 * time.Hour)
	closed, err := store.Close("payroll-aug", closedAt)
	if err != nil || closed.Status != StatusClosed || closed.ClosedAt == nil || !closed.ClosedAt.Equal(closedAt) {
		t.Fatalf("Close = %#v, %v", closed, err)
	}
	if _, err := store.Close("payroll-aug", closedAt.Add(time.Hour)); !errors.Is(err, ErrAlreadyClosed) {
		t.Fatalf("second different Close error = %v, want ErrAlreadyClosed", err)
	}
	if _, err := store.Close("payroll-aug", closedAt); err != nil {
		t.Fatalf("idempotent Close failed: %v", err)
	}
}

func TestManifestValidation(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	valid := RunManifest{RunID: "r", Tenant: "T", ValidAt: now, RulePins: map[string]string{}, FactPins: map[string]string{}, HierarchySnapshotID: "default", Status: StatusOpen, OpenedAt: now}
	tests := []struct {
		name   string
		mutate func(*RunManifest)
	}{
		{"missing identity", func(m *RunManifest) { m.RunID = "" }},
		{"missing time", func(m *RunManifest) { m.ValidAt = time.Time{} }},
		{"nil pins", func(m *RunManifest) { m.RulePins = nil }},
		{"bad rule key", func(m *RunManifest) { m.RulePins["bad"] = "1.0.0" }},
		{"empty fact version", func(m *RunManifest) { m.FactPins["fact"] = "" }},
		{"unknown status", func(m *RunManifest) { m.Status = "unknown" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := clone(valid)
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("Validate succeeded, want error")
			}
		})
	}
}

func TestStoreFailures(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := NewInMemoryStore()
	base := RunManifest{RunID: "r", Tenant: "T", ValidAt: now, OpenedAt: now, Status: StatusOpen, RulePins: map[string]string{}, FactPins: map[string]string{}, HierarchySnapshotID: "default"}
	if err := store.Put(base); err != nil {
		t.Fatal(err)
	}
	different := clone(base)
	different.Tenant = "T2"
	if err := store.Put(different); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("different duplicate error = %v", err)
	}
	if _, err := store.Close("missing", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Close error = %v", err)
	}
	if _, err := store.Close("r", time.Time{}); err == nil {
		t.Fatal("Close accepted zero time")
	}
	var nilStore *InMemoryStore
	if err := nilStore.Put(base); err == nil {
		t.Fatal("nil Store.Put succeeded")
	}
	if _, found, err := nilStore.Get(context.Background(), "r"); found || err == nil || nilStore.Len() != 0 {
		t.Fatal("nil store read returned data")
	}
	// A cancelled context is reported rather than ignored, which is the whole
	// reason the interface carries one.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := NewInMemoryStore().Get(cancelled, "r"); err == nil {
		t.Fatal("a cancelled read was served")
	}
}
