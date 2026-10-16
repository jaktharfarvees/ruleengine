package decisiontable

import (
	"context"
	"fmt"
	"sort"
	"sync"

	baseengine "github.com/arrowhr/ruleengine/engine"
	"github.com/arrowhr/ruleengine/internal/expr"
	"github.com/arrowhr/ruleengine/rule"
)

// Warning reports a valid but suspicious table discovered during compilation.
type Warning struct {
	Code    string `json:"code"`
	RowID   string `json:"rowId"`
	Message string `json:"message"`
}

type compiledRow struct {
	id         string
	priority   int
	conditions []compiledCondition
	outputs    map[string]compiledOutput
}

// Table is an immutable compiled decision table safe for concurrent use.
type Table struct {
	hitPolicy rule.HitPolicy
	rows      []compiledRow
}

type cacheEntry struct {
	once     sync.Once
	table    *Table
	warnings []Warning
	err      error
}

// Engine caches immutable compiled tables and evaluates them without I/O.
type Engine struct{ cache sync.Map }

// New constructs a decision-table engine.
func New() *Engine { return &Engine{} }

// Name returns the stable implementation name.
func (*Engine) Name() string { return "decisiontable" }

// Capabilities derives the advertised features from the implemented grammar
// and operator/policy tables.
func (*Engine) Capabilities() baseengine.Capabilities {
	return baseengine.Capabilities{
		HitPolicies: hitPolicies(),
		Operators:   conditionOperatorNames(),
		FieldRefs:   true,
		Arithmetic:  len(expr.OperatorSymbols()) > 0,
		Trace:       true,
		Decimal:     true,
	}
}

// Compile parses and validates a table once and returns any non-fatal load
// warnings. An empty catch-all row makes every following row unreachable.
func Compile(logic rule.RuleLogic) (*Table, []Warning, error) {
	if _, supported := hitPolicyTable[logic.HitPolicy]; !supported {
		return nil, nil, fmt.Errorf("unsupported hit policy %q", logic.HitPolicy)
	}
	if len(logic.Rows) == 0 {
		return nil, nil, fmt.Errorf("decision table has no rows")
	}
	table := &Table{hitPolicy: logic.HitPolicy, rows: make([]compiledRow, 0, len(logic.Rows))}
	seenIDs := make(map[string]struct{}, len(logic.Rows))
	catchAllSeen := false
	var warnings []Warning
	for rowIndex, authored := range logic.Rows {
		if authored.ID == "" {
			return nil, nil, fmt.Errorf("row %d has an empty id", rowIndex)
		}
		if _, duplicate := seenIDs[authored.ID]; duplicate {
			return nil, nil, fmt.Errorf("row %q has a duplicate id", authored.ID)
		}
		seenIDs[authored.ID] = struct{}{}
		if len(authored.Then) == 0 {
			return nil, nil, fmt.Errorf("row %q has no outputs", authored.ID)
		}
		if catchAllSeen && logic.HitPolicy == rule.HitFirst {
			warnings = append(warnings, Warning{Code: "unreachable-row", RowID: authored.ID, Message: "row follows an unconditional catch-all row"})
		}
		if len(authored.When) == 0 && logic.HitPolicy == rule.HitFirst {
			catchAllSeen = true
		}
		row := compiledRow{id: authored.ID, priority: authored.Priority, outputs: make(map[string]compiledOutput, len(authored.Then))}
		fields := make([]string, 0, len(authored.When))
		for field := range authored.When {
			fields = append(fields, field)
		}
		sort.Strings(fields)
		for _, field := range fields {
			if field == "" {
				return nil, nil, fmt.Errorf("row %q has an empty condition field", authored.ID)
			}
			condition, err := parseCondition(field, authored.When[field])
			if err != nil {
				return nil, nil, fmt.Errorf("row %q condition %q: %w", authored.ID, field, err)
			}
			row.conditions = append(row.conditions, condition)
		}
		for name, value := range authored.Then {
			if name == "" {
				return nil, nil, fmt.Errorf("row %q has an empty output name", authored.ID)
			}
			output, err := compileOutput(value)
			if err != nil {
				return nil, nil, fmt.Errorf("row %q output %q: %w", authored.ID, name, err)
			}
			row.outputs[name] = output
		}
		table.rows = append(table.rows, row)
	}
	return table, warnings, nil
}

