# 2. Derive precedence from the tenant's own org hierarchy

Date: 2026-10-15

## Status

Accepted. Implements locked decision D10' and supersedes the fixed-level model of
implementation plan v1.0.

## Context

Plan v1.0 ranked rules on six fixed precedence levels: GLOBAL, COUNTRY,
LEGAL_ENTITY, TENANT, EMPLOYEE_GROUP and EMPLOYEE_EXCEPTION. A rule carried an
authored precedence integer and a `scope` block of attribute matchers, and the
winner was the one at the highest named level whose matchers all held.

That model cannot absorb a level a tenant adds after go-live. Tenants do not
share one shape: a tenant may run Group → Company → Department → Branch, another
only Group → Department, and a third may insert a Division or a Team years into
production. Under fixed levels, inserting a level means renumbering authored
rules, which changes decisions for employees nobody intended to touch.

## Decision

Precedence is derived, never authored. A rule is anchored to exactly one
hierarchy node. The subject's node path is computed from the tenant's own
hierarchy, root to leaf. Among candidates whose anchor lies on that path, the
deepest anchor wins. Candidates at the same node are then compared by filter
specificity and explicit tie-break priority.

Concretely:

- There is no `PrecedenceLevel` type, and no fixed level name, anywhere in the
  module. `architecture_test.go` fails if one reappears.
- `RuleDefinition` has no precedence field. `hierarchy.DerivePrecedence` is the
  only producer of `DerivedPrecedence`, and it is read-only.
- The wire contract uses `anchor` plus optional `attributeFilters`. Filters narrow a rule within its anchor; they
  never raise it above a deeper anchor (test RT06).
- The retired `employee` anchor kind is rejected by validation, authorization,
  hierarchy precedence derivation, and resolution (test RT04).

## Consequences

Adding, removing or renaming a hierarchy level changes no rule and no code: new
nodes simply sit at a new depth and deepest-wins keeps working. Test RT08 pins
this by inserting a Division level and asserting the winner is unchanged.

Because resolution now walks a path on every evaluation, the hierarchy package
is on the critical path of every decision. Node paths are therefore precomputed
at load time and the cache is swapped atomically. `Snapshot.Path` returns a
defensive copy, and readers never take a lock.

The cost is that a rule cannot be written against an abstract level such as "all
companies in Canada". That is expressed instead by anchoring at the node and
resolving country from the nearest declaring ancestor (test RT09), which is more
honest: the rule names the part of the organisation it actually governs.

An employee-specific policy is not a separate precedence level. It is modeled
as a node in the tenant hierarchy where that granularity is required, or as an
attribute filter among rules anchored at the same node.
