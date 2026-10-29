package ruleengine_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/audit"
	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/hierarchy"
	"github.com/arrowhr/ruleengine/manifest"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/ruletest"
	"github.com/arrowhr/ruleengine/store"
)

func baseConfig() ruleengine.Config {
	return ruleengine.Config{Tenant: tenant, Domain: "leave", FactBudget: ruletest.Budget()}
}

// TestConfigValidationRefusesAnUnsetBudget is the configuration rule worth
// stating twice: a fact budget has no default, because a silent default on a
// fact budget is a silent change to a payroll figure.
func TestConfigValidationRefusesAnUnsetBudget(t *testing.T) {
	config := baseConfig()
	config.FactBudget = fact.Budget{}
	if err := config.Validate(); err == nil {
		t.Fatal("an unset fact budget was accepted")
	}
	if _, err := ruleengine.New(config); err == nil {
		t.Fatal("New accepted an unset fact budget")
	} else if !errors.Is(err, ruleengine.ErrConfig) {
		t.Fatalf("New error = %v, want ErrConfig", err)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*ruleengine.Config)
		valid  bool
	}{
		{"the baseline", func(*ruleengine.Config) {}, true},
		{"no tenant", func(c *ruleengine.Config) { c.Tenant = "" }, false},
		{"no domain", func(c *ruleengine.Config) { c.Domain = "" }, false},
		{"unknown validation mode", func(c *ruleengine.Config) { c.Validation = 99 }, false},
		{"negative reload interval", func(c *ruleengine.Config) { c.CacheReloadInterval = -time.Second }, false},
		{"negative candidate cap", func(c *ruleengine.Config) { c.MaxCandidatesPerKey = -1 }, false},
		{"negative idempotency retention", func(c *ruleengine.Config) { c.IdempotencyRetention = -time.Second }, false},
		{"short idempotency retention", func(c *ruleengine.Config) { c.IdempotencyRetention = time.Hour }, false},
		{"idempotency without durable audit", func(c *ruleengine.Config) { c.RequireIdempotency = true }, false},
		{"money-moving safeguards", func(c *ruleengine.Config) {
			c.RequireIdempotency = true
			c.RequireDurableAudit = true
		}, true},
		{"empty fail-closed domain", func(c *ruleengine.Config) { c.FailClosedDomains = []string{""} }, false},
		{"schema-only validation", func(c *ruleengine.Config) { c.Validation = ruleengine.ValidationSchemaOnly }, true},
		{"validation off", func(c *ruleengine.Config) { c.Validation = ruleengine.ValidationOff }, true},
		{"a fail-closed domain", func(c *ruleengine.Config) { c.FailClosedDomains = []string{"payroll"} }, true},
		{"a candidate cap", func(c *ruleengine.Config) { c.MaxCandidatesPerKey = 10 }, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := baseConfig()
			test.change(&config)
			err := config.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate = %v, want nil", err)
			}
			if !test.valid && err == nil {
				t.Fatal("Validate accepted an invalid configuration")
			}
		})
	}
	// The modes name themselves, for a log line an operator reads.
	for mode, want := range map[ruleengine.ValidationMode]string{
		ruleengine.ValidationStrict: "strict", ruleengine.ValidationSchemaOnly: "schema-only",
		ruleengine.ValidationOff: "off",
	} {
		if mode.String() != want {
			t.Errorf("%d.String() = %q, want %q", int(mode), mode.String(), want)
		}
	}
	if ruleengine.ValidationMode(99).String() == "" {
		t.Error("an unknown mode printed as empty")
	}
}

