// Package telemetry declares the metric and trace interfaces the library emits
// through, together with no-op implementations.
//
// The library takes no metrics or tracing dependency: a consumer adapts these
// interfaces to its own vendor in a few lines. A consumer that supplies nothing
// pays nothing, because the no-op implementations allocate on no path.
package telemetry
