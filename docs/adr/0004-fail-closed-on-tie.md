# 4. An unresolved precedence tie fails closed

Date: 2026-10-15

## Status

Accepted. Implements locked decision D20.

## Context

Two active rules can end up equally ranked: anchored at the same node, equally
specific in their filters, with no explicit tie-break priority. The RT07 fixture
pair is exactly this — one filtered on `employeeGroup=Finance`, one on
`workerCategory=PERMANENT`, for a subject matching both.

An engine has three options: pick one by some incidental order, substitute a
default, or refuse. Picking one makes the decision depend on map iteration or
file order, so the same employee can get different answers on different
replicas, and a payroll replay months later cannot be reproduced. Substituting a
default silently invents a policy nobody authored, in a domain where the output
is somebody's pay.

## Decision

Resolution never guesses. `resolve.Resolve` returns a `Result` with `Tie` set,
`FailClosed` true and reason code `PRECEDENCE_TIE_UNRESOLVED`, listing every
candidate that remained tied. The pipeline turns that into a `Decision` with
`Failed` true carrying the same reason code and the `Evaluated` list built so
far, and increments a counter that is intended to alert.

Before giving up, the algorithm applies every documented tie-break in order:
anchor depth, hierarchy type order, filter count, and then `TieBreakPriority`
when every remaining candidate declares one. A tie is therefore the genuine
case, not a missing comparison. Two employee anchors matching the same subject
are also a tie, because that is a data error rather than a policy.

## Consequences

A tie is a held decision rather than a wrong one. A held decision is
recoverable: an administrator sets a priority or narrows a filter and the next
evaluation succeeds. A wrong payroll figure is not recoverable in the same way.

This pushes work to publish time, which is where it belongs: the Rule Service's
anchor and attribute-overlap lint should make a tie nearly impossible to
publish, so a tie in production is a near-miss worth alerting on rather than
routine noise. Services must therefore handle a failed decision on every
decision point, which the shadow-mode rollout exercises before enforcement.
