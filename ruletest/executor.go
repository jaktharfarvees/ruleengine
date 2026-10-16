package ruletest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/arrowhr/ruleengine/dataset"
	"github.com/arrowhr/ruleengine/internal/decimalx"
	"github.com/arrowhr/ruleengine/registry"
	"github.com/arrowhr/ruleengine/rule"
)

// Row is one synthetic dataset row: column name to value.
type Row map[string]any

// InMemoryExecutor runs compiled queries over rows held in memory.
//
// It exists so a service team can test its fields, rules and decision points
// before writing a real executor, and so the library's own fact tests exercise
// the same code path a service will. It is a reference implementation of the
// Executor contract, not a storage engine: it scans every row.
//
// It is deliberately strict about the contract it is demonstrating. It refuses
// a query carrying no injected tenant predicate rather than assuming the
// library added one, honours cancellation, applies the row limit, and reports
// rows scanned honestly.
type InMemoryExecutor struct {
	mutex     sync.RWMutex
	manifests map[string]dataset.Manifest
	rows      map[string][]Row
	// delay simulates a slow store, for testing timeout behaviour.
	delay time.Duration
}

// NewInMemoryExecutor builds an executor serving the given manifests.
func NewInMemoryExecutor(manifests ...dataset.Manifest) (*InMemoryExecutor, error) {
	executor := &InMemoryExecutor{
		manifests: make(map[string]dataset.Manifest, len(manifests)),
		rows:      make(map[string][]Row, len(manifests)),
	}
	for _, manifest := range manifests {
		if err := manifest.Validate(); err != nil {
			return nil, fmt.Errorf("manifest %q: %w", manifest.Dataset, err)
		}
		executor.manifests[manifest.Dataset] = manifest
	}
	return executor, nil
}

// Add appends rows to a dataset. Every row must carry the tenant column, since
// a real store's rows do and the tenant predicate is what filters them.
func (e *InMemoryExecutor) Add(datasetName string, rows ...Row) error {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	if _, served := e.manifests[datasetName]; !served {
		return fmt.Errorf("executor does not serve dataset %q", datasetName)
	}
	for index, row := range rows {
		if _, scoped := row[dataset.TenantColumn]; !scoped {
			return fmt.Errorf("dataset %q row %d carries no %q", datasetName, index, dataset.TenantColumn)
		}
		e.rows[datasetName] = append(e.rows[datasetName], row)
	}
	return nil
}

// SetDelay makes every Run sleep before answering, so a test can exercise the
// per-fact timeout against a real executor rather than a stub.
func (e *InMemoryExecutor) SetDelay(delay time.Duration) {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	e.delay = delay
}

// Manifests returns the datasets this executor serves, ordered by name.
func (e *InMemoryExecutor) Manifests() []dataset.Manifest {
	e.mutex.RLock()
	defer e.mutex.RUnlock()
	names := make([]string, 0, len(e.manifests))
	for name := range e.manifests {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]dataset.Manifest, 0, len(names))
	for _, name := range names {
		out = append(out, e.manifests[name])
	}
	return out
}

