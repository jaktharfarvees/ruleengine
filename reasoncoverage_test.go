package ruleengine_test

import (
	"context"
	"testing"
	"time"

	ruleengine "github.com/arrowhr/ruleengine"
	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/ruletest"
	"github.com/arrowhr/ruleengine/store"
)

// mutableClock drives cache staleness without waiting for real time.
type mutableClock struct{ value time.Time }

func (c *mutableClock) Now() time.Time { return c.value }

// scenario drives one decision to one governing reason code. The bundle and
// the request are both adjustable, because most codes need either a rule or a
// field shaped to produce them.
type scenario struct {
	name    string
	want    ruleengine.ReasonCode
	config  func(*ruleengine.Config)
	bundle  func(*store.Bundle)
	request func(*ruleengine.EvalRequest)
	// options adjusts the engine's collaborators, for the cases that need a
	// slow executor or a controlled clock.
	options func(*scenarioSetup)
	// skipLoad leaves the cache empty, for the cases that are about having no
	// rules at all.
	skipLoad bool
}

type scenarioSetup struct {
	executor *ruletest.InMemoryExecutor
	clock    *mutableClock
	options  []ruleengine.Option
}

// TestEveryReasonCodeIsProduced is a phase exit criterion: no reason code may
// exist that nothing can produce.
//
// A code nothing produces is worse than a missing one — it tells an operator a
// condition is handled when it is not. So this drives the real pipeline for
// each one and then checks the catalogue is covered, rather than asserting
// each case in isolation.
func TestEveryReasonCodeIsProduced(t *testing.T) {
	produced := map[ruleengine.ReasonCode]string{}

	for _, test := range reasonScenarios() {
		t.Run(test.name, func(t *testing.T) {
			decision := runScenario(t, test)
			if len(decision.ReasonCodes) == 0 {
				t.Fatal("the decision carried no reason code at all")
			}
			for _, code := range decision.ReasonCodes {
				if _, seen := produced[code]; !seen {
					produced[code] = test.name
				}
			}
			if !hasReason(decision, test.want) {
				t.Fatalf("reason codes = %v, want %s", decision.ReasonCodes, test.want)
			}
			// A fatal code must fail the decision and a non-fatal one must not,
			// or an operator cannot tell which outcomes stand.
			if test.want.Fatal() != decision.Failed {
				t.Fatalf("%s is fatal=%t but the decision failed=%t",
					test.want, test.want.Fatal(), decision.Failed)
			}
		})
	}

	// CROSS_TENANT_RULE_DROPPED cannot be reached through the pipeline: the
	// catalog is keyed by tenant, so another tenant's rule is never a
	// candidate. It is defence in depth (INV7) and is covered where it lives,
	// by the resolution suite's RT11. Recorded here so the gap is deliberate
	// rather than discovered.
	produced[ruleengine.ReasonCrossTenantRuleDropped] = "resolve RT11 (unreachable through the catalog by design)"

	for _, code := range ruleengine.ReasonCodes() {
		if _, covered := produced[code]; !covered {
			t.Errorf("no test produces reason code %s", code)
		}
	}
	t.Logf("covered %d of %d catalogue reason codes", len(produced), len(ruleengine.ReasonCodes()))
}