// TestNilOptionsAreIgnored pins that a consumer assembling options
// conditionally cannot accidentally clear a default with a nil.
func TestNilOptionsAreIgnored(t *testing.T) {
	instance, err := ruleengine.New(baseConfig(),
		nil,
		ruleengine.WithEngine(nil), ruleengine.WithLoader(nil), ruleengine.WithExecutor(nil),
		ruleengine.WithNativeFacts(nil), ruleengine.WithContextBuilder(nil),
		ruleengine.WithDecisionLogger(nil), ruleengine.WithManifestStore(nil),
		ruleengine.WithMetrics(nil), ruleengine.WithTracer(nil), ruleengine.WithLogger(nil),
		ruleengine.WithClock(nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	// It still decides, which proves the defaults survived.
	if err := instance.ApplyBundle(sharedBundle(t)); err != nil {
		t.Fatal(err)
	}
	decision, err := instance.Evaluate(context.Background(),
		leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
			map[string]any{"joinDate": "2026-05-01"}))
	if err != nil || decision.Failed {
		t.Fatalf("decision = %v, %v", decision.ReasonCodes, err)
	}
}

// TestReloadNeedsALoader pins that an engine told to load with nothing to load
// from says so, rather than serving an empty cache.
func TestReloadNeedsALoader(t *testing.T) {
	instance, err := ruleengine.New(baseConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Reload(context.Background()); !errors.Is(err, ruleengine.ErrConfig) {
		t.Fatalf("Reload without a loader = %v, want ErrConfig", err)
	}
}

// TestStartIsAbsentWithoutAnInterval pins the lifecycle rule: no background
// goroutine exists unless a reload interval asks for one, so a batch run can
// pin its rules for the whole run without a timer changing them underneath it.
func TestStartIsAbsentWithoutAnInterval(t *testing.T) {
	instance, err := ruleengine.New(baseConfig())
	if err != nil {
		t.Fatal(err)
	}
	// Start is a no-op and needs no loader, because nothing is started.
	if err := instance.Start(); err != nil {
		t.Fatalf("Start without an interval = %v, want nil", err)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}

	// With an interval but no loader, there is nothing to reconcile from.
	config := baseConfig()
	config.CacheReloadInterval = time.Minute
	instance, err = ruleengine.New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Start(); !errors.Is(err, ruleengine.ErrConfig) {
		t.Fatalf("Start with an interval and no loader = %v, want ErrConfig", err)
	}
}

// TestStartReconcilesInTheBackground pins that the loop runs, publishes, and
// stops on Close.
func TestStartReconcilesInTheBackground(t *testing.T) {
	loader := &countingLoader{bundle: sharedBundle(t)}
	config := baseConfig()
	config.CacheReloadInterval = 10 * time.Millisecond
	instance, err := ruleengine.New(config, ruleengine.WithLoader(loader))
	if err != nil {
		t.Fatal(err)
	}
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
	// Starting twice is harmless.
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for instance.Ready() != nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := instance.Ready(); err != nil {
		t.Fatalf("the background reconcile never published: %v", err)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	// Close is idempotent, and after it the loop is stopped.
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	settled := loader.count()
	time.Sleep(50 * time.Millisecond)
	if loader.count() != settled {
		t.Fatal("the reconcile goroutine kept running after Close")
	}
}

// TestReconcileFailureKeepsServing pins that a reconcile that cannot reach its
// source leaves the previous rules in place: a late reload is a degraded
// cache, not an outage.
func TestReconcileFailureKeepsServing(t *testing.T) {
	loader := &countingLoader{bundle: sharedBundle(t)}
	config := baseConfig()
	config.CacheReloadInterval = 10 * time.Millisecond
	instance, err := ruleengine.New(config, ruleengine.WithLoader(loader))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	loader.fail(errors.New("rule service unavailable"))
	if err := instance.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)

	// Still ready, still deciding, on the rules it already had.
	if err := instance.Ready(); err != nil {
		t.Fatalf("a failed reconcile took the engine out of service: %v", err)
	}
	decision, err := instance.Evaluate(context.Background(),
		leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
			map[string]any{"joinDate": "2026-05-01"}))
	if err != nil || decision.Failed {
		t.Fatalf("decision after a failed reconcile = %v, %v", decision.ReasonCodes, err)
	}
}

func TestRuntimeSwapNeverMixesRulesHierarchyAndFacts(t *testing.T) {
	oldBundle := sharedBundle(t)
	newBundle := sharedBundle(t)
	for index := range newBundle.Hierarchy.Nodes {
		node := &newBundle.Hierarchy.Nodes[index]
		if node.NodeID == "C002" {
			node.NodeID = "C009"
		}
		if node.ParentNodeID == "C002" {
			node.ParentNodeID = "C009"
		}
	}
	for index := range newBundle.Rules {
		definition := &newBundle.Rules[index]
		if definition.Name != "min-service" || definition.Version != "1.1.0" {
			continue
		}
		definition.Anchor = rule.Anchor{Kind: rule.AnchorNode, NodeID: "C009"}
		definition.Scope.Anchor = definition.Anchor
		for row := range definition.Logic.Rows {
			definition.Logic.Rows[row].Then["eligible"] = false
			definition.Logic.Rows[row].Then["reasonCode"] = "NEW_SNAPSHOT"
		}
	}
	instance, err := ruleengine.New(baseConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.ApplyBundle(oldBundle); err != nil {
		t.Fatal(err)
	}
	request := leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
		map[string]any{"joinDate": "2026-05-01"})

	stop := make(chan struct{})
	errorsFound := make(chan error, 32)
	var readers sync.WaitGroup
	for worker := 0; worker < 32; worker++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				decision, err := instance.Evaluate(context.Background(), request)
				if err != nil || decision.Failed || decision.ResolvedAt == nil {
					errorsFound <- errors.New("evaluation observed an incomplete runtime snapshot")
					return
				}
				eligible, _ := decision.Result["eligible"].(bool)
				oldState := decision.ResolvedAt.NodeID == "C002" && eligible
				newState := decision.ResolvedAt.NodeID == "C009" && !eligible
				if !oldState && !newState {
					errorsFound <- errors.New("evaluation mixed old and new runtime artifacts")
					return
				}
			}
		}()
	}
	for iteration := 0; iteration < 50; iteration++ {
		if err := instance.ApplyBundle(newBundle); err != nil {
			t.Fatal(err)
		}
		if err := instance.ApplyBundle(oldBundle); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	readers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
}

