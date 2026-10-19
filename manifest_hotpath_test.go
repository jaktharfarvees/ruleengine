package ruleengine

import (
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/manifest"
	"github.com/arrowhr/ruleengine/store"
)

type countingManifestReader struct {
	run   manifest.RunManifest
	reads int
}

func (r *countingManifestReader) Get(runID string) (manifest.RunManifest, bool) {
	r.reads++
	return r.run, r.run.RunID == runID
}

func TestEvaluationSessionReadsRunManifestLocallyOnce(t *testing.T) {
	now := time.Date(2026, time.August, 31, 0, 0, 0, 0, time.UTC)
	reader := &countingManifestReader{run: manifest.RunManifest{
		RunID: "payroll-aug", Tenant: "T1", ValidAt: now,
		RulePins: map[string]string{}, FactPins: map[string]string{},
		HierarchySnapshotID: "org-aug", Status: manifest.StatusOpen, OpenedAt: now,
	}}
	engine := &Engine{versions: store.NewVersionResolver(nil, reader)}
	session := &session{engine: engine}

	first, err := session.runManifest("T1", "payroll-aug")
	if err != nil {
		t.Fatal(err)
	}
	second, err := session.runManifest("T1", "payroll-aug")
	if err != nil {
		t.Fatal(err)
	}
	if reader.reads != 1 {
		t.Fatalf("local manifest reads = %d, want one per evaluation session", reader.reads)
	}
	if first.HierarchySnapshotID != "org-aug" || second.HierarchySnapshotID != first.HierarchySnapshotID {
		t.Fatalf("session manifest changed: first %#v, second %#v", first, second)
	}
}
