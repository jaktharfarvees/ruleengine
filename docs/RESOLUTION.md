# Resolution

Resolution chooses the effective rule for one tenant, decision point, subject,
and business time. It is pure: no I/O, clock reads, logging, or mutation.

## Model

A tenant defines an organization tree of any depth. Every rule is anchored to
exactly one hierarchy node. The hierarchy returns paths in root-to-leaf order.
There is no separate employee anchor: an employee-specific policy must use an
approved hierarchy leaf node or a same-node attribute filter.

Given `G001 -> C002 -> D002 -> BR001`, a matching `D002` rule outranks a
matching `C002` rule. Level names are descriptive; no fixed global hierarchy
ladder exists. Precedence is derived from the effective hierarchy snapshot and
is never authored as an integer.

Attribute filters narrow a rule within its anchor. They cannot promote a rule
above a deeper anchor.

## Algorithm

For the version-constrained candidates of one exact rule key:

1. Reject cross-tenant, non-evaluable, and out-of-interval candidates.
2. Compute the subject's effective root-to-leaf path.
3. Retain node anchors on the subject's path.
4. Retain candidates whose attribute filters all match.
5. Prefer greater anchor depth.
6. At one anchor, prefer more attribute filters, then lower explicit
   `tieBreakPriority` according to the resolver's documented ordering.
7. If candidates remain equal, choose by the stable candidate identity and
   attach a `Tie` explanation with `PRECEDENCE_CONFLICT`.

The store collapses immutable versions before resolution: each distinct anchor
and filter scope contributes only its newest evaluable version effective at
`validAt`. A run manifest replaces effective-time selection with its exact rule
and hierarchy pins.

## Ambiguity policy

Ambiguous active rules are publication errors. `rulelint` rejects equal-rank
same-anchor candidates; the Rule Service must run the same check before making
a catalog visible.

Runtime still protects determinism if invalid data bypasses publication. The
stable fallback produces the same winner regardless of loader or input order,
returns the complete conflict explanation, increments the tie metric, and emits
non-fatal `PRECEDENCE_CONFLICT`. Operators must correct the catalog. See
[ADR 4](adr/0004-deterministic-tie-fallback.md).

## RWB example

For an employee at `BR001`, the path is:

```text
G001 -> C002 -> D002 -> BR001
```

Two versions of `urn:arrowhr:rule:leave:eligibility:min-service:v1` are
effective:

| Version | Anchor | Rule |
|---|---|---|
| `1.0.0` | `G001` | service months at least 6 |
| `1.1.0` | `C002` | service months at least 3 |

Both anchors are on the path, so `C002` wins at depth 1 over `G001` at depth 0.
The response exposes `resolvedAt: "C002"`, both candidates and their reasons,
and the full root-to-leaf `anchorPath`.

Hierarchy attributes resolve independently from the nearest declaring ancestor.
In the fixtures, country, currency, and locale come from `C002`, while timezone
comes from `BR001`.

## Failure and default behavior

| Situation | Reason | Failed |
|---|---|---|
| no effective hierarchy snapshot or subject path | `HIERARCHY_NOT_CONFIGURED` | yes |
| no rule and no exact authorized default | `NO_RULE_RESOLVED` | yes |
| exact tenant-authorized catalog default used | `DEFAULT_APPLIED` | no |
| equal-rank rules escaped publication | `PRECEDENCE_CONFLICT` | no; deterministic winner |
| a rule returns a negative business result | `OK` | no infrastructure failure |

Defaults are not global configuration. A tenant author explicitly publishes a
default for one `domain/ruleSet/name`; absence always fails closed.

## Contract tests

The resolver suite covers deeper anchors, subtree inheritance, root baselines,
rejection of the retired employee-anchor kind, off-path exclusion, filter narrowing, deterministic
conflict fallback, inserted and repeated levels, inactive nodes, cross-tenant
defense, explicit priorities, and stable explanations. Property tests cover
monotonicity across arbitrary N-level hierarchies whose type order is unrelated
to depth, order-independent determinism, and malformed-input no-panic behavior.
The monotonicity property deliberately gives shallower candidates more matching
filters and a preferred priority, proving neither can promote a shallow anchor
over a deeper eligible node.