// TestCloseDrainsTheAuditLogger pins that shutting an engine down flushes the
// audit trail rather than discarding it.
func TestCloseDrainsTheAuditLogger(t *testing.T) {
	sink := &collectingSink{}
	logger, err := audit.NewAsync(sink, 16, nil, audit.WithBatchSize(1))
	if err != nil {
		t.Fatal(err)
	}
	instance := engineOver(t, baseConfig(), ruleengine.WithDecisionLogger(logger))
	if _, err := instance.Evaluate(context.Background(),
		leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
			map[string]any{"joinDate": "2026-05-01"})); err != nil {
		t.Fatal(err)
	}
	if err := instance.Close(); err != nil {
		t.Fatal(err)
	}
	records := sink.records()
	if len(records) != 1 {
		t.Fatalf("audit records = %d, want 1 after Close drained", len(records))
	}
	record := records[0]
	// An audit record explains the decision: which rule, which anchor, which
	// facts, for whom.
	if record.RuleVersion != "1.1.0" || record.ResolvedNodeID != "C002" {
		t.Fatalf("record = %+v, want the C002 override at 1.1.0", record)
	}
	if record.SubjectID != employee || len(record.SubjectPath) != 4 {
		t.Fatalf("record subject = %q with path %v", record.SubjectID, record.SubjectPath)
	}
	if len(record.Candidates) != 2 || len(record.Facts) != 2 {
		t.Fatalf("record explains %d candidates and %d facts",
			len(record.Candidates), len(record.Facts))
	}
	if len(record.ReasonCodes) != 1 || record.ReasonCodes[0] != "OK" {
		t.Fatalf("record reason codes = %v", record.ReasonCodes)
	}
	if len(record.InputsHash) != 64 {
		t.Fatalf("record input digest = %q, want SHA-256", record.InputsHash)
	}
	if record.Inputs != nil {
		t.Fatalf("best-effort audit retained exact inputs: %#v", record.Inputs)
	}
}

type failingOutbox struct{ err error }

func (o failingOutbox) Append(context.Context, audit.Record) error { return o.err }

type capturingOutbox struct {
	record audit.Record
}

func (o *capturingOutbox) Append(_ context.Context, record audit.Record) error {
	o.record = record
	return nil
}

