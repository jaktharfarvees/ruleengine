# 11. Run manifests are local on the evaluation hot path

Date: 2026-10-19

## Status

Accepted.

## Context

Option 2 requires decisions to continue from local state while the Rule
Service is unavailable. The original `manifest.Store` accepted a context and
returned an infrastructure error, deliberately allowing an implementation to
call the Rule Service from `Evaluate`. That contradicted the no-network-hop
architecture and made a large payroll run depend on control-plane latency and
availability. A decision also looked up the same manifest for hierarchy pins,
rule pins, and each fact-source pin.

## Decision

`manifest.Reader` is a process-local, non-blocking lookup with no context or
error return. The engine integration accepts `*manifest.InMemoryStore`, making
the local boundary explicit. Services obtain or receive a frozen manifest in
their control path and call `Put` before evaluating the run.

An evaluation session reads the manifest once and retains that immutable view
for hierarchy, rule-version, and fact-source selection. `EvaluateAll` shares
the same view across all decision points. Missing local data fails closed as
`PINNED_VERSION_UNAVAILABLE`; synchronization failures are handled and
observed outside the decision path.

## Consequences

- Rule Service or network failure cannot interrupt an already synchronized
  payroll run.
- Evaluation performs no manifest network or database I/O.
- One request cannot observe different manifest state between decision points.
- Services must synchronize the manifest before admitting work for its run.
- The in-memory store continues returning defensive copies, so callers cannot
  mutate frozen pins.
