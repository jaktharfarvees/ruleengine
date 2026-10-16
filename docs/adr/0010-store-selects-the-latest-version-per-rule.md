# 10. The store selects the latest version per rule, and the store's API shape

Date: 2026-10-15

## Status

Accepted. Records a defect fixed in the store and the API deviations from the
build specification's `domainTypes.store` and `domainTypes.manifest`.

## Context

### The defect

Plan v2.0 section 5.3 makes rule versions immutable: *"a change creates a new
version with effective.validFrom"*. Nothing retro-dates the old version's
`validTo`, because an immutable version cannot be edited. So after a routine
version bump, two versions of one rule are both effective at the same business
time — that is the normal state of a published rule set, not an edge case.

The pipeline's version stage says which one to use: *"the latest version whose
`Effective` contains validAt."*

The first implementation of `Snapshot.Candidates` returned every evaluable
version in the catalog bucket, and `VersionResolver.Resolve` returned every
effective version in the family. Resolution ranks candidates by anchor depth,
hierarchy-type order, filter count and then `TieBreakPriority`, and has no
version tie-break at all — its determinism sort uses version **ascending** as a
last resort, which would prefer the *oldest*. Two versions of one rule at one
anchor therefore ranked identically and resolution reported an unresolved
precedence tie.

The effect: publishing version 1.1.0 of a rule made every decision for that
rule fail closed with `PRECEDENCE_TIE_UNRESOLVED` until someone retired 1.0.0.
A test asserted this as intended behaviour.

### Where the collapse belongs

Resolution cannot do it: adding a version tie-break would change an algorithm
frozen by RT01–RT14, and version choice is the store's job — the pipeline asks
the cache for candidates and the resolver for a version.

## Decision

The store collapses versions before resolution sees them. A rule's identity for
this purpose is its key plus its **scope** — the anchor it governs and the
filters that narrow it:

- Two definitions sharing a scope are successive versions of one rule. Only the
  newest one that is evaluable and effective at the requested time becomes a
  candidate.
- Two definitions with different scopes are different rules. Both survive, and
  resolution compares them by anchor depth as before.

`scopeIdentity` canonicalises the scope and sorts the filters, so reordering a
filter list cannot create a second rule. The catalog is sorted by
(scope, version descending) at load time, so `collapse` selects in one pass with
no map and one allocation.

Two consequences follow from the same reasoning:

- **Version order is semantic.** The catalog previously ordered equal-dated
  versions by string comparison, which puts `1.9.0` ahead of `1.10.0`. From a
  tenth minor release onwards that selected a superseded rule. It now uses the
  same `compareSemanticVersions` the family index uses.
- **A pin names a version, not permission to use it.** The pinned path filtered
  nothing by status, so a run manifest or request pin could select a `draft`,
  `pending-approval`, `deprecated` or `retired` version — an unapproved rule
  deciding someone's pay. The specification already required this
  (*"a rule whose Status is not Evaluable() is never selected, even by a pin"*);
  it is now enforced, as `PINNED_VERSION_UNAVAILABLE`.

Collapsing happens at the requested business time, not at load, so a
retroactive run still resolves the version that was in force then.

## Consequences

A version bump is now a deployment-free change, which is the point of
effective-dated versions. `store/selection_test.go` pins the behaviour: the
version bump, per-anchor and per-filter independence, filter-order identity, the
retroactive case, a scheduled version not shadowing the one in force, semantic
ordering, and the pinned-status refusal.

`Candidates` now always allocates its result slice instead of sometimes
returning the snapshot's own. That is a gain as well as a cost: a caller can no
longer reach into a published snapshot through a returned slice, which
`Versions` and `Version` now also prevent.

## API deviations from the build specification

Recorded here rather than left to a reader to discover. Most are improvements;
the last one is a concern.

| Specification | Implemented | Why |
|---|---|---|
| `Cache.Apply(Bundle) error` | `Cache.Replace(Bundle) error` | Names the all-or-nothing semantics: the snapshot is replaced, never merged |
| `Cache.Candidates(tenant string, f rule.Family, at time.Time)` | `Candidates(tenant, domain, ruleSet, name string, at time.Time)` | The four parts are the catalog key; `rule.Family` would be unpacked immediately |
| `Cache.Rule(tenant, key, version) (*RuleDefinition, bool)` | `Cache.Version(tenant, key, version) []*RuleDefinition` | One key and version legitimately has several anchored variants — a baseline and an employee exception — which the single-value shape cannot express |
| `Cache.Versions(...) []string` | `Versions(...) []*RuleDefinition` | Callers need the effective dates and status to choose, not just the strings |
| `Cache.Manifests() map[string]Manifest` | *(was missing; now restored alongside `Manifest(name)`)* | `fact.NewResolver` takes the whole set, so the cache has to be able to enumerate it |
| `CacheStats` | `Stats` | `store.Stats` does not need the package name repeated |
| `Bundle.Etag` | `Bundle.Cursor` | The loaders are changes-since based, and a cursor is not an HTTP entity tag |
| `Bundle.HierarchyTypes` + `Bundle.HierarchyNodes` | `Bundle.Hierarchy HierarchyBundle` | Keeps the two halves of one artifact together in the wire form |
| `Loader{LoadAll, Changes}` | `Loader{LoadAll}` + `ChangesLoader{LoadChanges}` | A file loader has no real changes feed; small interfaces are declared where consumed |
| `store/index.go` | catalog index inside `store/snapshot.go` | The index is only meaningful as part of a snapshot and shares its construction |
| `manifest.Store interface{Get(ctx, runID) (RunManifest, bool, error)}` | `manifest.Reader interface{Get(string) (RunManifest, bool)}`, with `Store` as the in-memory implementation | **Open.** See below |

### The manifest reader signature is an open question

`manifest.Reader.Get` drops both `context.Context` and `error`. For the
in-memory store that is honest — it cannot fail or block. But run manifests are
created and frozen by the Rule Service (plan section 5.3), so a consumer will
eventually read them over the network, and then:

- without a context, a remote read cannot honour a deadline or cancellation, on
  a path that runs inside a payroll decision;
- without an error, "this run has no manifest" is indistinguishable from "the
  manifest store is unreachable", and those have opposite correct responses —
  the first is `PINNED_VERSION_UNAVAILABLE` and fails the decision closed, the
  second is an infrastructure fault the caller should retry or alert on.

The specification's signature anticipated both. Widening `Reader` back to
`Get(ctx, runID) (RunManifest, bool, error)` costs little now and is awkward
later, once services depend on it. Raised in `docs/OPEN_QUESTIONS.md`.
