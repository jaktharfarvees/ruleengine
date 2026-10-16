package ruleengine_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/ruletest"
	"github.com/arrowhr/ruleengine/store"
)

// BenchmarkPipeline measures a whole decision, with 0, 1 and 5 dataset facts.
//
// The budget this defends is the programme's: a full evaluation at p99 under
// 5 ms, excluding the time a service's own storage takes to answer. The
// executor here is in-memory, so what is measured is the library's share —
// resolution, version selection, compiling and de-duplicating the fact
// queries, validation, evaluation and building the decision.
//
// The fact counts matter because each one is a compiled query, and a payroll
// rule needing five facts is ordinary.
func BenchmarkPipeline(b *testing.B) {
	for _, facts := range []int{0, 1, 5} {
		b.Run(fmt.Sprintf("datasetFacts=%d", facts), func(b *testing.B) {
			engine, request := benchmarkEngine(b, facts)
			ctx := context.Background()

			// Prove the decision actually succeeds before timing it: a
			// benchmark of a fail-closed path would measure the wrong thing.
			decision, err := engine.Evaluate(ctx, request)
			if err != nil || decision.Failed {
				b.Fatalf("decision failed: %v %v", decision.ReasonCodes, err)
			}
			if len(decision.FactsResolved) != facts+1 {
				b.Fatalf("resolved %d facts, want %d", len(decision.FactsResolved), facts+1)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := engine.Evaluate(ctx, request); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkPipelineEvaluateAll measures the call a service actually makes:
// several decision points for one subject, sharing their facts. It should cost
// meaningfully less than evaluating each point separately, which is the whole
// reason de-duplication is mandatory rather than an optimisation.
func BenchmarkPipelineEvaluateAll(b *testing.B) {
	engine, request := benchmarkEngine(b, 5)
	points := []ruleengine.Point{
		{Key: rule.RuleKey("urn:arrowhr:rule:leave:bench:probe-0:v1")},
		{Key: rule.RuleKey("urn:arrowhr:rule:leave:bench:probe-1:v1")},
		{Key: rule.RuleKey("urn:arrowhr:rule:leave:bench:probe-2:v1")},
	}
	ctx := context.Background()
	results, err := engine.EvaluateAll(ctx, points, request)
	if err != nil || !results.AllPassed() {
		b.Fatalf("points failed: %v %v", results.Failures(), err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := engine.EvaluateAll(ctx, points, request); err != nil {
			b.Fatal(err)
		}
	}
}

// benchmarkEngine builds an engine whose rule declares exactly the requested
// number of dataset facts, plus one passed fact so there is always something
// to validate.
func benchmarkEngine(b *testing.B, datasetFacts int) (*ruleengine.Engine, ruleengine.EvalRequest) {
	b.Helper()
	return benchmarkEngineWithValidation(b, datasetFacts, ruleengine.ValidationStrict)
}

func benchmarkEngineWithValidation(
	b *testing.B, datasetFacts int, validation ruleengine.ValidationMode,
) (*ruleengine.Engine, ruleengine.EvalRequest) {
	b.Helper()
	at := time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC)

	manifest, err := ruletest.LoadManifest("leave.balances")
	if err != nil {
		b.Fatal(err)
	}
	executor, err := ruletest.NewInMemoryExecutor(manifest)
	if err != nil {
		b.Fatal(err)
	}
	// A handful of rows, so the executor does real work without the row count
	// dominating the measurement.
	for index := range 10 {
		if err := executor.Add("leave.balances", ruletest.Row{
			dataset.TenantColumn: "RWB", "employeeId": "E1001", "leaveType": "ANNUAL",
			"balanceDays": "12.5",
			"asOfDate":    at.AddDate(0, 0, -index-1).Format(time.DateOnly),
		}); err != nil {
			b.Fatal(err)
		}
	}

	fields := []registry.FieldDefinition{{
		Name: "requestedDays", Domain: "leave", DataType: registry.DataDecimal,
		OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
		FactSource: registry.FactSource{Kind: registry.FactPassed},
	}, {
		Name: "allowed", Domain: "leave", DataType: registry.DataBoolean,
		OwnerService: "leave-service", Status: "active", Role: registry.RoleOutput,
	}}
	inputs := []string{"requestedDays"}
	for index := range datasetFacts {
		name := fmt.Sprintf("benchBalance%d", index)
		fields = append(fields, registry.FieldDefinition{
			Name: name, Domain: "leave", DataType: registry.DataDecimal,
			OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
			FactSource: registry.FactSource{
				Kind: registry.FactDataset, Dataset: "leave.balances",
				// Each field asks a different question, so the queries do not
				// de-duplicate into one and the count is real.
				Window:    &registry.FactWindow{Column: "asOfDate", RelativeTo: dataset.TokenValidAt, LastDays: index + 30},
				Aggregate: &registry.FactAggregate{Fn: registry.AggregateLatest, On: "balanceDays"},
				OnEmpty:   "0",
			},
		})
		inputs = append(inputs, name)
	}

	hierarchyFixture, err := ruletest.LoadHierarchyFixture("rwb.json")
	if err != nil {
		b.Fatal(err)
	}
	bundle := store.Bundle{
		Fields:    fields,
		Manifests: []dataset.Manifest{manifest},
		Hierarchy: store.HierarchyBundle{Types: hierarchyFixture.Types, Nodes: hierarchyFixture.Nodes},
		LoadedAt:  at,
	}
	// Three rules so EvaluateAll has points to share facts across.
	for index := range 3 {
		bundle.Rules = append(bundle.Rules, benchmarkRule(fmt.Sprintf("probe-%d", index), inputs, at))
	}

	// A budget generous enough for the largest fact count measured, so the
	// benchmark measures work rather than a budget breach.
	budget := ruletest.Budget()
	budget.MaxDatasetFacts = 16
	engine, err := ruleengine.New(ruleengine.Config{
		Tenant: "RWB", Domain: "leave", FactBudget: budget, Validation: validation,
	}, ruleengine.WithExecutor(executor))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = engine.Close() })
	if err := engine.ApplyBundle(bundle); err != nil {
		b.Fatal(err)
	}

	request := ruleengine.EvalRequest{
		SchemaVersion: 2,
		Tenant:        ruleengine.Tenant{ID: "RWB"},
		Routing:       ruleengine.Routing{RuleKey: "urn:arrowhr:rule:leave:bench:probe-0:v1"},
		Subject: ruleengine.Subject{
			Type: "employee",
			Identifier: ruleengine.Identifier{
				System: "urn:arrowhr:ns:employee-id", Value: "E1001", IDType: "employeeId",
			},
			NodeID: "BR001",
		},
		Context: map[string]any{"requestedDays": "2"},
		AsOf:    at,
	}
	return engine, request
}

func benchmarkRule(name string, inputs []string, at time.Time) rule.RuleDefinition {
	return rule.RuleDefinition{
		RuleKey: rule.RuleKey("urn:arrowhr:rule:leave:bench:" + name + ":v1"),
		Domain:  "leave", RuleSet: "bench", Name: name,
		Version: "1.0.0", ChangeType: rule.ChangeMajor, Status: rule.StatusActive,
		Tenant: "RWB",
		Scope:  rule.Scope{Anchor: rule.Anchor{Kind: rule.AnchorNode, NodeID: "G001"}},
		// Effective well before the request's business time.
		Effective:       rule.Effective{ValidFrom: at.AddDate(-1, 0, 0)},
		DeclaredInputs:  inputs,
		DeclaredOutputs: []string{"allowed"},
		DecisionType:    "leave-validation",
		Logic: rule.RuleLogic{HitPolicy: rule.HitFirst, Rows: []rule.DecisionRow{
			{ID: "within", When: map[string]string{"requestedDays": "<= 5"}, Then: map[string]any{"allowed": true}},
			{ID: "over", When: map[string]string{}, Then: map[string]any{"allowed": false}},
		}},
		EngineRef: rule.EngineRef{Engine: "decisiontable"},
		Metadata:  rule.Metadata{Owner: "bench", CreatedBy: "bench", CreatedAt: at},
	}
}

// BenchmarkPipelineValidationModes measures what each validation mode costs,
// so the trade a consumer makes by relaxing it is a number rather than a
// guess.
func BenchmarkPipelineValidationModes(b *testing.B) {
	for name, mode := range map[string]ruleengine.ValidationMode{
		"strict":     ruleengine.ValidationStrict,
		"schemaOnly": ruleengine.ValidationSchemaOnly,
		"off":        ruleengine.ValidationOff,
	} {
		b.Run(name, func(b *testing.B) {
			engine, request := benchmarkEngineWithValidation(b, 0, mode)
			ctx := context.Background()
			if _, err := engine.Evaluate(ctx, request); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := engine.Evaluate(ctx, request); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
