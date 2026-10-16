// Package dataset compiles a declared fact source into a validated, structured
// query that an owning service executes against its own storage.
//
// The library compiles and validates; the service only executes. Every
// guardrail — dataset published, no query syntax in author strings, columns
// permitted by the manifest, window within the dataset maximum, aggregate
// offered by the dataset, filter values typed, only the four substitution
// tokens honoured, the tenant predicate injected, and a row limit always set —
// is applied here, once, so that all fourteen services inherit the same checks
// and no service team can accidentally omit one.
//
// A Query therefore cannot be usefully constructed outside Compile: its fields
// are unexported and it exposes read accessors only. There is no SQL in this
// package and no string that becomes SQL; translating a Query into a storage
// dialect is the Executor's job. See
// docs/adr/0003-library-compiles-service-executes.md.
package dataset