func TestDurableAuditCapturesExactEffectiveInputs(t *testing.T) {
	config := baseConfig()
	config.RequireDurableAudit = true
	outbox := &capturingOutbox{}
	logger, err := audit.NewOutboxLogger(outbox)
	if err != nil {
		t.Fatal(err)
	}
	instance := engineOverBundle(t, config, func(bundle *store.Bundle) {
		for index := range bundle.Fields {
			if bundle.Fields[index].Name == "joinDate" {
				bundle.Fields[index].PII = true
			}
		}
	}, ruleengine.WithDecisionLogger(logger))
	request := leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
		map[string]any{"joinDate": "2026-05-01"})
	request.Subject.Attributes = map[string]any{"designation": "Senior"}

	decision, err := instance.Evaluate(context.Background(), request)
	if err != nil || decision.Failed {
		t.Fatalf("decision = %#v, error = %v", decision, err)
	}
	record := outbox.record
	if record.Inputs == nil {
		t.Fatal("durable audit omitted exact inputs")
	}
	if got := record.Inputs.Facts["joinDate"]; got != "2026-05-01" {
		t.Fatalf("exact PII fact = %#v, want original value", got)
	}
	if _, found := record.Inputs.Facts["serviceMonths"]; !found {
		t.Fatalf("derived effective fact is absent: %#v", record.Inputs.Facts)
	}
	if got := record.Inputs.Attributes["designation"]; got != "Senior" {
		t.Fatalf("exact subject attribute = %#v", got)
	}
	if len(record.InputsHash) != 64 {
		t.Fatalf("durable record input digest = %q, want SHA-256", record.InputsHash)
	}

	// Audit evidence is independent of caller-owned maps after Evaluate returns.
	request.Subject.Attributes["designation"] = "Changed"
	if got := record.Inputs.Attributes["designation"]; got != "Senior" {
		t.Fatalf("caller mutation changed audit evidence to %#v", got)
	}
}

func TestDurableAuditIsEnforced(t *testing.T) {
	config := baseConfig()
	config.RequireDurableAudit = true
	if _, err := ruleengine.New(config); !errors.Is(err, ruleengine.ErrConfig) {
		t.Fatalf("New without durable audit = %v, want ErrConfig", err)
	}
	cause := errors.New("outbox failed for person@example.com")
	logger, err := audit.NewOutboxLogger(failingOutbox{err: cause})
	if err != nil {
		t.Fatal(err)
	}
	instance := engineOver(t, config, ruleengine.WithDecisionLogger(logger))
	decision, err := instance.Evaluate(context.Background(), leaveRequest(
		"urn:arrowhr:rule:leave:eligibility:min-service:v1", map[string]any{"joinDate": "2026-05-01"}))
	if err == nil || decision.DecisionID == "" {
		t.Fatalf("durable audit failure = decision %#v, error %v", decision, err)
	}
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "person@example.com") {
		t.Fatalf("durable audit error did not preserve the cause safely: %v", err)
	}
}

// TestManifestPinnedRunUsesTheFrozenVersion pins the batch path: a run
// manifest freezes the version, so a replay in September still uses August's
// rule even though a newer one has been published since.
func TestManifestPinnedRunUsesTheFrozenVersion(t *testing.T) {
	runs := manifest.NewInMemoryStore()
	key := "urn:arrowhr:rule:leave:eligibility:min-service:v1"
	if err := runs.Put(manifest.RunManifest{
		RunID: "leave-aug", Tenant: tenant, ValidAt: asOf, Status: manifest.StatusOpen, OpenedAt: asOf,
		// Pinned to the group baseline even though C002's override is deeper
		// and newer.
		RulePins:            map[string]string{key: "1.0.0"},
		FactPins:            map[string]string{},
		HierarchySnapshotID: "default",
	}); err != nil {
		t.Fatal(err)
	}
	instance := engineOver(t, baseConfig(), ruleengine.WithManifestStore(runs))

	request := leaveRequest(key, map[string]any{"joinDate": "2026-05-01"})
	request.RunID = "leave-aug"
	decision, err := instance.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Failed {
		t.Fatalf("pinned decision failed: %v", decision.ReasonCodes)
	}
	if decision.RuleVersion != "1.0.0" {
		t.Fatalf("rule version = %q, want the pinned 1.0.0", decision.RuleVersion)
	}
	if decision.RunID != "leave-aug" {
		t.Fatalf("decision runId = %q", decision.RunID)
	}

	// A run with no pin for the rule fails closed rather than silently using
	// the current version: the run promised a frozen list.
	if err := runs.Put(manifest.RunManifest{
		RunID: "leave-sep", Tenant: tenant, ValidAt: asOf, Status: manifest.StatusOpen, OpenedAt: asOf,
		RulePins: map[string]string{}, FactPins: map[string]string{}, HierarchySnapshotID: "default",
	}); err != nil {
		t.Fatal(err)
	}
	request.RunID = "leave-sep"
	decision, err = instance.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Failed || !hasReason(decision, ruleengine.ReasonPinnedVersionUnavailable) {
		t.Fatalf("an unpinned rule in a run = %v, want a pin failure", decision.ReasonCodes)
	}
}

