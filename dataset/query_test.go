package dataset

import (
	"strings"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
)

func TestQueryAccessorsReturnCopies(t *testing.T) {
	query, _, err := Compile(datasetSource(), validManifest(), testParams())
	if err != nil {
		t.Fatal(err)
	}
	// An executor must not be able to mutate a query another decision point is
	// about to reuse from the de-duplication cache.
	first := query.Predicates()
	first[0].Column = "tampered"
	first[0].Values[0] = "tampered"
	second := query.Predicates()
	if second[0].Column == "tampered" || second[0].Values[0] == "tampered" {
		t.Fatal("Predicates() handed out the query's own slice")
	}

	tenant, present := query.TenantPredicate()
	if !present || tenant.Column != TenantColumn || !tenant.Injected || tenant.Values[0] != "RWB" {
		t.Fatalf("tenant predicate = %+v, %t", tenant, present)
	}

	// A zero Query is what an executor would see if one were hand-built, and
	// reporting no tenant predicate is how it can tell.
	if _, present := (Query{}).TenantPredicate(); present {
		t.Fatal("a zero query claims a tenant predicate")
	}
	if _, windowed := (Query{}).Window(); windowed {
		t.Fatal("a zero query claims a window")
	}
}

func TestFingerprintIgnoresAuthoredOrderOnly(t *testing.T) {
	employee := rule.FilterCondition{Attribute: "employeeId", Op: rule.FilterEquals, Value: TokenEmployeeID}
	shift := rule.FilterCondition{Attribute: "shiftCode", Op: rule.FilterEquals, Value: "MORNING"}

	ordered := datasetSource()
	ordered.Filters = []rule.FilterCondition{employee, shift}
	swapped := datasetSource()
	swapped.Filters = []rule.FilterCondition{shift, employee}

	left := mustCompile(t, ordered)
	right := mustCompile(t, swapped)
	if left.Fingerprint() != right.Fingerprint() {
		t.Fatal("filter order changed the fingerprint")
	}
	if left.Fingerprint() != left.Fingerprint() {
		t.Fatal("the fingerprint is not stable across calls")
	}

	// Every ingredient of the answer must change the identity, or the
	// de-duplication cache would serve one question's answer to another.
	variants := map[string]func(*registry.FactSource){
		"window length":    func(s *registry.FactSource) { s.Window.LastDays = 29 },
		"aggregate":        func(s *registry.FactSource) { s.Aggregate.Fn = AggregateLatest },
		"aggregate column": func(s *registry.FactSource) { s.Aggregate.On = "lateMinutes" },
		"aggregate unit":   func(s *registry.FactSource) { s.Aggregate.Unit = "hours" },
		"filter value":     func(s *registry.FactSource) { s.Filters[0].Value = "E9999" },
		"filter operator": func(s *registry.FactSource) {
			s.Filters[0].Op = rule.FilterIn
			s.Filters[0].Value = []any{"E1001"}
		},
	}
	seen := map[string]string{left.Fingerprint(): "baseline"}
	for name, change := range variants {
		source := datasetSource()
		source.Filters = []rule.FilterCondition{employee, shift}
		change(&source)
		query := mustCompile(t, source)
		fingerprint := query.Fingerprint()
		if previous, clash := seen[fingerprint]; clash {
			t.Fatalf("%s shares a fingerprint with %s", name, previous)
		}
		seen[fingerprint] = name
	}

	// The fingerprint identifies the resolved question, not the authored text.
	// Two anchors that resolve to the same absolute window are the same
	// question, and sharing one answer between them is correct.
	sameWindow := datasetSource()
	sameWindow.Filters = []rule.FilterCondition{employee, shift}
	sameWindow.Window.RelativeTo = TokenAsOf
	if mustCompile(t, sameWindow).Fingerprint() != left.Fingerprint() {
		t.Fatal("two anchors resolving to the same window produced different identities")
	}
	// When they resolve differently, the identities must differ too.
	retroactive := testParams()
	retroactive.AsOf = compileAt.AddDate(0, 0, 10)
	shifted, _, err := Compile(sameWindow, validManifest(), retroactive)
	if err != nil {
		t.Fatal(err)
	}
	if shifted.Fingerprint() == left.Fingerprint() {
		t.Fatal("a window anchored at a different time shares an identity")
	}

	// A different tenant, row limit or dataset version is a different query.
	for name, change := range map[string]func(*Params, *Manifest){
		"tenant":          func(p *Params, _ *Manifest) { p.Tenant = "EARROW" },
		"row limit":       func(p *Params, _ *Manifest) { p.RowLimit = 100 },
		"dataset version": func(_ *Params, m *Manifest) { m.Version = "2.0.0" },
	} {
		params, manifest := testParams(), validManifest()
		change(&params, &manifest)
		query, _, err := Compile(datasetSource(), manifest, params)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if query.Fingerprint() == left.Fingerprint() {
			t.Fatalf("%s did not change the fingerprint", name)
		}
	}
}

