package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/arrowhr/ruleengine/rule"
	"github.com/arrowhr/ruleengine/telemetry"
)

// TestPublishedSnapshotIsIndependentOfTheBundle pins the guarantee the whole
// cache rests on: once a bundle is published, nothing a loader or a caller
// still holds can change what readers see. A loader that reuses its decoding
// buffers, or a service that keeps a bundle to inspect, must not be able to
// alter a rule mid-decision.
func TestPublishedSnapshotIsIndependentOfTheBundle(t *testing.T) {
	validTo := january.Add(365 * 24 * time.Hour)
	approvedAt := january
	priority := 10
	definition := anchoredRule("1.0.0", "G001", january,
		rule.FilterCondition{Attribute: "employeeGroup", Op: rule.FilterIn, Value: []any{"Finance", "Ops"}},
	)
	definition.TieBreakPriority = &priority
	definition.Effective.ValidTo = &validTo
	definition.Metadata.ApprovedAt = &approvedAt
	definition.Metadata.Tags = []string{"payroll"}
	definition.Guards = []string{"guard-one"}
	definition.DeclaredInputs = []string{"serviceMonths"}
	definition.DeclaredOutputs = []string{"eligible", "nested", "list", "pair", "pointer", "absent"}
	// Nested authored values exercise every branch of the deep copy.
	definition.Logic.Rows = []rule.DecisionRow{{
		ID:   "r",
		When: map[string]string{"serviceMonths": ">= 6"},
		Then: map[string]any{
			"eligible": true,
			"nested":   map[string]any{"inner": []any{1, "two"}},
			"list":     []any{map[string]any{"deep": "value"}},
			"pair":     [2]string{"a", "b"},
			"pointer":  &priority,
			"absent":   nil,
		},
	}}

	cache := NewCache()
	if err := cache.Replace(Bundle{
		Rules:     []rule.RuleDefinition{definition},
		Hierarchy: twoLevelHierarchy(),
		LoadedAt:  january,
	}); err != nil {
		t.Fatal(err)
	}

	before := cache.Candidates("T1", "leave", "eligibility", "alpha", january.Add(24*time.Hour))
	if len(before) != 1 {
		t.Fatalf("Candidates = %s", versionsOf(before))
	}
	snapshot := reflect.ValueOf(*before[0])

	// Mutate every mutable part of the caller's own copy.
	definition.DeclaredInputs[0] = "mutated"
	definition.DeclaredOutputs[0] = "mutated"
	definition.Guards[0] = "mutated"
	definition.Metadata.Tags[0] = "mutated"
	definition.Scope.Filters[0].Attribute = "mutated"
	definition.Scope.Filters[0].Value.([]any)[0] = "mutated"
	definition.Logic.Rows[0].When["serviceMonths"] = "mutated"
	definition.Logic.Rows[0].Then["eligible"] = "mutated"
	definition.Logic.Rows[0].Then["nested"].(map[string]any)["inner"].([]any)[0] = "mutated"
	definition.Logic.Rows[0].Then["list"].([]any)[0].(map[string]any)["deep"] = "mutated"
	priority = 99

	after := cache.Candidates("T1", "leave", "eligibility", "alpha", january.Add(24*time.Hour))
	if !reflect.DeepEqual(reflect.ValueOf(*after[0]).Interface(), snapshot.Interface()) {
		t.Fatalf("the published snapshot changed when the caller mutated its bundle:\n before %#v\n after  %#v",
			snapshot.Interface(), *after[0])
	}
	if *after[0].TieBreakPriority != 10 {
		t.Fatalf("TieBreakPriority = %d, want the published 10", *after[0].TieBreakPriority)
	}
}

