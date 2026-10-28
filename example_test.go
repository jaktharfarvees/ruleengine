package ruleengine_test

import (
	"context"
	"fmt"
	"log"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/ruletest"
	"github.com/arrowhr/ruleengine/store/filestore"
)

// Example wires the engine into a service and asks it for one decision.
//
// This is the README's quick start, compiled and run by CI so that it cannot
// drift from the API it documents.
func Example() {
	// Where rules come from. A deployed service swaps this for
	// store/servicestore, which reads them from the Rule Service, and changes
	// nothing else.
	loader, err := filestore.New("testdata")
	if err != nil {
		log.Fatal(err)
	}

	// Where dataset-sourced facts come from. A real service implements
	// dataset.Executor over its own storage and proves it with
	// datasettest.ConformanceSuite.
	manifests, err := ruletest.LoadManifests("leave.balances", "leave.applications")
	if err != nil {
		log.Fatal(err)
	}
	executor, err := ruletest.NewInMemoryExecutor(manifests["leave.balances"], manifests["leave.applications"])
	if err != nil {
		log.Fatal(err)
	}

	engine, err := ruleengine.New(ruleengine.Config{
		Tenant: "RWB",
		Domain: "leave",
		// A fact budget is required, not defaulted: these numbers decide how
		// much one decision may cost, and a silent default on them would be a
		// silent change to a payroll figure.
		FactBudget: fact.Budget{
			MaxDatasetFacts:  8,
			PerFactTimeout:   250 * time.Millisecond,
			TotalFactTimeout: 2 * time.Second,
			MaxRowsScanned:   5000,
		},
		FailClosedDomains: []string{"leave", "payroll"},
	},
		ruleengine.WithLoader(loader),
		ruleengine.WithExecutor(executor),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = engine.Close() }()

	// Load once at start-up. With a CacheReloadInterval set, also call
	// engine.Start() to reconcile in the background.
	if err := engine.Reload(context.Background()); err != nil {
		log.Fatal(err)
	}
	if err := engine.Ready(); err != nil {
		log.Fatal(err)
	}

	// One decision point. The rule key names what is being decided; the rules
	// decide how it is decided for this particular employee.
	decision, err := engine.Evaluate(context.Background(), ruleengine.EvalRequest{
		SchemaVersion: 2,
		Tenant:        ruleengine.Tenant{ID: "RWB"},
		Routing:       ruleengine.Routing{RuleKey: "urn:arrowhr:rule:leave:eligibility:min-service:v1"},
		Subject: ruleengine.Subject{
			Type: "employee",
			Identifier: ruleengine.Identifier{
				System: "urn:arrowhr:ns:employee-id", Value: "E1001", IDType: "employeeId",
			},
			// The subject's node is what precedence is derived from: the
			// deepest rule anchor on this employee's path wins.
			NodeID: "BR001",
		},
		// Facts the caller already has. Anything else the rule declares is
		// fetched by the engine, within the budget.
		Context:       map[string]any{"joinDate": "2026-05-01"},
		AsOf:          time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC),
		CorrelationID: "example-1",
	})
	if err != nil {
		// A Go error means retry, alert or fix configuration. A business
		// refusal is not an error: it is a Decision with Failed set.
		log.Fatal(err)
	}

	if decision.Failed {
		fmt.Println("refused:", decision.ReasonCodes)
		return
	}
	fmt.Printf("rule %s decided at %s: eligible=%v\n",
		decision.RuleVersion, decision.ResolvedAt.NodeID, decision.Result["eligible"])

	// Output:
	// rule 1.1.0 decided at C002: eligible=true
}