// Run executes a compiled query.
func (e *InMemoryExecutor) Run(ctx context.Context, query dataset.Query) (dataset.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return dataset.Result{}, err
	}

	// The contract every executor owes the library: assert the tenant
	// predicate is present rather than trust that it is. A query without one
	// would return every tenant's rows.
	tenant, scoped := query.TenantPredicate()
	if !scoped || len(tenant.Values) != 1 {
		return dataset.Result{}, fmt.Errorf("query for dataset %q carries no injected tenant predicate", query.Dataset())
	}

	e.mutex.RLock()
	manifest, served := e.manifests[query.Dataset()]
	rows := e.rows[query.Dataset()]
	delay := e.delay
	e.mutex.RUnlock()
	if !served {
		return dataset.Result{}, fmt.Errorf("executor does not serve dataset %q", query.Dataset())
	}

	if delay > 0 {
		select {
		case <-ctx.Done():
			return dataset.Result{}, ctx.Err()
		case <-time.After(delay):
		}
	}

	window, windowed := query.Window()
	matched := make([]Row, 0, len(rows))
	scanned := 0
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return dataset.Result{}, err
		}
		// A real store counts a row as scanned when it reads it, whether or
		// not it matches, which is what the row limit is protecting.
		scanned++
		if scanned > query.RowLimit() {
			return dataset.Result{}, fmt.Errorf("dataset %q holds more rows than the limit of %d",
				query.Dataset(), query.RowLimit())
		}
		keep := true
		for _, predicate := range query.Predicates() {
			ok, err := rowMatches(row, predicate)
			if err != nil {
				return dataset.Result{}, err
			}
			if !ok {
				keep = false
				break
			}
		}
		if keep && windowed {
			inside, err := insideWindow(row, window)
			if err != nil {
				return dataset.Result{}, err
			}
			keep = inside
		}
		if keep {
			matched = append(matched, row)
		}
	}

	// Ordering is the executor's job, and a streak or a latest is meaningless
	// without it. Most recent first, by the window column when there is one.
	if windowed {
		if err := sortByColumn(matched, window.Column); err != nil {
			return dataset.Result{}, err
		}
	}

	value, err := aggregate(query.Aggregate(), matched, manifest)
	if err != nil {
		return dataset.Result{}, err
	}
	return dataset.Result{Value: value, RowsScanned: scanned}, nil
}

func rowMatches(row Row, predicate dataset.Predicate) (bool, error) {
	// The tenant predicate is applied like any other: it filters on a column
	// the rows carry, which is the whole point of injecting it.
	actual, present := row[predicate.Column]
	switch predicate.Op {
	case rule.FilterExists:
		return present, nil
	case rule.FilterNotExists:
		return !present, nil
	}
	if !present {
		return false, nil
	}
	switch predicate.Op {
	case rule.FilterEquals:
		return sameValue(actual, predicate.Values[0]), nil
	case rule.FilterIn, rule.FilterNotIn:
		found := false
		for _, want := range predicate.Values {
			if sameValue(actual, want) {
				found = true
				break
			}
		}
		if predicate.Op == rule.FilterNotIn {
			return !found, nil
		}
		return found, nil
	case rule.FilterRange:
		low, lowOK := compare(actual, predicate.Values[0])
		high, highOK := compare(actual, predicate.Values[1])
		return lowOK && highOK && low >= 0 && high <= 0, nil
	default:
		return false, fmt.Errorf("executor cannot apply operator %q", predicate.Op)
	}
}

func insideWindow(row Row, window dataset.WindowSpec) (bool, error) {
	moment, err := rowTime(row, window.Column)
	if err != nil {
		return false, err
	}
	// From is inclusive and To is exclusive, so a day belongs to exactly one
	// window however a store stores its timestamps.
	return !moment.Before(window.From) && moment.Before(window.To), nil
}

func rowTime(row Row, column string) (time.Time, error) {
	value, present := row[column]
	if !present {
		return time.Time{}, fmt.Errorf("row carries no %q to window on", column)
	}
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), nil
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.DateOnly} {
			if parsed, err := time.Parse(layout, typed); err == nil {
				return parsed.UTC(), nil
			}
		}
		return time.Time{}, fmt.Errorf("column %q is not a usable date", column)
	default:
		return time.Time{}, fmt.Errorf("column %q is not a date", column)
	}
}

func sortByColumn(rows []Row, column string) error {
	var failure error
	sort.SliceStable(rows, func(left, right int) bool {
		leftTime, leftErr := rowTime(rows[left], column)
		rightTime, rightErr := rowTime(rows[right], column)
		if leftErr != nil && failure == nil {
			failure = leftErr
		}
		if rightErr != nil && failure == nil {
			failure = rightErr
		}
		return leftTime.After(rightTime)
	})
	return failure
}