// Evaluate compiles each distinct authored table once per Engine and evaluates
// the cached immutable representation.
func (e *Engine) Evaluate(ctx context.Context, logic rule.RuleLogic, input baseengine.Input) (baseengine.Output, error) {
	loaded, _ := e.cache.LoadOrStore(logicFingerprint(logic), &cacheEntry{})
	entry := loaded.(*cacheEntry)
	entry.once.Do(func() { entry.table, entry.warnings, entry.err = Compile(logic) })
	if entry.err != nil {
		return baseengine.Output{}, entry.err
	}
	return entry.table.Evaluate(ctx, input)
}

// Evaluate evaluates an already compiled table.
func (t *Table) Evaluate(ctx context.Context, input baseengine.Input) (baseengine.Output, error) {
	if t == nil {
		return baseengine.Output{}, fmt.Errorf("decision table is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	output := baseengine.Output{Result: make(map[string]any)}
	if input.Trace {
		output.Trace = &baseengine.Trace{Rows: make([]baseengine.RowTrace, 0, len(t.rows))}
	}
	matched := make([]int, 0, len(t.rows))
	for rowIndex, row := range t.rows {
		if err := ctx.Err(); err != nil {
			return baseengine.Output{}, err
		}
		rowMatches, rowTrace, err := evaluateRow(row, input.Facts, input.Trace)
		if err != nil {
			return baseengine.Output{}, fmt.Errorf("row %q: %w", row.id, err)
		}
		if output.Trace != nil {
			output.Trace.Rows = append(output.Trace.Rows, rowTrace)
		}
		if !rowMatches {
			continue
		}
		matched = append(matched, rowIndex)
		if t.hitPolicy == rule.HitFirst {
			break
		}
	}
	if len(matched) == 0 {
		return output, nil
	}
	switch t.hitPolicy {
	case rule.HitFirst:
		row := t.rows[matched[0]]
		result, err := resolveOutputs(row.outputs, input.Facts)
		if err != nil {
			return baseengine.Output{}, fmt.Errorf("row %q: %w", row.id, err)
		}
		output.Result, output.MatchedRows = result, []string{row.id}
	case rule.HitPriority:
		winner := matched[0]
		for _, rowIndex := range matched[1:] {
			if t.rows[rowIndex].priority > t.rows[winner].priority {
				winner = rowIndex
			}
		}
		row := t.rows[winner]
		result, err := resolveOutputs(row.outputs, input.Facts)
		if err != nil {
			return baseengine.Output{}, fmt.Errorf("row %q: %w", row.id, err)
		}
		output.Result, output.MatchedRows = result, []string{row.id}
	case rule.HitCollect:
		for _, rowIndex := range matched {
			row := t.rows[rowIndex]
			result, err := resolveOutputs(row.outputs, input.Facts)
			if err != nil {
				return baseengine.Output{}, fmt.Errorf("row %q: %w", row.id, err)
			}
			for key, value := range result {
				output.Result[key] = value
			}
			output.MatchedRows = append(output.MatchedRows, row.id)
		}
	}
	return output, nil
}

func evaluateRow(row compiledRow, facts map[string]any, tracing bool) (bool, baseengine.RowTrace, error) {
	rowTrace := baseengine.RowTrace{RowID: row.id, Matched: true}
	if tracing {
		rowTrace.Conditions = make([]baseengine.ConditionTrace, 0, len(row.conditions))
	}
	for _, condition := range row.conditions {
		actual, exists := facts[condition.field]
		if !exists {
			return false, rowTrace, fmt.Errorf("fact %q is unavailable", condition.field)
		}
		conditionMatches, err := condition.evaluate(actual, facts)
		if err != nil {
			return false, rowTrace, fmt.Errorf("condition %q: %w", condition.field, err)
		}
		if tracing {
			rowTrace.Conditions = append(rowTrace.Conditions, newConditionTrace(condition.field, condition.source, actual, conditionMatches))
		}
		if !conditionMatches {
			rowTrace.Matched = false
		}
	}
	return rowTrace.Matched, rowTrace, nil
}