func TestMissingExplicitPinReloadsAndUsesFreshSnapshot(t *testing.T) {
	full := sharedBundle(t)
	initial := full
	initial.Rules = nil
	for _, definition := range full.Rules {
		if definition.Version != "1.0.0" {
			initial.Rules = append(initial.Rules, definition)
		}
	}
	loader := &countingLoader{bundle: full}
	instance, err := ruleengine.New(baseConfig(), ruleengine.WithLoader(loader))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.ApplyBundle(initial); err != nil {
		t.Fatal(err)
	}

	request := leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
		map[string]any{"joinDate": "2026-05-01"})
	request.RuleVersionPin = "1.0.0"
	decision, err := instance.Evaluate(context.Background(), request)
	if err != nil || decision.Failed {
		t.Fatalf("decision after pin reload = %v, %v", decision.ReasonCodes, err)
	}
	if decision.RuleVersion != "1.0.0" || loader.count() != 1 {
		t.Fatalf("pin reload selected version %q after %d loads", decision.RuleVersion, loader.count())
	}
}

func TestManifestPinnedRunUsesHistoricalHierarchy(t *testing.T) {
	bundle := sharedBundle(t)
	oldHierarchy := bundle.Hierarchy
	oldHierarchy.SnapshotID = "org-before-move"
	oldHierarchy.ValidFrom = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	cutover := asOf.Add(24 * time.Hour)
	oldHierarchy.ValidTo = &cutover
	newHierarchy := store.HierarchyBundle{
		SnapshotID: "org-after-move", ValidFrom: cutover,
		Types: append([]hierarchy.Type(nil), oldHierarchy.Types...),
		Nodes: append([]hierarchy.Node(nil), oldHierarchy.Nodes...),
	}
	for index := range newHierarchy.Nodes {
		if newHierarchy.Nodes[index].NodeID == "C002" {
			newHierarchy.Nodes[index].NodeID = "C009"
		}
		if newHierarchy.Nodes[index].ParentNodeID == "C002" {
			newHierarchy.Nodes[index].ParentNodeID = "C009"
		}
	}
	bundle.Hierarchies = []store.HierarchyBundle{oldHierarchy, newHierarchy}
	runs := manifest.NewInMemoryStore()
	key := "urn:arrowhr:rule:leave:eligibility:min-service:v1"
	if err := runs.Put(manifest.RunManifest{
		RunID: "historical-org", Tenant: tenant, ValidAt: asOf,
		RulePins: map[string]string{key: "1.1.0"}, FactPins: map[string]string{},
		HierarchySnapshotID: "org-before-move", Status: manifest.StatusOpen, OpenedAt: asOf,
	}); err != nil {
		t.Fatal(err)
	}
	instance, err := ruleengine.New(baseConfig(), ruleengine.WithManifestStore(runs))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	if err := instance.ApplyBundle(bundle); err != nil {
		t.Fatal(err)
	}
	request := leaveRequest(key, map[string]any{"joinDate": "2026-05-01"})
	request.AsOf = cutover.Add(30 * 24 * time.Hour)
	request.RunID = "historical-org"
	decision, err := instance.Evaluate(context.Background(), request)
	if err != nil || decision.Failed {
		t.Fatalf("historical run = %v, %v", decision.ReasonCodes, err)
	}
	if decision.HierarchySnapshotID != "org-before-move" || decision.ResolvedAtNode != "C002" || !decision.ValidAt.Equal(asOf) {
		t.Fatalf("historical run used snapshot %q, anchor %q, validAt %s", decision.HierarchySnapshotID, decision.ResolvedAtNode, decision.ValidAt)
	}
}