// Example_evaluateAll shows the call a service actually makes: several checks
// for one subject, evaluated together so a user sees every problem at once,
// with each fact resolved once across the whole call.
func Example_evaluateAll() {
	engine := exampleEngine()
	defer func() { _ = engine.Close() }()

	points := []ruleengine.Point{
		{Key: "urn:arrowhr:rule:leave:eligibility:min-service:v1", DecisionType: "leave-eligibility"},
		{Key: "urn:arrowhr:rule:leave:validation:balance:v1", DecisionType: "leave-validation"},
	}

	results, err := engine.EvaluateAll(context.Background(), points, ruleengine.EvalRequest{
		SchemaVersion: 2,
		Tenant:        ruleengine.Tenant{ID: "RWB"},
		Routing:       ruleengine.Routing{Domain: "leave"},
		Subject: ruleengine.Subject{
			Type: "employee",
			Identifier: ruleengine.Identifier{
				System: "urn:arrowhr:ns:employee-id", Value: "E1001", IDType: "employeeId",
			},
			NodeID: "BR001",
		},
		Context: map[string]any{"joinDate": "2026-05-01", "requestedDays": "3"},
		AsOf:    time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("all passed:", results.AllPassed())
	for _, decision := range results.All() {
		fmt.Printf("  %-12s %v\n", decision.RuleKey.Family().Name, decision.Result)
	}

	// Output:
	// all passed: true
	//   min-service  map[eligible:true reasonCode:OK]
	//   balance      map[allowed:true reasonCode:BALANCE_SUFFICIENT]
}

// Example_explainADecision shows how to find out why a decision came out the
// way it did, which is the question support and audit both ask.
func Example_explainADecision() {
	engine := exampleEngine()
	defer func() { _ = engine.Close() }()

	decision, err := engine.Evaluate(context.Background(), ruleengine.EvalRequest{
		SchemaVersion: 2,
		Tenant:        ruleengine.Tenant{ID: "RWB"},
		Routing:       ruleengine.Routing{RuleKey: "urn:arrowhr:rule:leave:eligibility:min-service:v1"},
		Subject: ruleengine.Subject{
			Type: "employee",
			Identifier: ruleengine.Identifier{
				System: "urn:arrowhr:ns:employee-id", Value: "E1001", IDType: "employeeId",
			},
			NodeID: "BR001",
		},
		Context: map[string]any{"joinDate": "2026-05-01"},
		AsOf:    time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		log.Fatal(err)
	}

	// Every candidate is recorded, with the reason it was not chosen.
	for _, evaluated := range decision.Evaluated {
		if evaluated.Winner {
			fmt.Printf("winner     %s anchored %s\n", evaluated.RuleVersion, evaluated.Anchor.NodeID)
			continue
		}
		fmt.Printf("eliminated %s anchored %s: %s\n",
			evaluated.RuleVersion, evaluated.Anchor.NodeID, evaluated.Reason)
	}

	// Output:
	// winner     1.1.0 anchored C002
	// eliminated 1.0.0 anchored G001: Company(1) outranks Group(0)
}

// exampleEngine is the wiring from Example, factored out so the other
// examples stay about the call they are demonstrating.
func exampleEngine() *ruleengine.Engine {
	loader, err := filestore.New("testdata")
	if err != nil {
		log.Fatal(err)
	}
	manifests, err := ruletest.LoadManifests("leave.balances", "leave.applications")
	if err != nil {
		log.Fatal(err)
	}
	executor, err := ruletest.NewInMemoryExecutor(manifests["leave.balances"], manifests["leave.applications"])
	if err != nil {
		log.Fatal(err)
	}
	if err := executor.Add("leave.balances", ruletest.Row{
		dataset.TenantColumn: "RWB", "employeeId": "E1001", "leaveType": "ANNUAL",
		"balanceDays": "12.5", "asOfDate": "2026-10-14",
	}); err != nil {
		log.Fatal(err)
	}
	engine, err := ruleengine.New(ruleengine.Config{
		Tenant: "RWB", Domain: "leave",
		FactBudget: fact.Budget{
			MaxDatasetFacts:  8,
			PerFactTimeout:   250 * time.Millisecond,
			TotalFactTimeout: 2 * time.Second,
			MaxRowsScanned:   5000,
		},
	}, ruleengine.WithLoader(loader), ruleengine.WithExecutor(executor))
	if err != nil {
		log.Fatal(err)
	}
	if err := engine.Reload(context.Background()); err != nil {
		log.Fatal(err)
	}
	return engine
}
