package fact

import "github.com/arrowhr/ruleengine/registry"

// The declaration types are defined in the registry package, with the field
// contract they belong to, so that this package can import both registry and
// dataset to resolve a field without creating an import cycle. They are
// aliased here because a caller reads and writes them as fact concepts:
// fact.FactSource and registry.FactSource are the same type, not two.
// See docs/adr/0009-fact-source-declaration-lives-with-the-field.md.
type (
	// Kind identifies how a fact value is obtained.
	Kind = registry.FactKind
	// FactSource declares how a registered field is resolved.
	FactSource = registry.FactSource
	// Window describes a bounded fact-source time window.
	Window = registry.FactWindow
	// Aggregate describes the operation applied to a dataset fact source.
	Aggregate = registry.FactAggregate
	// AggregateFn identifies a closed dataset aggregation operation.
	AggregateFn = registry.AggregateFn
)

// The four fact-source kinds. A field declares exactly one.
const (
	// KindPassed arrives in the request context; nothing is fetched.
	KindPassed = registry.FactPassed
	// KindDerived is a formula over other already-resolved fields.
	KindDerived = registry.FactDerived
	// KindDataset is a compiled query over a published dataset.
	KindDataset = registry.FactDataset
	// KindNative is a Go function the owning service registers.
	KindNative = registry.FactNative
)