func reasonScenarios() []scenario {
	return []scenario{
		{
			name: "a rule decides normally",
			want: ruleengine.ReasonOK,
		},
		{
			name:    "the request names another tenant",
			want:    ruleengine.ReasonSchemaInvalid,
			request: func(r *ruleengine.EvalRequest) { r.Tenant.ID = "EARROW" },
		},
		{
			name:     "nothing has been published",
			want:     ruleengine.ReasonHierarchyUnavailable,
			skipLoad: true,
		},
		{
			name:    "an employee arrives with no node",
			want:    ruleengine.ReasonHierarchyNotConfigured,
			request: func(r *ruleengine.EvalRequest) { r.Subject.NodeID = "" },
		},
		{
			name: "no rule governs the decision point",
			want: ruleengine.ReasonNoRuleResolved,
			request: func(r *ruleengine.EvalRequest) {
				r.Routing.RuleKey = "urn:arrowhr:rule:leave:eligibility:no-such-rule:v1"
			},
		},
		{
			name: "a tenant-authorized default governs an otherwise unresolved rule",
			want: ruleengine.ReasonDefaultApplied,
			bundle: func(b *store.Bundle) {
				b.Catalogs = []rule.RuleCatalog{{
					CatalogManifestID: "catalog-defaults-v1", Tenant: tenant,
					Defaults: []rule.DefaultPolicy{{
						Domain: "leave", RuleSet: "eligibility", Name: "tenant-default",
						DecisionType: "leave-eligibility",
						Result:       map[string]any{"eligible": false, "reasonCode": "TENANT_DEFAULT"},
						ReasonCode:   "TENANT_DEFAULT", AuthorizedBy: "tenant-author:RWB",
						AuthorizedAt: asOf.Add(-time.Hour),
					}},
				}}
			},
			request: func(r *ruleengine.EvalRequest) {
				r.Routing.RuleKey = "urn:arrowhr:rule:leave:eligibility:tenant-default:v1"
			},
		},
		{
			name: "two equally specific rules at one anchor",
			want: ruleengine.ReasonPrecedenceConflict,
			bundle: func(b *store.Bundle) {
				for index := range b.Rules {
					if b.Rules[index].RuleSet == "conflict" && b.Rules[index].Name == "overlap" {
						b.Rules[index].TieBreakPriority = nil
					}
				}
			},
			request: func(r *ruleengine.EvalRequest) {
				r.Routing.RuleKey = "urn:arrowhr:rule:leave:conflict:overlap:v1"
				// BR003 sits under D003, where both conflict rules anchor.
				r.Subject.NodeID = "BR003"
				r.Subject.Attributes = map[string]any{
					"employeeGroup": "Finance", "workerCategory": "PERMANENT",
				}
				r.Context = map[string]any{"requestedDays": "1"}
			},
		},
		{
			name:    "a pin names a version nothing holds",
			want:    ruleengine.ReasonPinnedVersionUnavailable,
			request: func(r *ruleengine.EvalRequest) { r.RuleVersionPin = "9.9.9" },
		},
		{
			name: "a rule declares a field nobody registered",
			want: ruleengine.ReasonFieldNotRegistered,
			bundle: func(b *store.Bundle) {
				b.Rules = append(b.Rules, leaveRule("unregistered", []string{"notAField"},
					map[string]string{"notAField": ">= 1"}))
			},
			request: pointAt("unregistered"),
		},
		{
			name: "a decision uses a deprecated field",
			want: ruleengine.ReasonFieldDeprecated,
			bundle: func(b *store.Bundle) {
				for index := range b.Fields {
					if b.Fields[index].Name == "requestedDays" {
						b.Fields[index].Status = "deprecated"
					}
				}
				b.Rules = append(b.Rules, leaveRule("deprecated-input", []string{"requestedDays"},
					map[string]string{"requestedDays": ">= 1"}))
			},
			request: withContext(pointAt("deprecated-input"), map[string]any{"requestedDays": "2"}),
		},
		{
			name:    "a supplied value is the wrong type",
			want:    ruleengine.ReasonFieldTypeMismatch,
			request: withContext(nil, map[string]any{"joinDate": true}),
		},
		{
			name:    "a passed field was not passed",
			want:    ruleengine.ReasonFactUnresolvable,
			request: withContext(nil, map[string]any{}),
		},
		{
			name: "one decision needs more dataset facts than the budget",
			want: ruleengine.ReasonFactBudgetExceeded,
			config: func(c *ruleengine.Config) {
				budget := c.FactBudget
				budget.MaxDatasetFacts = 1
				c.FactBudget = budget
			},
			bundle: func(b *store.Bundle) {
				b.Rules = append(b.Rules, leaveRule("two-datasets",
					[]string{"availableBalance", "casualLeaveTakenThisYear"},
					map[string]string{"availableBalance": ">= 0"}))
			},
			request: pointAt("two-datasets"),
		},
		{
			name: "the store answers too slowly",
			want: ruleengine.ReasonFactTimeout,
			config: func(c *ruleengine.Config) {
				budget := c.FactBudget
				budget.PerFactTimeout = 10 * time.Millisecond
				budget.TotalFactTimeout = 50 * time.Millisecond
				c.FactBudget = budget
			},
			options: func(s *scenarioSetup) { s.executor.SetDelay(200 * time.Millisecond) },
			bundle: func(b *store.Bundle) {
				b.Rules = append(b.Rules, leaveRule("slow", []string{"availableBalance"},
					map[string]string{"availableBalance": ">= 0"}))
			},
			request: pointAt("slow"),
		},
		{
			name: "a field smuggles query syntax into a filter",
			want: ruleengine.ReasonFactRawQueryRejected,
			bundle: func(b *store.Bundle) {
				b.Fields = append(b.Fields, datasetField("injected", &registry.FactSource{
					Kind: registry.FactDataset, Dataset: "leave.balances",
					Filters: []rule.FilterCondition{
						{Attribute: "leaveType", Op: rule.FilterEquals, Value: "ANNUAL'; DROP TABLE x; --"},
					},
					Window:    &registry.FactWindow{Column: "asOfDate", RelativeTo: dataset.TokenValidAt, LastDays: 30},
					Aggregate: &registry.FactAggregate{Fn: registry.AggregateLatest, On: "balanceDays"},
				}))
				b.Rules = append(b.Rules, leaveRule("injected-rule", []string{"injected"},
					map[string]string{"injected": ">= 0"}))
			},
			request: pointAt("injected-rule"),
		},
		{
			name: "a field filters a column the manifest does not permit",
			want: ruleengine.ReasonFactColumnNotAllowed,
			bundle: func(b *store.Bundle) {
				b.Fields = append(b.Fields, datasetField("unfilterable", &registry.FactSource{
					Kind: registry.FactDataset, Dataset: "leave.balances",
					// balanceDays is aggregatable but deliberately not filterable.
					Filters: []rule.FilterCondition{
						{Attribute: "balanceDays", Op: rule.FilterEquals, Value: "1"},
					},
					Window:    &registry.FactWindow{Column: "asOfDate", RelativeTo: dataset.TokenValidAt, LastDays: 30},
					Aggregate: &registry.FactAggregate{Fn: registry.AggregateLatest, On: "balanceDays"},
				}))
				b.Rules = append(b.Rules, leaveRule("unfilterable-rule", []string{"unfilterable"},
					map[string]string{"unfilterable": ">= 0"}))
			},
			request: pointAt("unfilterable-rule"),
		},
		{
			name: "a field asks for an aggregate the dataset withholds",
			want: ruleengine.ReasonFactAggregateUnsupported,
			bundle: func(b *store.Bundle) {
				b.Fields = append(b.Fields, datasetField("streaked", &registry.FactSource{
					Kind: registry.FactDataset, Dataset: "leave.balances",
					Window: &registry.FactWindow{Column: "asOfDate", RelativeTo: dataset.TokenValidAt, LastDays: 30},
					// leave.balances offers no streak.
					Aggregate: &registry.FactAggregate{Fn: registry.AggregateStreak, On: "balanceDays"},
				}))
				b.Rules = append(b.Rules, leaveRule("streaked-rule", []string{"streaked"},
					map[string]string{"streaked": ">= 0"}))
			},
			request: pointAt("streaked-rule"),
		},
		{
			name: "a field asks for a window wider than the dataset allows",
			want: ruleengine.ReasonFactWindowTooWide,
			bundle: func(b *store.Bundle) {
				b.Fields = append(b.Fields, datasetField("wide", &registry.FactSource{
					Kind: registry.FactDataset, Dataset: "leave.balances",
					// leave.balances caps the window at 366 days.
					Window:    &registry.FactWindow{Column: "asOfDate", RelativeTo: dataset.TokenValidAt, LastDays: 400},
					Aggregate: &registry.FactAggregate{Fn: registry.AggregateLatest, On: "balanceDays"},
				}))
				b.Rules = append(b.Rules, leaveRule("wide-rule", []string{"wide"},
					map[string]string{"wide": ">= 0"}))
			},
			request: pointAt("wide-rule"),
		},
		{
			name: "two derived fields depend on each other",
			want: ruleengine.ReasonFactDerivedCycle,
			bundle: func(b *store.Bundle) {
				b.Fields = append(b.Fields,
					derivedField("alpha", "$beta + 1"),
					derivedField("beta", "$alpha + 1"),
				)
				b.Rules = append(b.Rules, leaveRule("cyclic", []string{"alpha"},
					map[string]string{"alpha": ">= 0"}))
			},
			request: pointAt("cyclic"),
		},
		{
			name: "a fact and a subject attribute share a name",
			want: ruleengine.ReasonFactShadowedAttribute,
			request: func(r *ruleengine.EvalRequest) {
				r.Context = map[string]any{"joinDate": "2026-05-01"}
				// The rule's own fact must win, and the shadow is recorded.
				r.Subject.Attributes = map[string]any{"serviceMonths": 99}
			},
		},
		{
			name: "a field names a dataset this service does not hold",
			want: ruleengine.ReasonDatasetNotPublished,
			bundle: func(b *store.Bundle) {
				b.Manifests = nil
				b.Rules = append(b.Rules, leaveRule("no-dataset", []string{"availableBalance"},
					map[string]string{"availableBalance": ">= 0"}))
			},
			request: pointAt("no-dataset"),
		},
		{
			name: "a decision reads a dataset that is retiring",
			want: ruleengine.ReasonDatasetDeprecated,
			bundle: func(b *store.Bundle) {
				for index := range b.Manifests {
					if b.Manifests[index].DatasetID == "leave.balances" {
						b.Manifests[index].Status = dataset.Deprecated
					}
				}
				b.Rules = append(b.Rules, leaveRule("deprecated-dataset", []string{"availableBalance"},
					map[string]string{"availableBalance": ">= 0"}))
			},
			request: pointAt("deprecated-dataset"),
		},
		{
			name: "a filter reads a personal column that is not the subject key",
			want: ruleengine.ReasonPIIColumnInFilter,
			bundle: func(b *store.Bundle) {
				for index := range b.Manifests {
					if b.Manifests[index].DatasetID != "leave.balances" {
						continue
					}
					for column := range b.Manifests[index].Columns {
						if b.Manifests[index].Columns[column].Name == "leaveType" {
							b.Manifests[index].Columns[column].PII = true
						}
					}
				}
				b.Rules = append(b.Rules, leaveRule("pii-filter", []string{"availableBalance"},
					map[string]string{"availableBalance": ">= 0"}))
			},
			request: pointAt("pii-filter"),
		},
		{
			name: "the engine cannot evaluate the rule's logic",
			want: ruleengine.ReasonEngineError,
			bundle: func(b *store.Bundle) {
				// joinDate is a date; comparing it numerically fails at
				// evaluation rather than at load.
				b.Rules = append(b.Rules, leaveRule("bad-logic", []string{"joinDate"},
					map[string]string{"joinDate": ">= 6"}))
			},
			request: withContext(pointAt("bad-logic"), map[string]any{"joinDate": "2026-05-01"}),
		},
		{
			name: "a rule does not produce an output it declared",
			want: ruleengine.ReasonOutputContractViolation,
			bundle: func(b *store.Bundle) {
				incomplete := leaveRule("incomplete", []string{"requestedDays"},
					map[string]string{"requestedDays": ">= 0"})
				// Both rows set allowed but neither sets reasonCode.
				incomplete.Logic.Rows = []rule.DecisionRow{
					{ID: "only", When: map[string]string{}, Then: map[string]any{"allowed": true}},
				}
				b.Rules = append(b.Rules, incomplete)
			},
			request: withContext(pointAt("incomplete"), map[string]any{"requestedDays": "1"}),
		},
		{
			name:   "the cache has not reconciled for far too long",
			want:   ruleengine.ReasonCacheStale,
			config: func(c *ruleengine.Config) { c.CacheReloadInterval = time.Minute },
			options: func(s *scenarioSetup) {
				// Published now, read an hour later: far past three missed
				// reconciles.
				s.clock.value = asOf
				s.options = append(s.options, ruleengine.WithClock(s.clock))
			},
		},
	}
}

