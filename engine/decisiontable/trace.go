package decisiontable

import "github.com/arrowhr/ruleengine/engine"

func newConditionTrace(field, source string, actual any, matched bool) engine.ConditionTrace {
	return engine.ConditionTrace{Field: field, Expr: source, Actual: clone(actual), Matched: matched}
}