func TestExplainDescribesEveryShape(t *testing.T) {
	tests := []struct {
		name   string
		change func(*registry.FactSource)
		want   []string
	}{
		{"streak", func(s *registry.FactSource) {}, []string{"consecutive", "isLate"}},
		{"count", func(s *registry.FactSource) {
			s.Aggregate = &registry.FactAggregate{Fn: AggregateCount}
		}, []string{"The number of rows"}},
		{"exists", func(s *registry.FactSource) {
			s.Aggregate = &registry.FactAggregate{Fn: AggregateExists}
		}, []string{"Whether any row exists"}},
		{"latest", func(s *registry.FactSource) {
			s.Aggregate = &registry.FactAggregate{Fn: AggregateLatest, On: "lateMinutes", Unit: "minutes"}
		}, []string{"The most recent lateMinutes minutes"}},
		{"sum", func(s *registry.FactSource) {
			s.Aggregate = &registry.FactAggregate{Fn: AggregateSum, On: "lateMinutes"}
		}, []string{"The sum of lateMinutes"}},
		{"in", func(s *registry.FactSource) {
			s.Filters = []rule.FilterCondition{{Attribute: "status", Op: rule.FilterIn, Value: []any{"OK", "LATE"}}}
		}, []string{"status is one of OK, LATE"}},
		{"notIn", func(s *registry.FactSource) {
			s.Filters = []rule.FilterCondition{{Attribute: "status", Op: rule.FilterNotIn, Value: []any{"LATE"}}}
		}, []string{"status is none of LATE"}},
		{"range", func(s *registry.FactSource) {
			s.Filters = []rule.FilterCondition{{Attribute: "lateMinutes", Op: rule.FilterRange, Value: []any{1, 60}}}
		}, []string{"lateMinutes is between 1 and 60"}},
		{"exists filter", func(s *registry.FactSource) {
			s.Filters = []rule.FilterCondition{{Attribute: "shiftCode", Op: rule.FilterExists}}
		}, []string{"shiftCode is present"}},
		{"notExists filter", func(s *registry.FactSource) {
			s.Filters = []rule.FilterCondition{{Attribute: "shiftCode", Op: rule.FilterNotExists}}
		}, []string{"shiftCode is absent"}},
		{"no filters", func(s *registry.FactSource) {
			s.Filters = nil
		}, []string{"attendance.days", "scoped to this tenant"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := datasetSource()
			test.change(&source)
			query := mustCompile(t, source)
			explanation := query.Explain()
			for _, want := range test.want {
				if !strings.Contains(explanation, want) {
					t.Fatalf("Explain() = %q, missing %q", explanation, want)
				}
			}
			// The author did not write the tenant predicate, so it is
			// described as scoping rather than listed among their filters.
			if strings.Contains(explanation, TenantColumn) {
				t.Fatalf("Explain() exposes the tenant column: %q", explanation)
			}
			if !strings.Contains(explanation, "at most") {
				t.Fatalf("Explain() does not state the row limit: %q", explanation)
			}
		})
	}
}

func TestExplainWithoutAWindow(t *testing.T) {
	source := datasetSource()
	source.Window = nil
	source.Aggregate = &registry.FactAggregate{Fn: AggregateCount}
	query := mustCompile(t, source)
	if strings.Contains(query.Explain(), "up to") {
		t.Fatalf("Explain() described a window that was not asked for: %q", query.Explain())
	}
}

func TestScalarKeyRenderings(t *testing.T) {
	moment := time.Date(2026, time.June, 15, 10, 30, 0, 0, time.UTC)
	tests := []struct {
		value any
		want  string
	}{
		{nil, "null"},
		{"ANNUAL", "ANNUAL"},
		{true, "true"},
		{false, "false"},
		{moment, "2026-06-15T10:30:00Z"},
		{42, "42"},
	}
	for _, test := range tests {
		if got := scalarKey(test.value); got != test.want {
			t.Errorf("scalarKey(%#v) = %q, want %q", test.value, got, test.want)
		}
	}
	// A time in another zone is the same instant, so it must render the same
	// way: two spellings of one value would split a de-duplication cache.
	elsewhere := moment.In(time.FixedZone("IST", 5*3600+1800))
	if scalarKey(elsewhere) != scalarKey(moment) {
		t.Fatal("the same instant rendered differently in two zones")
	}
}

func TestDescribePredicateFallsBackForAnUnknownOperator(t *testing.T) {
	description := describePredicate(Predicate{Column: "x", Op: "regex", Values: []any{"a"}})
	if !strings.Contains(description, "regex") || !strings.Contains(description, "x") {
		t.Fatalf("describePredicate = %q", description)
	}
}

func mustCompile(t *testing.T, source registry.FactSource) Query {
	t.Helper()
	query, _, err := Compile(source, validManifest(), testParams())
	if err != nil {
		t.Fatal(err)
	}
	return query
}

// TestUntenantedCopy covers the helper the conformance suite in
// dataset/datasettest depends on. It must strip the injected predicate, leave
// the author's own filters alone, and not touch the original: the suite reuses
// the compiled query for its other checks.
func TestUntenantedCopy(t *testing.T) {
	query := Query{
		dataset: "probe.rows",
		predicates: []Predicate{
			{Column: TenantColumn, Op: rule.FilterEquals, Values: []any{"T1"}, Injected: true},
			{Column: "employeeId", Op: rule.FilterEquals, Values: []any{"E1"}},
		},
		rowLimit: 10,
	}

	stripped := UntenantedCopy(query)
	if _, present := stripped.TenantPredicate(); present {
		t.Fatal("the injected tenant predicate survived")
	}
	remaining := stripped.Predicates()
	if len(remaining) != 1 || remaining[0].Column != "employeeId" {
		t.Fatalf("predicates = %+v, want only the author's own filter", remaining)
	}
	if _, present := query.TenantPredicate(); !present {
		t.Fatal("UntenantedCopy mutated its argument")
	}

	// A query that carries nothing but the tenant predicate becomes empty
	// rather than nil-panicking on the next read.
	only := Query{predicates: []Predicate{
		{Column: TenantColumn, Values: []any{"T1"}, Injected: true},
	}}
	if got := UntenantedCopy(only).Predicates(); len(got) != 0 {
		t.Fatalf("predicates = %+v, want none", got)
	}
}
