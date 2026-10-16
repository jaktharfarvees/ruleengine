# rulelint

Validates rules, field registries, dataset manifests and org hierarchies
offline. No network, no database.

Everything it reports would otherwise surface as a fail-closed decision in
production, where finding it costs a held payroll line rather than a red build.
Run it before committing, and in CI.

## Usage

```sh
# the standard fixture layout: rules/, registry/, datasets/, hierarchy/
rulelint -root testdata

# specific files or directories; each artifact's kind is inferred
rulelint rules/leave/eligibility-min-service-root.json registry/

# fail on warnings too
rulelint -root testdata -strict
```

Exits non-zero if anything is wrong, so it works as a CI gate.

## What it checks

Per artifact:

- every file validates against its published JSON Schema;
- a rule passes shape validation, and its logic **compiles on the engine that
  will run it** — a rule that can be published but never evaluated is caught
  here;
- a rule that stops applying before it starts is rejected;
- a manifest is internally consistent, and no two manifests claim one dataset;
- a hierarchy has no cycle, no orphan, and one root per tenant.

Across artifacts, which is where the mistakes that matter live:

- a rule declaring an input or output **no registry defines**, or using one in
  the wrong role;
- a scope filter on a hierarchy attribute — placement belongs in the anchor,
  not in a filter;
- a dataset-sourced field whose manifest does not permit what it asks, checked
  by the same `Compile` that will run in production;
- a rule anchored at a node no linted hierarchy contains, so it could never
  win.

Warnings are valid but probably unintended:

- an unreachable decision row after a catch-all;
- a deprecated field still in use;
- **two equally specific active rules at one anchor with no tie-break** — a
  decision for a matching subject would fail closed, so this is the warning
  worth never ignoring.

## Pass the registry

The cross-artifact checks need the registry. Linting a rule on its own skips
them and says so:

```sh
rulelint rules/ registry/     # checks rules against the field contracts
rulelint rules/               # shape and schema only, with a warning
```
