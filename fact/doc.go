// Package fact resolves a rule's declared inputs into values.
//
// A field declares where its value comes from, in one of four kinds: passed in
// the request, derived from other fields, read from a published dataset, or
// computed by a function the owning service registers. The declaration itself
// lives with the field contract in the registry package and is aliased here;
// this package is the layer that acts on it. See
// docs/adr/0009-fact-source-declaration-lives-with-the-field.md.
//
// This is the only part of the library that performs I/O, and every fetch is
// bounded by a budget and a timeout. Exceeding either fails the whole decision
// closed: there is never a partial fact set, because a missing fact silently
// becomes a different decision, and a held decision is recoverable where a
// wrong payroll figure is not.
//
// Facts are resolved once per request rather than once per decision point. A
// service evaluating four checks for one employee shares the facts between
// them, so a fact three checks need is fetched once. That is required, not an
// optimisation: the budget is a request budget, and asking the same question
// three times risks three answers.
package fact
