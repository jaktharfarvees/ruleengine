# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

#### Developer tools, the example and the test kit

- `cmd/rulelint`: offline validation of rules, registries, manifests and
  hierarchies, with cross-artifact checks — a rule declaring a field no
  registry defines, a filter on a hierarchy attribute, a dataset field its
  manifest does not permit, a rule anchored where nothing exists — and a
  warning for two equally specific rules at one anchor, which would fail every
  matching decision closed.
- `cmd/ruleplay`: evaluates one request against local files and explains the
  result, naming the winner, the anchor, the subject path, every rejected
  candidate with its reason, and where each fact came from. Exits 2 on a
  decision that failed closed, as distinct from 1 when the tool could not run.
- `examples/leaveservice`: runnable wiring showing construction, a decision
  point list, and the shadow-mode pattern. It produces a real disagreement
  between the legacy check and the rule, which is what shadow mode is for.
- `ruletest` completed: `RunGolden` and `RunGoldenOn`, `FixtureBuilder` and
  `RuleBuilder`, `HierarchyBuilder`, `FakeClock`, `DecisionEquals` with the
  assertion helpers, and `Explain`, which renders a decision as the report a
  failing test or a support session reads.
- `hierarchy.Snapshot.Nodes`, and raw fixture loaders so a caller can assemble
  a bundle from its parts.

#### The evaluation pipeline, audit and authorisation

- The root facade: `New` with functional options, `Config` with validation that
  refuses an unset fact budget, the six-stage `Evaluate`, `EvaluateAll` with
  fact de-duplication across decision points, `Ready`, `Start`, `Close` and
  `Stats`. Every fail-closed path carries a reason code from the catalogue, and
  every metric and span the observability list names is emitted.
- `audit`: the decision record, the non-blocking async logger that drops with a
  counter rather than holding up a decision, and an slog sink for development.
- `authz`: hierarchy subtree grants, `IsWithinAuthority` and `EffectiveNodes`,
  so the Rule Service and Rule Studio reach the same answer from one place.
- `contextbuilder`: the interface for facts a service computes itself, with a
  static double.
- `ReasonCodes()` exposes the catalogue, and a test asserts every code in it is
  produced by a real decision — a code nothing can produce would tell an
  operator a condition is handled when it is not.
- `hierarchy.Snapshot.Nodes` enumerates a tenant's nodes, which is what scopes
  an author's node picker.

#### Store, cache and versions

- The `store` package: `Bundle`, `Loader` and `ChangesLoader`, a `Cache` that
  publishes immutable snapshots through an atomic pointer, a catalog index keyed
  by tenant, domain, rule set and name, and `VersionResolver` applying run
  manifest, explicit pin and effective-at precedence.
- `store/filestore` reading the committed testdata layout, and
  `store/servicestore` calling bootstrap and changes-since with retries and a
  staleness metric, opening no connection until called.
- `manifest`: `RunManifest` with rule and fact pins, and a concurrency-safe
  in-memory store whose reads are defensive copies.
- `Cache.Manifests()` and `Snapshot.Manifests()` enumerate every published
  dataset manifest, which is what building a fact resolver needs.

#### Datasets and fact resolution

- The `dataset` package: manifests with column permissions and safety limits,
  a `Query` type constructible only by `Compile`, guardrails G1 to G12, a
  stable `Fingerprint` for per-request de-duplication, a plain-language
  `Explain` for the authoring preview, the `Executor` interface each service
  implements, and `ConformanceSuite` so a service team can prove its executor
  honours the injected tenant predicate.
- The injection guard and its committed corpus under
  `testdata/security/injection-corpus.txt`, with a fuzz target asserting that
  nothing it accepts carries a banned sequence or an unknown token.
- The `fact` package: a bounded resolver over all four fact kinds, derived
  evaluation with cycle detection, the native registry, per-request budget
  accounting, and de-duplication that spans every decision point in a request.
- `telemetry`: metric and trace interfaces, zero-allocation no-op
  implementations, and the single home for every metric and span name.
- `ruletest.InMemoryExecutor` over synthetic rows, plus loaders for the shared
  dataset, registry and budget fixtures.
- The four dataset manifest fixtures, and FT01 through FT16.

- Initial Go module, repository governance, quality tooling, and CI workflows.
- Injectable system and fixed clocks for deterministic lifecycle tests.
- Canonical ArrowHR rule-key parsing and formatting.
- Core request, decision, rule, trace, and explanation transport types.
- Complete reason-code catalogue and structured operational error taxonomy.
- Authored rule validation with hierarchy anchors and closed filter operators.
- Seven embedded versioned boundary schemas and offline schema validation.
- Immutable multi-tenant hierarchy snapshots with load-time graph validation.
- Precomputed deepest-first node paths and nearest-ancestor attribute resolution.
- Hierarchy-derived node and employee-exception precedence.
- Lock-free atomic hierarchy cache and shared RWB hierarchy fixtures.
- Pure deterministic rule resolution with hierarchy-derived precedence.
- Closed scope-filter matching with human-readable failure explanations.
- Fail-closed unresolved-rule and precedence-tie results.
- RT01 through RT14, randomized resolution properties, and resolver benchmarks.
- Immutable field registry with strict input, output, enum, and filter validation.
- Pure decision-engine interface, trace model, and honest capability reporting.
- Reference first-hit test engine with comparisons and field references.
- Exact decimal and currency-scale helpers with shared registry and golden fixtures.
- Production decision-table engine with first, priority, and collect policies.
- Closed, allowlisted expression parser with exact arithmetic and date functions.
- Parsed-table caching, load-time warnings, deterministic traces, fuzz targets,
  benchmarks, and cross-engine conformance fixtures.