// aggregate applies the operation to the matched rows, which arrive most
// recent first.
func aggregate(spec dataset.AggregateSpec, rows []Row, manifest dataset.Manifest) (any, error) {
	switch spec.Fn {
	case dataset.AggregateCount:
		return len(rows), nil
	case dataset.AggregateExists:
		return len(rows) > 0, nil
	case dataset.AggregateStreak:
		// The streak is the run of most-recent rows satisfying the column,
		// stopping at the first that does not. A subject whose latest day is
		// not late has a streak of zero, which is the case FT01 pins.
		count := 0
		for _, row := range rows {
			if !truthy(row[spec.On]) {
				break
			}
			count++
		}
		return count, nil
	case dataset.AggregateLatest:
		if len(rows) == 0 {
			return nil, nil
		}
		return rows[0][spec.On], nil
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return numericAggregate(spec, rows, manifest)
}

func numericAggregate(spec dataset.AggregateSpec, rows []Row, manifest dataset.Manifest) (any, error) {
	column, _ := manifest.Column(spec.On)
	total := decimalx.Decimal{}
	var extreme decimalx.Decimal
	counted := 0
	for _, row := range rows {
		value, present := row[spec.On]
		if !present {
			continue
		}
		number, ok := decimalx.FromValue(value)
		if !ok {
			return nil, fmt.Errorf("column %q holds a value the %q aggregate cannot use", spec.On, spec.Fn)
		}
		if counted == 0 || (spec.Fn == dataset.AggregateMax && number.Cmp(extreme) > 0) ||
			(spec.Fn == dataset.AggregateMin && number.Cmp(extreme) < 0) {
			extreme = number
		}
		total = total.Add(number)
		counted++
	}
	if counted == 0 {
		return nil, nil
	}
	switch spec.Fn {
	case dataset.AggregateSum:
		return quantize(total, column), nil
	case dataset.AggregateMax, dataset.AggregateMin:
		return quantize(extreme, column), nil
	case dataset.AggregateAvg:
		// Money and rates are exact, so an average is computed at a declared
		// scale rather than through binary floating point.
		mean, err := total.Div(decimalx.FromInt(int64(counted)), 6)
		if err != nil {
			return nil, fmt.Errorf("averaging column %q: %w", spec.On, err)
		}
		return mean, nil
	default:
		return nil, fmt.Errorf("executor cannot apply aggregate %q", spec.Fn)
	}
}

// quantize keeps an integer column integral, so a sum over an integer column
// type-checks as an integer rather than as a decimal that happens to be whole.
func quantize(value decimalx.Decimal, column dataset.Column) any {
	if column.Type == registry.DataInteger {
		return value.Quantize(0)
	}
	return value
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != "" && typed != "false"
	default:
		if number, ok := decimalx.FromValue(value); ok {
			return number.Sign() != 0
		}
		return true
	}
}

func sameValue(left, right any) bool {
	if comparison, ok := compare(left, right); ok {
		return comparison == 0
	}
	leftText, leftIsText := left.(string)
	rightText, rightIsText := right.(string)
	return leftIsText && rightIsText && leftText == rightText
}

func compare(left, right any) (int, bool) {
	if leftNumber, ok := decimalx.FromValue(left); ok {
		if rightNumber, rightOK := decimalx.FromValue(right); rightOK {
			return leftNumber.Cmp(rightNumber), true
		}
		return 0, false
	}
	leftTime, leftIsTime := asTime(left)
	rightTime, rightIsTime := asTime(right)
	if leftIsTime && rightIsTime {
		switch {
		case leftTime.Before(rightTime):
			return -1, true
		case leftTime.After(rightTime):
			return 1, true
		default:
			return 0, true
		}
	}
	leftBool, leftIsBool := left.(bool)
	rightBool, rightIsBool := right.(bool)
	if leftIsBool && rightIsBool {
		if leftBool == rightBool {
			return 0, true
		}
		return -1, false
	}
	leftText, leftIsText := left.(string)
	rightText, rightIsText := right.(string)
	if leftIsText && rightIsText {
		switch {
		case leftText < rightText:
			return -1, true
		case leftText > rightText:
			return 1, true
		default:
			return 0, true
		}
	}
	return 0, false
}

func asTime(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC(), true
	case string:
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.DateOnly} {
			if parsed, err := time.Parse(layout, typed); err == nil {
				return parsed.UTC(), true
			}
		}
	}
	return time.Time{}, false
}