// runScenario builds an engine for one scenario and evaluates it.
func runScenario(t *testing.T, test scenario) ruleengine.Decision {
	t.Helper()
	manifests, err := ruletest.LoadManifests("leave.balances", "leave.applications")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := ruletest.NewInMemoryExecutor(manifests["leave.balances"], manifests["leave.applications"])
	if err != nil {
		t.Fatal(err)
	}
	addBalance(t, executor, "12.5", asOf.AddDate(0, 0, -1))

	setup := &scenarioSetup{executor: executor, clock: &mutableClock{value: asOf}}
	setup.options = []ruleengine.Option{ruleengine.WithExecutor(executor)}
	if test.options != nil {
		test.options(setup)
	}

	config := ruleengine.Config{Tenant: tenant, Domain: "leave", FactBudget: ruletest.Budget()}
	if test.config != nil {
		test.config(&config)
	}
	instance, err := ruleengine.New(config, setup.options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = instance.Close() })

	if !test.skipLoad {
		bundle := sharedBundle(t)
		bundle.Rules = append(bundle.Rules, pipelineRules()...)
		if test.bundle != nil {
			test.bundle(&bundle)
		}
		if err := instance.ApplyBundle(bundle); err != nil {
			t.Fatal(err)
		}
	}
	// Staleness is measured after publication, so the clock moves last.
	if test.want == ruleengine.ReasonCacheStale {
		setup.clock.value = asOf.Add(time.Hour)
	}

	request := leaveRequest("urn:arrowhr:rule:leave:eligibility:min-service:v1",
		map[string]any{"joinDate": "2026-05-01"})
	if test.request != nil {
		test.request(&request)
	}
	decision, err := instance.Evaluate(context.Background(), request)
	if err != nil {
		t.Fatalf("Evaluate returned a Go error rather than a failed decision: %v", err)
	}
	return decision
}

// pointAt retargets the request at one of the test-only validation rules.
func pointAt(name string) func(*ruleengine.EvalRequest) {
	return func(r *ruleengine.EvalRequest) {
		r.Routing = ruleengine.Routing{Domain: "leave",
			RuleKey: rule.RuleKey("urn:arrowhr:rule:leave:validation:" + name + ":v1")}
	}
}

// withContext layers a request context onto another adjustment.
func withContext(base func(*ruleengine.EvalRequest), values map[string]any) func(*ruleengine.EvalRequest) {
	return func(r *ruleengine.EvalRequest) {
		if base != nil {
			base(r)
		}
		r.Context = values
	}
}

func datasetField(name string, source *registry.FactSource) registry.FieldDefinition {
	return registry.FieldDefinition{
		Name: name, Domain: "leave", DataType: registry.DataDecimal,
		OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
		FactSource: *source,
	}
}

func derivedField(name, expression string) registry.FieldDefinition {
	return registry.FieldDefinition{
		Name: name, Domain: "leave", DataType: registry.DataDecimal,
		OwnerService: "leave-service", Status: "active", Role: registry.RoleInput,
		FactSource: registry.FactSource{Kind: registry.FactDerived, Expression: expression},
	}
}