- The complete shared rule-fixture set: leave, attendance, payroll and the two
  same-anchor conflict pairs, with `ruletest.LoadRule` and `ruletest.LoadRules`
  so services and Rule Studio assert outcomes from the same files.
- Attendance and payroll golden fixtures, and the five mandatory ADRs.
- An architecture test enforcing one-way imports (INV1) and the absence of the
  superseded fixed-precedence vocabulary.
- Benchmark baseline with a regression gate, and CI jobs for `govulncheck` and
  benchmark comparison.

### Changed

- A request need not repeat what its rule key already says: the domain, rule
  set and name are filled from the key, which is what makes `EvaluateAll`
  usable with a bare list of keys. A routing block that contradicts its own key
  is refused, because one of the two is a mistake and there is no way to tell
  which.
- `manifest.Store` is the read-only interface, taking a context and returning
  an error as the specification has it, and `manifest.InMemoryStore` is the
  in-memory implementation. A remote reader can honour a decision's deadline,
  and a missing manifest is distinguishable from an unreachable store.
- Version selection happens before anchor resolution, because a version
  carries its own anchor: publishing a company override is a new version of the
  same rule key (plan Appendix A.2). A run manifest's pin therefore constrains
  which rules can win, so a run pinned to an older version sees the rule set as
  it was then rather than failing.
- The fact-source declaration types live in `registry`, with the field contract
  plan section 4.4 puts them in, so that `fact` can import both `registry` and
  `dataset` to resolve a field. `fact.FactSource` and friends remain as
  aliases, so no documented name changed. See
  [ADR 9](docs/adr/0009-fact-source-declaration-lives-with-the-field.md).
- `dataset.Manifest` carries an optional `rowLimit`, and `Compile` applies the
  lower of it and the request budget.
- Schema identifiers use the URN form the plan mandates
  (`urn:arrowhr:schema:<artifact>:v2`).
- `availableBalance` is registered as a `dataset`-kind field over
  `leave.balances`, matching plan Appendix A.7 and F.2.
- The per-package coverage floor now covers `resolve`, `fact`, `dataset` and
  `engine/decisiontable` rather than the decision-table engine alone.

### Fixed

- The conflict fixture pair carried different rule names, so being in separate
  catalog buckets they could never actually tie. They now share a decision
  point, which is what makes an unresolved precedence tie reachable.
- **The store returned every effective version of a rule rather than the
  latest, so publishing a new version made resolution report an unresolved
  precedence tie and failed every decision for that rule closed.** Versions are
  immutable and carry no `validTo`, so this was the normal state after any
  version bump. The catalog now collapses to the latest version per rule, where
  a rule is its key plus its scope. See
  [ADR 10](docs/adr/0010-store-selects-the-latest-version-per-rule.md).
- The catalog ordered equal-dated versions by string comparison, so `1.9.0`
  ranked above `1.10.0` and a superseded rule would be selected from the tenth
  minor release onwards.
- A run-manifest or request pin could select a `draft`, `pending-approval`,
  `deprecated` or `retired` version, letting an unapproved rule decide an
  outcome. A pin to a version that is not evaluable is now
  `PINNED_VERSION_UNAVAILABLE`.
- `Candidates`, `Versions` and `Version` could hand a caller the published
  snapshot's own slice.
- `docs/` was excluded by `.gitignore`, so no ADR or design document was under
  version control.
- Scope-filter comparison went through `float64`, so two integers above 2^53
  compared equal and a decimal range boundary could be decided wrongly. Filters
  now compare as exact decimals.
- The decision-table engine identified a cached table by JSON-encoding and
  reflecting over the authored logic on every evaluation, costing about 1 ms for
  a hundred-row table. A content fingerprint replaces it, about fifteen times
  faster, with no reflection on the evaluation path.
- An output value that no authored rule can carry, such as a function, is now
  refused by `Compile` rather than by a side effect of the cache key.
- A golden fixture naming an unknown capability skipped silently on every
  engine; it now fails.
- `registry` refused an exact decimal as an `integer` value, so an
  integer-typed field could not be computed by arithmetic.
- `urn:...:v01` parsed but did not round-trip, so a rule key had two spellings
  and a version pin written one way could miss a cache keyed the other.