// TestCloneAnyHandlesEveryAuthoredShape covers the deep copy directly, since a
// shape it silently shares is a shape a caller can mutate inside a published
// snapshot.
func TestCloneAnyHandlesEveryAuthoredShape(t *testing.T) {
	number := 7
	var nilPointer *int
	var nilSlice []string
	var nilMap map[string]any
	var nilInterface any

	tests := []any{
		nil, nilInterface, true, "text", 1, 1.5,
		nilPointer, &number,
		nilSlice, []string{"a"}, []any{1, "two", nil},
		nilMap, map[string]any{"k": "v"},
		map[string]any{"nested": map[string]any{"deep": []any{1}}},
		[2]string{"a", "b"},
		[]any{&number},
		map[string]string{"k": "v"},
	}
	for _, original := range tests {
		cloned := cloneAny(original)
		if !reflect.DeepEqual(cloned, original) {
			t.Fatalf("cloneAny(%#v) = %#v, want an equal value", original, cloned)
		}
	}

	// A pointer must be copied, not shared.
	cloned := cloneAny(&number).(*int)
	*cloned = 99
	if number == 99 {
		t.Fatal("cloneAny shared a pointer with its source")
	}
	// A nested slice inside a map must be copied too.
	source := map[string]any{"list": []any{"first"}}
	copied := cloneAny(source).(map[string]any)
	copied["list"].([]any)[0] = "second"
	if source["list"].([]any)[0] != "first" {
		t.Fatal("cloneAny shared a nested slice with its source")
	}
	// An array inside a slice is a value type but still walked.
	arraySource := []any{[2]string{"a", "b"}}
	arrayCopy := cloneAny(arraySource).([]any)
	if arrayCopy[0].([2]string)[0] != "a" {
		t.Fatalf("cloneAny mangled an array: %#v", arrayCopy)
	}
}

// TestCloneStringMapAndAnyMapHandleNil pins that a nil map stays nil rather
// than becoming an empty one, so a round trip through the cache does not
// change what a rule says.
func TestCloneStringMapAndAnyMapHandleNil(t *testing.T) {
	if cloneStringMap(nil) != nil {
		t.Fatal("cloneStringMap(nil) allocated a map")
	}
	if cloneAnyMap(nil) != nil {
		t.Fatal("cloneAnyMap(nil) allocated a map")
	}
	strings := cloneStringMap(map[string]string{"a": "b"})
	strings["a"] = "c"
	if strings["a"] != "c" || len(strings) != 1 {
		t.Fatalf("cloneStringMap = %#v", strings)
	}
}

// TestNilReceiversAnswerRatherThanPanic pins that every read path tolerates an
// unpublished cache. A service that evaluates before its first load must get a
// fail-closed answer, not a panic that takes the process down.
func TestNilReceiversAnswerRatherThanPanic(t *testing.T) {
	var nilCache *Cache
	if nilCache.Load() != nil || nilCache.StaleSeconds() != 0 {
		t.Fatal("a nil cache did not answer empty")
	}
	if err := nilCache.Replace(Bundle{}); err == nil {
		t.Fatal("a nil cache accepted a bundle")
	}

	empty := NewCache()
	if empty.Load() != nil {
		t.Fatal("a new cache is not empty")
	}
	if got := empty.StaleSeconds(); got != 0 {
		t.Fatalf("an unpublished cache reported %v seconds stale", got)
	}
	key := rule.RuleKey("urn:arrowhr:rule:leave:eligibility:alpha:v1")
	if empty.Candidates("T1", "leave", "eligibility", "alpha", january) != nil {
		t.Fatal("an empty cache returned candidates")
	}
	if empty.Versions("T1", key) != nil || empty.Version("T1", key, "1.0.0") != nil {
		t.Fatal("an empty cache returned versions")
	}
	if empty.Registry() != nil || empty.Hierarchy() != nil {
		t.Fatal("an empty cache returned a registry or hierarchy")
	}
	if _, found := empty.Manifest("leave.items"); found {
		t.Fatal("an empty cache found a manifest")
	}
	if got := empty.Manifests(); len(got) != 0 {
		t.Fatalf("an empty cache returned %d manifests", len(got))
	}
	if got := empty.Stats(); got.Rules != 0 || !got.LoadedAt.IsZero() {
		t.Fatalf("an empty cache reported %#v", got)
	}

	// The same for a nil snapshot reached directly.
	var nilSnapshot *Snapshot
	if nilSnapshot.Candidates("T1", "leave", "eligibility", "alpha", january) != nil ||
		nilSnapshot.Versions("T1", key) != nil || nilSnapshot.Version("T1", key, "1.0.0") != nil ||
		nilSnapshot.Registry() != nil || nilSnapshot.Hierarchy() != nil ||
		len(nilSnapshot.Manifests()) != 0 {
		t.Fatal("a nil snapshot did not answer empty")
	}
	if _, found := nilSnapshot.Manifest("x"); found {
		t.Fatal("a nil snapshot found a manifest")
	}
	if got := nilSnapshot.Stats(); got != (Stats{}) {
		t.Fatalf("a nil snapshot reported %#v", got)
	}
}

