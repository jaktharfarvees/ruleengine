# 4. Reject ambiguous rules at publication and use a deterministic runtime fallback

Date: 2026-10-16

## Status

Accepted. Supersedes the earlier fail-closed-on-tie wording.

## Context

Two active rules can be anchored at the same node, have the same filter
specificity, and carry the same explicit priority while both match one subject.
This is an authoring conflict. Choosing by file, map, or loader order would make
replicas and replays disagree. Failing every runtime decision is deterministic,
but it turns a publication defect into an availability incident.

## Decision

The publication gate rejects intersecting, equal-rank active rules. Authors
must narrow the filters or assign distinct `tieBreakPriority` values.

Resolution still defends itself if an invalid catalog reaches runtime. It sorts
the remaining candidates by a stable identity derived from rule key, version,
anchor, and filters; selects the first; attaches the complete `Tie` explanation;
and emits non-fatal `PRECEDENCE_CONFLICT`. The result is deterministic across
replicas and input ordering, while telemetry makes the escaped publication
defect visible.

No unconfigured business default is invented. Defaults are separate,
tenant-authorized `RuleCatalog.defaults` policies and apply only when no rule
resolves for their exact decision point.

## Consequences

- Ambiguity is a publication error, not a normal runtime branch.
- A runtime conflict remains reproducible and available while alerting loudly.
- Consumers should alert on any `PRECEDENCE_CONFLICT` and correct the catalog.
- The fallback identity is part of deterministic behavior and must remain
  covered by input-order property tests.