// TestValidAtDrivesARetroactiveDecision pins that business time, not wall
// time, selects the rule: an August correction run in October resolves
// August's version.
func TestValidAtDrivesARetroactiveDecision(t *testing.T) {
	instance, _ := leaveEngine(t)
	// Before C002's override took effect, the group baseline applied.
	before := time.Date(2025, time.December, 1, 0, 0, 0, 0, time.UTC)
	request := leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
		map[string]any{"joinDate": "2025-01-01"})
	request.ValidAt = &before

	decision, err := instance.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	// Both fixtures take effect on 2026-01-01, so nothing governs an earlier
	// business time and the decision fails closed rather than guessing.
	if !decision.Failed || !hasReason(decision, ruleengine.ReasonNoRuleResolved) {
		t.Fatalf("a decision before any rule was effective = %v, want NO_RULE_RESOLVED", decision.ReasonCodes)
	}
	if !decision.ValidAt.Equal(before) {
		t.Fatalf("validAt = %s, want the requested %s", decision.ValidAt, before)
	}
}

// TestConcurrentEvaluationIsSafe pins that one engine serves many goroutines,
// which is the normal case inside an HTTP handler.
func TestConcurrentEvaluationIsSafe(t *testing.T) {
	instance, _ := leaveEngine(t)
	const workers = 32
	var waiting sync.WaitGroup
	failures := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			for attempt := 0; attempt < 20; attempt++ {
				decision, err := instance.Evaluate(context.Background(),
					leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
						map[string]any{"joinDate": "2026-05-01"}))
				if err != nil || decision.Failed || decision.RuleVersion != "1.1.0" {
					failures <- errors.New("concurrent decision disagreed: " + decision.RuleVersion)
					return
				}
			}
		}()
	}
	waiting.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

// TestStatsReportWhatIsLoaded pins the operational view a service exposes.
func TestStatsReportWhatIsLoaded(t *testing.T) {
	instance, _ := leaveEngine(t)
	stats := instance.Stats()
	if stats.Rules != 11 || stats.Manifests != 4 || stats.HierarchyNodes == 0 {
		t.Fatalf("stats = %+v, want the committed fixture counts", stats)
	}
	if stats.LoadedAt.IsZero() {
		t.Fatal("stats report no load time")
	}
}

// TestNilEngineIsInert pins that a consumer that failed to construct an engine
// gets an error rather than a panic.
func TestNilEngineIsInert(t *testing.T) {
	var absent *ruleengine.Engine
	if err := absent.Ready(); !errors.Is(err, ruleengine.ErrNotReady) {
		t.Fatalf("a nil engine's Ready = %v, want ErrNotReady", err)
	}
	if err := absent.Close(); err != nil {
		t.Fatalf("a nil engine's Close = %v", err)
	}
}

// --- doubles ---

type countingLoader struct {
	mu     sync.Mutex
	bundle store.Bundle
	err    error
	calls  int
}

func (l *countingLoader) LoadAll(context.Context) (store.Bundle, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.err != nil {
		return store.Bundle{}, l.err
	}
	return l.bundle, nil
}

func (l *countingLoader) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

func (l *countingLoader) fail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.err = err
}

type collectingSink struct {
	mu   sync.Mutex
	rows []audit.Record
}

func (s *collectingSink) Write(_ context.Context, batch []audit.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, batch...)
	return nil
}

func (s *collectingSink) records() []audit.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]audit.Record(nil), s.rows...)
}

// TestRuleNamingAnotherEngineIsRefusedAtLoad pins that an engineRef the
// configured engine cannot serve fails the load, not a later decision.
func TestRuleNamingAnotherEngineIsRefusedAtLoad(t *testing.T) {
	bundle := sharedBundle(t)
	if len(bundle.Rules) == 0 {
		t.Fatal("the shared bundle has no rules")
	}
	bundle.Rules[0].EngineRef.Engine = "no-such-engine"

	instance, err := ruleengine.New(baseConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })
	err = instance.ApplyBundle(bundle)
	if !errors.Is(err, ruleengine.ErrConfig) {
		t.Fatalf("ApplyBundle = %v, want ErrConfig", err)
	}
	for _, want := range []string{"no-such-engine", "decisiontable", bundle.Rules[0].RuleKey.String()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if instance.Ready() == nil {
		t.Fatal("a refused bundle was published")
	}
}