// TestCandidatesRejectIncompleteLookups pins that a partial catalog key is
// refused rather than widened into a scan.
func TestCandidatesRejectIncompleteLookups(t *testing.T) {
	cache := selectionCache(t, anchoredRule("1.0.0", "G001", january))
	at := january.Add(24 * time.Hour)
	tests := []struct {
		name                             string
		tenant, domain, ruleSet, ruleNam string
		at                               time.Time
	}{
		{"no tenant", "", "leave", "eligibility", "alpha", at},
		{"no domain", "T1", "", "eligibility", "alpha", at},
		{"no rule set", "T1", "leave", "", "alpha", at},
		{"no name", "T1", "leave", "eligibility", "", at},
		{"no business time", "T1", "leave", "eligibility", "alpha", time.Time{}},
		{"unknown bucket", "T1", "leave", "eligibility", "missing", at},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := cache.Candidates(test.tenant, test.domain, test.ruleSet, test.ruleNam, test.at); got != nil {
				t.Fatalf("Candidates = %s, want none", versionsOf(got))
			}
		})
	}
}

// TestReloadPublishesAndKeepsTheOldSnapshotOnFailure covers the full-bundle
// path, as distinct from the changes-since path.
func TestReloadPublishesAndKeepsTheOldSnapshotOnFailure(t *testing.T) {
	cache := NewCache()
	loader := &fullLoader{bundle: Bundle{
		Rules:     []rule.RuleDefinition{anchoredRule("1.0.0", "G001", january)},
		Hierarchy: twoLevelHierarchy(), LoadedAt: january,
	}}
	if err := cache.Reload(context.Background(), loader); err != nil {
		t.Fatal(err)
	}
	published := cache.Load()
	if published == nil || cache.Stats().Rules != 1 {
		t.Fatalf("Reload did not publish: %#v", cache.Stats())
	}

	loader.err = errors.New("service unavailable")
	if err := cache.Reload(context.Background(), loader); err == nil {
		t.Fatal("a failed Reload was hidden")
	}
	if cache.Load() != published {
		t.Fatal("a failed Reload replaced the live snapshot")
	}

	// An invalid bundle is refused by validation, with the same guarantee.
	loader.err = nil
	loader.bundle.Rules = []rule.RuleDefinition{anchoredRule("not-semver", "G001", january)}
	if err := cache.Reload(context.Background(), loader); err == nil {
		t.Fatal("an invalid bundle was published")
	}
	if cache.Load() != published {
		t.Fatal("an invalid bundle replaced the live snapshot")
	}

	if err := cache.Reload(context.Background(), nil); err == nil {
		t.Fatal("a nil loader was accepted")
	}
	if err := cache.Reconcile(context.Background(), nil); err == nil {
		t.Fatal("a nil changes loader was accepted")
	}
}

// TestWithMetricsReportsSizeAndStaleness pins that the cache reports what an
// operator needs: how much it holds, and how long since it last succeeded.
// Staleness is the signal that a service is deciding on old rules.
func TestWithMetricsReportsSizeAndStaleness(t *testing.T) {
	recorder := &recordingMetrics{observed: map[string][]float64{}}
	clock := &mutableClock{value: january}
	cache := NewCache(WithMetrics(recorder), WithClock(clock))

	if err := cache.Replace(Bundle{
		Rules:     []rule.RuleDefinition{anchoredRule("1.0.0", "G001", january)},
		Hierarchy: twoLevelHierarchy(),
	}); err != nil {
		t.Fatal(err)
	}
	if got := recorder.observed[telemetry.MetricCacheEntries]; len(got) != 4 {
		t.Fatalf("cache entries observed %v, want one per kind", got)
	}
	if got := recorder.observed[telemetry.MetricCacheStaleSeconds]; len(got) != 1 || got[0] != 0 {
		t.Fatalf("staleness after a successful publish = %v, want a single 0", got)
	}

	// A failure reports how stale the live snapshot has become.
	clock.value = january.Add(90 * time.Second)
	if err := cache.Replace(Bundle{Rules: []rule.RuleDefinition{anchoredRule("bad", "G001", january)}}); err == nil {
		t.Fatal("an invalid bundle was published")
	}
	staleness := recorder.observed[telemetry.MetricCacheStaleSeconds]
	if len(staleness) != 2 || staleness[1] != 90 {
		t.Fatalf("staleness after a failure = %v, want 90 seconds", staleness)
	}

	// Nil options are ignored rather than replacing the defaults with nil.
	if NewCache(WithMetrics(nil), WithClock(nil)) == nil {
		t.Fatal("nil options broke construction")
	}
}

