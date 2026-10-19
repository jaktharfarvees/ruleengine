// Command leaveservice is a minimal example of wiring the rule engine into a
// service.
//
// It shows the three things a service team has to get right:
//
//  1. Construction: build one engine at start-up with a validated Config, load
//     the rules once, and close it on shutdown.
//  2. Decision points: name the checks a request runs as a list, and evaluate
//     them together so the user sees every problem at once.
//  3. Shadow mode: run the engine beside the code it will replace, compare the
//     answers, and keep the legacy answer authoritative until the comparison
//     is boring.
//
// It reads the repository's own fixtures, so it runs with no setup:
//
//	go run ./examples/leaveservice
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/audit"
	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/fact"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/ruletest"
	"github.com/arrowhr/ruleengine/store/filestore"
	"github.com/arrowhr/ruleengine/telemetry"
)

const (
	tenant = "RWB"
	domain = "leave"
)

// leaveApplication is what this service's own handler receives.
type leaveApplication struct {
	EmployeeID    string
	NodeID        string
	EmployeeGroup string
	JoinDate      string
	RequestedDays string
	AsOf          time.Time
}

// decisionPoints are the checks a leave application runs through.
//
// Naming them in one place is the point: the code decides *what* is checked,
// and the rules decide *how* it is checked for this particular employee. A new
// tenant policy changes a rule, not this list.
var decisionPoints = []ruleengine.Point{
	{Key: "urn:arrowhr:rule:leave:eligibility:min-service:v1", DecisionType: "leave-eligibility"},
	{Key: "urn:arrowhr:rule:leave:validation:balance:v1", DecisionType: "leave-validation"},
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("leave service failed", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	engine, closeEngine, err := newEngine(logger)
	if err != nil {
		return err
	}
	defer closeEngine()

	// A service checks readiness before it accepts traffic, so it never has to
	// fail a request closed for want of rules it has not loaded yet.
	if err := engine.Ready(); err != nil {
		return fmt.Errorf("engine is not ready: %w", err)
	}
	stats := engine.Stats()
	logger.Info("rules loaded",
		"rules", stats.Rules, "fields", stats.Fields,
		"datasets", stats.Manifests, "nodes", stats.HierarchyNodes)

	application := leaveApplication{
		EmployeeID: "E1001", NodeID: "BR001", EmployeeGroup: "Development team",
		JoinDate: "2026-05-01", RequestedDays: "3",
		AsOf: time.Date(2026, time.October, 15, 0, 0, 0, 0, time.UTC),
	}

	fmt.Println("\n== enforcing ==")
	if err := decide(context.Background(), engine, application, logger); err != nil {
		return err
	}

	fmt.Println("\n== shadow mode ==")
	shadow(context.Background(), engine, application, logger)
	return nil
}

// newEngine builds the engine once. A real service does this at start-up and
// keeps it: it is safe for concurrent use and its caches are the reason
// evaluation needs no network hop.
func newEngine(logger *slog.Logger) (*ruleengine.Engine, func(), error) {
	// Rules come from files here. A deployed service swaps this for
	// store/servicestore, which reads them from the Rule Service, and changes
	// nothing else.
	loader, err := filestore.New("testdata")
	if err != nil {
		return nil, nil, fmt.Errorf("open rule files: %w", err)
	}

	executor, err := newExecutor()
	if err != nil {
		return nil, nil, err
	}

	// Audit records are written off the decision path. A real service points
	// the sink at its audit store; this one prints a line per decision.
	decisions, err := audit.NewAsync(audit.NewSlogSink(logger), 1024, telemetry.Nop(),
		audit.WithBatchSize(16), audit.WithFlushInterval(time.Second))
	if err != nil {
		return nil, nil, fmt.Errorf("start audit logger: %w", err)
	}

	engine, err := ruleengine.New(ruleengine.Config{
		Tenant: tenant,
		Domain: domain,
		// A budget is required, not defaulted: these numbers are a decision
		// about how much a decision may cost, and they belong in a service's
		// own configuration.
		FactBudget: fact.Budget{
			MaxDatasetFacts:  8,
			PerFactTimeout:   250 * time.Millisecond,
			TotalFactTimeout: 2 * time.Second,
			MaxRowsScanned:   5000,
		},
		// Money and compliance domains fail closed rather than falling back.
		// Zero means no background goroutine; this example loads once.
		CacheReloadInterval: 0,
	},
		ruleengine.WithLoader(loader),
		ruleengine.WithExecutor(executor),
		ruleengine.WithDecisionLogger(decisions),
		ruleengine.WithLogger(logger),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("build engine: %w", err)
	}

	if err := engine.Reload(context.Background()); err != nil {
		return nil, nil, fmt.Errorf("load rules: %w", err)
	}
	// With a reload interval set, a service would also call engine.Start()
	// here to reconcile in the background, and Close stops it.
	return engine, func() {
		if err := engine.Close(); err != nil {
			logger.Warn("engine did not close cleanly", "error", err)
		}
	}, nil
}

// newExecutor stands in for this service's own storage.
//
// A real leave service implements dataset.Executor over its database and
// proves it with dataset.ConformanceSuite. The in-memory one keeps this
// example runnable.
func newExecutor() (dataset.Executor, error) {
	manifests, err := ruletest.LoadManifests("leave.balances", "leave.applications")
	if err != nil {
		return nil, fmt.Errorf("load dataset manifests: %w", err)
	}
	executor, err := ruletest.NewInMemoryExecutor(manifests["leave.balances"], manifests["leave.applications"])
	if err != nil {
		return nil, fmt.Errorf("build executor: %w", err)
	}
	if err := executor.Add("leave.balances", ruletest.Row{
		dataset.TenantColumn: tenant, "employeeId": "E1001", "leaveType": "ANNUAL",
		"balanceDays": "12.5", "asOfDate": "2026-10-14",
	}); err != nil {
		return nil, fmt.Errorf("seed balances: %w", err)
	}
	return executor, nil
}

// decide evaluates every decision point for one application.
func decide(
	ctx context.Context, engine *ruleengine.Engine, application leaveApplication, logger *slog.Logger,
) error {
	results, err := engine.EvaluateAll(ctx, decisionPoints, requestFor(application))
	if err != nil {
		// A Go error is an operational fault, not a refusal: retry or alert.
		return fmt.Errorf("evaluate leave application: %w", err)
	}

	if results.AllPassed() {
		fmt.Println("application accepted")
		for _, decision := range results.All() {
			fmt.Printf("  %-18s %s @ %s -> %v\n",
				decision.RuleKey.Family().Name, decisionAnchor(decision), decision.RuleVersion, decision.Result)
		}
		return nil
	}

	// Every problem at once, which is what a user filling in a form needs.
	fmt.Println("application refused")
	for _, decision := range results.Failures() {
		fmt.Printf("  %-18s %v\n", decision.RuleKey.Family().Name, decision.ReasonCodes)
		// The explanation is already in the decision; a support engineer needs
		// no access to the service to read it.
		logger.Info("decision point failed",
			"rule", decision.RuleKey, "reasons", decision.ReasonCodes, "decisionId", decision.DecisionID)
	}
	return nil
}

// shadow is the pattern for adopting a rule without betting on it.
//
// The legacy answer stays authoritative. The engine runs beside it and the two
// are compared, so a disagreement is a logged metric rather than a wrong
// decision for an employee. Only when the disagreements are understood does
// the rule take over, one check at a time.
//
// The legacy code must not be deleted before a root-anchored baseline rule
// exists for every tenant: without one, a tenant that never configured a rule
// would get no decision at all.
func shadow(
	ctx context.Context, engine *ruleengine.Engine, application leaveApplication, logger *slog.Logger,
) {
	legacy := legacyEligibility(application)

	decision, err := engine.Evaluate(ctx, requestWithKey(application, decisionPoints[0].Key))
	if err != nil {
		// Shadow mode never fails the request: the legacy answer stands.
		logger.Warn("shadow evaluation errored; legacy answer stands", "error", err)
		fmt.Printf("legacy %t, engine unavailable\n", legacy)
		return
	}
	if decision.Failed {
		logger.Warn("shadow evaluation failed closed; legacy answer stands",
			"reasons", decision.ReasonCodes)
		fmt.Printf("legacy %t, engine refused (%v)\n", legacy, decision.ReasonCodes)
		return
	}

	proposed, _ := decision.Result["eligible"].(bool)
	switch {
	case proposed == legacy:
		fmt.Printf("agree: both %t (rule %s @ %s)\n", legacy, decisionAnchor(decision), decision.RuleVersion)
	default:
		// This is the line a team watches during adoption. A disagreement is
		// either a bug in the rule or a behaviour the legacy code had that
		// nobody had written down.
		fmt.Printf("DISAGREE: legacy %t, rule %t (%s @ %s)\n",
			legacy, proposed, decisionAnchor(decision), decision.RuleVersion)
		logger.Warn("shadow disagreement",
			"legacy", legacy, "rule", proposed,
			"ruleVersion", decision.RuleVersion, "decisionId", decision.DecisionID)
	}
	// Whatever the engine said, the caller still uses `legacy` until this
	// check is cut over.
	_ = legacy
}

// legacyEligibility is the hard-coded check the rule replaces: six months of
// service, for everyone, with no way for a tenant to differ.
func legacyEligibility(application leaveApplication) bool {
	joined, err := time.Parse(time.DateOnly, application.JoinDate)
	if err != nil {
		return false
	}
	months := int(application.AsOf.Sub(joined).Hours() / 24 / 30)
	return months >= 6
}

func requestFor(application leaveApplication) ruleengine.EvalRequest {
	return requestWithKey(application, "")
}

func requestWithKey(application leaveApplication, key rule.RuleKey) ruleengine.EvalRequest {
	request := ruleengine.EvalRequest{
		SchemaVersion: 2,
		Tenant:        ruleengine.Tenant{ID: tenant},
		Routing:       ruleengine.Routing{Domain: domain, Action: "apply"},
		Subject: ruleengine.Subject{
			Type: "employee",
			Identifier: ruleengine.Identifier{
				System: "urn:arrowhr:ns:employee-id", Value: application.EmployeeID, IDType: "employeeId",
			},
			NodeID: application.NodeID,
			// Attributes narrow a rule within its anchor. Hierarchy placement
			// is not here: that is what nodeId is for.
			Attributes: map[string]any{"employeeGroup": application.EmployeeGroup},
		},
		// Facts the caller already has. Anything else the rules declare is
		// fetched by the engine, within the budget.
		Context: map[string]any{
			"joinDate":      application.JoinDate,
			"requestedDays": application.RequestedDays,
		},
		AsOf:          application.AsOf,
		CorrelationID: "example-" + application.EmployeeID,
	}
	if key != "" {
		request.Routing.RuleKey = key
	}
	return request
}

func decisionAnchor(decision ruleengine.Decision) string {
	if decision.ResolvedAt == nil {
		return "(unresolved)"
	}
	return decision.ResolvedAt.NodeID
}
