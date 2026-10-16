# 1. Record architecture decisions

Date: 2026-10-15

## Status

Accepted.

## Context

This library decides leave, attendance and payroll outcomes for real employees
in several countries. Fourteen services will depend on it and will upgrade on
their own cadence. Decisions taken here — how precedence is derived, where
guardrails live, what fails closed — are re-litigated repeatedly unless the
reasoning is written down next to the code it shapes.

The build specification also instructs that where a detail is unspecified, the
option most consistent with the architectural invariants is implemented and the
choice recorded as an ADR. That instruction needs somewhere to record it.

## Decision

Significant decisions are recorded as Architecture Decision Records in
`docs/adr/`, numbered sequentially, in Nygard format: Title, Date, Status,
Context, Decision, Consequences.

An ADR is written when a decision constrains the public API, changes an
evaluation outcome, selects or removes a dependency, or resolves an ambiguity
in the build specification. ADRs are immutable once accepted: a reversal is a
new ADR that supersedes the old one, and the old one is marked as superseded
rather than edited away.

## Consequences

A reviewer can see why the code is the way it is without reconstructing it from
the diff. A decision that turns out to be wrong leaves a visible trail, which is
what makes it safe to change. The cost is a file per decision; the numbering
gaps that arise when records are planned before they are written are preferred
over renumbering, because ADR numbers are cited from code comments and reviews.