// TestFactPinBoundaries covers the fact-pin path's own guards, which enforce
// the same tenant boundary as rule selection.
func TestFactPinBoundaries(t *testing.T) {
	cache := selectionCache(t, anchoredRule("1.0.0", "G001", january))
	resolver := NewVersionResolver(cache, nil)

	if _, _, err := resolver.FactPin(context.Background(), "T1", "", "balance"); err == nil {
		t.Fatal("FactPin accepted an empty run")
	}
	if _, _, err := resolver.FactPin(context.Background(), "T1", "run-1", ""); err == nil {
		t.Fatal("FactPin accepted an empty field")
	}
	// With no manifest store, a run cannot be consulted at all.
	if _, _, err := resolver.FactPin(context.Background(), "T1", "run-1", "balance"); !errors.Is(err, ErrRunManifestUnavailable) {
		t.Fatalf("FactPin without a store = %v, want ErrRunManifestUnavailable", err)
	}
}

// TestResolveRejectsMalformedRequests pins the request guards, so a caller
// cannot get a silent empty answer from a bad request.
func TestResolveRejectsMalformedRequests(t *testing.T) {
	cache := selectionCache(t, anchoredRule("1.0.0", "G001", january))
	resolver := NewVersionResolver(cache, nil)
	key := rule.RuleKey("urn:arrowhr:rule:leave:eligibility:alpha:v1")

	tests := []struct {
		name    string
		request VersionRequest
	}{
		{"no tenant", VersionRequest{RuleKey: key, EffectiveAt: january}},
		{"no rule key", VersionRequest{Tenant: "T1", EffectiveAt: january}},
		{"unparseable rule key", VersionRequest{Tenant: "T1", RuleKey: "not-a-urn", EffectiveAt: january}},
		{"no pin and no business time", VersionRequest{Tenant: "T1", RuleKey: key}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := resolver.Resolve(context.Background(), test.request); err == nil {
				t.Fatal("Resolve accepted a malformed request")
			}
		})
	}

	// An unpublished cache is its own failure, distinct from a missing rule.
	if _, err := NewVersionResolver(NewCache(), nil).Resolve(context.Background(), VersionRequest{
		Tenant: "T1", RuleKey: key, EffectiveAt: january,
	}); !errors.Is(err, ErrCacheUnavailable) {
		t.Fatalf("Resolve on an empty cache = %v, want ErrCacheUnavailable", err)
	}
	var nilResolver *VersionResolver
	if _, err := nilResolver.Resolve(context.Background(), VersionRequest{Tenant: "T1", RuleKey: key}); !errors.Is(err, ErrCacheUnavailable) {
		t.Fatalf("a nil resolver = %v, want ErrCacheUnavailable", err)
	}
}

// TestSemanticVersionComparison covers the ordering the catalog depends on.
func TestSemanticVersionComparison(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{
		{"1.0.0", "1.0.0", 0},
		{"2.0.0", "1.9.9", 1},
		{"1.10.0", "1.9.0", 1},
		{"1.0.10", "1.0.9", 1},
		{"1.0.0", "2.0.0", -1},
		{"1.0.0", "1.0.0-rc1", 1},
		{"1.0.0-rc1", "1.0.0", -1},
		{"1.0.0-rc2", "1.0.0-rc1", 1},
		{"1.0.0-rc1", "1.0.0-rc1", 0},
	}
	for _, test := range tests {
		left, err := parseSemanticVersion(test.left)
		if err != nil {
			t.Fatal(err)
		}
		right, err := parseSemanticVersion(test.right)
		if err != nil {
			t.Fatal(err)
		}
		if got := compareSemanticVersions(left, right); got != test.want {
			t.Errorf("compare(%s, %s) = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

// --- doubles ---

type fullLoader struct {
	bundle Bundle
	err    error
}

func (l *fullLoader) LoadAll(context.Context) (Bundle, error) {
	if l.err != nil {
		return Bundle{}, l.err
	}
	return l.bundle, nil
}

type recordingMetrics struct {
	observed map[string][]float64
}

func (m *recordingMetrics) Counter(string, ...telemetry.Label) telemetry.Counter {
	return nopCounter{}
}

func (m *recordingMetrics) Histogram(name string, _ ...telemetry.Label) telemetry.Histogram {
	return &recordingHistogram{metrics: m, name: name}
}

type recordingHistogram struct {
	metrics *recordingMetrics
	name    string
}

func (h *recordingHistogram) Observe(value float64) {
	h.metrics.observed[h.name] = append(h.metrics.observed[h.name], value)
}

type nopCounter struct{}

func (nopCounter) Inc()        {}
func (nopCounter) Add(float64) {}
